package reviews

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ErrRegionInput marks a region that a person typed and that no pin or
// mute may hold. Every error of CheckPinRegion and CheckMuteRegion
// wraps it.
var ErrRegionInput = errors.New("invalid region")

// RegionInput is a typed region after CheckPinRegion or CheckMuteRegion. Value is the
// region to store. Notes name each change from the typed text, so a
// form can show the change before the user saves it.
type RegionInput struct {
	Value string
	Notes []string
}

// CheckPinRegion checks one region that a person typed for a pin. A
// region is a literal repository-relative file or directory: the region
// engine matches it as a path prefix (RegionMatches), and no reader
// expands a glob, a variable, or a home directory. So the check refuses
// text that looks like such an expansion, because the user would expect
// a match that never happens. Repository-wide scope is a choice of the
// form, never the typed text "*".
//
// The check removes surrounding spaces, a leading "./", and one
// trailing "/", and it names each removal in Notes. The pin key and the
// firing identity drop a trailing "/" anyway, so the removal changes no
// match. It changes nothing else: spaces inside the path and ordinary
// path punctuation stay as typed. A path that does not exist is valid,
// because a pin can name a future file. The check refuses a path name
// ".", because RegionMatches compares the text and does not clean a
// path. It refuses a path name "..", because the region must stay inside
// the repository.
//
// It also refuses ", " inside a region. The firing identity joins the
// regions of a pin with ", ", so a region "a, b" would share the
// delivery identity of a pin on the two regions a and b.
func CheckPinRegion(raw string) (RegionInput, error) {
	in, err := checkRegion(raw, true)
	if err != nil {
		return RegionInput{}, err
	}

	if strings.Contains(in.Value, ", ") {
		return RegionInput{}, regionInputError(`the region contains ", " (a comma and a space); ` +
			"the delivery identity of a pin joins its regions with that text")
	}

	return in, nil
}

// CheckMuteRegion checks one region that a person typed for a mute. The
// rules are the rules of CheckPinRegion, with two differences. A
// trailing "/" stays as typed, because the mute matcher compares the
// exact region before it compares the prefix: "api/" mutes only the
// lessons under api, and "api" also mutes a lesson on api itself. And
// ", " stays literal, because a mute has no delivery identity.
func CheckMuteRegion(raw string) (RegionInput, error) {
	return checkRegion(raw, false)
}

// checkRegion holds the rules that pins and mutes share. trimSlash
// selects the removal of one trailing "/".
func checkRegion(raw string, trimSlash bool) (RegionInput, error) {
	var in RegionInput

	// The control check runs before the trim. strings.TrimSpace removes
	// tabs, line breaks, and C1 controls such as NEL, so a later check
	// would read a removed control as harmless white space.
	if hasControl(raw) {
		return RegionInput{}, regionInputError("the region contains a control character")
	}

	value := strings.TrimSpace(raw)
	if value != raw && value != "" {
		in.Notes = append(in.Notes, "surrounding spaces removed")
	}

	if value == "" {
		return RegionInput{}, regionInputError("the region is empty")
	}

	if reason := refusedRegionText(value); reason != "" {
		return RegionInput{}, regionInputError(reason)
	}

	if rest, ok := strings.CutPrefix(value, "./"); ok {
		value = rest
		in.Notes = append(in.Notes, `leading "./" removed`)
	}

	if rest, ok := strings.CutSuffix(value, "/"); ok && trimSlash {
		value = rest
		in.Notes = append(in.Notes, `trailing "/" removed`)
	}

	// The removals can leave nothing ("./"), or expose a form that the
	// first check did not see ("./../x").
	if value == "" {
		return RegionInput{}, regionInputError("the region is empty")
	}

	if reason := refusedRegionText(value); reason != "" {
		return RegionInput{}, regionInputError(reason)
	}

	// The name rules run after the removals only, because the leading
	// "./" that the check removes is itself a "." name.
	if reason := refusedName(value); reason != "" {
		return RegionInput{}, regionInputError(reason)
	}

	in.Value = value

	return in, nil
}

// refusedRegionText returns why a region text is refused, or "".
func refusedRegionText(value string) string {
	switch {
	case strings.HasPrefix(value, "/"):
		return `the region is an absolute path; use a path relative to the repository root`
	case hasDriveLetter(value):
		return `the region starts with a drive letter; use a path relative to the repository root`
	case strings.Contains(value, `\`):
		return `the region contains "\"; use "/" between path names`
	case strings.HasPrefix(value, "~"):
		return `the region starts with "~"; no home directory is expanded`
	case strings.ContainsAny(value, "$`"):
		return "the region contains \"$\" or \"`\"; no variable or command is expanded"
	case strings.Contains(value, "//"):
		// The pin key removes one trailing "/" more than the check, so
		// "api//" would store "api/" but key as "api" (a hidden repeat).
		return `the region contains "//"; use one "/" between path names`
	case strings.Contains(value, "*"):
		return `the region contains "*"; regions are literal path prefixes, and repository-wide is a separate choice`
	}

	return ""
}

// hasDriveLetter reports a Windows drive prefix: a letter and a colon at
// the end or before a "/" or a "\". On other systems "a:b" is a valid
// file name, so a colon after one letter alone is no drive.
func hasDriveLetter(value string) bool {
	if len(value) < 2 || value[1] != ':' || !unicode.IsLetter(rune(value[0])) {
		return false
	}

	return len(value) == 2 || value[2] == '/' || value[2] == '\\'
}

// refusedName returns why a path name of the region is refused, or "".
// RegionMatches compares the region text with a path and cleans neither,
// so a "." name matches no file. A ".." name leaves the repository.
func refusedName(value string) string {
	for _, name := range strings.Split(value, "/") {
		switch name {
		case "..":
			return `the region contains ".."; it must stay inside the repository`
		case ".":
			return `the region contains "." as a path name; regions match as literal paths, so it would match no file`
		}
	}

	return ""
}

// hasControl reports a control or bidirectional formatting character.
func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || isBidiControl(r) {
			return true
		}
	}

	return false
}

// isBidiControl reports a bidirectional formatting character. Such a
// character can make a path read as another path on the screen.
func isBidiControl(r rune) bool {
	switch {
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0x061C:
		return true
	}

	return false
}

// regionInputError wraps ErrRegionInput with a reason in plain words.
func regionInputError(reason string) error {
	return fmt.Errorf("%w: %s", ErrRegionInput, reason)
}
