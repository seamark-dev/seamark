package lessoncheck

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// Mode is the normalized mode of a pin. The pin modes check and enforce
// make a pin executable: the checkers evaluate it.
type Mode string

// The closed list of modes. An omitted mode is advisory.
const (
	ModeAdvisory Mode = "advisory"
	ModeCheck    Mode = "check"
	ModeEnforce  Mode = "enforce"
)

// Kind names a checker and its version. A behavior change of a checker
// is a new kind version, never a change of a known kind, so a stored kind
// keeps one meaning.
type Kind string

// The closed list of checker kinds. Validate refuses every other kind.
const (
	KindForbidPathChange Kind = "forbid-path-change/1"
	KindNoAddedPattern   Kind = "no-added-pattern/1"
)

// knownKinds is the kind list of the check-kind-unknown message. The
// order is the sorted order of the kinds, so the message text is stable.
const knownKinds = string(KindForbidPathChange) + ", " + string(KindNoAddedPattern)

// Check is one compiled check definition. Config.Checks holds only the
// definitions without an error of their own, so a checker never reads a
// refused definition.
type Check struct {
	ID      string
	Kind    Kind
	Version string         // reviews.CheckVersion of the definition
	Pattern *regexp.Regexp // no-added-pattern/1 only; nil for other kinds
}

// CheckRef names the check of a pin in every output format: the ID, the
// kind, and the definition version. The outputs keep it as one object,
// so a reader never splits the three values.
type CheckRef struct {
	ID      string `json:"id"`
	Kind    Kind   `json:"kind"`
	Version string `json:"version"` // reviews.CheckVersion
}

// ConfigReport is the configuration state that the verification result
// and the capability report carry.
type ConfigReport struct {
	SHA256              string        `json:"sha256"`
	LessonChecksVersion int           `json:"lesson_checks_version"`
	Valid               bool          `json:"valid"`
	Errors              []ConfigError `json:"errors,omitempty"`
}

// ConfigErrorCode is the stable identifier of one configuration error.
type ConfigErrorCode string

// The closed list of codes. Validate returns the tier-2 codes. Only the
// reports use CodeDecodeError, for a file that does not read or decode
// (tier 1, reviews.ParseConfig).
const (
	CodeVersionUnsupported     ConfigErrorCode = "version-unsupported"
	CodeVersionMissing         ConfigErrorCode = "version-missing"
	CodeCheckIDInvalid         ConfigErrorCode = "check-id-invalid"
	CodeCheckFieldUnknown      ConfigErrorCode = "check-field-unknown"
	CodeCheckKindMissing       ConfigErrorCode = "check-kind-missing"
	CodeCheckKindUnknown       ConfigErrorCode = "check-kind-unknown"
	CodeCheckPatternMissing    ConfigErrorCode = "check-pattern-missing"
	CodeCheckPatternUnexpected ConfigErrorCode = "check-pattern-unexpected"
	CodeCheckPatternTooLong    ConfigErrorCode = "check-pattern-too-long"
	CodeCheckPatternInvalid    ConfigErrorCode = "check-pattern-invalid"
	CodePinModeUnknown         ConfigErrorCode = "pin-mode-unknown"
	CodePinCheckRequired       ConfigErrorCode = "pin-check-required"
	CodePinCheckMissing        ConfigErrorCode = "pin-check-missing"
	CodePinRegionInvalid       ConfigErrorCode = "pin-region-invalid"
	CodeDecodeError            ConfigErrorCode = "decode-error"
)

// configErrorPrefix starts the text of every configuration error. It
// equals the text of reviews.ErrLessonsConfig, so a tier-2 error reads
// like a tier-1 error of reviews.ParseConfig.
const configErrorPrefix = "lessons config: "

// configErrorText returns the text of a configuration error without the
// "lessons config: " prefix. ConfigError.Error adds the prefix again.
// The guard deny text and the post-edit findings print the message after
// their own lead. So the message has one form in every output, the same
// form as ConfigError.Message.
func configErrorText(err error) string {
	return strings.TrimPrefix(err.Error(), configErrorPrefix)
}

// ConfigError is one semantic configuration error. Code is the stable
// identifier. Error() is the fixed message text with the prefix
// "lessons config: ". The outputs print that text, so it does not change.
type ConfigError struct {
	Code    ConfigErrorCode `json:"code"`
	Subject string          `json:"subject"` // "lesson_checks_version", "checks.<id>", "pin <n>", or "lessons.yaml" (decode-error)
	Message string          `json:"message"` // without the "lessons config: " prefix
}

// Error returns the message with the "lessons config: " prefix.
func (e ConfigError) Error() string {
	return configErrorPrefix + e.Message
}

