package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"

	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/scaffold"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/workspace"
)

func TestLessonsDeliveryCallsNoModel(t *testing.T) {
	f := newDeliveryFixture(t, "")
	f.index(t)
	sentinel := newInvokerSentinel(t, f.root)

	// Existing state must remain intact, not just the reporting sources.
	for _, path := range []string{"lessons-hook-state.json", "lessons-explore-state.json", "checks/state.json"} {
		verifyWrite(t, f.root, ".seamark/"+path, "untouched-state\n")
	}

	before := deliveryTree(t, f.root)
	for _, args := range [][]string{nil, {"--json"}, {"--pin", f.pinRef()}, {"--pin", f.pinRef(), "--json"}} {
		out, err := deliveryRun(t, f.root, args...)
		require.NoError(t, err)
		require.NotEmpty(t, out)
		assert.Equal(t, before, deliveryTree(t, f.root), "route %v must be a read", args)
	}

	sentinel.requireNoCall(t)
}

// TestLessonsDeliveryJSONIsDelivery1 reads the delivery/1 document of an
// expansion. This build reads only the audit logs. So the fixed fields,
// which have no source in this build, keep their fixed values.
func TestLessonsDeliveryJSONIsDelivery1(t *testing.T) {
	f := newDeliveryFixture(t, "")
	r, out := deliveryReport(t, f.root, "--pin", f.pinRef())

	assert.Equal(t, "delivery/1", r.Schema)
	assert.Equal(t, version, r.SeamarkVersion)
	assert.Contains(t, out, "\n  \"schema\": \"delivery/1\",")
	assert.NotContains(t, out, ": null")
	assert.Equal(t, lessonreport.CaptureUnavailable, r.Capture.State)
	assert.Equal(t, lessonreport.StateAbsent, r.AsOf.Archive.State)
	assert.Equal(t, lessonreport.ArchiveNotReadError, r.AsOf.Archive.Error)
	require.Len(t, r.Pins, 2)
	assert.Empty(t, r.Historical)
	require.NotNil(t, r.Expanded)
	assert.Empty(t, r.Expanded.Occurrences)
	assert.Zero(t, r.Pins[0].Occurrences.Occurrences)
	assert.Equal(t, 1, r.Pins[0].Delivery.Injected)
	assert.Equal(t, 1, r.Totals.DeliveredRecords["hook"])
	assert.Equal(t, "assessed feedback occurrences: one feedback item and one pin version (association key; known copies of one feedback count once); by_origin counts each distinct origin of an occurrence once, so one occurrence can count under two origins", r.Units["occurrences"])
	assert.Equal(t, "delivered sessions", r.Pins[0].Observation.Unit)
	assert.Equal(t, "feedback items", r.Unlinked.Unit)
	assert.Equal(t, 1, r.Checks.Operations["verify"])
	assert.Zero(t, r.Checks.GuardAttempts.Prevented)

	// One JSON document only, with no text report appended.
	decoder := json.NewDecoder(strings.NewReader(out))
	var value any
	require.NoError(t, decoder.Decode(&value))
	require.ErrorIs(t, decoder.Decode(&value), io.EOF)
}

// TestLessonsDeliveryKeepsTheFixedFields reads the report of one hook
// delivery. The JSON keeps the fixed fields at their values, the text
// leaves them out, and the report writes nothing.
func TestLessonsDeliveryKeepsTheFixedFields(t *testing.T) {
	f := newDeliveryFixture(t, "")
	before := deliveryTree(t, f.root)

	r, _ := deliveryReport(t, f.root)
	assert.Equal(t, lessonreport.CaptureUnavailableMessage, r.Capture.Message)
	assert.Equal(t, "no-archive", r.Capture.Reason)
	assert.Equal(t, 1, r.Totals.DeliveredRecords["hook"])
	assert.Equal(t, 1, r.Pins[0].Delivery.Injected)
	assert.Equal(t, 1, r.Pins[0].Observation.IncompleteByReason[lessonreport.CodeSessionNotCaptured])

	// The text leaves out the lines of the fixed fields, and the JSON
	// above keeps their values.
	out, err := deliveryRun(t, f.root)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "Lesson delivery (delivery/1; seamark "), out)
	assert.Contains(t, out, "hook 1")
	assert.NotContains(t, out, "capture evidence unavailable")
	assert.NotContains(t, out, "session-not-captured")
	assert.Equal(t, before, deliveryTree(t, f.root))
	assert.NoFileExists(t, store.DefaultPath(f.root))

	// A wholly empty workspace must not even acquire a .seamark directory.
	empty := t.TempDir()
	_, err = deliveryRun(t, empty)
	require.NoError(t, err)
	assert.NoDirExists(t, filepath.Join(empty, ".seamark"))
}

