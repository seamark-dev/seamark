package integration

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// timingItemCases names, per client, item, and context, the cases of
// the gate-3 timing scenario whose replay and review show the item
// (gate-3 checklist, "The cases"). An item and context without a case
// has no recording that can verify it.
var timingItemCases = map[string]map[lessoncheck.AttributionItem]map[lessoncheck.PathContext][]string{
	ClaudeID: {
		lessoncheck.ItemMatchIdentity: {
			lessoncheck.ContextRoot:  {timingPriorResult, timingSameCall},
			lessoncheck.ContextChild: {timingSubagentRead},
		},
		lessoncheck.ItemLaneIdentity: {
			lessoncheck.ContextRoot:  {timingPriorResult},
			lessoncheck.ContextChild: {timingSubagentRead},
		},
		lessoncheck.ItemTurnBoundary: {
			lessoncheck.ContextRoot: {timingPriorResult, timingNewModelStep},
		},
		lessoncheck.ItemModelBoundary: {
			lessoncheck.ContextRoot: {timingReadThenEdit},
		},
	},
	CodexID: {
		lessoncheck.ItemMatchIdentity: {
			lessoncheck.ContextRoot: {timingSameCall},
		},
		lessoncheck.ItemLaneIdentity: {
			lessoncheck.ContextRoot: {timingSameCall},
		},
		lessoncheck.ItemTurnBoundary: {
			lessoncheck.ContextRoot: {timingPriorPreparation, timingPatchAfterPreparation},
		},
	},
}

// readTimingCases reads the manifest and the case names of the reviewed
// gate-3 timing recording of a client. It returns false when no
// recording exists. Unlike loadTimingRecording, it never skips, because
// the evidence check runs in every test run.
func readTimingCases(t *testing.T, client string) (nativeManifest, []string, bool) {
	t.Helper()

	dir := filepath.Join("testdata", client, "timing")

	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nativeManifest{}, nil, false
	}

	require.NoError(t, err)

	var manifest nativeManifest
	require.NoError(t, json.Unmarshal(data, &manifest))
	require.Equal(t, client, manifest.Client)

	var cases []string

	for _, line := range timingLines(t, filepath.Join(dir, timingCasesFile)) {
		var c struct {
			Case string `json:"case"`
		}

		require.NoError(t, json.Unmarshal(line, &c))

		cases = append(cases, c.Case)
	}

	return manifest, cases, true
}

// TestTimingEvidenceMatchesRecordings proves that TimingEvidence reads
// verified only for an item, client version, and surface that a reviewed
// gate-3 timing recording covers (plan step P4.11, contract K10). The
// recording is the one that the P3.14 replay tests decode. Verified
// evidence needs the recording in testdata/<client>/timing, its client
// version, a surface that names the item and the context, and every
// case that shows the item in that context. An item that the payloads
// can give stays pending without such a recording; any other item has
// no evidence. The test never skips.
func TestTimingEvidenceMatchesRecordings(t *testing.T) {
	for _, client := range slices.Sorted(maps.Keys(payloadIdentities)) {
		manifest, cases, recorded := readTimingCases(t, client)

		for _, item := range lessoncheck.AttributionItems {
			if item == lessoncheck.ItemContextReset {
				// The resets capability gives this item its evidence.
				continue
			}

			for _, context := range lessonContexts {
				label := client + " " + string(item) + " " + string(context)
				evidence := TimingEvidence(client, item, context)
				require.NoError(t, evidence.Validate(), label)

				if evidence.Level != VerificationVerified {
					if slices.Contains(payloadIdentities[client][context], item) {
						assert.Equal(t, VerificationPending, evidence.Level, label)
						assert.Contains(t, evidence.Surface, string(item), label)
						assert.Contains(t, evidence.Surface, string(context), label)
					} else {
						assert.Equal(t, VerificationEvidence{}, evidence, "%s: the payloads cannot give the item", label)
					}

					continue
				}

				require.True(t, recorded, "%s: verified evidence needs a reviewed gate-3 recording in testdata/%s/timing",
					label, client)
				assert.True(t, sameClientVersion(evidence.ClientVersion, manifest.ClientVersion),
					"%s: the evidence names %q, the recording ran on %q", label, evidence.ClientVersion, manifest.ClientVersion)
				assert.Contains(t, evidence.Surface, string(item), label)
				assert.Contains(t, evidence.Surface, string(context), label)

				want := timingItemCases[client][item][context]
				require.NotEmpty(t, want, "%s: no case of the gate-3 timing scenario shows this item in this context", label)
				assert.Subset(t, cases, want, "%s: the recording lacks a case that shows the item", label)
			}
		}
	}

	t.Run("each recorded entry names an item that the payloads give", func(t *testing.T) {
		for p := range timingRecorded {
			assert.Equal(t, VerificationVerified, TimingEvidence(p.client, p.item, p.context).Level,
				"%s %s %s: the entry names no attribution, so it never reaches the report", p.client, p.item, p.context)
		}
	})
}
