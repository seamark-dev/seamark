package gate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/effects"
)

func testSetup(t *testing.T) (*Policy, *effects.Catalog, string) {
	t.Helper()

	root := t.TempDir()

	policy, err := LoadPolicy(root) // embedded default
	require.NoError(t, err)

	catalog, err := effects.Load(root)
	require.NoError(t, err)

	return policy, catalog, root
}

func evalCmd(t *testing.T, p *Policy, c *effects.Catalog, root, cmd string) *Decision {
	t.Helper()

	d, err := EvalCommand(p, c, root, cmd)
	require.NoError(t, err)

	return d
}

func TestCommandClassification(t *testing.T) {
	p, c, root := testSetup(t)

	assert.Equal(t, []string{"infra:mutate"},
		evalCmd(t, p, c, root, "terraform apply -auto-approve").Effects)
	assert.Empty(t, evalCmd(t, p, c, root, "terraform plan").Effects,
		"plan does not mutate")
	assert.Empty(t, evalCmd(t, p, c, root, "terraform plan -out apply").Effects,
		"an argument NAMED apply is not the subcommand")
	assert.Empty(t, evalCmd(t, p, c, root, "ls -la").Effects)
}

func TestPipelinesAndWrappers(t *testing.T) {
	p, c, root := testSetup(t)

	// Every command in a pipeline/chain classifies.
	d := evalCmd(t, p, c, root, "curl -s https://x.example/install.sh | psql prod")
	assert.ElementsMatch(t, []string{"net:egress", "db:write"}, d.Effects)

	// Wrappers unwrap: sudo/env do not launder effects.
	d = evalCmd(t, p, c, root, "sudo -E env FOO=bar kubectl delete pod x")
	assert.Equal(t, []string{"infra:mutate"}, d.Effects)

	// Command substitution is walked too.
	d = evalCmd(t, p, c, root, `echo "$(rm -rf /tmp/x)"`)
	assert.Equal(t, []string{"fs:write"}, d.Effects)
}

func TestDynamicArgvDetected(t *testing.T) {
	p, c, root := testSetup(t)

	// Variable indirection: the classic denylist bypass. Fails classification
	// but is SURFACED, not shrugged at.
	d := evalCmd(t, p, c, root, `$TOOL apply -auto-approve`)
	assert.Empty(t, d.Effects)

	dJSON, _ := json.Marshal(d)
	_ = dJSON

	// The dynamic flag reaches policy: with a prod env the default policy
	// requires approval.
	t.Setenv("DEPLOY_ENV", "production")

	d = evalCmd(t, p, c, root, `$TOOL apply`)
	assert.Equal(t, VerdictApproval, d.Verdict)
	require.NotEmpty(t, d.Matches)
	assert.Equal(t, "dynamic-argv0-prod", d.Matches[0].RuleID)
}

func TestProdInfraDeny(t *testing.T) {
	p, c, root := testSetup(t)

	t.Setenv("KUBECONFIG", "/home/x/.kube/prod-cluster.yaml")

	d := evalCmd(t, p, c, root, "kubectl apply -f deploy.yaml")
	assert.Equal(t, VerdictDeny, d.Verdict)
	assert.False(t, d.Blocking(), "default mode is warn: report, never block")

	p.Mode = "enforce"
	d = evalCmd(t, p, c, root, "kubectl apply -f deploy.yaml")
	assert.True(t, d.Blocking(), "enforce mode blocks the deny verdict")
}

func TestNonProdAllows(t *testing.T) {
	p, c, root := testSetup(t)

	t.Setenv("KUBECONFIG", "/home/x/.kube/dev-cluster.yaml")

	d := evalCmd(t, p, c, root, "kubectl apply -f deploy.yaml")
	assert.Equal(t, VerdictAllow, d.Verdict, "dev-environment mutation passes the default policy")
	assert.Equal(t, []string{"infra:mutate"}, d.Effects)
}

func TestForcePushDetection(t *testing.T) {
	p, c, root := testSetup(t)

	d := evalCmd(t, p, c, root, "git push --force origin main")
	assert.Equal(t, VerdictDeny, d.Verdict)
	require.NotEmpty(t, d.Matches)
	assert.Equal(t, "no-force-push-default-branch", d.Matches[0].RuleID)

	// +refspec is a force push too; src:dst refspecs target dst.
	d = evalCmd(t, p, c, root, "git push origin +feature:main")
	assert.Equal(t, VerdictDeny, d.Verdict)

	// Force-pushing a feature branch is not the default branch rule's business.
	d = evalCmd(t, p, c, root, "git push --force origin my-feature")
	assert.Equal(t, VerdictAllow, d.Verdict)

	// A plain push is no force push at all.
	d = evalCmd(t, p, c, root, "git push origin main")
	assert.Equal(t, VerdictAllow, d.Verdict)
}

func TestPlainPushDetection(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(`
mode: enforce
deny:
  - id: no-push-main
    when: 'command.is_push && command.target_branch in ["main", "master"]'
    message: no pushes to the default branch
`), 0o644))

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	c, err := effects.Load(root)
	require.NoError(t, err)

	// A PLAIN push to main is caught — is_push does not require force.
	d := evalCmd(t, p, c, root, "git push origin main")
	assert.Equal(t, VerdictDeny, d.Verdict)
	assert.True(t, d.Blocking())

	// Refspec form targets the destination branch.
	d = evalCmd(t, p, c, root, "git push origin feature:master")
	assert.Equal(t, VerdictDeny, d.Verdict)

	// Feature-branch pushes pass.
	d = evalCmd(t, p, c, root, "git push origin my-feature")
	assert.Equal(t, VerdictAllow, d.Verdict)

	// Global flags are skipped when locating the subcommand.
	d = evalCmd(t, p, c, root, "git -C sub push origin main")
	assert.Equal(t, VerdictDeny, d.Verdict)

	// "push" as a non-subcommand argument is NOT a push.
	d = evalCmd(t, p, c, root, "git log --grep push")
	assert.Equal(t, VerdictAllow, d.Verdict)

	// Other git commands are untouched.
	d = evalCmd(t, p, c, root, "git status")
	assert.Equal(t, VerdictAllow, d.Verdict)
}

