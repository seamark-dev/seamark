package integration

// Write preview tests. They plan through the real coordinator, so each
// preview reads the files by the same guards that apply checks.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// docRegistry registers one test client that writes body to rel.
func docRegistry(t *testing.T, rel, body string) *Registry {
	t.Helper()

	reg, err := NewRegistry(docClient("doc", rel, body))
	require.NoError(t, err)

	return reg
}

// docIntent asks the test client for its document.
func docIntent() []ClientSetup {
	return []ClientSetup{{ClientID: "doc", RegisterMCP: true}}
}

// onlyPreview previews a plan that must hold exactly one write.
func onlyPreview(t *testing.T, plan *SetupPlan) WritePreview {
	t.Helper()

	previews := PreviewWrites(plan)
	require.Len(t, previews, 1)

	return previews[0]
}

func TestPreviewOfANewFileAddsEveryLine(t *testing.T) {
	root := t.TempDir()
	plan := mustPlan(t, integrationTestRegistry(t), SetupRequest{
		Root: root, Common: []Document{scaffold(".seamark/policy.yaml", "mode: warn\ndeny: []\n")},
	})

	got := onlyPreview(t, plan)

	assert.Equal(t, ".seamark/policy.yaml", got.Path)
	assert.True(t, got.Created)
	assert.False(t, got.Stale)
	require.NoError(t, got.Err)
	assert.Empty(t, got.Consumers, "a common document belongs to no client")
	assert.Equal(t, "starter", got.Detail)
	assert.Equal(t, 2, got.Added)
	assert.Zero(t, got.Removed)
	assert.Equal(t, "--- a/.seamark/policy.yaml\n"+
		"+++ b/.seamark/policy.yaml\n"+
		"@@ -0,0 +1,2 @@\n"+
		"+mode: warn\n"+
		"+deny: []\n", got.Diff)
}

func TestPreviewOfAnUpdateShowsARemovedAndAnAddedLine(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, "doc.txt", "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n")

	reg := docRegistry(t, "doc.txt", "one\ntwo\nthree\nfour\nFIVE\nsix\nseven\neight\n")
	got := onlyPreview(t, mustPlan(t, reg, SetupRequest{Root: root, Clients: docIntent()}))

	assert.False(t, got.Created)
	assert.False(t, got.Stale)
	assert.Equal(t, []string{"doc"}, got.Consumers)
	assert.Equal(t, "test document", got.Detail)
	assert.Equal(t, 1, got.Added)
	assert.Equal(t, 1, got.Removed)

	// Three lines of context on each side; the first line is out of it.
	assert.Equal(t, "--- a/doc.txt\n"+
		"+++ b/doc.txt\n"+
		"@@ -2,7 +2,7 @@\n"+
		" two\n"+
		" three\n"+
		" four\n"+
		"-five\n"+
		"+FIVE\n"+
		" six\n"+
		" seven\n"+
		" eight\n", got.Diff)
}

func TestPreviewOfANativeClientFileKeepsExplicitMarkers(t *testing.T) {
	// The real Claude Code adapter merges its hooks into an existing
	// settings file. Every diff line starts with a marker, so the change
	// reads without color.
	root := t.TempDir()
	writeRel(t, root, ".claude/settings.json", "{\n  \"model\": \"opus\"\n}\n")

	plan := mustPlan(t, Builtin(), SetupRequest{
		Root: root, Binary: testBinary, Clients: []ClientSetup{{ClientID: "claude", Hooks: true}},
	})
	got := onlyPreview(t, plan)

	assert.Equal(t, ".claude/settings.json", got.Path)
	assert.Equal(t, []string{"claude"}, got.Consumers)
	assert.Positive(t, got.Added)
	assert.Contains(t, got.Diff, "+    \"PreToolUse\": [\n")
	assert.Contains(t, got.Diff, "   \"model\": \"opus\"\n", "an unchanged line is context")
	assert.Contains(t, got.Diff, "\"command\": \""+testBinary+" gate --hook\"")

	for _, line := range strings.Split(strings.TrimSuffix(got.Diff, "\n"), "\n") {
		require.NotEmpty(t, line)
		assert.Contains(t, "+- @", line[:1], "line %q has no marker", line)
	}
}