// TestLessonsDeliveryOlderIndexStaysUntouched runs the report beside an
// index of an older schema. The report only reads the applied rows of
// the ledger, so the index never migrates.
func TestLessonsDeliveryOlderIndexStaysUntouched(t *testing.T) {
	f := newDeliveryFixture(t, "")
	f.index(t)
	deliverySQL(t, store.DefaultPath(f.root), "UPDATE meta SET value = '1' WHERE key = 'schema_version'")
	before := deliveryTree(t, f.root)

	for _, args := range [][]string{nil, {"--pin", f.pinRef()}} {
		r, _ := deliveryReport(t, f.root, args...)
		assert.Equal(t, lessonreport.StateAbsent, r.AsOf.Archive.State)
		assert.Equal(t, 1, r.Pins[0].Delivery.Injected)
		_, err := deliveryRun(t, f.root, args...)
		require.NoError(t, err)
		assert.Equal(t, before, deliveryTree(t, f.root), "the index may not migrate")
	}
}

// TestLessonsDeliveryPinExpansion expands the current pin. The overview
// names the expand command of the pin, and the expansion lists each
// version of the pin apart.
func TestLessonsDeliveryPinExpansion(t *testing.T) {
	f := newDeliveryFixture(t, "")
	overview, err := deliveryRun(t, f.root)
	require.NoError(t, err)
	assert.Contains(t, overview, "expand: seamark lessons --delivery --pin "+f.pinRef())

	r, _ := deliveryReport(t, f.root, "--pin", f.pinRef())
	require.NotNil(t, r.Expanded)
	assert.Equal(t, f.pinRef(), r.Expanded.PinRef)
	require.Len(t, r.Expanded.Versions, 1)
	assert.Equal(t, f.cfg.PinVersion(f.cfg.Pin[0]), r.Expanded.Versions[0])

	out, err := deliveryRun(t, f.root, "--pin", f.pinRef())
	require.NoError(t, err)
	assert.Contains(t, out, "Expansion: pin "+f.pinRef()+" (versions are never pooled)")
	assert.Contains(t, out, "  version "+r.Expanded.Versions[0])
}

func TestLessonsDeliveryRefusals(t *testing.T) {
	f := newDeliveryFixture(t, "")
	f.index(t)
	before := deliveryTree(t, f.root)

	// Every local modifier is rejected, including explicitly false booleans.
	cmd := newLessonsCmd(&options{})
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		switch flag.Name {
		case "delivery", "pin", "json", "help":
			return
		}

		t.Run(flag.Name, func(t *testing.T) {
			value := "x"
			if flag.Value.Type() == "bool" {
				value = "true"
			} else if flag.Value.Type() == "int" {
				value = "1"
			}

			want := "--delivery does not combine with --" + flag.Name
			for _, args := range [][]string{
				{"--delivery", "--" + flag.Name + "=" + value, "--json"},
				{"--" + flag.Name + "=" + value, "--json", "--delivery"},
			} {
				out, _, err := runIn(t, "", append([]string{"-C", f.root, "lessons"}, args...)...)
				requireExitOne(t, err)
				assert.EqualError(t, err, want)
				assert.Empty(t, out)
			}
		})
	})

	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "provide --file <path>, --list, --stats, --distill, --proposals, --verify, --prepare, --delivery, --apply, --dismiss, or --hook"},
		{[]string{"--pin", "12345678"}, "--pin applies to --set-mode and --delivery only"},
		{[]string{"--json"}, "--json applies to --verify, --prepare, and --delivery only"},
		{[]string{"--delivery", "--pin", "00000000", "--json"}, "pin reference 00000000 names no pin in .seamark/lessons.yaml; `seamark lessons --verify` lists the current references"},
		{[]string{"--delivery", "--pin", "bad", "--json"}, "pin reference \"bad\": give at least 8 hex characters from the pin column of `seamark lessons --verify`"},
		{[]string{"--delivery", "unexpected", "--json"}, "unexpected argument \"unexpected\" — provide --file, --list, --distill, --apply, --dismiss, or --prune"},
		{[]string{"--delivery=false", "--json"}, "--json applies to --verify, --prepare, and --delivery only"},
	} {
		out, _, err := runIn(t, "", append([]string{"-C", f.root, "lessons"}, tc.args...)...)
		requireExitOne(t, err)
		assert.EqualError(t, err, tc.want)
		assert.Empty(t, out)
	}

	for _, args := range [][]string{
		{"--delivery", "--stats=false", "--json"},
		{"--stats=false", "--json", "--delivery"},
	} {
		out, _, err := runIn(t, "", append([]string{"-C", f.root, "lessons"}, args...)...)
		requireExitOne(t, err)
		assert.EqualError(t, err, "--delivery does not combine with --stats")
		assert.Empty(t, out)
	}
	assert.Equal(t, before, deliveryTree(t, f.root))

	// This build reads no history of retired pins, so a retired identity
	// never resolves. It must not turn into a successful, unrelated
	// overview.
	ref := reviews.PinIdentityDigest(reviews.NewPinKey("retired-pin", "api", nil))[:12]
	out, err := deliveryRun(t, f.root, "--pin", ref, "--json")
	requireExitOne(t, err)
	var pinErr *reviews.PinRefError
	require.ErrorAs(t, err, &pinErr)
	assert.Equal(t, reviews.PinRefMissing, pinErr.Kind)
	assert.Empty(t, out)

	out, err = deliveryRun(t, filepath.Join(t.TempDir(), "missing"), "--json")
	requireExitOne(t, err)
	assert.Empty(t, out)
}

