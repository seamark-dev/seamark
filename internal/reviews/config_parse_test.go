package reviews

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeLessons writes body as .seamark/lessons.yaml under a new root.
func writeLessons(t *testing.T, body string) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(body), 0o644))

	return root
}

func TestParseConfigEqualsLoadConfig(t *testing.T) {
	// LoadConfig is a read plus ParseConfig. Every reader row of the pin
	// file map must give the same value and the same error text on both
	// entry points, because the CLI prints the LoadConfig text.
	cases := []struct {
		name, body string
		wantErr    string
		pins       int
	}{
		{name: "empty file", body: ""},
		{name: "comments only", body: "# nothing here\n"},
		{name: "unknown top key", body: "pins:\n  - rule: a\n"},
		{name: "unknown keys in an item", body: "pin:\n  - rule: a\n    regoins: [api]\n    enabled: false\n", pins: 1},
		{name: "duplicate top key", body: "pin:\n  - rule: a\npin:\n  - rule: b\n", wantErr: `mapping key "pin" already defined at line 1`},
		{name: "duplicate key in an item", body: "pin:\n  - rule: a\n    rule: b\n", wantErr: `mapping key "rule" already defined`},
		{name: "scalar regions", body: "pin:\n  - rule: a\n    regions: api\n", wantErr: "cannot unmarshal !!str `api` into []string"},
		{name: "list region", body: "pin:\n  - rule: a\n    region: [api]\n", wantErr: "cannot unmarshal !!seq into string"},
		{name: "mapping pin", body: "pin:\n  rule: a\n", wantErr: "cannot unmarshal !!map into []reviews.PinRule"},
		{name: "null item", body: "pin:\n  -\n  - rule: a\n", pins: 1},
		{name: "null pin", body: "pin:\n", pins: 0},
		{name: "second document", body: "pin:\n  - rule: a\n---\npin:\n  - rule: b\n", pins: 1},
		{name: "alias item", body: "pin:\n  - &p {rule: a}\n  - *p\n", pins: 2},
		{name: "bad delivery", body: "hook_delivery: sometimes\n", wantErr: `hook_delivery must be "always" or "once-per-context", got "sometimes"`},
		{name: "negative threshold", body: "threshold: -3\n"},
		{name: "tab indentation", body: "pin:\n\t- rule: a\n", wantErr: "found character that cannot start any token"},
		{name: "unquoted star", body: "pin:\n  - rule: a\n    region: *\n", wantErr: "did not find expected alphabetic or numeric character"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loaded, loadErr := LoadConfig(writeLessons(t, tc.body))
			parsed, parseErr := ParseConfig([]byte(tc.body))

			if tc.wantErr != "" {
				require.Error(t, loadErr)
				require.Error(t, parseErr)
				assert.Equal(t, loadErr.Error(), parseErr.Error())
				assert.Contains(t, parseErr.Error(), tc.wantErr)
				require.ErrorIs(t, parseErr, ErrLessonsConfig)
				assert.Nil(t, parsed)

				return
			}

			require.NoError(t, loadErr)
			require.NoError(t, parseErr)
			assert.Equal(t, loaded, parsed)
			assert.Len(t, parsed.Pin, tc.pins)
			assert.Equal(t, DefaultThreshold, parsed.Threshold, "a missing or invalid threshold reads as the default")
		})
	}
}

func TestParseConfigOfNoBytesIsTheDefault(t *testing.T) {
	cfg, err := ParseConfig(nil)

	require.NoError(t, err)
	assert.Equal(t, DefaultConfig(), cfg)
}

func TestLoadConfigKeepsTheReadErrorText(t *testing.T) {
	// A directory in place of the file is a read failure, not a parse
	// failure. Its text names the OS error, as before the split.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark", "lessons.yaml"), 0o755))

	_, err := LoadConfig(root)

	require.ErrorIs(t, err, ErrLessonsConfig)

	var pathErr *os.PathError
	assert.True(t, errors.As(err, &pathErr), "the read error stays wrapped: %v", err)
}

func TestProvenanceMarkerIsTheRenderedText(t *testing.T) {
	// The CLI prints and prunes comments with this text, so a change
	// would orphan every provenance comment in existing files.
	assert.Equal(t, "seamark lessons --distill", ProvenanceMarker)
}