func TestPreviewRedactsSecretsOfOtherServers(t *testing.T) {
	// .mcp.json holds the environment of every registered server. The
	// registration rewrites the file, so its tokens are in the diff and
	// must be scrubbed there.
	root := t.TempDir()
	writeRel(t, root, ".mcp.json", `{
  "mcpServers": {
    "github": {
      "command": "gh-mcp",
      "env": {
        "GITHUB_TOKEN": "ghp_verysecret",
        "API_KEY": "sk-alsosecret"
      }
    }
  }
}
`)

	plan := mustPlan(t, Builtin(), SetupRequest{
		Root: root, Binary: testBinary, Clients: []ClientSetup{{ClientID: "claude", RegisterMCP: true}},
	})
	got := onlyPreview(t, plan)

	assert.Equal(t, ".mcp.json", got.Path)
	assert.Contains(t, got.Diff, "\"seamark\": {", "the registration shows")
	assert.Contains(t, got.Diff, "\"GITHUB_TOKEN\": [REDACTED]")
	assert.NotContains(t, got.Diff, "ghp_verysecret")
	assert.NotContains(t, got.Diff, "sk-alsosecret")

	// The redaction is for display only: apply writes the real bytes.
	_, err := ApplySetup(plan, ApplyOptions{})
	require.NoError(t, err)
	assert.Contains(t, readRel(t, root, ".mcp.json"), "ghp_verysecret")
}

// liveSecret is a credential shape that the shared redact patterns do
// not know by itself. Only the preview layer can hide it.
const liveSecret = "sk-live-1234567890abcdef"

// previewOf returns the preview of the write to rel.
func previewOf(t *testing.T, plan *SetupPlan, rel string) WritePreview {
	t.Helper()

	for _, p := range PreviewWrites(plan) {
		if p.Path == rel {
			require.NoError(t, p.Err)

			return p
		}
	}

	require.Failf(t, "no preview", "the plan writes no %s", rel)

	return WritePreview{}
}

func TestPreviewHidesCredentialsOfOtherServers(t *testing.T) {
	// Each fixture puts a credential of another server where the diff
	// shows it: as a rewritten line, or as context next to the change.
	// The shown line proves that the diff reaches the value; the value
	// itself must never show. Seamark's own lines stay readable.
	tests := []struct {
		name   string
		rel    string
		body   string
		intent ClientSetup
		shown  []string
		own    []string
	}{
		{
			name: "mcp.json headers and env",
			rel:  ".mcp.json",
			body: `{"mcpServers": {"remote": {"type": "http", "url": "https://mcp.example.com",
  "headers": {"X-API-Key": "` + liveSecret + `", "Authorization": "Basic ` + liveSecret + `"}},
  "local": {"command": "local-mcp", "args": ["--api-key", "` + liveSecret + `"],
  "env": {"OPENAI_KEY": "` + liveSecret + `", "REGION": "` + liveSecret + `"}}}}
`,
			intent: ClientSetup{ClientID: ClaudeID, RegisterMCP: true},
			shown: []string{
				`"X-API-Key": "[REDACTED]"`, `"Authorization": "[REDACTED]"`,
				`"OPENAI_KEY": "[REDACTED]"`, `"REGION": "[REDACTED]"`, `"--api-key",`,
			},
			own: []string{`"seamark": {`, `"command": "seamark"`, `"mcp"`},
		},
		{
			name:   "claude settings env",
			rel:    ".claude/settings.json",
			body:   "{\n  \"env\": {\"OPENAI_KEY\": \"" + liveSecret + "\"},\n  \"model\": \"opus\"\n}\n",
			intent: ClientSetup{ClientID: ClaudeID, Hooks: true},
			shown:  []string{`"OPENAI_KEY": "[REDACTED]"`},
			own:    []string{`"command": "` + testBinary + ` gate --hook"`},
		},
		{
			name: "codex env inline table",
			rel:  ".codex/config.toml",
			body: "[mcp_servers.github]\ncommand = \"gh-mcp\"\n" +
				"env = { \"GITHUB_PERSONAL_ACCESS_TOKEN\" = \"" + liveSecret + "\", ORG = \"" + liveSecret + "\" }\n",
			intent: ClientSetup{ClientID: CodexID, RegisterMCP: true},
			shown:  []string{`env = { "GITHUB_PERSONAL_ACCESS_TOKEN" = "[REDACTED]", ORG = "[REDACTED]" }`},
			own:    []string{"+[mcp_servers.seamark]\n", "+command = \"seamark\"\n", "+args = [\"mcp\"]\n"},
		},
		{
			name: "codex http headers inline table",
			rel:  ".codex/config.toml",
			body: "[mcp_servers.remote]\nurl = \"https://mcp.example.com\"\n" +
				"http_headers = { \"X-Api-Key\" = \"" + liveSecret + "\" }\n",
			intent: ClientSetup{ClientID: CodexID, RegisterMCP: true},
			shown:  []string{`http_headers = { "X-Api-Key" = "[REDACTED]" }`},
			own:    []string{"+[mcp_servers.seamark]\n", "+command = \"seamark\"\n"},
		},
		{
			name: "codex env table above the hunk",
			// The hunk starts below the [..env] header, so only the whole
			// file tells that GITHUB_ORG is an environment variable.
			rel: ".codex/config.toml",
			body: "[mcp_servers.github]\ncommand = \"gh-mcp\"\n\n[mcp_servers.github.env]\n" +
				"A = \"1\"\nB = \"2\"\nC = \"3\"\nGITHUB_ORG = \"" + liveSecret + "\"\n",
			intent: ClientSetup{ClientID: CodexID, RegisterMCP: true},
			shown:  []string{` GITHUB_ORG = "[REDACTED]"`},
			own:    []string{"+[mcp_servers.seamark]\n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, tt.rel, tt.body)

			plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{tt.intent}})
			got := previewOf(t, plan, tt.rel)

			assert.NotContains(t, got.Diff, liveSecret)
			assert.NotContains(t, got.Diff, "1234567890abcdef")

			for _, want := range append(tt.shown, tt.own...) {
				assert.Contains(t, got.Diff, want)
			}

			// The redaction is for display only: apply writes the real bytes.
			_, err := ApplySetup(plan, ApplyOptions{})
			require.NoError(t, err)
			assert.Contains(t, readRel(t, root, tt.rel), liveSecret)
		})
	}
}

