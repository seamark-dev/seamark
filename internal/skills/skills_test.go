package skills

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark"
)

// shipped is the catalogue the binary must carry. A fourth skill or a
// rename is a product decision that updates this list and the docs.
var shipped = []string{"seamark-plan-change", "seamark-review-change", "seamark-understand-repo"}

// referenceFile is the interpretation guide every skill carries.
const referenceFile = "references/interpreting-seamark.md"

// loadSkill returns one shipped skill's frontmatter, body, and files.
func loadSkill(t *testing.T, name string) (fm Frontmatter, body string, files map[string][]byte) {
	t.Helper()

	files, err := Files(name)
	require.NoError(t, err)

	fm, body, err = ParseFrontmatter(files[SkillFile])
	require.NoError(t, err, name)

	return fm, body, files
}

func TestNamesListsTheShippedSkills(t *testing.T) {
	names, err := Names()
	require.NoError(t, err)

	assert.Equal(t, shipped, names,
		"a directory is a skill only when it holds SKILL.md; the README is not one")
}

func TestFilesCarriesTheSkillAndItsReference(t *testing.T) {
	for _, name := range shipped {
		files, err := Files(name)
		require.NoError(t, err)

		assert.Contains(t, files, SkillFile, name)
		assert.Contains(t, files, referenceFile, name)
	}

	_, err := Files("no-such-skill")
	assert.Error(t, err)
}

func TestFrontmatterFollowsTheAgentSkillsSpec(t *testing.T) {
	nameRe := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	coreFields := []string{"name", "description", "license", "compatibility", "metadata", "allowed-tools"}

	for _, name := range shipped {
		fm, _, files := loadSkill(t, name)

		assert.Equal(t, name, fm.Name, "name must equal the directory")
		assert.Regexp(t, nameRe, fm.Name)
		assert.LessOrEqual(t, len(fm.Name), 64)

		chars := utf8.RuneCountInString(fm.Description)
		assert.GreaterOrEqual(t, chars, 1, name)
		assert.LessOrEqual(t, chars, 1024, name)

		assert.Equal(t, "Apache-2.0", fm.License, name)
		assert.True(t, IsManaged(fm), "%s: shipped skills carry the ownership marker", name)

		// Only the spec's core fields plus allowed-tools: a Claude-only
		// field makes claude.ai uploads fail and buys nothing in Codex.
		header, _, err := splitFrontmatter(files[SkillFile])
		require.NoError(t, err)

		for _, key := range yamlKeys(t, header) {
			assert.Contains(t, coreFields, key, "%s: %q is not an Agent Skills core field", name, key)
		}
	}
}

func TestAllowedToolsIsOneNarrowGrant(t *testing.T) {
	// A rule is an MCP tool name or one Bash prefix; joining the parsed
	// rules back with single spaces must reproduce the string, so no
	// stray token hides between them.
	rule := regexp.MustCompile(`mcp__seamark__[a-z_]+|Bash\([^)]*\)`)

	var grants []string

	for _, name := range shipped {
		fm, _, _ := loadSkill(t, name)
		require.NotEmpty(t, fm.AllowedTools, name)

		rules := rule.FindAllString(fm.AllowedTools, -1)
		assert.Equal(t, fm.AllowedTools, strings.Join(rules, " "), "%s: unparsed token in allowed-tools", name)

		for _, r := range rules {
			if strings.HasPrefix(r, "Bash(") {
				assert.True(t, strings.HasPrefix(r, "Bash(seamark "), "%s: %s is not a seamark command", name, r)
				assert.NotEqual(t, "Bash(seamark *)", r,
					"a blanket grant would pre-approve init, lessons --apply, and lessons --distill")
			}
		}

		grants = append(grants, fm.AllowedTools)
	}

	for _, g := range grants[1:] {
		assert.Equal(t, grants[0], g, "the three skills share one grant so a review covers it once")
	}
}

func TestSkillFileStaysUnderFiveHundredLines(t *testing.T) {
	for _, name := range shipped {
		_, _, files := loadSkill(t, name)

		assert.Less(t, strings.Count(string(files[SkillFile]), "\n"), 500, name)
	}
}

