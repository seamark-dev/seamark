package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixtures under testdata/ are the native payload and configuration
// examples later slices decode and generate. This test keeps every
// fixture well-formed and labeled, so a decoder test never fails on a
// stray comma and a reader always finds the provenance note.
func TestFixturesAreWellFormedAndLabeled(t *testing.T) {
	for _, client := range []string{"claude", "codex"} {
		dir := filepath.Join("testdata", client)

		readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
		require.NoError(t, err, "%s: every fixture directory states its provenance", client)
		assert.Contains(t, string(readme), "synthetic", "%s: fixtures are labeled until a native capture confirms them", client)

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)

		seen := 0

		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}

			seen++
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			require.NoError(t, err)

			var doc map[string]any
			require.NoError(t, json.Unmarshal(data, &doc), "%s/%s", client, e.Name())

			switch {
			case strings.HasSuffix(e.Name(), "_oracle.json"):
				// Recorded results of a native tool, not a payload. The file
				// says how the results were made.
				provenance, _ := doc["provenance"].(string)
				assert.NotEmpty(t, provenance, "%s/%s: an oracle file states its provenance", client, e.Name())

				cases, _ := doc["cases"].([]any)
				assert.NotEmpty(t, cases, "%s/%s: an oracle file holds cases", client, e.Name())
			case strings.HasPrefix(e.Name(), "hooks."):
				_, ok := doc["hooks"].(map[string]any)
				assert.True(t, ok, "%s/%s: a hook configuration carries a hooks object", client, e.Name())
			case strings.HasPrefix(e.Name(), "settings."):
				_, ok := doc["hooks"].(map[string]any)
				assert.True(t, ok, "%s/%s: a settings fixture carries a hooks object", client, e.Name())
			default:
				event, _ := doc["hook_event_name"].(string)
				assert.NotEmpty(t, event, "%s/%s: a payload names its event", client, e.Name())
			}
		}

		assert.Positive(t, seen, "%s: fixture directory is not empty", client)
	}
}
