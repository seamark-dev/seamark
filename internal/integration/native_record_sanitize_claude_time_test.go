package integration

// Tests of the time shift that scripts/native-record-sanitize.py gives a
// Claude Code recording. They run the sanitizer with python3 on a
// synthetic recording, so they need no model and no client CLI. The
// fixture holds each time form of a Claude Code 2.1.325 recording: the
// ISO timestamps of transcript records and extras, a debug log line in
// a guard-hooks.jsonl note, and the startTime epoch in milliseconds of
// a cost-state record. It also holds two forms that no recording holds
// yet, because the pass reads any text and any member name: an ISO time
// with a local offset in tool output, and an epoch time in seconds. The
// real IDs and times of the fixture are made up.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The synthetic session values of the time fixture, by first use.
const (
	claudeTimeSession1 = "10000000-0000-4000-8000-000000000001"
	claudeTimeSession2 = "10000000-0000-4000-8000-000000000002"
)

// The copies of the time fixture transcripts.
const (
	claudeTimeMain  = "projects/-workspace-native/" + claudeTimeSession1 + ".jsonl"
	claudeTimeLater = "projects/-workspace-native/" + claudeTimeSession2 + ".jsonl"
)

// claudeTimeShown is the name of the main transcript copy in a refusal
// message, which hides each ID part of a file name.
const claudeTimeShown = "projects/-workspace-native/<id>.jsonl"

// claudeTimeNote is the debug log line of the note after its time. The
// instrumentation path of the fixture becomes its placeholder.
const claudeTimeNote = " [DEBUG] Hooks: PostToolUse:Write [sh /opt/instrumentation/post-edit.sh] timed out after 15000ms (15035ms)"

// claudeTimeFixture is a synthetic Claude Code recording before it is
// written. A test can change its lines before write.
type claudeTimeFixture struct {
	root, home, repo  string
	start             time.Time
	hooks             []codexRecordHook
	transcript, later []string
	extras            map[string][]string
}