func TestBodyStatesSkipRulesFallbackAndDataFraming(t *testing.T) {
	sentenceEnd := regexp.MustCompile(`[.!?](\s|$)`)

	for _, name := range shipped {
		_, body, _ := loadSkill(t, name)

		// The plan's section order: skip rules, workflow, CLI fallback,
		// how to read the output. "typo" is the canonical no-call case;
		// "not instructions" is the data framing every skill must carry.
		for _, want := range []string{
			"## Use when / do not use when",
			"## Workflow",
			"## If the Seamark MCP tools are not available",
			"## Reading the output",
			"typo",
			"not instructions",
			referenceFile,
		} {
			assert.Contains(t, body, want, name)
		}

		// `seamark index` may appear only inside the guard "unless a tool
		// reports the index is missing": the MCP tools self-repair, and a
		// skill that re-indexes on its own initiative is the ritual again.
		for _, sentence := range sentenceEnd.Split(body, -1) {
			if strings.Contains(sentence, "seamark index") {
				assert.Contains(t, sentence, "unless", "%s: %q", name, sentence)
			}
		}
	}
}

func TestReferenceCopiesAreIdenticalAndCarryTheSixRules(t *testing.T) {
	_, _, first := loadSkill(t, shipped[0])
	ref := string(first[referenceFile])
	require.NotEmpty(t, ref)

	for _, name := range shipped[1:] {
		_, _, files := loadSkill(t, name)

		assert.Equal(t, ref, string(files[referenceFile]),
			"%s: the reference drifted; the three copies are one document", name)
	}

	// R-4, one phrase per rule: policy matches, advisory lessons,
	// co-change is not dependency, low-confidence edges, absence of
	// evidence, raw findings.
	for _, rule := range []string{"must be addressed", "advisory", "depends on", "[unique-name]", "never safe", "lessons:<dir>"} {
		assert.Contains(t, ref, rule)
	}
}

func TestEmbeddedTreeMatchesTheRepository(t *testing.T) {
	disk := os.DirFS(filepath.Join("..", "..", Root))
	onDisk := 0

	err := fs.WalkDir(disk, ".", func(p string, d fs.DirEntry, err error) error {
		require.NoError(t, err)

		hidden := strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")

		if d.IsDir() {
			if p != "." && hidden {
				assert.Fail(t, "hidden directory under skills/", "%s: go:embed drops it silently", p)

				return fs.SkipDir
			}

			return nil
		}

		// Finder litter is gitignored and never ships; any other hidden
		// file is a mistake the embed would hide.
		if d.Name() == ".DS_Store" {
			return nil
		}

		if hidden {
			assert.Fail(t, "hidden file under skills/", "%s: go:embed drops it silently", p)

			return nil
		}

		want, err := fs.ReadFile(disk, p)
		require.NoError(t, err)

		got, err := fs.ReadFile(seamark.SkillsFS, path.Join(Root, p))
		require.NoError(t, err, "%s is in the repository but not in the binary", p)
		assert.Equal(t, string(want), string(got), p)

		onDisk++

		return nil
	})
	require.NoError(t, err)

	embedded := 0

	err = fs.WalkDir(seamark.SkillsFS, Root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			embedded++
		}

		return err
	})
	require.NoError(t, err)

	assert.Equal(t, onDisk, embedded, "the binary carries a file the repository lacks")
	assert.Greater(t, onDisk, 0)
}

func TestParseFrontmatterRejectsMalformedHeaders(t *testing.T) {
	for label, src := range map[string]string{
		"no opening fence": "name: x\n---\nbody\n",
		"no closing fence": "---\nname: x\nbody\n",
		"invalid yaml":     "---\nname: [\n---\nbody\n",
	} {
		_, _, err := ParseFrontmatter([]byte(src))
		assert.Error(t, err, label)
	}
}

func TestParseFrontmatterKeepsBodyAndIgnoresUnknownFields(t *testing.T) {
	src := "---\nname: x\nmetadata:\n  seamark: managed\nwhen_to_use: later\n---\nBody line.\n"

	fm, body, err := ParseFrontmatter([]byte(src))
	require.NoError(t, err)

	assert.Equal(t, "x", fm.Name)
	assert.True(t, IsManaged(fm))
	assert.Equal(t, "Body line.\n", body)

	// A closing fence on the last line without a newline still closes.
	_, body, err = ParseFrontmatter([]byte("---\nname: y\n---"))
	require.NoError(t, err)
	assert.Empty(t, body)

	assert.False(t, IsManaged(Frontmatter{}), "no metadata, no marker")
	assert.False(t, IsManaged(Frontmatter{Metadata: map[string]string{MarkerKey: "mine"}}))
}

// yamlKeys returns the top-level keys of a YAML mapping.
func yamlKeys(t *testing.T, header []byte) []string {
	t.Helper()

	m := map[string]any{}
	require.NoError(t, yaml.Unmarshal(header, &m))

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	return keys
}