// TestLessonsDeliveryNextStepsNameRealCommands parses each command that
// the report prints. This build emits no next step and no inspect
// command, so the commands are the expand line of each current pin.
func TestLessonsDeliveryNextStepsNameRealCommands(t *testing.T) {
	f := newDeliveryFixture(t, "")
	r, _ := deliveryReport(t, f.root, "--pin", f.pinRef())
	for _, p := range r.Pins {
		assert.Empty(t, p.NextSteps)
	}

	assert.Empty(t, r.Unlinked.NextSteps)
	require.NotNil(t, r.Expanded)
	assert.Empty(t, r.Expanded.Occurrences)

	out, err := deliveryRun(t, f.root)
	require.NoError(t, err)

	commands := []string{}
	for _, line := range strings.Split(out, "\n") {
		if command, ok := strings.CutPrefix(strings.TrimSpace(line), "expand: "); ok {
			commands = append(commands, command)
		}
	}

	require.Len(t, commands, len(r.Pins))
	for _, command := range commands {
		parsed, err := syntax.NewParser().Parse(strings.NewReader(command), "suggestion")
		require.NoError(t, err)
		require.Len(t, parsed.Stmts, 1)
		call, ok := parsed.Stmts[0].Cmd.(*syntax.CallExpr)
		require.True(t, ok, command)
		args, err := expand.Fields(nil, call.Args...)
		require.NoError(t, err)
		require.Equal(t, "seamark", args[0])
		root := New()
		cmd, rest, err := root.Find(args[1:])
		require.NoError(t, err, command)
		require.NotSame(t, root, cmd, command)
		require.NoError(t, cmd.ParseFlags(rest), command)
		// Parse only: never dispatch suggestions or start programs.
	}
}

func TestLessonsDeliveryNoRawText(t *testing.T) {
	f := newDeliveryFixture(t, "")
	require.NotEmpty(t, f.raw)

	for _, args := range [][]string{nil, {"--json"}, {"--pin", f.pinRef()}, {"--pin", f.pinRef(), "--json"}} {
		out, err := deliveryRun(t, f.root, args...)
		require.NoError(t, err)
		for _, raw := range f.raw {
			assert.NotContains(t, out, raw)
		}

		assert.NotContains(t, out, "before-construction 1")
		assert.NotContains(t, out, "enforced")
	}
}

func TestLessonsDeliveryTUIParity(t *testing.T) {
	for _, moved := range []bool{false, true} {
		t.Run(strconvBool(moved), func(t *testing.T) {
			identityRoot := ""
			if moved {
				identityRoot = t.TempDir()
				verifyWrite(t, identityRoot, ".seamark/lessons.yaml", "pin:\n  - rule: wrong-root-pin\n")
			}

			f := newDeliveryFixture(t, identityRoot)
			f.index(t)
			before := deliveryTree(t, f.root)
			ws, err := workspace.Resolve(f.root, "")
			require.NoError(t, err)
			svc := services.New(ws, version, "/unused/seamark")
			for _, ref := range []string{"", f.pinRef()} {
				args := []string{}
				if ref != "" {
					args = []string{"--pin", ref}
				}

				cli, _ := deliveryReport(t, f.root, args...)
				tui, err := svc.Delivery(lessonreport.Request{PinRef: ref})
				require.NoError(t, err)
				// Only the independently sampled clock value is volatile.
				tui.AsOf.GeneratedAt = cli.AsOf.GeneratedAt
				encoded, err := json.Marshal(tui)
				require.NoError(t, err)
				var normalized lessonreport.Report
				require.NoError(t, json.Unmarshal(encoded, &normalized))
				assert.Equal(t, cli, normalized)
				assert.Equal(t, 1, cli.Pins[0].Delivery.Injected)
				assert.Equal(t, f.cfg.Pin[0].Rule, cli.Pins[0].Rule, "the config comes from the workspace, not from the identity root")
			}

			assert.Equal(t, before, deliveryTree(t, f.root))
		})
	}
}

