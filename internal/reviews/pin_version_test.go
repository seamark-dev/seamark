package reviews

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// The K2 vectors of the spec example pin and of the second example pin.
// contracts.md lists them, and they were recomputed from the §1.2 scheme.
const (
	specIdentity   = "3215e7b483d134443a8204970767f1a0057d4726f235e946b6c0c27abd8cd055"
	specRef        = "3215e7b483d1"
	specVersion    = "ae4f17f75413b37e4142d80b529154f6166dd2bf155e6eac3dc86dc040aa7a7e"
	specVersionOff = "e79201579c97670797201977d5d764110397a5eaf44ebb056646ee5a32817d6f"
	strictIdentity = "ed5d4af5de3a230a089f19fb7de4c2b04a512cbec6dfb023eabc944d93a82488"
	strictRef      = "ed5d4af5de3a"
	strictVersion  = "f128e3e12b9d12825e8f7a4396ebb003b517d1e162955edb04bf147a5aa89897"
)

// specPin is the spec example pin without mode and check (spec
// "Configuration and executable checks").
var specPin = PinRule{
	Rule:   "frozen-assets",
	Region: "generated/",
	Note:   "Changes to these files require a maintainer-managed update.",
}

// strictPin is the second example pin of contracts L1, L3, and L4.
var strictPin = PinRule{
	Rule:   "keep-parser-strict",
	Region: "internal/parse",
	Note:   "Reject unknown fields in the parser.",
}

// TestPinIdentityIgnoresModeAndCheck proves that the firing identity of
// a pin stays as 96e14ef computes it (contracts K2). The goldens are the
// spec example pin: NewPinKey, PinIdentity, and the delivery digest of
// its rendered lesson. The firing log and the hook state join on these
// values, so a change here splits the history of every installed pin.
// The variants add a mode and a check, also values that Validate refuses,
// because the lenient readers see such values too. The goldens and the
// identity digest stay the same for every variant.
func TestPinIdentityIgnoresModeAndCheck(t *testing.T) {
	const deliveryDigest = "55057fe1065d828de2fe3cf35de5786fd67cb4a4f40f439d59119e618e42cbce"

	wantKey := PinKey{Rule: "frozen-assets", Region: "generated"}
	wantIdentity := FiredLesson{
		Region:  "generated",
		Symptom: "frozen-assets — changes to these files require a maintainer-managed update.",
	}

	// The spec example pin (spec "Configuration and executable checks").
	spec := PinRule{
		Rule:   "frozen-assets",
		Region: "generated/",
		Note:   "Changes to these files require a maintainer-managed update.",
	}

	withMode := func(mode, check string) PinRule {
		p := spec
		p.Mode, p.Check = mode, check

		return p
	}

	variants := []PinRule{
		spec,
		withMode("enforce", "frozen-assets"),
		withMode("advisory", ""),
		withMode("check", "no-todo"),
		withMode("advisory", "frozen-assets"),
		withMode("strict", "undefined-check"),
	}

	for _, p := range variants {
		assert.Equal(t, wantKey, NewPinKey(p.Rule, p.Region, p.Regions))
		assert.Equal(t, wantIdentity, PinIdentity(p))
		assert.Equal(t, deliveryDigest, lessonDeliveryDigest(pinLesson(p)))
		assert.Equal(t, specIdentity, PinIdentityDigest(NewPinKey(p.Rule, p.Region, p.Regions)))
	}
}

