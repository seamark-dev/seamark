package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWhyHelpSaysWhatMCounts keeps the meaning of M in "N/M commits" in
// the help. The partner line itself has no label, so a reader needs it here.
func TestWhyHelpSaysWhatMCounts(t *testing.T) {
	long := strings.Join(strings.Fields(newWhyCmd(&options{}).Long), " ")

	assert.Contains(t, long, "N commits changed both files")
	assert.Contains(t, long, "counts in the mined window")
	assert.Contains(t, long, "not the commit count of either file")
}

// TestWhyHelpNamesTheFileGrain keeps the grain of the history sections in
// the help. They describe the whole file, also for a symbol, so a reader
// must not take them for the history of the symbol.
func TestWhyHelpNamesTheFileGrain(t *testing.T) {
	long := strings.Join(strings.Fields(newWhyCmd(&options{}).Long), " ")

	assert.Contains(t, long, "The history sections describe the whole file")
}