// maskedFile returns content with the hidden ranges of its dialect
// replaced, line by line, as the preview shows them.
func maskedFile(rel, content string) string {
	side := newDiffSide(rel, []byte(content))

	var b strings.Builder

	for i, line := range side.lines {
		b.WriteString(applyMasks(line, side.hideAt(i)))
	}

	return b.String()
}

func TestPreviewRedactionReadsKeysAndTables(t *testing.T) {
	tests := []struct {
		name, rel, in, want string
	}{
		{
			name: "json credential names",
			rel:  "a.json",
			in: `{"X-API-Key": "a", "OPENAI_KEY": "b", "accessToken": "c", "apiKey": "d", "GITHUB_PAT": "e",
"Cookie": "f", "sessionId": "g", "db_password": "h", "Authorization": "token i", "client_secrets": "j"}` + "\n",
			want: `{"X-API-Key": "[REDACTED]", "OPENAI_KEY": "[REDACTED]", "accessToken": "[REDACTED]", "apiKey": "[REDACTED]", "GITHUB_PAT": "[REDACTED]",
"Cookie": "[REDACTED]", "sessionId": "[REDACTED]", "db_password": "[REDACTED]", "Authorization": "[REDACTED]", "client_secrets": "[REDACTED]"}` + "\n",
		},
		{
			name: "json names that are no credential",
			rel:  "a.json",
			in:   `{"command": "x", "url": "https://h", "path": "p", "author": "a", "oauth": "o", "keyboard": "k", "n": 1, "t": true}` + "\n",
			want: `{"command": "x", "url": "https://h", "path": "p", "author": "a", "oauth": "o", "keyboard": "k", "n": 1, "t": true}` + "\n",
		},
		{
			name: "json credential tables hide every string",
			rel:  "a.json",
			in:   "{\"env\": {\n  \"MODE\": \"x\",\n  \"LIST\": [\"y\"]\n},\n\"httpHeaders\": {\"X-Team\": \"z\"}, \"type\": \"stdio\"}\n",
			want: "{\"env\": {\n  \"MODE\": \"[REDACTED]\",\n  \"LIST\": [\"[REDACTED]\"]\n},\n\"httpHeaders\": {\"X-Team\": \"[REDACTED]\"}, \"type\": \"stdio\"}\n",
		},
		{
			name: "json seamark entry keeps its tables readable",
			rel:  ".mcp.json",
			in:   `{"mcpServers": {"seamark": {"command": "seamark", "args": ["mcp"], "env": {"SEAMARK_MODE": "warn", "API_TOKEN": "t"}}, "other": {"env": {"MODE": "m"}}}}` + "\n",
			want: `{"mcpServers": {"seamark": {"command": "seamark", "args": ["mcp"], "env": {"SEAMARK_MODE": "warn", "API_TOKEN": "[REDACTED]"}}, "other": {"env": {"MODE": "[REDACTED]"}}}}` + "\n",
		},
		{
			name: "json option arguments",
			rel:  "a.json",
			in:   `{"args": ["--api-key", "a", "--verbose", "b", "--header", "X-Api-Key: c", "--token=d", "-H", "e", "--token", 5, "f"]}` + "\n",
			want: `{"args": ["--api-key", "[REDACTED]", "--verbose", "b", "--header", "[REDACTED]", "--token=d", "-H", "[REDACTED]", "--token", 5, "f"]}` + "\n",
		},
		{
			name: "json escapes and comments",
			rel:  "a.jsonc",
			in:   "{\"token\": \"a\\\"b\", // \"token\": \"c\"\n/* \"d\" */ \"e\": \"f\"}\n",
			want: "{\"token\": \"[REDACTED]\", // \"token\": \"c\"\n/* \"d\" */ \"e\": \"f\"}\n",
		},
		{
			name: "toml keys, tables and strings",
			rel:  "config.toml",
			in: "model = \"o3\" # api_key = \"c\"\n" +
				"api_key = 'a'\n" +
				"\"X-Api-Key\" = \"b\"\n" +
				"env.OPENAI = \"c\"\n" +
				"[mcp_servers.x]\n" +
				"command = \"x\"\n" +
				"env = { \"GITHUB_PERSONAL_ACCESS_TOKEN\" = \"d\", ORG = 'e', N = 1 }\n" +
				"env_http_headers = { \"X-Team\" = \"TEAM\" }\n" +
				"args = [\n  \"--token\",\n  \"f\",\n]\n" +
				"[mcp_servers.x.http_headers]\n" +
				"Accept = \"g\"\n" +
				"[[profiles]]\n" +
				"password = \"\"\"\nh\n\n\"\"\"\n" +
				"name = \"visible\"\n",
			want: "model = \"o3\" # api_key = \"c\"\n" +
				"api_key = '[REDACTED]'\n" +
				"\"X-Api-Key\" = \"[REDACTED]\"\n" +
				"env.OPENAI = \"[REDACTED]\"\n" +
				"[mcp_servers.x]\n" +
				"command = \"x\"\n" +
				"env = { \"GITHUB_PERSONAL_ACCESS_TOKEN\" = \"[REDACTED]\", ORG = '[REDACTED]', N = 1 }\n" +
				"env_http_headers = { \"X-Team\" = \"[REDACTED]\" }\n" +
				"args = [\n  \"--token\",\n  \"[REDACTED]\",\n]\n" +
				"[mcp_servers.x.http_headers]\n" +
				"Accept = \"[REDACTED]\"\n" +
				"[[profiles]]\n" +
				"password = \"[REDACTED]\"\n[REDACTED]\n\n\"[REDACTED]\"\n" +
				"name = \"visible\"\n",
		},
		{
			name: "toml seamark table stays readable",
			rel:  "config.toml",
			in:   "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n[mcp_servers.seamark.env]\nMODE = \"warn\"\n",
			want: "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n[mcp_servers.seamark.env]\nMODE = \"warn\"\n",
		},
		{
			name: "other files keep the shared redaction only",
			rel:  "AGENTS.md",
			in:   `"env": {"MODE": "x"}` + "\n",
			want: `"env": {"MODE": "x"}` + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, maskedFile(tt.rel, tt.in))
		})
	}
}