// TestPinVersionVectors asserts the K2 vector table. The configs come
// from ParseConfig, the one lenient loader that the hook and the plan
// freeze share. The last assertions prove that the compose copy of this
// file gives the bytes of the inline loop in contextDigest.
func TestPinVersionVectors(t *testing.T) {
	assert.Equal(t, "3:pin13:frozen-assets9:generated", compose("pin", "frozen-assets", "generated"))

	specKey := NewPinKey(specPin.Rule, specPin.Region, specPin.Regions)
	assert.Equal(t, specIdentity, PinIdentityDigest(specKey))

	assert.Equal(t, "9ceb9cddb5053035c0d9779ffab326503647b5295cb3f43791d5fe1e9be9cf7a",
		CheckVersion(CheckDef{Kind: "forbid-path-change/1"}))
	assert.Equal(t, "c044c91d812033b1da35c51ad301b6a3a06b09503b8e1126b392c1d643d7aa3b",
		CheckVersion(CheckDef{Kind: "no-added-pattern/1", Pattern: `TODO\(`}))

	enforced, err := ParseConfig([]byte(specExample))
	require.NoError(t, err)
	require.Len(t, enforced.Pin, 1)
	assert.Equal(t, specVersion, enforced.PinVersion(enforced.Pin[0]))
	assert.Equal(t, specRef, enforced.ShortPinRef(specKey))

	plain := &Config{Pin: []PinRule{specPin}}
	assert.Equal(t, specVersionOff, plain.PinVersion(specPin))

	strictKey := NewPinKey(strictPin.Rule, strictPin.Region, strictPin.Regions)
	both := &Config{Pin: []PinRule{specPin, strictPin}}
	assert.Equal(t, strictIdentity, PinIdentityDigest(strictKey))
	assert.Equal(t, strictRef, both.ShortPinRef(strictKey))
	assert.Equal(t, strictVersion, both.PinVersion(strictPin))

	// An unknown mode and an undefined check keep a deterministic
	// version with a marked part (recomputed from the §1.2 scheme).
	unknownMode := specPin
	unknownMode.Mode = "strict"
	assert.Equal(t, "614d25b44eb82f4e81dff0a833eeba45174a11c8556ed1b032a1b1c716a0e432",
		plain.PinVersion(unknownMode))

	unresolved := specPin
	unresolved.Mode, unresolved.Check = "check", "no-such"
	assert.Equal(t, "8e3fcd3f1f3db466d695e87d4f7a93d0f619abe381de45692189c7c68648b847",
		plain.PinVersion(unresolved))

	dc := DeliveryContext{ClientID: "claude", ReceiverID: "receiver-1"}
	assert.Equal(t, contextDigest("/repo", dc), composeDigest("context", "/repo", dc.ClientID, dc.ReceiverID))
	assert.Equal(t, contextDigest("/repo", DeliveryContext{ClientID: "a", ReceiverID: "b\x00c"}),
		composeDigest("context", "/repo", "a", "b\x00c"))
}

