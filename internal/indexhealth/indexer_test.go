package indexhealth_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/indexhealth"
	"github.com/seamark-dev/seamark/internal/store"
)

// TestIndexerSummaryIsKnownCoverage proves that the strict reader accepts
// the summary that the indexer writes, with and without parse errors. A
// file without read permission is a parse error for the indexer.
func TestIndexerSummaryIsKnownCoverage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file without read permission")
	}

	for _, failing := range []int{0, 1, 3} {
		t.Run(fmt.Sprintf("%d parse errors", failing), func(t *testing.T) {
			root := t.TempDir()

			require.NoError(t, os.WriteFile(filepath.Join(root, "README.txt"), []byte("not code\n"), 0o644))

			for i := range 4 {
				p := filepath.Join(root, fmt.Sprintf("f%d.go", i))
				require.NoError(t, os.WriteFile(p, fmt.Appendf(nil, "package main\n\nfunc F%d() {}\n", i), 0o644))

				if i < failing {
					require.NoError(t, os.Chmod(p, 0o000))
					t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
				}
			}

			sum, err := index.Run(index.Options{Root: root})
			require.NoError(t, err)
			require.Equal(t, failing, sum.ParseErrors)

			st, err := store.Open(sum.DBPath)
			require.NoError(t, err)
			t.Cleanup(func() { _ = st.Close() })

			summary, err := st.GetMeta(store.MetaIndexSummary)
			require.NoError(t, err)

			c := indexhealth.ParseCoverage(summary)
			assert.True(t, c.Known, summary)
			assert.Equal(t, failing, c.ParseErrors, summary)
			assert.Equal(t, 4-failing, c.FilesParsed, summary)
		})
	}
}