// claudeISO writes a time with milliseconds in UTC, as Claude Code does.
func claudeISO(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// claudeSeconds writes a time in whole seconds in UTC, as the lesson
// check audit does.
func claudeSeconds(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z")
}

// claudeTimeRaw returns the recorded time points of the fixture: the
// startTime, the three records of the main session, the time in the tool
// output, the record of the later session, the note time, the audit
// time, and the epoch seconds of the Stop payload. The startTime comes
// 750 ms before the first record, so it is the earliest time, as in the
// recordings of Claude Code 2.1.325.
func claudeTimeRaw(start time.Time) []time.Time {
	return []time.Time{
		start.Add(-750 * time.Millisecond),
		start,
		start.Add(1500 * time.Millisecond),
		start.Add(3200 * time.Millisecond),
		start.Add(3 * time.Second).Truncate(time.Second),
		start.Add(4*time.Hour + 30*time.Minute),
		start.Add(20173 * time.Millisecond),
		start.Add(5 * time.Second).Truncate(time.Second),
		start.Add(time.Minute).Truncate(time.Second),
	}
}

// claudeTimeShift returns the offset that the sanitizer gives a fixture.
// It moves the earliest time, floored to the second, to 21:00:00 UTC on
// its own UTC date.
func claudeTimeShift(start time.Time) time.Duration {
	first := claudeTimeRaw(start)[0].UTC().Truncate(time.Second)

	return time.Date(first.Year(), first.Month(), first.Day(), 21, 0, 0, 0, time.UTC).Sub(first)
}

// newClaudeTimeFixture returns the synthetic recording with its times
// relative to start. The tool output writes its time with a local offset
// of one hour.
func newClaudeTimeFixture(t *testing.T, start time.Time) *claudeTimeFixture {
	t.Helper()

	root := t.TempDir()
	f := &claudeTimeFixture{root: root, home: filepath.Join(root, "operator-q7"), repo: sanitizeRepo, start: start}

	raw := claudeTimeRaw(start)
	ids := sanitizeReplacer(root, f.repo)
	times := strings.NewReplacer(
		"{START_MS}", fmt.Sprint(raw[0].UnixMilli()), "{TIME0}", claudeISO(raw[1]), "{TIME1}", claudeISO(raw[2]),
		"{TIME2}", claudeISO(raw[3]), "{LOCAL}", raw[4].In(time.FixedZone("", 3600)).Format(time.RFC3339),
		"{LATER}", claudeISO(raw[5]), "{NOTE}", claudeISO(raw[6]), "{AUDIT}", claudeSeconds(raw[7]),
		"{RESET}", fmt.Sprint(raw[8].Unix()),
	)

	fill := func(lines ...string) []string {
		out := make([]string, 0, len(lines))
		for _, line := range lines {
			out = append(out, times.Replace(ids.Replace(line)))
		}

		return out
	}

	path := `"transcript_path":"{HOME}/.claude/projects/{SLUG}/{S1}.jsonl","cwd":"{REPO}"`
	f.hooks = []codexRecordHook{
		{"UserPromptSubmit", `{"session_id":"{S1}","prompt_id":"{P1}",` + path + `,"hook_event_name":"UserPromptSubmit","prompt":"print the date"}`},
		{"PreToolUse", `{"session_id":"{S1}","prompt_id":"{P1}",` + path + `,"hook_event_name":"PreToolUse","tool_name":"Bash",` +
			`"tool_input":{"command":"date -Iseconds"},"tool_use_id":"{T3}"}`},
		{"PostToolUse", `{"session_id":"{S1}","prompt_id":"{P1}",` + path + `,"hook_event_name":"PostToolUse","tool_name":"Bash",` +
			`"tool_use_id":"{T3}","tool_response":{"stdout":"{LOCAL}\n","stderr":"","interrupted":false}}`},
		{"Stop", `{"session_id":"{S1}",` + path + `,"hook_event_name":"Stop","rate_limit_resets_at":{RESET}}`},
	}

	for i := range f.hooks {
		f.hooks[i].payload = fill(f.hooks[i].payload)[0]
	}

	f.transcript = fill(
		`{"type":"cost-state","sessionId":"{S1}","totalDuration":7842,"startTime":{START_MS}}`,
		`{"parentUuid":null,"uuid":"{U1}","sessionId":"{S1}","promptId":"{P1}","type":"user","timestamp":"{TIME0}",`+
			`"message":{"role":"user","content":"print the date"}}`,
		`{"parentUuid":"{U1}","uuid":"{U2}","sessionId":"{S1}","requestId":"{R1}","type":"assistant","timestamp":"{TIME1}",`+
			`"message":{"id":"{M1}","role":"assistant","content":[{"type":"tool_use","id":"{T3}","name":"Bash",`+
			`"input":{"command":"date -Iseconds"}}]}}`,
		`{"parentUuid":"{U2}","uuid":"{U3}","sessionId":"{S1}","type":"user","timestamp":"{TIME2}",`+
			`"toolUseResult":{"stdout":"{LOCAL}\n","stderr":""},`+
			`"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"{T3}","content":"{LOCAL}"}]}}`,
	)

	f.later = fill(`{"uuid":"{U4}","sessionId":"{S2}","type":"user","timestamp":"{LATER}",` +
		`"message":{"role":"user","content":"another session"}}`)

	f.extras = map[string][]string{
		"guard-hooks.jsonl": fill(
			`{"run":1,"hook":"post-edit","event":"PostToolUse","tool":"Write","status":0,"ms":12211}`,
			`{"run":1,"hook":"post-edit","note":"{NOTE} [DEBUG] Hooks: PostToolUse:Write [sh {REPO}/.native-instrumentation/post-edit.sh] `+
				`timed out after 15000ms (15035ms)"}`,
		),
		"lesson-check-audit.jsonl": fill(`{"schema":"lesson-check-audit/1","ts":"{AUDIT}","kind":"baseline","caller":"hook",` +
			`"client":"claude","session_sha256":"` + strings.Repeat("ab", 32) + `"}`),
	}

	return f
}

// write writes the recording and its record.json. The transcripts come
// in the order of their real paths, as the record step sorts them.
func (f *claudeTimeFixture) write(t *testing.T) sanitizeFixture {
	t.Helper()

	observed := filepath.Join(f.root, "observed")

	for i, h := range f.hooks {
		name := fmt.Sprintf("p%d", i+1)
		writeLines(t, filepath.Join(observed, name+".json"), h.payload)
		writeLines(t, filepath.Join(observed, name+".meta.json"),
			fmt.Sprintf(`{"event":%q,"start_ns":%d,"process_cwd":%q}`, h.event, 1000+i, f.repo))
	}

	project := filepath.Join(f.home, ".claude", "projects", claudeProjectSlug(f.repo))
	main := filepath.Join(project, sanitizeSession+".jsonl")
	later := filepath.Join(project, sanitizeSession2+".jsonl")
	writeLines(t, main, f.transcript...)
	writeLines(t, later, f.later...)

	transcripts := []string{main, later}
	slices.Sort(transcripts)

	extras := map[string]string{}
	for name, lines := range f.extras {
		extras[name] = filepath.Join(f.root, "extras", name)
		writeLines(t, extras[name], lines...)
	}

	return writeSanitizeRecord(t, f.root, map[string]any{
		"client": ClaudeID, "client_version": "2.1.325", "repo": f.repo, "home": f.home,
		"seamark": filepath.Join(f.root, "bin", "seamark"), "instrumentation": f.repo + "/.native-instrumentation",
		"observed": observed, "runs": []int{1000}, "transcripts": transcripts, "extras": extras,
	})
}

// claudeTimeModelText returns a change of the fixture that adds a model
// reply with the text to the main session, 9 seconds after the start. A
// test puts a value there that no member name marks.
func claudeTimeModelText(t *testing.T, text string) func(f *claudeTimeFixture) {
	t.Helper()

	return func(f *claudeTimeFixture) {
		line, err := json.Marshal(map[string]any{
			"parentUuid": sanitizeRecord(3), "uuid": sanitizeRecord(5), "sessionId": sanitizeSession, "type": "assistant",
			"timestamp": claudeISO(f.start.Add(9 * time.Second)),
			"message": map[string]any{"id": sanitizeMessage2, "role": "assistant",
				"content": []map[string]string{{"type": "text", "text": text}}},
		})
		require.NoError(t, err)

		f.transcript = append(f.transcript, string(line))
	}
}

// claudeTimeCopy reads each time point of a copy in the order of
// claudeTimeRaw. Numbers keep their text, so an epoch value is exact.
func claudeTimeCopy(t *testing.T, out string) []time.Time {
	t.Helper()

	parse := func(text string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, text)
		require.NoError(t, err, text)

		return v
	}

	epoch := func(v any) int64 {
		n, ok := v.(json.Number)
		require.True(t, ok, "%v is a number", v)

		i, err := n.Int64()
		require.NoError(t, err)

		return i
	}

	main := codexLines(t, filepath.Join(out, claudeTimeMain))
	result, _ := main[3]["toolUseResult"].(map[string]any)
	note := nativeString(codexLines(t, filepath.Join(out, "guard-hooks.jsonl"))[1], "note")
	audit := codexLines(t, filepath.Join(out, "lesson-check-audit.jsonl"))[0]

	data, err := os.ReadFile(filepath.Join(out, "hooks", "004-Stop.json"))
	require.NoError(t, err)

	d := json.NewDecoder(strings.NewReader(string(data)))
	d.UseNumber()

	var stop map[string]any
	require.NoError(t, d.Decode(&stop))

	return []time.Time{
		time.UnixMilli(epoch(main[0]["startTime"])),
		parse(nativeString(main[1], "timestamp")),
		parse(nativeString(main[2], "timestamp")),
		parse(nativeString(main[3], "timestamp")),
		parse(strings.TrimSpace(nativeString(result, "stdout"))),
		parse(nativeString(codexLines(t, filepath.Join(out, claudeTimeLater))[0], "timestamp")),
		parse(strings.TrimSuffix(note, claudeTimeNote)),
		parse(nativeString(audit, "ts")),
		time.Unix(epoch(stop["rate_limit_resets_at"]), 0),
	}
}

