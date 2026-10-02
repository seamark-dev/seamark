package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
)

// richConfig holds comments, blank lines, unknown top-level and nested
// keys, a duplicate key in an unowned section, and every owned section.
const richConfig = `# Team settings. Reviewed like code.
custom:            # unknown top-level key
  nested: [1, 2]
  dup: a
  dup: b           # a duplicate in an unowned section is harmless

agent:
  cli: claude      # preset
  model: opus      # unknown nested key

distill:
  write: false

reviews:
  window_days: 90  # three months

index:
  generated: false
  exclude:
    - vendor/
    # - "**/*_test.go"
`

// replaceOnce replaces the only occurrence of old in s.
func replaceOnce(t *testing.T, s, old, repl string) string {
	t.Helper()

	require.Equal(t, 1, strings.Count(s, old), "fixture text %q", old)

	return strings.Replace(s, old, repl, 1)
}

// planOf plans edits against a fresh snapshot of root.
func planOf(t *testing.T, root string, edits ...Edit) *Plan {
	t.Helper()

	p, err := PlanPatch(inspect(t, root), integration.Builtin(), edits)
	require.NoError(t, err)

	return p
}

// save plans and saves edits, and returns the result.
func save(t *testing.T, root string, edits ...Edit) Result {
	t.Helper()

	res, err := Save(planOf(t, root, edits...))
	require.NoError(t, err)

	return res
}

func TestSaveEachSettingPreservesTheRestOfTheFile(t *testing.T) {
	cases := []struct {
		name  string
		edits []Edit
		want  string
	}{
		{
			"set preset", []Edit{{Field: AgentCLI, String: "codex"}},
			replaceOnce(t, richConfig, "  cli: claude      # preset", "  cli: codex      # preset"),
		},
		{
			"reset preset", []Edit{{Field: AgentCLI, Reset: true}},
			replaceOnce(t, richConfig, "  cli: claude      # preset\n", ""),
		},
		{
			"enable pin writes", []Edit{{Field: DistillWrite, Bool: true}},
			replaceOnce(t, richConfig, "  write: false", "  write: true"),
		},
		{
			"reset pin writes removes the empty section", []Edit{{Field: DistillWrite, Reset: true}},
			replaceOnce(t, richConfig, "distill:\n  write: false\n\n", ""),
		},
		{
			"window days", []Edit{{Field: ReviewWindow, Days: 30}},
			replaceOnce(t, richConfig, "  window_days: 90  # three months", "  window_days: 30  # three months"),
		},
		{
			"window unlimited writes 0", []Edit{{Field: ReviewWindow, Unlimited: true}},
			replaceOnce(t, richConfig, "  window_days: 90  # three months", "  window_days: 0  # three months"),
		},
		{
			"reset window", []Edit{{Field: ReviewWindow, Reset: true}},
			replaceOnce(t, richConfig, "reviews:\n  window_days: 90  # three months\n\n", ""),
		},
		{
			"include generated", []Edit{{Field: IndexGenerated, Bool: true}},
			replaceOnce(t, richConfig, "  generated: false", "  generated: true"),
		},
		{
			"replace excludes keeps the comment", []Edit{{Field: IndexExclude, List: []string{"*.pb.go", "gen/"}}},
			replaceOnce(t, richConfig, "    - vendor/\n", "    - '*.pb.go'\n    - gen/\n"),
		},
		{
			"reset excludes keeps the comment", []Edit{{Field: IndexExclude, Reset: true}},
			replaceOnce(t, richConfig, "  exclude:\n    - vendor/\n", ""),
		},
		{
			"an empty list is a reset", []Edit{{Field: IndexExclude, List: []string{}}},
			replaceOnce(t, richConfig, "  exclude:\n    - vendor/\n", ""),
		},
		{
			"two index fields in one plan",
			[]Edit{{Field: IndexExclude, List: []string{"gen/"}}, {Field: IndexGenerated, Reset: true}},
			replaceOnce(t, replaceOnce(t, richConfig, "  generated: false\n", ""), "    - vendor/\n", "    - gen/\n"),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withConfig(t, richConfig)
			require.NoError(t, os.WriteFile(filepath.Join(root, "other.txt"), []byte("x"), 0o644))

			before := tree(t, root)

			res := save(t, root, c.edits...)
			assert.True(t, res.Written)
			assert.False(t, res.Created)
			assert.Equal(t, configPath(inspect(t, root).Root), res.Path)

			assert.Equal(t, c.want, readConfig(t, root))

			after := tree(t, root)
			delete(before, filepath.Join(".seamark", "config.yaml"))
			delete(after, filepath.Join(".seamark", "config.yaml"))
			assert.Equal(t, before, after, "only config.yaml changes")
		})
	}
}

