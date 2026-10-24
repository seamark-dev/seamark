package report

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// firingPin is the expected firing-log pin of the pin rule p under cfg.
func firingPin(cfg *reviews.Config, p reviews.PinRule, delivery reviews.PinDelivery, cause string) reviews.FiringPin {
	return reviews.FiringPin{
		Identity: reviews.PinIdentityDigest(reviews.NewPinKey(p.Rule, p.Region, p.Regions)),
		Version:  cfg.PinVersion(p),
		Delivery: delivery,
		Cause:    cause,
	}
}

// deliveries returns n copies of d, one per selected pin.
func deliveries(d reviews.PinDelivery, n int) []reviews.PinDelivery {
	out := make([]reviews.PinDelivery, n)
	for i := range out {
		out[i] = d
	}

	return out
}

func TestFiringPinsMapEverySelectionList(t *testing.T) {
	t.Run("each list gets its delivery and cause, in entry order", func(t *testing.T) {
		cfg := goldenConfig(append(distinctPins("api", 5),
			// Entry 6 has the wording of entry 1 on another region, so
			// it is another identity.
			reviews.PinRule{Rule: "alpha-guard", Region: "*", Note: "n"},
			// Entry 7 repeats entry 2 exactly: one identity, one version.
			distinctPins("api", 2)[1],
		)...)
		pin := func(entry int) reviews.SurfacedPin { return reviews.SurfacedPin{Pin: cfg.Pin[entry-1]} }

		// The fold order is not the entry order, and a mined lesson is
		// in the selection too.
		sel := Selection{
			Pins:       []reviews.SurfacedPin{pin(3), pin(1)},
			Restated:   []reviews.SurfacedPin{pin(6)},
			OverPinCap: []reviews.SurfacedPin{pin(5)},
			OverTotal:  []reviews.SurfacedPin{pin(4), pin(7)},
			Mined:      []model.Lesson{{Region: "api", Symptom: "RUF001", Occurrences: 4}},
		}

		got := sel.FiringPins(cfg, []reviews.PinDelivery{reviews.PinInjected, reviews.PinSuppressedRepeat})

		assert.Equal(t, []reviews.FiringPin{
			firingPin(cfg, cfg.Pin[0], reviews.PinSuppressedRepeat, ""),
			// Entry 7 resolves to entry 2: the two cannot be told apart.
			firingPin(cfg, cfg.Pin[1], reviews.PinHeldBack, reviews.CauseOverTotal),
			firingPin(cfg, cfg.Pin[2], reviews.PinInjected, ""),
			firingPin(cfg, cfg.Pin[3], reviews.PinHeldBack, reviews.CauseOverTotal),
			firingPin(cfg, cfg.Pin[4], reviews.PinHeldBack, reviews.CauseOverPinCap),
			firingPin(cfg, cfg.Pin[5], reviews.PinHeldBack, reviews.CauseRestated),
		}, got, "every pin list maps, no mined lesson does, and entries set the order")

		assert.Equal(t, 2, EntryOf(cfg, pin(7)), "an exact repeat resolves to its first entry")
		assert.Equal(t, 6, EntryOf(cfg, pin(6)))

		changed := cfg.Pin[0]
		changed.Note = "another meaning"
		assert.Zero(t, EntryOf(cfg, reviews.SurfacedPin{Pin: changed}), "a new version names no entry")
	})

	t.Run("a pin without an entry goes last", func(t *testing.T) {
		cfg := goldenConfig(distinctPins("api", 2)...)
		foreign := reviews.PinRule{Rule: "zulu-guard", Region: "api", Note: "n"}

		sel := Selection{Pins: []reviews.SurfacedPin{{Pin: foreign}, {Pin: cfg.Pin[1]}}}
		got := sel.FiringPins(cfg, deliveries(reviews.PinReturned, 2))

		assert.Equal(t, []reviews.FiringPin{
			firingPin(cfg, cfg.Pin[1], reviews.PinReturned, ""),
			firingPin(cfg, foreign, reviews.PinReturned, ""),
		}, got)
	})

	t.Run("a selected pin without a delivery is refused by the writer", func(t *testing.T) {
		cfg := goldenConfig(distinctPins("api", 2)...)
		sel := Selection{Pins: []reviews.SurfacedPin{{Pin: cfg.Pin[0]}, {Pin: cfg.Pin[1]}}}

		got := sel.FiringPins(cfg, []reviews.PinDelivery{reviews.PinInjected})
		require.Len(t, got, 2)
		assert.Empty(t, got[1].Delivery)

		root := t.TempDir()
		_, err := reviews.RecordEvent(reviews.AuditEvent{
			Root: root, Records: []reviews.EventRecord{{Status: reviews.DeliveryInjected}}, Pins: got,
		})
		require.ErrorContains(t, err, `unknown pin delivery ""`)
		assert.NoFileExists(t, reviews.AuditLogPath(root), "the writer refuses the whole event")
	})

	t.Run("the folds of the hook and of change_set", func(t *testing.T) {
		st := openLessonStore(t)

		cfg, err := reviews.ParseConfig([]byte("pin_budget: 2\nchange_budget: 2\npin:\n" +
			"  - {rule: alpha-guard, region: api, note: n}\n" +
			"  - {rule: alpha-guard, region: '*', note: n}\n" +
			"  - {rule: bravo-guard, region: api, note: n}\n" +
			"  - {rule: charlie-guard, region: api, note: n}\n" +
			"  - {rule: delta-guard, region: api, note: n}\n"))
		require.NoError(t, err)

		ev, err := ReadEvidence(st, []string{"api/a.go"}, cfg)
		require.NoError(t, err)

		hook, err := ev.HookSelection(cfg)
		require.NoError(t, err)

		// The collapse drops entry 2, and the pin cap holds back 4 and 5.
		got := hook.FiringPins(cfg, deliveries(reviews.PinInjected, len(hook.Pins)))
		assert.Equal(t, []reviews.FiringPin{
			firingPin(cfg, cfg.Pin[0], reviews.PinInjected, ""),
			firingPin(cfg, cfg.Pin[1], reviews.PinHeldBack, reviews.CauseRestated),
			firingPin(cfg, cfg.Pin[2], reviews.PinInjected, ""),
			firingPin(cfg, cfg.Pin[3], reviews.PinHeldBack, reviews.CauseOverPinCap),
			firingPin(cfg, cfg.Pin[4], reviews.PinHeldBack, reviews.CauseOverPinCap),
		}, got)
		assert.Equal(t, hook.HeldBack, countHeldBack(got), "the hook count is the held-back pins")

		changeSet, err := ev.ChangeSetSelection(cfg)
		require.NoError(t, err)

		// change_set has no pin cap: the one budget holds back 4 and 5.
		got = changeSet.FiringPins(cfg, deliveries(reviews.PinReturned, len(changeSet.Pins)))
		assert.Equal(t, []reviews.FiringPin{
			firingPin(cfg, cfg.Pin[0], reviews.PinReturned, ""),
			firingPin(cfg, cfg.Pin[1], reviews.PinHeldBack, reviews.CauseRestated),
			firingPin(cfg, cfg.Pin[2], reviews.PinReturned, ""),
			firingPin(cfg, cfg.Pin[3], reviews.PinHeldBack, reviews.CauseOverTotal),
			firingPin(cfg, cfg.Pin[4], reviews.PinHeldBack, reviews.CauseOverTotal),
		}, got)
	})
}