// claudeTimeISO finds every ISO time with a zone in a copy file.
var claudeTimeISO = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)

// TestNativeRecordSanitizeShiftsClaudeTimes proves the time shift on the
// synthetic Claude Code recording: every time form moves by one offset,
// the first time lands at 21:00:00 UTC on its own date, every duration
// stays, no local offset remains, the ID joins hold, and a second run
// gives the same bytes. A recording that starts before midnight UTC and
// ends after it moves back to the date of its first time.
func TestNativeRecordSanitizeShiftsClaudeTimes(t *testing.T) {
	requireTools(t, "python3")

	start := time.Date(2026, 11, 7, 10, 0, 0, 250e6, time.UTC)
	f := newClaudeTimeFixture(t, start).write(t)

	got, err := f.sanitize(t, f.out)
	require.NoError(t, err, got)

	files := outputFiles(t, f.out)
	shift := claudeTimeShift(start)
	raw := claudeTimeRaw(start)
	copied := claudeTimeCopy(t, f.out)

	t.Run("every time form moves by one offset", func(t *testing.T) {
		require.Equal(t, 11*time.Hour+1*time.Second, shift, "the startTime 09:59:59.500 floors to 09:59:59")

		for i := range raw {
			assert.Equal(t, raw[i].Add(shift).UTC(), copied[i].UTC(), "time point %d", i)
		}

		main := codexLines(t, filepath.Join(f.out, claudeTimeMain))
		assert.Equal(t, json.Number(fmt.Sprint(time.Date(2026, 11, 7, 21, 0, 0, 500e6, time.UTC).UnixMilli())),
			main[0]["startTime"], "the earliest time lands at 21:00:00 UTC on its own date and keeps its fraction")
		assert.Equal(t, "2026-11-07T21:00:01.250Z", main[1]["timestamp"])
		assert.Equal(t, "2026-11-07T21:00:04Z", strings.TrimSpace(nativeString(main[3]["toolUseResult"].(map[string]any), "stdout")),
			"the tool output time with a local offset is written in UTC")

		later := codexLines(t, filepath.Join(f.out, claudeTimeLater))
		assert.Equal(t, "2026-11-08T01:30:01.250Z", later[0]["timestamp"], "the later session lands after midnight UTC")

		hooks := codexLines(t, filepath.Join(f.out, "guard-hooks.jsonl"))
		assert.Equal(t, "2026-11-07T21:00:21.423Z"+claudeTimeNote, hooks[1]["note"], "the note keeps its debug log text")

		post := readJSON(t, filepath.Join(f.out, "hooks", "003-PostToolUse.json"))
		response, _ := post["tool_response"].(map[string]any)
		assert.Equal(t, "2026-11-07T21:00:04Z\n", response["stdout"], "the payload holds the same time as the transcript")
	})

	t.Run("every duration stays exact", func(t *testing.T) {
		pairs := 0

		for i := range raw {
			for j := i + 1; j < len(raw); j++ {
				assert.Equal(t, raw[j].Sub(raw[i]), copied[j].Sub(copied[i]), "time points %d and %d", i, j)
				pairs++
			}
		}

		assert.GreaterOrEqual(t, pairs, 10)

		main := codexLines(t, filepath.Join(f.out, claudeTimeMain))
		assert.Equal(t, json.Number("7842"), main[0]["totalDuration"], "a duration member stays")

		hooks := codexLines(t, filepath.Join(f.out, "guard-hooks.jsonl"))
		assert.Equal(t, json.Number("12211"), hooks[0]["ms"])
	})

	t.Run("every time lies in the night window in UTC", func(t *testing.T) {
		count := 0

		for name, data := range files {
			assert.False(t, codexNumericZone.MatchString(data+name), "%s holds a time with a numeric zone", name)

			for _, text := range claudeTimeISO.FindAllString(data, -1) {
				v, err := time.Parse(time.RFC3339Nano, text)
				require.NoError(t, err)

				hour := v.UTC().Hour()
				assert.True(t, hour >= 21 || hour < 8, "%s holds the time %s outside 21:00 to 08:00 UTC", name, text)
				count++
			}
		}

		// Five in the main transcript (three records and the tool output
		// twice), one in the later session, one in the payload, the note,
		// and the audit time.
		assert.Equal(t, 9, count)
	})

	t.Run("the manifest says only that the times are shifted", func(t *testing.T) {
		manifest := readJSON(t, filepath.Join(f.out, "manifest.json"))
		assert.Equal(t, true, manifest["times_shifted"])
		assert.NotContains(t, files["manifest.json"], fmt.Sprint(int(shift.Seconds())), "the offset is written nowhere")
	})

	t.Run("no real ID remains and the joins hold", func(t *testing.T) {
		reals := []string{sanitizeSession, sanitizeSession2, sanitizePrompt, sanitizeToolBash, sanitizeMessage, sanitizeRequest}
		for i := 1; i <= 4; i++ {
			reals = append(reals, sanitizeRecord(i))
		}

		for name, data := range files {
			for _, real := range reals {
				assert.NotContains(t, data+name, real, name)
			}
		}

		r := timingRecording{dir: f.out}
		require.NoError(t, json.Unmarshal([]byte(files["manifest.json"]), &r.manifest))

		pre := readJSON(t, filepath.Join(f.out, "hooks", "002-PreToolUse.json"))
		calls := claudeTimingCalls(t, r.transcript(t, nativeString(pre, "transcript_path")))
		assert.Equal(t, "msg_01NativeMessage000000001", calls[nativeString(pre, "tool_use_id")],
			"the payload tool_use_id names a tool_use block of the transcript that transcript_path names")
	})

	t.Run("a second run gives the same bytes", func(t *testing.T) {
		first, err := os.ReadFile(f.idMap)
		require.NoError(t, err)

		again := filepath.Join(f.root, "again")
		got, err := f.sanitize(t, again)
		require.NoError(t, err, got)

		assert.Equal(t, files, outputFiles(t, again))

		second, err := os.ReadFile(f.idMap)
		require.NoError(t, err)
		assert.Equal(t, string(first), string(second))
	})

	t.Run("a span across midnight UTC moves back to the date of its first time", func(t *testing.T) {
		late := time.Date(2026, 11, 7, 23, 59, 58, 250e6, time.UTC)
		f := newClaudeTimeFixture(t, late).write(t)

		got, err := f.sanitize(t, f.out)
		require.NoError(t, err, got)

		shift := claudeTimeShift(late)
		require.Equal(t, -(2*time.Hour + 59*time.Minute + 57*time.Second), shift, "the startTime 23:59:57.500 floors to 23:59:57")

		raw := claudeTimeRaw(late)
		require.Equal(t, 8, raw[3].Day(), "the third record lies after midnight UTC")

		copied := claudeTimeCopy(t, f.out)
		for i := range raw {
			assert.Equal(t, raw[i].Add(shift).UTC(), copied[i].UTC(), "time point %d", i)
		}

		main := codexLines(t, filepath.Join(f.out, claudeTimeMain))
		assert.Equal(t, "2026-11-07T21:00:01.250Z", main[1]["timestamp"])
		assert.Equal(t, "2026-11-07T21:00:04.450Z", main[3]["timestamp"], "the record after midnight moves to the date of the first time")
	})
}