func TestSavedValuesReadBackThroughTheCommandReaders(t *testing.T) {
	root := withConfig(t, richConfig)

	save(t, root, Edit{Field: AgentCLI, String: "codex"})
	save(t, root, Edit{Field: DistillWrite, Bool: true})
	save(t, root, Edit{Field: ReviewWindow, Unlimited: true})
	save(t, root, Edit{Field: IndexGenerated, Bool: true}, Edit{Field: IndexExclude, List: []string{"*.pb.go"}})

	acfg, err := agent.LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, "codex", acfg.Agent.CLI)

	dcfg, err := distill.LoadConfig(root)
	require.NoError(t, err)
	assert.True(t, dcfg.Distill.Write)

	icfg, err := index.LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, -1, icfg.ReviewWindowDays(), "0 means unlimited")
	assert.True(t, icfg.Index.Generated)
	assert.Equal(t, []string{"*.pb.go"}, icfg.Index.Exclude)

	assert.Contains(t, readConfig(t, root), "  model: opus      # unknown nested key", "unknown keys survive")
	assert.Contains(t, readConfig(t, root), "  dup: b           #", "unowned duplicates survive")
}

func TestSaveOnTheStarterFile(t *testing.T) {
	starter := starterConfig(t)

	t.Run("exclude keeps the commented example", func(t *testing.T) {
		root := withConfig(t, starter)

		save(t, root, Edit{Field: IndexExclude, List: []string{"*.pb.go"}})

		assert.Equal(t, replaceOnce(t, starter,
			"  exclude:\n    # - \"**/*_test.go\"",
			"  exclude:\n    - '*.pb.go'\n    # - \"**/*_test.go\""), readConfig(t, root))
	})

	t.Run("reset generated removes only its line", func(t *testing.T) {
		root := withConfig(t, starter)

		save(t, root, Edit{Field: IndexGenerated, Reset: true})

		assert.Equal(t, replaceOnce(t, starter, "  generated: false\n", ""), readConfig(t, root))
	})

	t.Run("a new section goes after the commented examples", func(t *testing.T) {
		root := withConfig(t, starter)

		save(t, root, Edit{Field: DistillWrite, Bool: true})

		assert.Equal(t, starter+"\ndistill:\n  write: true\n", readConfig(t, root))
	})

	t.Run("a section with only comments stays", func(t *testing.T) {
		root := withConfig(t, starter)

		save(t, root, Edit{Field: IndexGenerated, Reset: true}, Edit{Field: IndexExclude, Reset: true})

		want := replaceOnce(t, starter, "  generated: false\n", "")
		want = replaceOnce(t, want, "  exclude:\n", "")
		assert.Equal(t, want, readConfig(t, root))
	})
}

func TestSaveCreatesAMissingFileWithTheSelectedKeysOnly(t *testing.T) {
	root := workspace(t, nil)

	p := planOf(t, root, Edit{Field: DistillWrite, Bool: true})
	assert.True(t, p.Preview.Created)
	assert.Equal(t, 2, p.Preview.Added)
	assert.Contains(t, p.Preview.Diff, "+distill:\n+  write: true\n")

	res, err := Save(p)
	require.NoError(t, err)
	assert.True(t, res.Written)
	assert.True(t, res.Created)

	assert.Equal(t, "distill:\n  write: true\n", readConfig(t, root))
	assert.Equal(t, []string{".seamark/config.yaml"}, keys(tree(t, root)), "no other scaffold file")
}

// keys lists the slash paths of a tree.
func keys(m map[string][32]byte) []string {
	out := make([]string, 0, len(m))

	for k := range m {
		out = append(out, filepath.ToSlash(k))
	}

	return out
}