func TestPreviewRedactionHidesAuthorizationWithAnyScheme(t *testing.T) {
	// An argument list can carry a header where no key names it.
	tests := map[string]string{
		`"Authorization: Basic dXNlcjpwYXNz"`:      `"Authorization: [REDACTED]"`,
		`"authorization: token ghp_abc",`:          `"authorization: [REDACTED]",`,
		`"Authorization:dXNlcjpwYXNz"`:             `"Authorization:[REDACTED]"`,
		`"Authorization": "[REDACTED]",`:           `"Authorization": "[REDACTED]",`,
		`args = ["-H", "Authorization: Digest x"]`: `args = ["-H", "Authorization: [REDACTED]"]`,
	}

	for in, want := range tests {
		assert.Equal(t, want, scrubLine(".mcp.json", in), in)
	}

	assert.Equal(t, "Authorization: Basic dXNl", scrubLine("AGENTS.md", "Authorization: Basic dXNl"),
		"the rule is for native client files only")
}

func TestPreviewRedactionSurvivesMalformedFiles(t *testing.T) {
	// A stale file can hold anything. The scan never fails, never
	// recurses, and keeps the line count, so the diff stays aligned.
	for _, in := range []string{
		strings.Repeat("[", 100000) + "\"token\"",
		strings.Repeat("{", 100000),
		"{\"token\": \"open\nnext\": 1",
		"key = \"\"\"never closed\nline two",
		"]]]}}}::==,,\\\"",
		"",
	} {
		for _, rel := range []string{"a.json", "a.toml"} {
			side := newDiffSide(rel, []byte(in))
			assert.LessOrEqual(t, len(side.hide), len(side.lines), "%s: %.40q", rel, in)
			assert.NotPanics(t, func() { _ = maskedFile(rel, in) })
		}
	}

	// An unclosed string ends at its line. The scan goes on, and the
	// next string still counts as a value of the credential key.
	assert.Equal(t, "{\"token\": [REDACTED]\nnext[REDACTED]\n", maskedFile("a.json", "{\"token\": \"open\nnext\": 1}\n"))
}

