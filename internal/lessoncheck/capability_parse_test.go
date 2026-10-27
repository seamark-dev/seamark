package lessoncheck

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLessonCheckParseForCapability preserves the file reader's exact
// config, digest, and error text when another caller supplies the bytes.
func TestLessonCheckParseForCapability(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		mode os.FileMode
	}{
		{name: "valid", text: "pin:\n  - rule: keep-parser-strict\n"},
		{name: "tier-one", text: "pin: [\n"},
		{name: "tier-two", text: "pin:\n  - rule: keep-parser-strict\n    mode: other\n"},
		{name: "absent"},
		{name: "unreadable", text: "pin: []\n", mode: 0o000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root can read a file with no permission bits")
			}

			root := t.TempDir()
			path := configPath(root)

			if tc.name != "absent" {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(tc.text), 0o600))

				if tc.name == "unreadable" {
					require.NoError(t, os.Chmod(path, tc.mode))
				}
			}

			data, readErr := os.ReadFile(path)
			got, gotErrs := ParseForCapability(data, readErr)
			want, wantErrs := LoadForCapability(root)

			assert.Equal(t, want, got)
			assert.Equal(t, wantErrs, gotErrs)

			switch tc.name {
			case "tier-one", "tier-two", "unreadable":
				assert.NotEmpty(t, gotErrs)
			default:
				assert.Empty(t, gotErrs)
			}
		})
	}
}