func TestResetOnAMissingFileIsANoOp(t *testing.T) {
	root := workspace(t, nil)

	p := planOf(t, root, Edit{Field: AgentCLI, Reset: true})
	assert.True(t, p.NoOp)
	assert.Nil(t, p.Setup)

	res, err := Save(p)
	require.NoError(t, err)
	assert.False(t, res.Written)

	_, err = os.Stat(filepath.Join(root, ".seamark"))
	assert.ErrorIs(t, err, os.ErrNotExist, "a no-op creates nothing")
}

func TestUnchangedValueIsANoOp(t *testing.T) {
	root := withConfig(t, richConfig)

	p := planOf(t, root, Edit{Field: AgentCLI, String: "claude"})
	assert.True(t, p.NoOp)
	assert.Equal(t, []string{"Nothing changes: the file already holds these values."}, p.Notes)
	assert.Empty(t, p.Activation)

	res, err := Save(p)
	require.NoError(t, err)
	assert.False(t, res.Written)
	assert.Equal(t, richConfig, readConfig(t, root))
}

func TestSpliceShapes(t *testing.T) {
	cases := []struct {
		name, before string
		edits        []Edit
		after        string
	}{
		{
			"empty file", "",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"agent:\n  cli: codex\n",
		},
		{
			"comment-only file", "# settings\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"# settings\n\nagent:\n  cli: codex\n",
		},
		{
			"null section", "agent:\nindex:\n  generated: true\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"agent:\n  cli: codex\nindex:\n  generated: true\n",
		},
		{
			"null token on the section line", "agent: ~  # pick one\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"agent:  # pick one\n  cli: codex\n",
		},
		{
			"empty value keeps its comment", "agent:\n  cli:   # pick one\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"agent:\n  cli: codex   # pick one\n",
		},
		{
			"quoted value", "agent:\n  cli: \"claude\" # q\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"agent:\n  cli: codex # q\n",
		},
		{
			"new key follows the child indentation", "index:\n    generated: true\n",
			[]Edit{{Field: IndexExclude, List: []string{"gen/"}}},
			"index:\n    generated: true\n    exclude:\n      - gen/\n",
		},
		{
			"null token value keeps its comment", "distill:\n  write: ~ # unset\n",
			[]Edit{{Field: DistillWrite, Bool: true}},
			"distill:\n  write: true # unset\n",
		},
		{
			"tagged value loses its tag", "distill:\n  write: !!bool true # t\n",
			[]Edit{{Field: DistillWrite, Bool: false}},
			"distill:\n  write: false # t\n",
		},
		{
			"yaml 1.1 bool becomes canonical", "distill:\n  write: yes\n",
			[]Edit{{Field: DistillWrite, Bool: false}},
			"distill:\n  write: false\n",
		},
		{
			"flow list becomes a block list", "index:\n  exclude: ['a', \"b\"]  # c\n",
			[]Edit{{Field: IndexExclude, List: []string{"x/"}}},
			"index:\n  exclude:  # c\n    - x/\n",
		},
		{
			"items at the key indentation keep it", "index:\n  exclude:\n  - a\n  - b\n",
			[]Edit{{Field: IndexExclude, List: []string{"c"}}},
			"index:\n  exclude:\n  - c\n",
		},
		{
			"patterns are quoted as needed", "",
			[]Edit{{Field: IndexExclude, List: []string{"*.pb.go", "on", "5", "**/*_test.go"}}},
			"index:\n  exclude:\n    - '*.pb.go'\n    - \"on\"\n    - \"5\"\n    - '**/*_test.go'\n",
		},
		{
			"file without a final newline", "distill:\n  write: false",
			[]Edit{{Field: DistillWrite, Bool: true}},
			"distill:\n  write: true",
		},
		{
			"append to a file without a final newline", "distill:\n  write: false",
			[]Edit{{Field: ReviewWindow, Days: 30}},
			"distill:\n  write: false\n\nreviews:\n  window_days: 30\n",
		},
		{
			"indented top level", "  distill:\n    write: false\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"  distill:\n    write: false\n\n  agent:\n    cli: codex\n",
		},
		{
			"document end marker", "distill:\n  write: false\n...\n# tail\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"distill:\n  write: false\n\nagent:\n  cli: codex\n...\n# tail\n",
		},
		{
			"head comment keeps a bare section", "# who runs\nagent:\n  cli: codex\n",
			[]Edit{{Field: AgentCLI, Reset: true}},
			"# who runs\nagent:\n",
		},
		{
			"last section goes with its blank line", "a: 1\n\nagent:\n  cli: codex\n",
			[]Edit{{Field: AgentCLI, Reset: true}},
			"a: 1\n",
		},
		{
			"only section leaves an empty file", "agent:\n  cli: codex\n",
			[]Edit{{Field: AgentCLI, Reset: true}},
			"",
		},
		{
			"block scalar is removed whole", "agent:\n  cli: |\n    # not a comment\n    codex\n",
			[]Edit{{Field: AgentCLI, Reset: true}},
			"",
		},
		{
			"CRLF line breaks stay", "distill:\r\n  write: false\r\n",
			[]Edit{{Field: DistillWrite, Bool: true}},
			"distill:\r\n  write: true\r\n",
		},
		{
			"CRLF append", "distill:\r\n  write: false\r\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"distill:\r\n  write: false\r\n\r\nagent:\r\n  cli: codex\r\n",
		},
		{
			"multi-byte text before the value", "agent:\n  cli: claude # ü\n  é: 1\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"agent:\n  cli: codex # ü\n  é: 1\n",
		},
		// Review fix: a quote character inside a plain value is text. It
		// must not hide the comment after the value.
		{
			"plain value with an apostrophe keeps its comment", "agent:\n  cli: it's # my note\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"agent:\n  cli: codex # my note\n",
		},
		{
			"broken preset with an apostrophe keeps its comment", "agent:\n  cli: o'brien  # keep me\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"agent:\n  cli: codex  # keep me\n",
		},
		{
			"plain value with a double quote keeps its comment", "agent:\n  cli: o\"x  # note\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"agent:\n  cli: codex  # note\n",
		},
		{
			"flow list with an apostrophe keeps its comment", "index:\n  exclude: [it's]  # note\n",
			[]Edit{{Field: IndexExclude, List: []string{"vendor/"}}},
			"index:\n  exclude:  # note\n    - vendor/\n",
		},
		{
			"flow list with quotes and a quoted hash keeps its comment", "index:\n  exclude: [a 'b, 'c #d']  # note\n",
			[]Edit{{Field: IndexExclude, List: []string{"x/"}}},
			"index:\n  exclude:  # note\n    - x/\n",
		},
		// Review fix: yaml.v3 puts the column of a tagged value at its tag.
		// The splice finds the quoted text after the tag.
		{
			"tagged double-quoted value", "agent:\n  cli: !!str \"claude\"  # c\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"agent:\n  cli: codex  # c\n",
		},
		{
			"tagged single-quoted value", "agent:\n  cli: !!str 'claude'  # c\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"agent:\n  cli: codex  # c\n",
		},
		{
			"bare null tag keeps its comment", "agent:\n  cli: !!null # c\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"agent:\n  cli: codex # c\n",
		},
		{
			"tagged null section", "agent: !!null  # pick one\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"agent:  # pick one\n  cli: codex\n",
		},
		{
			"empty section line stays as it is", "agent:   \nindex:\n  generated: true\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"agent:   \n  cli: codex\nindex:\n  generated: true\n",
		},
		{
			"tagged block list", "index:\n  exclude: !!seq  # c\n    - a\n",
			[]Edit{{Field: IndexExclude, List: []string{"a", "b"}}},
			"index:\n  exclude:  # c\n    - a\n    - b\n",
		},
		// Review fix: every line of a multi-line value is value text, also
		// when it starts with "#". A reset removes all of them.
		{
			"multi-line quoted value is removed whole", "agent:\n  cli: \"cla\n    # ude\"\n  argv: [x]\n",
			[]Edit{{Field: AgentCLI, Reset: true}},
			"agent:\n  argv: [x]\n",
		},
		{
			"quoted continuation at column 0 is removed whole", "agent:\n  cli: 'cla\n# ude'\n  argv: [x]\n",
			[]Edit{{Field: AgentCLI, Reset: true}},
			"agent:\n  argv: [x]\n",
		},
		{
			"a comment after a multi-line value stays", "agent:\n  cli: \"cla\n    # ude\"\n    # real comment\n  argv: [x]\n",
			[]Edit{{Field: AgentCLI, Reset: true}},
			"agent:\n    # real comment\n  argv: [x]\n",
		},
		{
			"block scalar item goes with its list", "agent:\n  cli: codex\n  argv:\n    - |\n      # x\n",
			[]Edit{{Field: AgentArgv, Reset: true}},
			"agent:\n  cli: codex\n",
		},
		// Review fix: yaml.v3 does not count a byte order mark as a column.
		{
			"byte order mark before a null section", "\ufeffagent: ~  # pick one\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"\ufeffagent:  # pick one\n  cli: codex\n",
		},
		{
			"byte order mark before a quoted section key", "\ufeff\"agent\":\n  cli: claude\n",
			[]Edit{{Field: AgentCLI, String: "codex"}},
			"\ufeff\"agent\":\n  cli: codex\n",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withConfig(t, c.before)

			save(t, root, c.edits...)
			assert.Equal(t, c.after, readConfig(t, root))
		})
	}
}

