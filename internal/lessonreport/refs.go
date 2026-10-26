package lessonreport

import "github.com/seamark-dev/seamark/internal/reviews"

// ResolveRef resolves a pin reference to one pin identity digest. It
// reads the current config only (Config.ResolvePinRef). The report reads
// no capture archive, so a pin version that is no longer configured has
// no reference.
//
// Every failure is the *reviews.PinRefError of the config: a malformed,
// missing, ambiguous, or duplicate-entries reference. A nil config reads
// as an empty config.
func ResolveRef(cfg *reviews.Config, ref string) (identity string, err error) {
	if cfg == nil {
		cfg = &reviews.Config{}
	}

	index, err := cfg.ResolvePinRef(ref)
	if err != nil {
		return "", err
	}

	p := cfg.Pin[index]

	return reviews.PinIdentityDigest(reviews.NewPinKey(p.Rule, p.Region, p.Regions)), nil
}