// TestNativeRecordSanitizeEndsTheClaudeWindowAt0800UTC proves the end of
// the night window for a Claude Code recording. The first whole second
// of the fixture lands at 21:00:00 UTC, so a time 11 hours after it lands
// at 08:00:00 UTC of the next day. The sanitizer keeps a time one
// millisecond before that edge. It refuses a time at the edge, because no
// single offset then keeps every time in the window.
func TestNativeRecordSanitizeEndsTheClaudeWindowAt0800UTC(t *testing.T) {
	requireTools(t, "python3")

	start := time.Date(2026, 11, 7, 10, 0, 0, 250e6, time.UTC)
	edge := claudeTimeRaw(start)[0].Truncate(time.Second).Add(11 * time.Hour)

	t.Run("a time one millisecond before 08:00 UTC stays in the window", func(t *testing.T) {
		fixture := newClaudeTimeFixture(t, start)
		claudeTimeModelText(t, "late "+claudeISO(edge.Add(-time.Millisecond)))(fixture)
		f := fixture.write(t)

		got, err := f.sanitize(t, f.out)
		require.NoError(t, err, got)
		assert.Contains(t, outputFiles(t, f.out)[claudeTimeMain], "late 2026-11-08T07:59:59.999Z")
	})

	t.Run("a time at 08:00 UTC is refused", func(t *testing.T) {
		fixture := newClaudeTimeFixture(t, start)
		claudeTimeModelText(t, "late "+claudeISO(edge))(fixture)
		f := fixture.write(t)

		got, err := f.sanitize(t, f.out)
		require.Error(t, err, got)
		assert.Contains(t, got, "the recorded times span more than the window from 21:00 to 08:00 UTC")
		assert.NoDirExists(t, f.out, "the sanitizer writes nothing")
	})

	// The sanitizer masks the generated context of an attachment record,
	// so a time inside it never reaches the copy and must not widen the
	// span.
	t.Run("a time inside a masked attachment does not count", func(t *testing.T) {
		fixture := newClaudeTimeFixture(t, start)
		line, err := json.Marshal(map[string]any{
			"parentUuid": sanitizeRecord(3), "uuid": sanitizeRecord(5), "sessionId": sanitizeSession, "type": "attachment",
			"timestamp":  claudeISO(start.Add(9 * time.Second)),
			"attachment": map[string]any{"type": "date", "content": "now " + claudeISO(edge)},
		})
		require.NoError(t, err)

		fixture.transcript = append(fixture.transcript, string(line))
		f := fixture.write(t)

		got, err := f.sanitize(t, f.out)
		require.NoError(t, err, got)
		assert.NotContains(t, outputFiles(t, f.out)[claudeTimeMain], "now ")
	})
}