func TestExcludeItemsThatStayKeepTheirLines(t *testing.T) {
	cases := []struct {
		name, before string
		list         []string
		after        string
	}{
		{
			"an added item leaves the old item and its comment",
			"index:\n  exclude:\n    - vendor/  # third-party\n",
			[]string{"vendor/", "gen/"},
			"index:\n  exclude:\n    - vendor/  # third-party\n    - gen/\n",
		},
		{
			"a new order moves the lines with their comments",
			"index:\n  exclude:\n    - a # one\n    - b # two\n",
			[]string{"b", "a"},
			"index:\n  exclude:\n    - b # two\n    - a # one\n",
		},
		{
			"a removed item goes with its comment",
			"index:\n  exclude:\n    - a # one\n    - b\n",
			[]string{"c"},
			"index:\n  exclude:\n    - c\n",
		},
		{
			"an item that is not a string gets new text and keeps its comment",
			"index:\n  exclude:\n    - 5  # five\n",
			[]string{"5", "gen/"},
			"index:\n  exclude:\n    - \"5\"  # five\n    - gen/\n",
		},
		{
			"the same item twice keeps both lines",
			"index:\n  exclude:\n    - a # first\n    - a # second\n",
			[]string{"a", "a", "b"},
			"index:\n  exclude:\n    - a # first\n    - a # second\n    - b\n",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withConfig(t, c.before)

			save(t, root, Edit{Field: IndexExclude, List: c.list})
			assert.Equal(t, c.after, readConfig(t, root))
		})
	}
}