func strconvBool(value bool) string {
	if value {
		return "moved-root"
	}

	return "ordinary-root"
}

// TestLessonsDeliveryAppliedLinks reads the report beside each state of
// the ledger. The ledger is optional: an absent, corrupt, or failed
// ledger leaves the audit counts as they are, and the report writes
// nothing.
func TestLessonsDeliveryAppliedLinks(t *testing.T) {
	for _, ledger := range []string{"applied", "absent", "corrupt", "query-failed"} {
		t.Run(ledger, func(t *testing.T) {
			f := newDeliveryFixture(t, "")
			if ledger != "absent" {
				f.index(t)
			}

			path := filepath.Join(f.root, ".seamark", "lessons-audit.jsonl")
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			var first reviews.Firing
			require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
			first.Delivery, first.Emission, first.Pins, first.File = reviews.DeliveryEvaluation, "not-needed", nil, "outside.go"
			encoded, err := json.Marshal(first)
			require.NoError(t, err)
			lines[0] = string(encoded)
			require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

			switch ledger {
			case "corrupt":
				require.NoError(t, os.WriteFile(store.DefaultPath(f.root), []byte("not sqlite"), 0o644))
			case "query-failed":
				deliverySQL(t, store.DefaultPath(f.root), "DROP TABLE proposal")
			}

			before := deliveryTree(t, f.root)
			r, _ := deliveryReport(t, f.root)
			assert.Equal(t, lessonreport.CaptureUnavailable, r.Capture.State)
			assert.Equal(t, 1, r.Totals.EvaluationRecords)
			assert.Zero(t, r.Pins[0].Delivery.Injected)
			assert.Empty(t, r.Pins[0].NextSteps)
			assert.Equal(t, before, deliveryTree(t, f.root))
		})
	}
}

func TestLessonsDeliverySourceFailures(t *testing.T) {
	for _, source := range []string{"audit", "check-audit", "config"} {
		t.Run(source, func(t *testing.T) {
			f := newDeliveryFixture(t, "")
			var path string
			switch source {
			case "audit":
				path = filepath.Join(f.root, ".seamark", "lessons-audit.jsonl")
			case "check-audit":
				path = filepath.Join(f.root, ".seamark", "lesson-check-audit.jsonl")
			case "config":
				path = filepath.Join(f.root, ".seamark", "lessons.yaml")
			}

			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Mkdir(path, 0o755))
			before := deliveryTree(t, f.root)
			var text string
			for _, args := range [][]string{nil, {"--json"}} {
				out, err := deliveryRun(t, f.root, args...)
				require.NoError(t, err, "source failures are report data")
				require.NotEmpty(t, out)

				if args == nil {
					text = out
				}
			}

			assertNoEmptySlot(t, text)

			if source != "config" {
				assert.Contains(t, text, "unreadable (.seamark/", "the failed source names its state")
				assert.Regexp(t, `unreadable \(\.seamark/[^)]*\);[^\n]*; error \S`, text, "the failed read names its error")
			}

			r, _ := deliveryReport(t, f.root)
			switch source {
			case "audit":
				assert.Equal(t, "unreadable", r.AsOf.Audit.State)
			case "check-audit":
				assert.Equal(t, "unreadable", r.AsOf.CheckAudit.State)
			case "config":
				assert.Equal(t, "invalid", r.AsOf.Config.State)
			}

			assert.Equal(t, before, deliveryTree(t, f.root))
		})
	}
}

