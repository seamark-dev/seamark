package reviews

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCountFloorMatchesTheDocumentedValue(t *testing.T) {
	// README and docs/lessons.md promise the newest 200 comments. The
	// settings editor shows CountFloor, so both must name one number.
	assert.Equal(t, 200, CountFloor)
	assert.Equal(t, reviewCountFloor, CountFloor)
}