func TestAddingAnExcludeShowsOneAddedLine(t *testing.T) {
	root := withConfig(t, "index:\n  exclude:\n    - 'vendor/'  # third-party\n")

	p := planOf(t, root, Edit{Field: IndexExclude, List: []string{"vendor/", "gen/"}})
	assert.Equal(t, 1, p.Preview.Added)
	assert.Equal(t, 0, p.Preview.Removed)

	same := planOf(t, root, Edit{Field: IndexExclude, List: []string{"vendor/"}})
	assert.True(t, same.NoOp, "the quoted old item already holds the value")
}

func TestPresetUnderArgvNeedsTheExplicitRemoval(t *testing.T) {
	const config = "agent:\n  cli: claude\n  argv: [\"my-llm\", \"--print\"]  # our wrapper\ndistill:\n  write: true\n"

	root := withConfig(t, config)
	snap := inspect(t, root)

	// A preset alone would be saved but stay ineffective (R-12).
	_, err := PlanPatch(snap, integration.Builtin(), []Edit{{Field: AgentCLI, String: "codex"}})
	require.ErrorIs(t, err, ErrInvalidEdit)
	assert.Contains(t, err.Error(), "agent.argv overrides agent.cli")

	p, err := PlanPatch(snap, integration.Builtin(), []Edit{
		{Field: AgentCLI, String: "codex"},
		{Field: AgentArgv, Reset: true},
	})
	require.NoError(t, err)

	assert.Contains(t, p.Preview.Diff, "-  argv: [\"my-llm\", \"--print\"]  # our wrapper\n", "the preview shows the removal")
	assert.Contains(t, p.Preview.Diff, "+  cli: codex\n")

	before := map[Field]string{}
	for _, st := range p.Before {
		before[st.Field] = st.Effective
	}

	after := map[Field]string{}
	for _, st := range p.After {
		after[st.Field] = st.Effective
	}

	assert.Equal(t, map[Field]string{AgentCLI: "custom command (agent.argv)", AgentArgv: "my-llm --print"}, before)
	assert.Equal(t, map[Field]string{AgentCLI: "codex", AgentArgv: "none"}, after)

	_, err = Save(p)
	require.NoError(t, err)
	assert.Equal(t, "agent:\n  cli: codex\ndistill:\n  write: true\n", readConfig(t, root))
}