// Unwrap returns reviews.ErrLessonsConfig. Callers then test every
// configuration error, of either tier, with one errors.Is.
func (e ConfigError) Unwrap() error {
	return reviews.ErrLessonsConfig
}

// Config is the validated, evaluation-ready view of lessons.yaml.
type Config struct {
	Raw    *reviews.Config
	SHA256 string           // hex SHA-256 of the lessons.yaml bytes ("" for an absent file)
	Pins   []Pin            // every pin entry, file order
	Checks map[string]Check // compiled definitions; a definition that does not compile is absent
	// Errors holds the tier-2 errors of Validate. Non-empty means
	// invalid: Evaluate then runs no checker.
	Errors []ConfigError
}

// Pin is one pin entry with its identity and its normalized mode.
type Pin struct {
	Entry    int // 1-based position in the pin list
	Rule     reviews.PinRule
	Key      reviews.PinKey
	Identity string   // reviews.PinIdentityDigest, 64 hex
	Ref      string   // (*reviews.Config).ShortPinRef, the printed pin-ref
	Version  string   // (*reviews.Config).PinVersion
	Mode     Mode     // normalized; an unknown mode gives ModeAdvisory
	Scope    []string // AllRegions(), sorted; non-nil; empty = repository-wide
	CheckID  string   // "" when none
}

// The subjects of Validate. A check subject is "checks." + the ID, and a
// pin subject is "pin " + the entry number.
const (
	subjectVersion = "lesson_checks_version"
	subjectChecks  = "checks."
)

// checkIDPattern is the rule for a check ID. An ID that passes it holds
// no control character, so a message can print it raw.
var checkIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// pinLabelRunes bounds the pin key text inside a pin message. The value
// is part of the fixed message text, so it is not a Limits field.
const pinLabelRunes = 80

// Validate is the strict semantic tier (tier 2). Tier 1 is the decode
// of reviews.ParseConfig. Validate returns every error in this order:
// lesson_checks_version, then checks.<id> sorted by ID, then pin <n> by
// entry, and Code (byte-wise) inside one subject. A Go map holds no file
// order of check IDs, so the ID sort keeps the output deterministic. nil
// means valid.
func Validate(cfg *reviews.Config) []ConfigError {
	errs, _ := validate(cfg)

	return errs
}

// validate returns the errors of Validate and the compiled definitions.
// One pass gives both, so Parse never compiles a pattern twice.
func validate(cfg *reviews.Config) (errs []ConfigError, checks map[string]Check) {
	checks = map[string]Check{}

	// A newer format can hold keys and kinds that this binary does not
	// know, so the version error is the only error. ParseConfig refuses
	// a negative version at tier 1. A caller that skips ParseConfig still
	// gets an error here, because no version below 0 is supported.
	if v := cfg.LessonChecksVersion; v < 0 || v > reviews.MaxLessonChecksVersion {
		return []ConfigError{{
			Code:    CodeVersionUnsupported,
			Subject: subjectVersion,
			Message: fmt.Sprintf("lesson_checks_version %d is not supported; this seamark reads up to 1", v),
		}}, checks
	}

	if cfg.LessonChecksVersion == 0 && usesLessonChecks(cfg) {
		errs = append(errs, ConfigError{
			Code:    CodeVersionMissing,
			Subject: subjectVersion,
			Message: "checks needs lesson_checks_version: 1",
		})
	}

	for _, id := range slices.Sorted(maps.Keys(cfg.Checks)) {
		checkErrs, check := validateCheck(id, cfg.Checks[id])
		if len(checkErrs) == 0 {
			checks[id] = check
		}

		errs = append(errs, checkErrs...)
	}

	for i, pin := range cfg.Pin {
		errs = append(errs, validatePin(cfg, i+1, pin)...)
	}

	return errs, checks
}

// usesLessonChecks reports whether the file uses a key that needs
// lesson_checks_version. An explicit "mode: advisory" does not, because
// it means the same as an omitted mode.
func usesLessonChecks(cfg *reviews.Config) bool {
	if len(cfg.Checks) > 0 {
		return true
	}

	for _, pin := range cfg.Pin {
		if Mode(pin.Mode).Executable() || pin.Check != "" {
			return true
		}
	}

	return false
}