// TestNativeRecordSanitizeRefusesAnUnshiftedClaudeTime proves that the
// Claude Code time residual check refuses a copy that keeps a time the
// pass cannot move. Each refusal names the file and the kind, never the
// value and never a real ID.
func TestNativeRecordSanitizeRefusesAnUnshiftedClaudeTime(t *testing.T) {
	requireTools(t, "python3")

	start := time.Date(2026, 11, 7, 10, 0, 0, 250e6, time.UTC)
	digits := fmt.Sprint(start.Unix() + 5)
	far := fmt.Sprint(start.Add(72 * time.Hour).UnixMilli())
	named := fmt.Sprint(start.Add(2 * time.Second).UnixMilli())

	model := func(text string) func(f *claudeTimeFixture) {
		return claudeTimeModelText(t, text)
	}

	for _, tc := range []struct {
		name, want, value string
		change            func(f *claudeTimeFixture)
	}{
		{"a time without a zone", claudeTimeShown + ": holds a timestamp without a time zone", "2026-11-07T10:00:20",
			model("at 2026-11-07T10:00:20")},
		{"epoch digits at the end of a sentence", claudeTimeShown + ": holds epoch digits of a recorded time", digits,
			model("started at " + digits + ".")},
		{"an epoch number far from the recorded times", claudeTimeShown + ": holds an unshifted epoch time", far,
			func(f *claudeTimeFixture) {
				f.transcript = append(f.transcript, fmt.Sprintf(`{"type":"cost-state","sessionId":%q,"totalDuration":1,"startTime":%s}`,
					sanitizeSession, far))
			}},
		{"a file name time in a text", claudeTimeShown + ": holds an unshifted file name time", "2026-11-07T10-00-07",
			model("see snapshot-2026-11-07T10-00-07.log")},
		{"epoch digits in an output file name", "snapshot-<time>.jsonl: holds epoch digits of a recorded time", named,
			func(f *claudeTimeFixture) {
				f.extras["snapshot-"+named+".jsonl"] = []string{`{"note":"no time here"}`}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newClaudeTimeFixture(t, start)
			tc.change(fixture)
			f := fixture.write(t)

			got, err := f.sanitize(t, f.out)
			require.Error(t, err, got)
			assert.Contains(t, got, tc.want)
			assert.NotContains(t, got, tc.value, "the message holds no value")
			assert.NotContains(t, got, sanitizeSession, "the message holds no real ID")
		})
	}
}