func TestResetPresetUnderArgvSaysNothingEffectiveChanges(t *testing.T) {
	root := withConfig(t, "agent:\n  cli: codex\n  argv: [\"my-llm\"]\n")

	p := planOf(t, root, Edit{Field: AgentCLI, Reset: true})

	require.Len(t, p.Activation, 1)
	assert.Contains(t, p.Activation[0].Notes, "agent.argv stays in effect, so the command that runs does not change.")
	assert.Equal(t, p.Before[0].Effective, p.After[0].Effective)
}

func TestArgvRemovalMustLeaveAResolvableAgent(t *testing.T) {
	root := withConfig(t, "agent:\n  cli: bogus\n  argv: [\"my-llm\"]\n")

	_, err := PlanPatch(inspect(t, root), integration.Builtin(), []Edit{{Field: AgentArgv, Reset: true}})
	require.ErrorIs(t, err, ErrInvalidEdit)
	assert.Contains(t, err.Error(), `unknown agent cli "bogus"`)
}

func TestInvalidEditsAreRejected(t *testing.T) {
	cases := []struct {
		name  string
		edits []Edit
		want  string
	}{
		{"no edits", nil, "no edits"},
		{"two groups", []Edit{{Field: DistillWrite, Bool: true}, {Field: IndexGenerated, Bool: true}}, "one plan edits one settings group"},
		{"same field twice", []Edit{{Field: IndexGenerated, Bool: true}, {Field: IndexGenerated, Reset: true}}, "appears twice"},
		{"unknown field", []Edit{{Field: "index.depth", Bool: true}}, `unknown field "index.depth"`},
		{"unknown preset", []Edit{{Field: AgentCLI, String: "hal9000"}}, "agent.cli must be one of: claude, codex"},
		{"argv authoring", []Edit{{Field: AgentArgv}}, "agent.argv can only be removed here"},
		{"negative window", []Edit{{Field: ReviewWindow, Days: -5}}, "cannot be negative"},
		{"zero window", []Edit{{Field: ReviewWindow}}, "choose a positive day count, unlimited, or the default"},
		{"days and unlimited", []Edit{{Field: ReviewWindow, Days: 3, Unlimited: true}}, "not both"},
		{"bad pattern", []Edit{{Field: IndexExclude, List: []string{"a/**/b.go"}}}, "exclude \"a/**/b.go\": `**` is only supported as a leading `**/`"},
		{"empty pattern", []Edit{{Field: IndexExclude, List: []string{""}}}, "empty pattern"},
		{"control character", []Edit{{Field: IndexExclude, List: []string{"a\nb"}}}, "control character"},
		{"reset with a value", []Edit{{Field: DistillWrite, Reset: true, Bool: true}}, "does not take that kind of value"},
		{"foreign value", []Edit{{Field: DistillWrite, String: "on"}}, "does not take that kind of value"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withConfig(t, richConfig)

			_, err := PlanPatch(inspect(t, root), integration.Builtin(), c.edits)
			require.ErrorIs(t, err, ErrInvalidEdit)
			assert.Contains(t, err.Error(), c.want)
			assert.Equal(t, richConfig, readConfig(t, root))
		})
	}
}

