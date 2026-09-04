package bench

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowPreflightHermetic(t *testing.T) {
	for _, instance := range WorkflowInstances() {
		t.Run(instance.ID, func(t *testing.T) {
			err := WorkflowPreflight(context.Background(), WorkflowConfig{
				Instance: instance, SeamarkBin: "/opt/seamark/bin/seamark", PrepareIndex: false,
			})
			require.NoError(t, err)
		})
	}

	err := WorkflowPreflight(context.Background(), WorkflowConfig{Instance: SchemaSyncCochangeInstance()})
	require.ErrorContains(t, err, "preflight requires the seamark binary")

	err = WorkflowPreflight(context.Background(), WorkflowConfig{
		Instance: SchemaSyncCochangeInstance(), SeamarkBin: "/opt/seamark", Arms: []WorkflowArm{"future"},
	})
	require.ErrorContains(t, err, "unknown workflow arm")
}

func TestParseWhyPartnersReadsTheSectionLabels(t *testing.T) {
	output := `server/schema.py

defines (1)
  var      line 2     server/schema.SCHEMAS

usually changed with  (empirical, lift > 1 means beyond chance)
   3/6   commits  lift 2.0   server/presenters.py  · mostly workspace_summary
   2/6   commits  lift 1.3   web/src/api/generated.ts
   1/6   commits  lift 0.9   web/src/api/client.ts

recent decisions
   2/6   commits  lift 9.9   not/a/partner.py
`

	partners := parseWhyPartners(output)
	assert.Equal(t, map[string]int{
		"server/presenters.py": 3, "web/src/api/generated.ts": 2, "web/src/api/client.ts": 1,
	}, partners)
	assert.NotContains(t, partners, "not/a/partner.py", "lines outside the section are ignored")

	assert.Empty(t, parseWhyPartners("server/schema.py\n\ndefines (1)\n"))
}

func TestMCPServerNameReadsTheInitializeReply(t *testing.T) {
	name, err := mcpServerName([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"seamark","version":"dev"}}}` + "\n"))
	require.NoError(t, err)
	assert.Equal(t, "seamark", name)

	_, err = mcpServerName([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`))
	require.ErrorContains(t, err, "initialize failed")

	_, err = mcpServerName([]byte("not json\n"))
	require.ErrorContains(t, err, "not JSON-RPC")

	_, err = mcpServerName(nil)
	require.ErrorContains(t, err, "no reply")
}

func TestValidateWorkflowWiringNamesTheDefect(t *testing.T) {
	instance := SchemaSyncCochangeInstance()
	cfg := WorkflowConfig{SeamarkBin: "/opt/seamark/bin/seamark"}

	wire := func(t *testing.T) (only, withSkills string) {
		t.Helper()

		only = filepath.Join(t.TempDir(), "only")
		withSkills = filepath.Join(t.TempDir(), "skills")
		require.NoError(t, instance.Generate(only))
		require.NoError(t, instance.Generate(withSkills))

		_, err := wireWorkflowArm(context.Background(), only, cfg, ArmMCPOnly)
		require.NoError(t, err)
		_, err = wireWorkflowArm(context.Background(), withSkills, cfg, ArmMCPSkills)
		require.NoError(t, err)

		return only, withSkills
	}

	cases := []struct {
		name   string
		mutate func(t *testing.T, only, withSkills string)
		want   string
	}{
		{"skills missing", func(t *testing.T, _, withSkills string) {
			require.NoError(t, os.RemoveAll(filepath.Join(withSkills, ".claude", "skills")))
		}, "not the 3 managed copies"},
		{"skills leaked into mcp-only", func(t *testing.T, only, withSkills string) {
			require.NoError(t, os.Rename(filepath.Join(withSkills, ".claude", "skills"), filepath.Join(only, ".claude", "skills")))
		}, "mcp-only arm contains .claude/skills"},
		{"lesson installed", func(t *testing.T, only, _ string) {
			require.NoError(t, writeLessons(only, schemaSyncLessonYAML))
		}, "contains .seamark/lessons.yaml"},
		{"mcp registration file", func(t *testing.T, _, withSkills string) {
			require.NoError(t, os.WriteFile(filepath.Join(withSkills, ".mcp.json"), []byte("{}"), 0o644))
		}, "contains .mcp.json"},
		{"hook", func(t *testing.T, only, _ string) {
			require.NoError(t, writeAgentSettings(only, "seamark lessons --hook", ""))
		}, "settings contain a hook"},
		{"rules missing", func(t *testing.T, only, _ string) {
			require.NoError(t, writeAgentSettings(only, "", ""))
		}, "lack the allow rule mcp__seamark__orient"},
		{"skill rules in mcp-only", func(t *testing.T, only, _ string) {
			require.NoError(t, writeWorkflowSettings(only, []string{"mcp__seamark__orient", "mcp__seamark__why",
				"mcp__seamark__change_set", "mcp__seamark__check", "mcp__seamark__expand", "Skill(seamark-plan-change)"}))
		}, "contain the skill rule"},
		{"skill rules missing", func(t *testing.T, _, withSkills string) {
			rules, err := workflowAllowRules(ArmMCPOnly)
			require.NoError(t, err)
			require.NoError(t, writeWorkflowSettings(withSkills, rules))
		}, "lack the allow rule Skill("},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			only, withSkills := wire(t)
			tc.mutate(t, only, withSkills)

			err := validateWorkflowWiring(only, withSkills)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