func TestPreviewDiffEqualsDifflibOutput(t *testing.T) {
	// The preview writes the unified format itself, so each line can hide
	// its own ranges. Without hidden ranges it must equal difflib.
	pairs := [][2]string{
		{"", "a\nb\n"},
		{"a\nb\n", ""},
		{"a\n", "a\n"},
		{"1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n", "0\n1\n2\n3\nX\n5\n6\n7\n8\n9\n10\n11\nY\n13\n14\n15\n16\n"},
		{"a\nb\nc", "a\nB\nc\n"},
	}

	for _, pair := range pairs {
		before, after := newDiffSide("doc.txt", []byte(pair[0])), newDiffSide("doc.txt", []byte(pair[1]))

		want, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A: before.lines, B: after.lines, FromFile: "a/doc.txt", ToFile: "b/doc.txt", Context: previewContext,
		})
		require.NoError(t, err)

		assert.Equal(t, want, unifiedDiff("doc.txt", before, after), "%q -> %q", pair[0], pair[1])
	}
}

func TestPreviewUnchangedLineHidesWhatEitherSideHides(t *testing.T) {
	// A context line shows once. When only one side knows that the line
	// sits in a credential table, the line must still hide its value.
	before := newDiffSide("a.toml", []byte("[x.env]\nA = \"secret-a\"\n"))
	after := newDiffSide("a.toml", []byte("[x.plain]\nA = \"secret-a\"\nB = 1\n"))

	got := unifiedDiff("a.toml", before, after)

	assert.NotContains(t, got, "secret-a")
	assert.Contains(t, got, " A = \"[REDACTED]\"\n")
}

func TestPreviewKeepsControlSequencesInert(t *testing.T) {
	// A repository file can hold escape sequences. The preview must not
	// pass them to the terminal, and it must keep the line text visible.
	root := t.TempDir()
	writeRel(t, root, "doc.txt", "keep\n\x1b[31mred\x1b[0m\n\u009b2Jclear\n")

	reg := docRegistry(t, "doc.txt", "keep\n\x1b]0;title\x07new\u202eflip\n")
	got := onlyPreview(t, mustPlan(t, reg, SetupRequest{Root: root, Clients: docIntent()}))

	require.NoError(t, got.Err)
	assert.NotContains(t, got.Diff, "\x1b")
	assert.NotContains(t, got.Diff, "\x07")
	assert.NotContains(t, got.Diff, "\u009b")
	assert.NotContains(t, got.Diff, "\u202e")
	assert.Contains(t, got.Diff, "-[31mred[0m\n")
	assert.Contains(t, got.Diff, "-2Jclear\n")
	assert.Contains(t, got.Diff, "+]0;titlenewflip\n")
	assert.Equal(t, 1, got.Added)
	assert.Equal(t, 2, got.Removed)
}