// validateCheck validates one definition and compiles it. The check is
// valid only when the returned slice is empty.
func validateCheck(id string, def reviews.CheckDef) ([]ConfigError, Check) {
	var errs []ConfigError

	// The ID goes into the text raw only after it passes the ID rule. A
	// refused ID can hold control characters, so the text quotes it.
	subject := subjectChecks + id

	if !checkIDPattern.MatchString(id) {
		subject = subjectChecks + fmt.Sprintf("%q", id)
		errs = append(errs, ConfigError{
			Code:    CodeCheckIDInvalid,
			Subject: subject,
			Message: fmt.Sprintf("checks: check id %q must match [a-z0-9][a-z0-9._-]{0,63}", id),
		})
	}

	add := func(code ConfigErrorCode, text string) {
		errs = append(errs, ConfigError{Code: code, Subject: subject, Message: subject + ": " + text})
	}

	for _, key := range slices.Sorted(maps.Keys(def.Unknown)) {
		add(CodeCheckFieldUnknown, fmt.Sprintf("unknown field %q", key))
	}

	check := Check{ID: id, Kind: Kind(def.Kind), Version: reviews.CheckVersion(def)}

	// The pattern rules run only for a known kind, because only the kind
	// says whether a pattern belongs to the definition.
	switch check.Kind {
	case "":
		add(CodeCheckKindMissing, "kind is required")
	case KindForbidPathChange:
		if def.Pattern != "" {
			add(CodeCheckPatternUnexpected, "forbid-path-change/1 takes no pattern")
		}
	case KindNoAddedPattern:
		switch {
		case def.Pattern == "":
			add(CodeCheckPatternMissing, "no-added-pattern/1 requires a non-empty pattern")
		case len(def.Pattern) > MaxPatternBytes:
			// The length test runs before the compile, so an oversized
			// pattern never reaches the regexp parser.
			add(CodeCheckPatternTooLong, fmt.Sprintf("pattern is %d bytes; the limit is %d", len(def.Pattern), MaxPatternBytes))
		default:
			pattern, err := regexp.Compile(def.Pattern)
			if err != nil {
				add(CodeCheckPatternInvalid, "pattern does not compile: "+render.Sanitize(err.Error()))
			}

			check.Pattern = pattern
		}
	default:
		add(CodeCheckKindUnknown, fmt.Sprintf("unknown checker kind %q (known: %s)", def.Kind, knownKinds))
	}

	sortByCode(errs)

	return errs, check
}

// sortByCode puts the errors of one subject in byte-wise Code order. The
// sort is stable, so repeated check-field-unknown errors keep their key
// order and repeated pin-region-invalid errors keep their scope order.
func sortByCode(errs []ConfigError) {
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Code < errs[j].Code })
}

// validatePin validates one pin entry. entry is its 1-based position.
func validatePin(cfg *reviews.Config, entry int, pin reviews.PinRule) []ConfigError {
	var errs []ConfigError

	key := reviews.NewPinKey(pin.Rule, pin.Region, pin.Regions)
	subject := fmt.Sprintf("pin %d", entry)
	label := fmt.Sprintf("pin %d (%s)", entry, render.Truncate(render.Sanitize(key.String()), pinLabelRunes))

	add := func(code ConfigErrorCode, text string) {
		errs = append(errs, ConfigError{Code: code, Subject: subject, Message: label + ": " + text})
	}

	mode, known := NormalizeMode(pin.Mode)
	if !known {
		add(CodePinModeUnknown, fmt.Sprintf("unknown mode %q; use advisory, check, or enforce", pin.Mode))
	}

	executable := mode.Executable()

	if executable && pin.Check == "" {
		add(CodePinCheckRequired, fmt.Sprintf("mode %s requires check: <check-id>", mode))
	}

	// The reference must resolve in every mode. The test reads the raw
	// map: a definition that fails its own rules is still defined, and
	// its own error names the problem.
	if pin.Check != "" {
		if _, ok := cfg.Checks[pin.Check]; !ok {
			add(CodePinCheckMissing, fmt.Sprintf("check %q is not defined under checks", pin.Check))
		}
	}

	// Only an executable pin needs canonical regions. The checker scope
	// is AllRegions(), and the scope must equal the PinKey regions.
	if executable {
		for _, region := range SortedScope(pin.AllRegions()) {
			if reason := regionProblem(region); reason != "" {
				add(CodePinRegionInvalid, fmt.Sprintf("region %q cannot scope a check: %s", region, reason))
			}
		}
	}

	sortByCode(errs)

	return errs
}

// regionProblem returns why a scope region is not canonical, or "". A
// canonical region equals its reviews.CheckPinRegion value.
func regionProblem(region string) string {
	in, err := reviews.CheckPinRegion(region)

	switch {
	case err != nil:
		return render.Sanitize(err.Error())
	case in.Value == region:
		return ""
	case strings.TrimSpace(region) == in.Value:
		return "surrounding spaces"
	default:
		// No fixed text exists for another removal, such as a leading
		// "./". So the notes of CheckPinRegion name each removal.
		return render.Sanitize(strings.Join(in.Notes, ", "))
	}
}