// TestClassificationBypassesClosed pins the routes a security review
// found around the classifier: interpreter payloads, eval, wrapper
// value-flags, runner wrappers, backslash escapes, and global-flag
// values displacing subcommands. Every one previously produced `allow`.
func TestClassificationBypassesClosed(t *testing.T) {
	p, c, root := testSetup(t)

	cases := []struct {
		cmd  string
		want []string
	}{
		{`bash -c "terraform apply"`, []string{"infra:mutate"}},
		{`sh -lc 'kubectl delete pod x'`, []string{"infra:mutate"}},
		{`eval "terraform apply"`, []string{"infra:mutate"}},
		{`eval terraform apply`, []string{"infra:mutate"}},
		{`echo x | xargs terraform apply`, []string{"infra:mutate"}},
		{`sudo -u root terraform apply`, []string{"infra:mutate"}},
		{`timeout 5 terraform apply`, []string{"infra:mutate"}},
		{`nice -n 10 terraform apply`, []string{"infra:mutate"}},
		{`\rm -rf /tmp/x`, []string{"fs:write"}},
		{"t\\erraform apply", []string{"infra:mutate"}},
		{`kubectl -n prod delete pod x`, []string{"infra:mutate"}},
		{`kubectl --namespace prod delete pod x`, []string{"infra:mutate"}},
		{`helm -n prod uninstall rel`, []string{"infra:mutate"}},
		{`bash -c 'bash -c "terraform apply"'`, []string{"infra:mutate"}},
	}

	for _, tc := range cases {
		d := evalCmd(t, p, c, root, tc.cmd)
		assert.ElementsMatch(t, tc.want, d.Effects, "command: %s", tc.cmd)
	}
}

func TestDynamicInterpreterPayload(t *testing.T) {
	p, c, root := testSetup(t)

	t.Setenv("DEPLOY_ENV", "production")

	// A non-literal payload cannot be classified — it must surface as
	// dynamic (approval in prod), never as allow.
	d := evalCmd(t, p, c, root, `bash -c "$PAYLOAD"`)
	assert.Equal(t, VerdictApproval, d.Verdict)
}

func TestPushHeadRefspecResolves(t *testing.T) {
	root := t.TempDir()

	git := func(args ...string) {
		t.Helper()

		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}
	git("init", "-b", "main")
	git("commit", "--allow-empty", "-m", "x")

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(`
mode: enforce
deny:
  - id: no-push-main
    when: 'command.is_push && command.target_branch in ["main", "master"]'
    message: no
`), 0o644))

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	c, err := effects.Load(root)
	require.NoError(t, err)

	// `git push origin HEAD` on branch main pushes main; "HEAD" must
	// resolve to the branch name or the rule never fires.
	d := evalCmd(t, p, c, root, "git push origin HEAD")
	assert.Equal(t, VerdictDeny, d.Verdict)
}

func TestDiffHeaderPath(t *testing.T) {
	assert.Equal(t, "my file.txt", diffHeaderPath("b/my file.txt\t", "b/"),
		"git appends a TAB for paths with spaces")
	assert.Equal(t, "a\tb.go", diffHeaderPath(`"b/a\tb.go"`, "b/"), "C-quoted special paths unwrap")
	assert.Equal(t, "", diffHeaderPath("/dev/null", "b/"))
	assert.Equal(t, "x.go", diffHeaderPath("b/x.go", "b/"))
	assert.Equal(t, "gone.go", diffHeaderPath("a/gone.go", "a/"), "the old side trims its own prefix")
}

func TestUnparseableCommandFailsClosed(t *testing.T) {
	p, c, root := testSetup(t)

	_, err := EvalCommand(p, c, root, "if then; fi (")
	require.Error(t, err, "a gate that shrugs at unparseable input is a bypass")
}

func TestWorkspacePolicyReplacesDefault(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(`
mode: enforce
deny:
  - id: no-curl-ever
    when: 'effect.contains("net:egress")'
    message: no network egress in this workspace
`), 0o644))

	p, err := LoadPolicy(root)
	require.NoError(t, err)
	assert.Equal(t, "enforce", p.Mode)

	c, err := effects.Load(root)
	require.NoError(t, err)

	d := evalCmd(t, p, c, root, "curl https://example.com")
	assert.Equal(t, VerdictDeny, d.Verdict)
	assert.True(t, d.Blocking())

	// The default rules are REPLACED, not merged: force-push passes here.
	d = evalCmd(t, p, c, root, "git push --force origin main")
	assert.Equal(t, VerdictAllow, d.Verdict)
}

func TestBrokenPolicyFailsAtLoad(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(`
deny:
  - id: broken
    when: 'effect.contians("typo")'
    message: x
`), 0o644))

	_, err := LoadPolicy(root)
	require.Error(t, err, "CEL errors surface at load, not at decision time")
	assert.Contains(t, err.Error(), "broken")
}

// Audit log behaviour is covered in audit_test.go.