// countHeldBack counts the held-back pins of a firing record.
func countHeldBack(pins []reviews.FiringPin) int {
	n := 0

	for _, p := range pins {
		if p.Delivery == reviews.PinHeldBack {
			n++
		}
	}

	return n
}

func TestCheckAdvisoryWritesAV2RecordAndKeepsItsText(t *testing.T) {
	const header = "advisory — recurring lessons for touched files (not part of the verdict)"

	files := []string{"scripts/task.py", "web/new.ts"}
	eventID := regexp.MustCompile(`^[0-9a-f]{32}$`)

	t.Run("pins returned and held back", func(t *testing.T) {
		st, root := seedStore(t)
		writeLessonsYAML(t, root, "change_budget: 2\npin:\n"+
			"  - rule: guard-empty-datasets\n    region: scripts\n    note: Guard datasets before reductions.\n"+
			"  - rule: guard-empty-datasets\n    region: '*'\n    note: Guard datasets before reductions.\n"+
			"  - rule: transaction-atomicity\n    region: scripts\n    note: Wrap dependent writes in one transaction.\n"+
			"  - rule: close-cursors\n    region: scripts\n    note: Close cursors in a finally block.\n")

		cfg, err := reviews.LoadConfig(root)
		require.NoError(t, err)

		// The text is the one of the selector the advisory used before.
		lessons, trimmed, err := LessonsForFiles(st, cfg, files, cfg.ChangeSetBudget())
		require.NoError(t, err)

		var want strings.Builder
		PrintLessonBlock(&want, header, lessons, trimmed)

		var got strings.Builder
		CheckAdvisory(&got, st, root, files)
		assert.Equal(t, want.String(), got.String(), "the advisory keeps its text")

		rec := readOneFiring(t, root)
		assert.Equal(t, reviews.Firing{Surface: "check", Files: files, Fired: firedOf(lessons)}, legacyFields(rec))

		assert.Equal(t, reviews.AuditSchemaVersion, rec.SchemaVersion)
		assert.Regexp(t, eventID, rec.EventID)
		assert.Equal(t, 1, rec.EventSeq)
		assert.Equal(t, 1, rec.EventRecords)
		assert.Equal(t, "returned", rec.Emission)
		assert.Equal(t, "complete", rec.Evaluation)
		require.NotNil(t, rec.PathsComplete)
		assert.True(t, *rec.PathsComplete)
		assert.Equal(t, cfg.GuidanceDigest(), rec.GuidanceSHA)
		assert.Equal(t, GuidanceOK, rec.GuidanceStatus)

		// Budget 2: the collapse drops entry 2, and the budget holds back
		// entry 4. The mined lesson has no pin, so it is in fired only.
		// The record orders the pins by delivery, then by entry.
		assert.Equal(t, []reviews.FiringPin{
			firingPin(cfg, cfg.Pin[0], reviews.PinReturned, ""),
			firingPin(cfg, cfg.Pin[2], reviews.PinReturned, ""),
			firingPin(cfg, cfg.Pin[1], reviews.PinHeldBack, reviews.CauseRestated),
			firingPin(cfg, cfg.Pin[3], reviews.PinHeldBack, reviews.CauseOverTotal),
		}, rec.Pins)

		// The advisory has no receiver (contract L3, L1 receiver unknown).
		assert.Empty(t, rec.TurnSHA)
		assert.Empty(t, rec.Lane)
		assert.Nil(t, rec.ContextResettable)
		assert.False(t, rec.CaptureOptIn)
		assert.Empty(t, rec.ScopeSHA, "scope_sha256 belongs to change_set and prepare")
		assert.Zero(t, rec.RejectedPaths)
	})

	t.Run("no lessons.yaml", func(t *testing.T) {
		st, root := seedStore(t)

		CheckAdvisory(&strings.Builder{}, st, root, files)

		rec := readOneFiring(t, root)
		assert.Equal(t, GuidanceAbsent, rec.GuidanceStatus)
		assert.Equal(t, reviews.DefaultConfig().GuidanceDigest(), rec.GuidanceSHA, "absent guidance is the defaults")
		assert.Empty(t, rec.Pins, "mined lessons are never pins")
		assert.NotEmpty(t, rec.Fired)
	})

	t.Run("an invalid lessons.yaml", func(t *testing.T) {
		st, root := seedStore(t)
		writeLessonsYAML(t, root, "pin: [\n")

		var got strings.Builder
		CheckAdvisory(&got, st, root, files)
		assert.Contains(t, got.String(), header, "the defaults still advise")

		rec := readOneFiring(t, root)
		assert.Equal(t, GuidanceInvalid, rec.GuidanceStatus)
		assert.Empty(t, rec.GuidanceSHA, "the pins of the file are unknown, so no digest applies")
	})

	t.Run("an unreadable lessons.yaml is not absent", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores file permissions")
		}

		st, root := seedStore(t)
		writeLessonsYAML(t, root, "pin: []\n")

		path := filepath.Join(root, ".seamark", "lessons.yaml")
		require.NoError(t, os.Chmod(path, 0o000))
		t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

		CheckAdvisory(&strings.Builder{}, st, root, files)

		rec := readOneFiring(t, root)
		assert.Equal(t, GuidanceInvalid, rec.GuidanceStatus)
	})
}