// TestGuidanceDigestVectors asserts the guidance/1 vectors of K2. File
// order, budgets, mute, threshold, hook_delivery, and unreferenced checks
// are not parts, so they keep the digest.
func TestGuidanceDigestVectors(t *testing.T) {
	const (
		noPins      = "d977f991ac883e37bdee2be1d3f606d34e22e18002dcb4cffd01ef59119e0c5f"
		plainOnly   = "65924b3adbef88757244295fca4d0c39f128049e099e9d61a7892caf7c07511f"
		enforceOnly = "61fdb1daab8004bd2309765f056e74b299b6d8a350364dc5de40909c74e2b3a1"
		duplicates  = "578a96a10d63daf932707de906b528d5d63622c14f0d98fa23bc8c3af92b5363"
		exampleL1   = "bdeadde9766c9bca721cbdeea2af6a929d344f35e394847a06468dd59545d4e3"
	)

	parse := func(t *testing.T, text string) *Config {
		t.Helper()

		cfg, err := ParseConfig([]byte(text))
		require.NoError(t, err)

		return cfg
	}

	const plainEntry = `  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
`

	const enforceEntry = `  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
    mode: enforce
    check: frozen-assets
`

	const strictEntry = `  - rule: keep-parser-strict
    region: internal/parse
    note: Reject unknown fields in the parser.
`

	const checks = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
`

	assert.Equal(t, noPins, parse(t, "").GuidanceDigest())
	assert.Equal(t, noPins, (&Config{}).GuidanceDigest())
	assert.Equal(t, plainOnly, parse(t, "pin:\n"+plainEntry).GuidanceDigest())
	assert.Equal(t, enforceOnly, parse(t, specExample).GuidanceDigest())
	assert.Equal(t, enforceOnly, parse(t, checks+"pin:\n"+enforceEntry).GuidanceDigest(),
		"the unreferenced no-todo check of the spec example is not a part")

	assert.Equal(t, duplicates, parse(t, checks+"pin:\n"+plainEntry+enforceEntry).GuidanceDigest())
	assert.Equal(t, duplicates, parse(t, checks+"pin:\n"+enforceEntry+plainEntry).GuidanceDigest(),
		"file order is cosmetic")

	assert.Equal(t, exampleL1, parse(t, checks+"pin:\n"+enforceEntry+strictEntry).GuidanceDigest())
	assert.Equal(t, exampleL1, parse(t, checks+"pin:\n"+strictEntry+enforceEntry).GuidanceDigest())
	assert.Equal(t, exampleL1, parse(t, `threshold: 5
hook_delivery: once-per-context
pin_budget: 1
change_budget: 2
mute:
  - rule: E501
    region: scripts
`+specExample+strictEntry).GuidanceDigest(), "budgets, mute, threshold, and hook_delivery are not parts")
}

// TestPinVersionCosmeticVersusSemantic walks the AC-16 table of K2. Each
// case parses two byte-different lessons.yaml texts with one pin and
// compares the pin version and the pin-ref of that pin.
func TestPinVersionCosmeticVersusSemantic(t *testing.T) {
	const checks = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
`

	// bound is the base of the mode and check rows: one enforce pin bound
	// to the frozen-assets check.
	bound := checks + `pin:
  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
    mode: enforce
    check: frozen-assets
`

	cases := []struct {
		name        string
		before      string
		after       string
		sameVersion bool
		sameRef     bool
	}{
		{
			name:        "reorder regions",
			before:      "pin:\n  - rule: r\n    region: a\n    regions: [b, c]\n    note: n\n",
			after:       "pin:\n  - rule: r\n    region: a\n    regions: [c, b]\n    note: n\n",
			sameVersion: true, sameRef: true,
		},
		{
			name:        "move a region between region and regions",
			before:      "pin:\n  - rule: r\n    region: a\n    regions: [b]\n    note: n\n",
			after:       "pin:\n  - rule: r\n    region: b\n    regions: [a]\n    note: n\n",
			sameVersion: true, sameRef: true,
		},
		{
			name:        "trailing slash",
			before:      "pin:\n  - rule: r\n    region: a/b/\n    note: n\n",
			after:       "pin:\n  - rule: r\n    region: a/b\n    note: n\n",
			sameVersion: true, sameRef: true,
		},
		{
			name:        "duplicate region",
			before:      "pin:\n  - rule: r\n    region: a\n    note: n\n",
			after:       "pin:\n  - rule: r\n    region: a\n    regions: [a, a/]\n    note: n\n",
			sameVersion: true, sameRef: true,
		},
		{
			name:        "rule case and surrounding spaces",
			before:      "pin:\n  - rule: frozen-assets\n    region: a\n    note: n\n",
			after:       "pin:\n  - rule: '  Frozen-Assets '\n    region: a\n    note: n\n",
			sameVersion: true, sameRef: true,
		},
		{
			name: "quoting, flow and block, comments, key order, indentation",
			before: `pin:
  - rule: frozen-assets
    region: a
    regions: [b, c]
    note: Keep it.
    mode: check
    check: frozen-assets
` + checks,
			after: checks + `# A comment.
pin:
    -   note: "Keep it."   # trailing comment
        check: 'frozen-assets'
        mode: "check"
        regions:
            - c
            - b
        region: 'a'
        rule: "frozen-assets"
`,
			sameVersion: true, sameRef: true,
		},
		{
			name:        "note surrounding spaces",
			before:      "pin:\n  - rule: r\n    region: a\n    note: Keep it.\n",
			after:       "pin:\n  - rule: r\n    region: a\n    note: '  Keep it.  '\n",
			sameVersion: true, sameRef: true,
		},
		{
			name:        "note CRLF versus LF",
			before:      "pin:\n  - rule: r\n    region: a\n    note: \"one\\ntwo\"\n",
			after:       "pin:\n  - rule: r\n    region: a\n    note: \"one\\r\\ntwo\"\n",
			sameVersion: true, sameRef: true,
		},
		{
			name:        "mode omitted versus mode advisory",
			before:      "pin:\n  - rule: r\n    region: a\n    note: n\n",
			after:       "pin:\n  - rule: r\n    region: a\n    note: n\n    mode: advisory\n",
			sameVersion: true, sameRef: true,
		},
		{
			name:   "rename a check ID and keep its definition",
			before: bound,
			after: strings.NewReplacer("  frozen-assets:\n", "  frozen:\n", "check: frozen-assets", "check: frozen").
				Replace(bound),
			sameVersion: true, sameRef: true,
		},
		{
			name:        "note wording",
			before:      "pin:\n  - rule: r\n    region: a\n    note: Keep it.\n",
			after:       "pin:\n  - rule: r\n    region: a\n    note: Keep it stable.\n",
			sameVersion: false, sameRef: true,
		},
		{
			name:        "note case",
			before:      "pin:\n  - rule: r\n    region: a\n    note: Keep it.\n",
			after:       "pin:\n  - rule: r\n    region: a\n    note: keep it.\n",
			sameVersion: false, sameRef: true,
		},
		{
			name:        "retarget",
			before:      "pin:\n  - rule: r\n    region: a\n    note: n\n",
			after:       "pin:\n  - rule: r\n    region: a\n    regions: [b]\n    note: n\n",
			sameVersion: false, sameRef: false,
		},
		{
			name:        "rule rename",
			before:      "pin:\n  - rule: r\n    region: a\n    note: n\n",
			after:       "pin:\n  - rule: s\n    region: a\n    note: n\n",
			sameVersion: false, sameRef: false,
		},
		{
			name:        "mode change",
			before:      bound,
			after:       strings.Replace(bound, "mode: enforce", "mode: check", 1),
			sameVersion: false, sameRef: true,
		},
		{
			name:        "check reference to a different definition",
			before:      bound,
			after:       strings.Replace(bound, "check: frozen-assets", "check: no-todo", 1),
			sameVersion: false, sameRef: true,
		},
		{
			name:   "pattern change",
			before: strings.Replace(bound, "check: frozen-assets", "check: no-todo", 1),
			after: strings.Replace(strings.Replace(bound, "check: frozen-assets", "check: no-todo", 1),
				`'TODO\('`, `'FIXME'`, 1),
			sameVersion: false, sameRef: true,
		},
		{
			name:   "kind change",
			before: bound,
			after: strings.Replace(bound, "    kind: forbid-path-change/1\n",
				"    kind: no-added-pattern/1\n    pattern: x\n", 1),
			sameVersion: false, sameRef: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beforeRef, beforeVersion := onePinRefAndVersion(t, tc.before)
			afterRef, afterVersion := onePinRefAndVersion(t, tc.after)

			assert.Equal(t, tc.sameVersion, beforeVersion == afterVersion, "pin version")
			assert.Equal(t, tc.sameRef, beforeRef == afterRef, "pin-ref")
		})
	}
}

