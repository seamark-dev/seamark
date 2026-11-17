package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file proves one pin version across the surfaces. The firing
// record of the edit hook and lesson-preparation/1 name one pin
// definition with one pin_version, so delivery/1 can join them. No step
// calls a model.

// pinVersionYAML holds a null pin item and an enforce pin on a.go that
// is bound to a no-added-pattern/1 check. yaml.v3 drops the null item,
// and the pin listing skips it, so a surface that counts positions
// differently would name another definition.
const pinVersionYAML = `lesson_checks_version: 1
checks:
  no-todo-calls:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  -
  - rule: no-todo-calls
    region: a.go
    note: Track open work in the issue tracker.
    mode: enforce
    check: no-todo-calls
`

// pinVersionWire is one pin of a JSON document, by the shared key
// names. Each surface decodes its own bytes through it, so a surface
// with another key name fails the test too.
type pinVersionWire struct {
	PinIdentity string `json:"pin_identity"`
	PinVersion  string `json:"pin_version"`
}

// pinVersionDoc holds the keys of a firing record or a preparation that
// name pins. A key that a document lacks stays empty.
type pinVersionDoc struct {
	Surface string           `json:"surface"`
	Pins    []pinVersionWire `json:"pins"`
	Checks  []pinVersionWire `json:"checks"`
}

// pinVersionDecode decodes one JSON document of a surface.
func pinVersionDecode(t *testing.T, data []byte) pinVersionDoc {
	t.Helper()

	var doc pinVersionDoc
	require.NoError(t, json.Unmarshal(data, &doc), "%s", data)

	return doc
}

// pinVersionHookRecords returns each line of the firing log under root
// that the edit hook wrote with pins. The edit hook writes no surface.
func pinVersionHookRecords(t *testing.T, root string) []pinVersionDoc {
	t.Helper()

	data, err := os.ReadFile(reviews.AuditLogPath(root))
	require.NoError(t, err)

	var records []pinVersionDoc

	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		rec := pinVersionDecode(t, sc.Bytes())
		if rec.Surface == "" && len(rec.Pins) > 0 {
			records = append(records, rec)
		}
	}

	require.NoError(t, sc.Err())

	return records
}

// TestLessonsPinVersionAgreesAcrossSurfaces proves the cross-surface
// version: for an enforce pin bound to a no-added-pattern/1 check, after
// a null pin item, one lessons --hook record and lessons --prepare --json
// give byte-equal pin_version values. Each one equals the version of one
// reviews.ParseConfig of the file.
func TestLessonsPinVersionAgreesAcrossSurfaces(t *testing.T) {
	root := hookRepo(t)
	verifyWrite(t, root, ".seamark/lessons.yaml", pinVersionYAML)
	sentinel := newInvokerSentinel(t, root)

	cfg, err := reviews.LoadConfig(root)
	require.NoError(t, err)
	require.Len(t, cfg.Pin, 1, "the null item is no pin")

	pin := cfg.Pin[0]
	want := pinVersionWire{
		PinIdentity: reviews.PinIdentityDigest(reviews.NewPinKey(pin.Rule, pin.Region, pin.Regions)),
		PinVersion:  cfg.PinVersion(pin),
	}

	// Mode and check are version parts. So a surface that drops them
	// gives another version, and the comparison below fails.
	advisory := pin
	advisory.Mode, advisory.Check = "", ""
	require.NotEqual(t, cfg.PinVersion(advisory), want.PinVersion)

	// One edit of a.go: the hook advises the pin and records it.
	out, _, err := runIn(t, editPayload(filepath.Join(root, "a.go"), "session-1", "toolu-1", "prompt-1"),
		"-C", root, "lessons", "--hook")
	require.NoError(t, err)
	assert.Contains(t, out, "no-todo-calls")

	hook := pinVersionHookRecords(t, root)
	require.Len(t, hook, 1, "one hook record carries the pins")
	assert.Equal(t, []pinVersionWire{want}, hook[0].Pins, "audit v2 pins[]")

	// The preparation names the pin and its check with the same version.
	stdout, err := prepareRun(t, root, filepath.Join(root, "a.go"), "--json")
	require.NoError(t, err)

	prepared := pinVersionDecode(t, []byte(stdout))
	assert.Equal(t, "prepare", prepared.Surface)
	assert.Equal(t, []pinVersionWire{want}, prepared.Pins, "lesson-preparation/1 pins[]")
	assert.Equal(t, []pinVersionWire{{PinVersion: want.PinVersion}}, prepared.Checks,
		"lesson-preparation/1 checks[]")

	sentinel.requireNoCall(t)
}
