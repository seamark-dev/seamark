package reviews

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFindingBodyCapKeepsNoSuffix pins the cut of a stored finding: no
// suffix, a trim after the cut, and the cut on invalid UTF-8.
func TestFindingBodyCapKeepsNoSuffix(t *testing.T) {
	fits := strings.Repeat("a", findingBodyCap)
	assert.Equal(t, fits, findingBody(fits), "a body at the cap stays whole")

	spaced := strings.Repeat("a", findingBodyCap-1) + " é"
	assert.Equal(t, strings.Repeat("a", findingBodyCap-1), findingBody(spaced),
		"the trim removes the space at the cut")

	stray := strings.Repeat("a", findingBodyCap-1) + "b" + strings.Repeat("\x80", 8)
	assert.Equal(t, strings.Repeat("a", findingBodyCap-1), findingBody(stray),
		"the walk back stops at the nearest rune start")

	assert.Empty(t, findingBody(strings.Repeat("\x80", findingBodyCap+1)), "no rune start leaves nothing")
}