// TestLessonsDeliveryTextHasNoEmptySlot runs the text on the fixture, with
// lessons.yaml and both audits removed, and with the starter lessons.yaml
// that seamark init writes. An empty slot would end a line with a stray
// separator or space.
func TestLessonsDeliveryTextHasNoEmptySlot(t *testing.T) {
	f := newDeliveryFixture(t, "")
	out, err := deliveryRun(t, f.root)
	require.NoError(t, err)
	assertNoEmptySlot(t, out)

	assert.Contains(t, out, "\n  pin ", "the fixture prints its pins")
	assert.Contains(t, out, "rule: keep-docs-current\n    scope: repository-wide\n", "a pin without a region is repository-wide")
	assert.Contains(t, out, "rule: close-db-handles\n    scope: api\n")

	for _, name := range []string{"lessons.yaml", "lessons-audit.jsonl", "lesson-check-audit.jsonl"} {
		require.NoError(t, os.Remove(filepath.Join(f.root, ".seamark", name)))
	}

	out, err = deliveryRun(t, f.root)
	require.NoError(t, err)
	assertNoEmptySlot(t, out)

	lines := strings.Split(out, "\n")
	require.Greater(t, len(lines), 5)
	assert.Equal(t, "delivery audit: absent (.seamark/lessons-audit.jsonl); generations 0; bytes 0; records 0; "+
		"legacy 0; malformed 0; newer schema 0; partial tail false", lines[2])
	assert.Equal(t, "check audit: absent (.seamark/lesson-check-audit.jsonl); generations 0; bytes 0; records 0; "+
		"legacy 0; malformed 0; newer schema 0; partial tail false", lines[3])
	assert.Equal(t, "config: absent; pins 0", lines[4])

	// The starter reads and has no pins. Its config line still names the
	// guidance digest, because every config that reads has one.
	starter := starterLessonsYAML(t)
	verifyWrite(t, f.root, pinfile.File, starter)

	cfg, err := reviews.ParseConfig([]byte(starter))
	require.NoError(t, err)
	require.Empty(t, cfg.Pin, "the starter has no pins")

	out, err = deliveryRun(t, f.root)
	require.NoError(t, err)
	assertNoEmptySlot(t, out)

	lines = strings.Split(out, "\n")
	require.Greater(t, len(lines), 5)
	assert.Equal(t, "config: ok; pins 0; guidance "+cfg.GuidanceDigest(), lines[4])
}

// starterLessonsYAML returns the lessons.yaml that seamark init writes. The
// test reads it from the scaffold, so it follows the template.
func starterLessonsYAML(t *testing.T) string {
	t.Helper()

	for _, doc := range scaffold.Documents(gateModeWarn) {
		if doc.Path == pinfile.File {
			body, err := doc.Compose(nil, false)
			require.NoError(t, err)

			return string(body)
		}
	}

	require.FailNow(t, "the scaffold has no lessons.yaml")

	return ""
}

// assertNoEmptySlot fails when a line of the text ends with a space or a
// separator, the mark of a slot that printed with no value.
func assertNoEmptySlot(t *testing.T, text string) {
	t.Helper()

	require.NotEmpty(t, text)

	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		assert.False(t, strings.HasSuffix(line, " ") || strings.HasSuffix(line, ";"), "line %q ends with a stray separator", line)
	}
}

func TestLessonsDeliveryGlobalFlags(t *testing.T) {
	f := newDeliveryFixture(t, "")
	f.index(t)
	before := deliveryTree(t, f.root)

	for _, args := range [][]string{
		{"-C", f.root, "--db", store.DefaultPath(f.root), "lessons", "--delivery", "--json"},
		{"lessons", "--delivery", "--json", "--db", store.DefaultPath(f.root), "-C", f.root},
	} {
		out, _, err := runIn(t, "", args...)
		require.NoError(t, err)
		assert.Contains(t, out, "\"schema\": \"delivery/1\"")
	}

	// An additional inherited context flag must stay out of lessons-only
	// conflict checking; no hard-coded global allowlist is needed.
	root := New()
	root.PersistentFlags().String("test-context", "", "test context")
	root.SetArgs([]string{"-C", f.root, "lessons", "--delivery", "--json", "--test-context", "x"})
	root.SetOut(io.Discard)
	require.NoError(t, root.Execute())
	assert.Equal(t, before, deliveryTree(t, f.root))
}

func TestLessonsDeliveryWriterErrors(t *testing.T) {
	f := newDeliveryFixture(t, "")
	failure := errors.New("output failed")
	for _, args := range [][]string{nil, {"--json"}} {
		cmd := New()
		cmd.SetOut(deliveryFailWriter{failure})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(append([]string{"-C", f.root, "lessons", "--delivery"}, args...))
		require.ErrorIs(t, cmd.Execute(), failure)
	}
}

type deliveryFailWriter struct{ err error }

func (w deliveryFailWriter) Write([]byte) (int, error) { return 0, w.err }
