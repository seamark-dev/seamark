package reviews

// Tests of the typed region check of pins and mutes, and of the mute
// matcher that explains an exclusion.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

func TestCheckPinRegionKeepsLiteralPaths(t *testing.T) {
	cases := map[string]string{
		"api":                    "api",
		"internal/tui/pins.go":   "internal/tui/pins.go",
		"docs/my notes/a b.md":   "docs/my notes/a b.md",
		"a-b_c.d@e+f,g[1]?.go":   "a-b_c.d@e+f,g[1]?.go",
		"a,b":                    "a,b",
		"future/not/yet/here.go": "future/not/yet/here.go",
		".github/workflows":      ".github/workflows",
		"a/..b/c":                "a/..b/c",
		"a/.b/c.":                "a/.b/c.",
		// Finding L2: a one-letter name before a colon is no drive letter.
		"a:b":   "a:b",
		"x:y/z": "x:y/z",
	}

	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			got, err := CheckPinRegion(raw)
			require.NoError(t, err)
			assert.Equal(t, want, got.Value)
			assert.Empty(t, got.Notes, "a literal path needs no change")
		})
	}
}

func TestCheckPinRegionNamesEveryChange(t *testing.T) {
	got, err := CheckPinRegion("  ./api/  ")
	require.NoError(t, err)

	assert.Equal(t, "api", got.Value)
	assert.Equal(t, []string{"surrounding spaces removed", `leading "./" removed`, `trailing "/" removed`}, got.Notes)
}

func TestCheckPinRegionRefusesExpansionsAndEscapes(t *testing.T) {
	cases := map[string]string{
		"":              "empty",
		"   ":           "empty",
		"./":            "empty",
		"/":             "absolute",
		"/etc/passwd":   "absolute",
		"C:/repo/api":   "drive letter",
		"C:":            "drive letter",
		"c:/":           "drive letter",
		`D:\x`:          "drive letter",
		`api\handlers`:  `"\"`,
		"~/notes":       `"~"`,
		"$HOME/api":     `"$"`,
		"api/`id`":      "\"`\"",
		"*":             `"*"`,
		"api/*.go":      `"*"`,
		"../outside":    `".."`,
		"api/../../etc": `".."`,
		"./../x":        `".."`,
		// Finding L3: RegionMatches compares the text, so a "." name
		// would make a pin that never fires.
		".":             `"."`,
		"x/.":           `"."`,
		"./x/./y":       `"."`,
		"././x":         `"."`,
		"api/./web":     `"."`,
		"api\x00x":      "control",
		"api\tx":        "control",
		"api\nx":        "control",
		"api\u202ex":    "control",
		"api\u0085":     "control",
		"a, b":          `", "`,
		"api//":         `"//"`,
		"api//handlers": `"//"`,
	}

	for raw, reason := range cases {
		t.Run(reason+" "+raw, func(t *testing.T) {
			_, err := CheckPinRegion(raw)
			require.ErrorIs(t, err, ErrRegionInput)
			assert.Contains(t, err.Error(), reason)
		})
	}
}

func TestCheckMuteRegionKeepsTheTrailingSlashAndCommas(t *testing.T) {
	got, err := CheckMuteRegion(" ./api/ ")
	require.NoError(t, err)

	assert.Equal(t, "api/", got.Value, "a mute keeps its trailing slash, because the matcher reads it")
	assert.Equal(t, []string{"surrounding spaces removed", `leading "./" removed`}, got.Notes)

	got, err = CheckMuteRegion("a, b")
	require.NoError(t, err)
	assert.Equal(t, "a, b", got.Value, "a mute has no delivery identity, so a comma and a space stay literal")

	for _, raw := range []string{"", "./", "*", "/abs", "../x", "$X", "a\tb", "api//", ".", "x/./", "C:"} {
		_, err := CheckMuteRegion(raw)
		assert.ErrorIs(t, err, ErrRegionInput, raw)
	}
}

func TestMuteMatchesNamesEveryMatchingEntry(t *testing.T) {
	cfg := &Config{
		Threshold: 2,
		Mute: []MuteRule{
			{Rule: "f541"},                // case-insensitive rule
			{Region: "scripts"},           // directional region prefix
			{},                            // an empty entry matches nothing
			{Rule: "F541", Region: "api"}, // both conditions must hold
			{Region: "scripts/a.py"},      // a child never mutes its parent
		},
	}

	lesson := model.Lesson{Region: "scripts", Symptom: "F541", Occurrences: 3}

	assert.Equal(t, []int{0, 1}, cfg.MuteMatches(lesson))
	assert.True(t, cfg.Muted(lesson))

	assert.Empty(t, cfg.MuteMatches(model.Lesson{Region: "api", Symptom: "E501", Occurrences: 3}))
	assert.False(t, cfg.Muted(model.Lesson{Region: "api", Symptom: "E501", Occurrences: 3}))
	assert.Equal(t, []int{0, 3}, cfg.MuteMatches(model.Lesson{Region: "api/x.go", Symptom: "F541"}))
}