// firedOf returns the fired list of a record that holds lessons.
func firedOf(lessons []model.Lesson) []reviews.FiredLesson {
	fired := make([]reviews.FiredLesson, len(lessons))
	for i, l := range lessons {
		fired[i] = reviews.FiredLesson{Region: l.Region, Symptom: l.Symptom}
	}

	return fired
}

func TestPrintFiringSummaryNamesExploreAndPrepareOnlyWhenPresent(t *testing.T) {
	lesson := []model.Lesson{{Region: "api", Symptom: "RUF001"}}

	// event writes one record of a surface the way its writer does.
	event := func(surface string, status reviews.DeliveryStatus) reviews.AuditEvent {
		return reviews.AuditEvent{
			Surface: surface, Files: []string{"api/a.go"},
			Records: []reviews.EventRecord{{Status: status, Lessons: lesson}},
		}
	}

	hook := event("", reviews.DeliveryInjected)
	explore := event("explore", reviews.DeliveryInjected)
	prepare := event("prepare", "")

	cases := []struct {
		name   string
		events []reviews.AuditEvent
		line   string
	}{
		{"neither surface", []reviews.AuditEvent{hook, event("check", "")},
			"lesson firings — 1 hook reminders, 0 change_set, 1 check — across 1 files\n"},
		{"explore only", []reviews.AuditEvent{hook, explore, explore},
			"lesson firings — 1 hook reminders, 0 change_set, 0 check, 2 explore — across 1 files\n"},
		{"prepare only", []reviews.AuditEvent{hook, prepare},
			"lesson firings — 1 hook reminders, 0 change_set, 0 check, 1 prepare — across 1 files\n"},
		{"both surfaces", []reviews.AuditEvent{hook, explore, prepare, prepare},
			"lesson firings — 1 hook reminders, 0 change_set, 0 check, 1 explore, 2 prepare — across 1 files\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()

			for _, ev := range tc.events {
				ev.Root = root
				_, err := reviews.RecordEvent(ev)
				require.NoError(t, err)
			}

			firings, err := reviews.ReadFirings(root)
			require.NoError(t, err)

			var out strings.Builder
			PrintFiringSummary(&out, reviews.Summarize(firings, nil))

			first, _, _ := strings.Cut(out.String(), "\n")
			assert.Equal(t, tc.line, first+"\n")
		})
	}
}