func TestPreviewShowsAMissingFinalNewline(t *testing.T) {
	// A change of the final newline alone is still a change. The marker
	// line keeps the diff truthful and is not counted as a file line.
	root := t.TempDir()
	writeRel(t, root, "doc.txt", "one\ntwo")

	reg := docRegistry(t, "doc.txt", "one\ntwo\nthree\n")
	got := onlyPreview(t, mustPlan(t, reg, SetupRequest{Root: root, Clients: docIntent()}))

	assert.Equal(t, "--- a/doc.txt\n"+
		"+++ b/doc.txt\n"+
		"@@ -1,3 +1,3 @@\n"+
		" one\n"+
		" two\n"+
		"-\\ No newline at end of file\n"+
		"+three\n", got.Diff)
	assert.Equal(t, 1, got.Added)
	assert.Zero(t, got.Removed)
}

func TestPreviewDetectsAFileChangedAfterPlanning(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, "doc.txt", "old\n")

	reg := docRegistry(t, "doc.txt", "new\n")
	plan := mustPlan(t, reg, SetupRequest{Root: root, Clients: docIntent()})

	assert.False(t, onlyPreview(t, plan).Stale, "an unchanged file is current")

	writeRel(t, root, "doc.txt", "edited elsewhere\n")

	got := onlyPreview(t, plan)
	assert.True(t, got.Stale)
	require.NoError(t, got.Err)
	assert.Contains(t, got.Diff, "-edited elsewhere\n", "the diff shows the file as it is now")

	// Apply agrees with the preview: the plan no longer applies.
	_, err := ApplySetup(plan, ApplyOptions{})
	require.ErrorIs(t, err, ErrStalePlan)
}

func TestPreviewDetectsAStarterCreatedAfterPlanning(t *testing.T) {
	// A create-only document is guarded by its presence alone. The
	// preview never reads the new file: setup must not look inside it.
	root := t.TempDir()
	starter := scaffold(".seamark/lessons.yaml", "threshold: 2\n")
	starter.CreateOnly = true

	plan := mustPlan(t, integrationTestRegistry(t), SetupRequest{Root: root, Common: []Document{starter}})
	require.Equal(t, GuardPresence, plan.Reads[0].Kind)

	writeRel(t, root, ".seamark/lessons.yaml", "private: content\n")

	got := onlyPreview(t, plan)
	assert.True(t, got.Stale)
	assert.True(t, got.Created, "the plan was made for a new file")
	assert.NotContains(t, got.Diff, "private")
	assert.Contains(t, got.Diff, "+threshold: 2\n")
}

func TestPreviewReportsAReadFailure(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, "doc.txt", "old\n")

	reg := docRegistry(t, "doc.txt", "new\n")
	plan := mustPlan(t, reg, SetupRequest{Root: root, Clients: docIntent()})

	// The document becomes a directory: apply refuses it, and so does
	// the preview.
	require.NoError(t, os.Remove(filepath.Join(root, "doc.txt")))
	require.NoError(t, os.Mkdir(filepath.Join(root, "doc.txt"), 0o755))

	got := onlyPreview(t, plan)
	require.Error(t, got.Err)
	assert.Contains(t, got.Err.Error(), "not a regular file")
	assert.True(t, got.Stale)
	assert.Empty(t, got.Diff)
	assert.Zero(t, got.Added)
	assert.Zero(t, got.Removed)
}

func TestPreviewKeepsPlanOrderAndWritesNothing(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".gitignore", "bin/\n")

	reg := docRegistry(t, "doc.txt", "new\n")
	plan := mustPlan(t, reg, SetupRequest{
		Root: root, Clients: docIntent(),
		Common: []Document{
			scaffold(".seamark/policy.yaml", "mode: warn\n"),
			scaffold(".seamark/config.yaml", "index: {}\n"),
			{
				Path: ".gitignore", Detail: "carve-outs",
				Compose: func(existing []byte, _ bool) ([]byte, error) {
					return append(existing, ".seamark/*\n"...), nil
				},
			},
		},
	})
	before := treeFiles(t, root)

	previews := PreviewWrites(plan)
	require.Len(t, previews, len(plan.Writes))

	var got, want []string

	for i, p := range previews {
		got = append(got, p.Path)
		want = append(want, plan.Writes[i].Path)
	}

	assert.Equal(t, want, got)
	assert.Equal(t, []string{".seamark/policy.yaml", ".seamark/config.yaml", ".gitignore", "doc.txt"}, got)
	assert.Equal(t, before, treeFiles(t, root), "a preview writes nothing")
	assert.Nil(t, PreviewWrites(nil))
}

// integrationTestRegistry is a registry with no client, for plans of
// common documents alone.
func integrationTestRegistry(t *testing.T) *Registry {
	t.Helper()

	reg, err := NewRegistry()
	require.NoError(t, err)

	return reg
}
