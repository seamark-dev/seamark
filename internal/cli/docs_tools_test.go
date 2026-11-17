package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/approve"
)

// TestDocsNameNoCLICommandForAnMCPOnlyTool checks that no doc writes an
// MCP-only tool as a command line. Some MCP tools, such as expand, have no
// CLI command. A doc that shows `seamark expand` sends the reader to a
// command that does not exist. The check reads every Markdown file of the
// README, the changelog, docs/ and skills/, because each one reaches users.
func TestDocsNameNoCLICommandForAnMCPOnlyTool(t *testing.T) {
	commands := map[string]bool{}

	for _, c := range New().Commands() {
		commands[c.Name()] = true
	}

	// A tool counts as MCP-only when no command carries its name, with
	// "_" or with "-". The list must not be empty, or the check proves
	// nothing.
	var mcpOnly []string

	for _, tool := range approve.Tools {
		if !commands[tool] && !commands[strings.ReplaceAll(tool, "_", "-")] {
			mcpOnly = append(mcpOnly, tool)
		}
	}

	require.Contains(t, mcpOnly, "expand", "expand is an MCP tool with no CLI command")

	repo := filepath.Join("..", "..")
	docs := []string{"README.md", "CHANGELOG.md"}

	for _, dir := range []string{"docs", "skills"} {
		err := fs.WalkDir(os.DirFS(repo), dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
				docs = append(docs, p)
			}

			return err
		})
		require.NoError(t, err)
	}

	for _, tool := range mcpOnly {
		dashed := strings.ReplaceAll(tool, "_", "-")
		spelled := regexp.MustCompile(`\bseamark (` + regexp.QuoteMeta(tool) + `|` + regexp.QuoteMeta(dashed) + `)\b`)

		for _, doc := range docs {
			data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(doc)))
			require.NoError(t, err)

			// The match list, not the whole file, goes into the failure
			// message, so the message stays short.
			assert.Empty(t, spelled.FindAllString(string(data), -1),
				"%s writes a CLI command for %s, but %s is an MCP tool with no CLI command", doc, tool, tool)
		}
	}
}
