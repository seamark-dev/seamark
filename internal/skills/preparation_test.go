package skills

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// verifyGrants are the only Bash rules for lessons --verify. The plain
// rule has no wildcard, so it matches only the bare command. The two
// wildcard forms reach --hook only together with --json or --file, and
// the post-edit hook entry refuses those forms before dispatch. A broad
// "--verify*" rule would also grant the hook entry.
var verifyGrants = []string{
	"Bash(seamark lessons --verify)",
	"Bash(seamark lessons --verify --json*)",
	"Bash(seamark lessons --verify --file *)",
}

// prepareGrant lets the skills run the CLI preparation on the no-MCP
// path. The lessons --prepare handler refuses every mode and decision
// flag before dispatch, so the prefix grant reaches no write path.
const prepareGrant = "Bash(seamark lessons --prepare *)"

// grantRule turns one Bash(...) rule into an anchored pattern. A "*"
// matches any text and a rule without "*" matches only the exact
// command. This follows the Claude Code permission rule syntax, so the
// test can show which commands one grant admits.
func grantRule(rule string) *regexp.Regexp {
	body := strings.TrimSuffix(strings.TrimPrefix(rule, "Bash("), ")")
	parts := strings.Split(body, "*")

	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}

	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
}

// bodySection returns the text under one "## " heading of a skill body,
// up to the next heading of the same level.
func bodySection(body, heading string) string {
	i := strings.Index(body, heading)
	if i < 0 {
		return ""
	}

	rest := body[i+len(heading):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}

	return rest
}

// workflowStep returns the one numbered line of a workflow section that
// starts with "<n>. ", or "" when the step does not exist.
func workflowStep(workflow string, n int) string {
	prefix := strconv.Itoa(n) + ". "

	for line := range strings.SplitSeq(workflow, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}

	return ""
}

// TestPlanAndReviewSkillsRequirePreparation pins the preparation
// guidance to the shipped skills. Preparation comes before the
// construction of any change, one file and a new path included. The
// result informs a later construction step, never a patch built in the
// same generated program, and it authorizes no write. The review skill
// verifies the working tree before completion and prepares a repair
// before it constructs it.
func TestPlanAndReviewSkillsRequirePreparation(t *testing.T) {
	// The shared grant: TestAllowedToolsIsOneNarrowGrant checks only the
	// rule shape and the blanket rule, so the exact verify forms are
	// asserted here.
	for _, name := range shipped {
		fm, _, _ := loadSkill(t, name)

		rules := regexp.MustCompile(`Bash\([^)]*\)`).FindAllString(fm.AllowedTools, -1)

		var verify []string

		for _, r := range rules {
			if strings.HasPrefix(r, "Bash(seamark lessons --verify") {
				verify = append(verify, r)
			}
		}

		assert.Equal(t, verifyGrants, verify, "%s: the grant holds exactly the three explicit --verify forms", name)
		assert.NotContains(t, rules, "Bash(seamark lessons --verify*)", name)
		assert.Contains(t, rules, prepareGrant, name)

		patterns := make([]*regexp.Regexp, 0, len(rules))
		for _, r := range rules {
			patterns = append(patterns, grantRule(r))
		}

		admits := func(cmd string) bool {
			for _, p := range patterns {
				if p.MatchString(cmd) {
					return true
				}
			}

			return false
		}

		for _, cmd := range []string{
			"seamark lessons --verify",
			"seamark lessons --verify --json",
			"seamark lessons --verify --file internal/a.go",
			"seamark lessons --prepare internal/a.go internal/new.go",
		} {
			assert.True(t, admits(cmd), "%s: the grant must admit %q", name, cmd)
		}

		// The post-edit hook entry consumes pending baselines and appends
		// audit records. No rule of the skill grant may admit it.
		for _, cmd := range []string{
			"seamark lessons --verify --hook",
			"seamark lessons --verify --hook --client claude",
		} {
			assert.False(t, admits(cmd), "%s: the grant must not admit %q", name, cmd)
		}
	}

	t.Run("plan-change prepares every change before the first edit", func(t *testing.T) {
		fm, body, _ := loadSkill(t, "seamark-plan-change")

		// The skip rule keeps the typo, comment, and formatting cases and
		// drops the one-file skip: a pin can cover one file.
		assert.NotContains(t, fm.Description, "one-file change in an area already understood")
		assert.Contains(t, fm.Description, "Skip for a typo, a comment, or a formatting edit.")

		for _, phrase := range []string{"in one file or several", "a new one", "lesson preparation", "before the first edit"} {
			assert.Contains(t, fm.Description, phrase)
		}

		use := bodySection(body, "## Use when / do not use when")
		require.NotEmpty(t, use)
		assert.NotContains(t, use, "more than one file")
		assert.Contains(t, use, "A one-file change counts too")
		assert.Contains(t, use, "a typo, a comment, or a formatting edit")

		workflow := bodySection(body, "## Workflow")
		step := workflowStep(workflow, 2)
		require.NotEmpty(t, step, "the workflow has a step 2")

		for _, phrase := range []string{
			"`change_set`",
			"before the first edit",
			"one file",
			"a path that does not exist yet",
			"when the file set grows",
			"later step",
			"same generated program",
			"authorizes no write",
		} {
			assert.Contains(t, step, phrase, "step 2 states the preparation obligation")
		}

		assert.Contains(t, workflowStep(workflow, 6), "prepare it with `change_set` before you construct that edit",
			"a file added during the edit grows the file set")

		fallback := bodySection(body, "## If the Seamark MCP tools are not available")
		assert.Contains(t, fallback, "`seamark lessons --prepare <path>...`")
	})

	t.Run("review-change verifies before completion and prepares a repair", func(t *testing.T) {
		_, body, _ := loadSkill(t, "seamark-review-change")

		workflow := bodySection(body, "## Workflow")
		require.NotEmpty(t, workflow)

		assert.Contains(t, workflowStep(workflow, 2), "`check` tool once, with `lesson_checks: true`")
		assert.Contains(t, workflowStep(workflow, 6), "before you report completion")

		repair := workflowStep(workflow, 7)
		for _, phrase := range []string{
			"Prepare every repair before you construct it",
			"`change_set`",
			"one file or a new path",
			"later step",
			"same generated program",
			"authorizes no write",
		} {
			assert.Contains(t, repair, phrase, "the repair step states the preparation obligation")
		}

		fallback := bodySection(body, "## If the Seamark MCP tools are not available")
		for _, cmd := range []string{"`seamark lessons --verify`", "`seamark check --lessons`", "`seamark lessons --prepare <path>...`"} {
			assert.Contains(t, fallback, cmd)
		}
	})
}