// NormalizeMode maps a raw mode to its Mode. It reports false for an
// unknown value and then returns ModeAdvisory. It uses ModeAdvisory because
// an invalid file still lists every pin, and an unknown mode must never make
// a pin executable. The delivery report and the preparation call it, so each
// prints the mode that the checkers use.
func NormalizeMode(raw string) (Mode, bool) {
	switch Mode(raw) {
	case "", ModeAdvisory:
		return ModeAdvisory, true
	case ModeCheck, ModeEnforce:
		return Mode(raw), true
	default:
		return ModeAdvisory, false
	}
}

// Executable reports whether the checkers evaluate a pin of this mode.
// Every package that asks this question calls it, so the rule has one
// form.
func (m Mode) Executable() bool {
	return m == ModeCheck || m == ModeEnforce
}

// ExecutablePinsCovering counts the pins of mode check or enforce whose
// scope holds at least one of paths. It uses the scope rule of the
// checkers. check without lesson verification names the count, so a
// check that did not run is not silent.
func (c *Config) ExecutablePinsCovering(paths []string) int {
	n := 0

	for _, pin := range c.Pins {
		if !pin.Mode.Executable() {
			continue
		}

		for _, p := range paths {
			if inScope(p, pin.Scope) {
				n++

				break
			}
		}
	}

	return n
}

// SortedScope returns a sorted copy of a region list. A pin scope is
// SortedScope(pin.AllRegions()). The slice is never nil, so an empty
// scope encodes as [] (repository-wide). The checkers, the preparation,
// and the delivery report share it, so each shows one region order.
func SortedScope(regions []string) []string {
	scope := append([]string{}, regions...)
	sort.Strings(scope)

	return scope
}

// Load reads <root>/.seamark/lessons.yaml, decodes it with
// reviews.ParseConfig, and validates it.
//
// Only an absent file means "no checks": it gives a Config without pins
// and SHA256 "". Load returns every other read error, such as a
// permission error, because an unreadable file is not an empty one. A
// read or decode error (tier 1) returns err, which wraps
// reviews.ErrLessonsConfig, and no Config.
//
// Tier-2 errors return the Config anyway, with Errors equal to the
// returned slice. Verification and the capability report need it,
// because they list every pin of an invalid file. Every pin entry is in
// Pins, and a pin with an unknown mode gets ModeAdvisory.
func Load(root string) (*Config, []ConfigError, error) {
	return parseRead(os.ReadFile(configPath(root)))
}

// parseRead applies the rules of Load to one file read: its bytes and
// its read error. Load and the configuration of Verify both call it, so
// they treat an absent file and a read error the same way.
func parseRead(data []byte, readErr error) (*Config, []ConfigError, error) {
	if errors.Is(readErr, os.ErrNotExist) {
		cfg, errs := newConfig(reviews.DefaultConfig(), "")

		return cfg, errs, nil
	}

	if readErr != nil {
		return nil, nil, fmt.Errorf("%w: %w", reviews.ErrLessonsConfig, readErr)
	}

	return Parse(data)
}

// Parse is Load on bytes that the caller already read, with the same
// result shape. A pin writer calls it on its candidate bytes.
func Parse(data []byte) (*Config, []ConfigError, error) {
	raw, err := reviews.ParseConfig(data)
	if err != nil {
		return nil, nil, err
	}

	cfg, errs := newConfig(raw, hexSHA256(data))

	return cfg, errs, nil
}

// newConfig validates a decoded config and derives the pin views. The
// identity, the pin-ref, and the version come from reviews, so one
// derivation of each exists.
func newConfig(raw *reviews.Config, digest string) (*Config, []ConfigError) {
	errs, checks := validate(raw)

	pins := make([]Pin, 0, len(raw.Pin))

	// The hooks load the config on each edit, so the references come from
	// one pass. One ShortPinRef call per pin is quadratic in the pin count.
	refs := raw.ShortPinRefs()

	for i, rule := range raw.Pin {
		key := reviews.NewPinKey(rule.Rule, rule.Region, rule.Regions)
		mode, _ := NormalizeMode(rule.Mode)

		pins = append(pins, Pin{
			Entry:    i + 1,
			Rule:     rule,
			Key:      key,
			Identity: reviews.PinIdentityDigest(key),
			Ref:      refs[key],
			Version:  raw.PinVersion(rule),
			Mode:     mode,
			Scope:    SortedScope(rule.AllRegions()),
			CheckID:  rule.Check,
		})
	}

	return &Config{Raw: raw, SHA256: digest, Pins: pins, Checks: checks, Errors: errs}, errs
}