// onePinRefAndVersion parses a lessons.yaml text with exactly one pin
// and returns the pin-ref and the pin version of that pin.
func onePinRefAndVersion(t *testing.T, text string) (ref, version string) {
	t.Helper()

	cfg, err := ParseConfig([]byte(text))
	require.NoError(t, err)
	require.Len(t, cfg.Pin, 1)

	p := cfg.Pin[0]

	return cfg.ShortPinRef(NewPinKey(p.Rule, p.Region, p.Regions)), cfg.PinVersion(p)
}

// TestResolvePinRef proves the input rule and the four K2 failures with
// their exact messages. The rules r1080792 and r8398215 (region x) were
// found by a search: their identity digests share the first 12 hex
// characters, so they prove the prefix extension of ShortPinRef. The
// missing message is asserted by its head only (plan table (e)): the
// final form appends the --delivery clause at the end in milestone P5,
// and this test then passes unchanged.
func TestResolvePinRef(t *testing.T) {
	const (
		nearA = "95c42e0485c95ac582fdd7385cd4d9af17b1386a1e27d2012676886eaf51660f"
		nearB = "95c42e0485c90c7ccc4afb68e9ef75209bd3a9ddc7f688886c4043544e715b1e"
	)

	pinA := PinRule{Rule: "r1080792", Region: "x", Note: "a"}
	pinB := PinRule{Rule: "r8398215", Region: "x", Note: "b"}
	keyA := NewPinKey(pinA.Rule, pinA.Region, pinA.Regions)
	keyB := NewPinKey(pinB.Rule, pinB.Region, pinB.Regions)

	cfg := &Config{Pin: []PinRule{specPin, strictPin, pinA, pinB}}

	resolve := func(t *testing.T, cfg *Config, ref string) *PinRefError {
		t.Helper()

		_, err := cfg.ResolvePinRef(ref)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrPinRef)

		var refErr *PinRefError
		require.True(t, errors.As(err, &refErr))

		return refErr
	}

	t.Run("a unique prefix names one entry", func(t *testing.T) {
		for ref, want := range map[string]int{
			specRef:               0,
			specIdentity:          0,
			"  3215E7B483D1 \t":   0,
			specRef[:MinPinRef]:   0,
			strictRef:             1,
			"95c42e0485c95":       2,
			"95c42e0485c90":       3,
			nearB[:ShownPinRef+2]: 3,
		} {
			got, err := cfg.ResolvePinRef(ref)
			require.NoError(t, err, ref)
			assert.Equal(t, want, got, ref)
		}
	})

	t.Run("ShortPinRef grows past a shared prefix", func(t *testing.T) {
		assert.Equal(t, nearA, PinIdentityDigest(keyA))
		assert.Equal(t, nearB, PinIdentityDigest(keyB))
		assert.Equal(t, nearA[:ShownPinRef+1], cfg.ShortPinRef(keyA))
		assert.Equal(t, nearB[:ShownPinRef+1], cfg.ShortPinRef(keyB))
		assert.Equal(t, specRef, cfg.ShortPinRef(NewPinKey(specPin.Rule, specPin.Region, nil)))

		alone := &Config{Pin: []PinRule{pinA}}
		assert.Equal(t, nearA[:ShownPinRef], alone.ShortPinRef(keyA), "without the near pin the shown length is enough")

		for i, p := range cfg.Pin {
			got, err := cfg.ResolvePinRef(cfg.ShortPinRef(NewPinKey(p.Rule, p.Region, p.Regions)))
			require.NoError(t, err)
			assert.Equal(t, i, got, "every printed pin-ref resolves to its own entry")
		}
	})

	t.Run("malformed", func(t *testing.T) {
		for _, ref := range []string{"", "  ", "3215e7b", "xyz12345", "3215e7b4-83d1", "3215e7b483d1g",
			strings.Repeat("a", 65)} {
			refErr := resolve(t, cfg, ref)
			assert.Equal(t, PinRefMalformed, refErr.Kind, ref)
		}

		refErr := resolve(t, cfg, " 3215E7B ")
		assert.Equal(t, "3215e7b", refErr.Ref, "the reference is trimmed and lowercased")
		assert.Equal(t,
			"pin reference \"3215e7b\": give at least 8 hex characters from the pin column of `seamark lessons --verify`",
			refErr.Error())
	})

	t.Run("missing", func(t *testing.T) {
		refErr := resolve(t, cfg, "DEADBEEF")
		assert.Equal(t, PinRefMissing, refErr.Kind)
		assert.Equal(t, "deadbeef", refErr.Ref)
		assert.True(t, strings.HasPrefix(refErr.Error(),
			"pin reference deadbeef names no pin in .seamark/lessons.yaml; "+
				"`seamark lessons --verify` lists the current references"), refErr.Error())

		absent := NewPinKey("not-pinned", "x", nil)
		assert.Len(t, cfg.ShortPinRef(absent), ShownPinRef)
		assert.Equal(t, PinRefMissing, resolve(t, cfg, cfg.ShortPinRef(absent)).Kind,
			"a key that the config does not hold gets a reference that names no entry")
	})

	t.Run("ambiguous prefix", func(t *testing.T) {
		for _, ref := range []string{nearA[:MinPinRef], nearA[:ShownPinRef]} {
			refErr := resolve(t, cfg, ref)
			assert.Equal(t, PinRefAmbiguousPrefix, refErr.Kind)
			assert.Empty(t, refErr.Entries)
			assert.Equal(t, "pin reference "+ref+" names more than one pin; give more hex characters", refErr.Error())
		}
	})

	t.Run("duplicate entries", func(t *testing.T) {
		// Entry 3 has another note and another region spelling, but the
		// same rule and region set, so it shares the identity of entry 1.
		twin := specPin
		twin.Region, twin.Note = "generated", "Another note."

		dup := &Config{Pin: []PinRule{specPin, strictPin, twin}}
		assert.Equal(t, specRef, dup.ShortPinRef(NewPinKey(twin.Rule, twin.Region, nil)),
			"duplicate entries share one digest and keep the shown length")

		refErr := resolve(t, dup, specRef)
		assert.Equal(t, PinRefDuplicateEntries, refErr.Kind)
		assert.Equal(t, []int{1, 3}, refErr.Entries)
		assert.Equal(t, "pin reference 3215e7b483d1 names 2 entries with the same rule and regions (pins 1,3); "+
			"remove the duplicate entry first", refErr.Error())

		got, err := dup.ResolvePinRef(strictRef)
		require.NoError(t, err)
		assert.Equal(t, 1, got)
	})
}