func TestPlanPatchRejectsInvalidArguments(t *testing.T) {
	_, err := PlanPatch(nil, integration.Builtin(), []Edit{{Field: DistillWrite, Bool: true}})
	require.Error(t, err)

	_, err = PlanPatch(inspect(t, t.TempDir()), nil, []Edit{{Field: DistillWrite, Bool: true}})
	require.Error(t, err)

	_, err = PlanPatch(&Snapshot{}, integration.Builtin(), []Edit{{Field: DistillWrite, Bool: true}})
	require.Error(t, err, "a snapshot must come from Inspect")
}

func TestSetThenResetRestoresTheStarterBytes(t *testing.T) {
	starter := starterConfig(t)
	root := withConfig(t, starter)

	save(t, root, Edit{Field: DistillWrite, Bool: true})
	require.NotEqual(t, starter, readConfig(t, root))

	save(t, root, Edit{Field: DistillWrite, Reset: true})
	assert.Equal(t, starter, readConfig(t, root))
}

func TestActivationNotesPerGroup(t *testing.T) {
	cases := []struct {
		edit Edit
		when string
		note string
	}{
		{Edit{Field: AgentCLI, String: "codex"}, "next prepared distillation", "Claude and Codex integration files do not change."},
		{Edit{Field: DistillWrite, Bool: true}, "next proposal preview", "Saving applies no proposal; a new proposal preview is necessary."},
		{Edit{Field: ReviewWindow, Days: 30}, "next confirmed evidence refresh", "The newest 200 review comments are always kept."},
		{Edit{Field: IndexGenerated, Bool: true}, "next explicit index build", "Nothing is scanned or removed until the build."},
	}

	for _, c := range cases {
		p := planOf(t, withConfig(t, richConfig), c.edit)

		require.Len(t, p.Activation, 1)
		assert.Equal(t, c.edit.Field, p.Activation[0].Field)
		assert.Equal(t, c.when, p.Activation[0].When)
		assert.Contains(t, p.Activation[0].Notes, c.note)
		assert.Contains(t, p.Notes, "The index reads as stale until the next index build, because its fingerprint covers the whole configuration file.")
		assert.Contains(t, p.Notes, "Review the diff, then commit the file so the team shares it.")
	}
}

// Review fix: the preview stated "The file is committed" also for a new
// or untracked file. The note must be advice that is true in every case.
func TestSaveNotesMakeNoClaimAboutGit(t *testing.T) {
	for _, root := range []string{withConfig(t, richConfig), workspace(t, nil)} {
		p := planOf(t, root, Edit{Field: DistillWrite, Bool: true})

		require.NotEmpty(t, p.Notes)

		for _, note := range p.Notes {
			assert.NotContains(t, note, "is committed", "the plan does not ask git whether the file is tracked")
		}
	}
}

func TestInferenceSaveLeavesIntegrationFilesAlone(t *testing.T) {
	root := withConfig(t, richConfig)

	for rel, body := range map[string]string{
		".claude/settings.json": `{"hooks": {}}`,
		".mcp.json":             `{"mcpServers": {"seamark": {"command": "seamark"}}}`,
		".codex/config.toml":    "[mcp_servers.seamark]\ncommand = \"seamark\"\n",
		"AGENTS.md":             "# agents\n",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644))
	}

	before := tree(t, root)

	save(t, root, Edit{Field: AgentCLI, String: "codex"})

	after := tree(t, root)
	assert.NotEqual(t, before[filepath.Join(".seamark", "config.yaml")], after[filepath.Join(".seamark", "config.yaml")])

	delete(before, filepath.Join(".seamark", "config.yaml"))
	delete(after, filepath.Join(".seamark", "config.yaml"))
	assert.Equal(t, before, after)
}

func TestPreviewRedactsSecretShapedArgv(t *testing.T) {
	root := withConfig(t, "agent:\n  argv: [\"my-llm\", \"--token=abc123\"]\n")

	p := planOf(t, root, Edit{Field: AgentArgv, Reset: true})

	assert.Contains(t, p.Preview.Diff, "--token=[REDACTED]")
	assert.NotContains(t, p.Preview.Diff, "abc123")
	assert.Equal(t, File, p.Preview.Path)
	assert.Equal(t, 2, p.Preview.Removed)
}