// TestIdentityDigestsMatchFiringLog proves that the exported identity
// digests equal the values that the firing log writes for the same root.
// Records of other streams join the log on these bytes. The fixed vectors
// were recomputed with a plain SHA-256, so they also freeze the bytes.
func TestIdentityDigestsMatchFiringLog(t *testing.T) {
	assert.Equal(t, "9602f27aebff954a8c02f0c98a30a635e5e2936ae6024e2a8e72a87684893d35",
		SessionDigest("/repo", "session-1"))
	assert.Equal(t, "4ce657128ec85364875d9381fdefc20c285668b0f984ba73f93a3f9a5362e0a7",
		MatchDigest("/repo", "session-1", "tool-use-1"))
	assert.Equal(t, "37dd97e2a63c2e3e160a07399021b1c5d143b49b67f1e6fc85999fef949d5c83",
		ContextDigest("/repo", "claude", "receiver-1"))

	root := t.TempDir()
	lessons := []model.Lesson{{Region: "api", Symptom: "Keep the generated client synchronized."}}

	require.NoError(t, RecordHookDeliveryFiles(root, []string{"api/a.go"}, "Edit", lessons, HookDelivery{
		Status: DeliveryInjected, SessionID: "session-1", MatchID: "tool-use-1", ContextBytes: 90,
		ClientID: "claude", Mechanism: "pre-tool-use-context", ReceiverID: "receiver-1",
	}))

	firings, err := ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 1)

	assert.Equal(t, SessionDigest(root, "session-1"), firings[0].SessionSHA)
	assert.Equal(t, MatchDigest(root, "session-1", "tool-use-1"), firings[0].MatchSHA)
	assert.Equal(t, ContextDigest(root, "claude", "receiver-1"), firings[0].ContextSHA)
	assert.NotEqual(t, SessionDigest("/repo", "session-1"), firings[0].SessionSHA,
		"another root gives another digest: identity is never loosened")
}
