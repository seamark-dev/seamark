package integration

// The native differential of the Codex Update projection. Seeded random
// Update patches run through the installed applier and through the
// guard projection. The content oracle pins one Codex version; this
// check runs on whatever version is installed, so it finds a change of
// the applier rules that no recorded case covers.
//
// The applier is the codex binary under the name apply_patch, in a
// scratch directory with a scratch CODEX_HOME: no model, no login.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// The sizes and seeds of the differential. The seeds stay fixed, so a
// failure repeats on the same binary. Each case starts two processes,
// the applier and git, so the cases run in parallel shards: about 500
// cases then take about ten seconds.
const (
	differentialRandomCases  = 400
	differentialGrammarCases = 100
	differentialRandomSeed   = 1
	differentialGrammarSeed  = 11
	differentialShards       = 8
)

// differentialMinExact is the smallest share of random cases that must
// project exactly. A projection that gives unknown for every patch
// passes the two zero counts, so the floor shows that the check
// compares content. On codex-cli 0.169.0 about two thirds project.
const differentialMinExact = 0.4

// differentialCase is one generated Update of f.txt. grammar marks a
// case of the grammar set.
type differentialCase struct {
	name    string
	before  string
	patch   string
	grammar bool
}

// differentialStats counts the outcomes of the differential.
type differentialStats struct {
	exact           int // the projection equals the applier result
	unknownAccepted int // the projection is unknown, the applier wrote the file
	unknownRefused  int // the projection is unknown, the applier refused the patch
	mismatch        int // the projection names other bytes than the applier wrote
	claimRefused    int // the projection gives content, the applier refused the patch
}

// add adds the counts of other.
func (s *differentialStats) add(other differentialStats) {
	s.exact += other.exact
	s.unknownAccepted += other.unknownAccepted
	s.unknownRefused += other.unknownRefused
	s.mismatch += other.mismatch
	s.claimRefused += other.claimRefused
}

// TestNativeCodexUpdateProjectionMatchesTheApplier runs every generated
// case through the installed applier and through the projection. It
// requires zero mismatches and zero projections for a refused patch.
// An unknown projection of an accepted patch is a needless deny, not a
// wrong allow, so the check only counts it.
func TestNativeCodexUpdateProjectionMatchesTheApplier(t *testing.T) {
	codex := nativeCodex(t)
	scratchCodexHome(t)
	requireTools(t, "git")

	bin := applierBinary(t, codex)

	cases := randomUpdateCases(rand.New(rand.NewPCG(differentialRandomSeed, 0)), differentialRandomCases)
	cases = append(cases, grammarUpdateCases(rand.New(rand.NewPCG(differentialGrammarSeed, 0)), differentialGrammarCases)...)

	var (
		mu                        sync.Mutex
		randomStats, grammarStats differentialStats
	)

	// The group returns when every parallel shard is done. Each shard
	// has its own repository, because a case writes f.txt there.
	t.Run("shards", func(t *testing.T) {
		for shard := range differentialShards {
			t.Run(fmt.Sprintf("shard-%d", shard), func(t *testing.T) {
				t.Parallel()

				root := t.TempDir()

				out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput()
				require.NoError(t, err, string(out))

				var random, grammar differentialStats

				for i := shard; i < len(cases); i += differentialShards {
					c := cases[i]
					stats := &random

					if c.grammar {
						stats = &grammar
					}

					got := runApplier(t, bin, map[string]string{"f.txt": c.before}, c.patch)
					compareDifferential(t, root, c, got, stats)
				}

				mu.Lock()
				defer mu.Unlock()

				randomStats.add(random)
				grammarStats.add(grammar)
			})
		}
	})

	t.Logf("installed %s; random %+v; grammar %+v", codexVersion(t, codex), randomStats, grammarStats)

	for _, s := range []differentialStats{randomStats, grammarStats} {
		require.Zero(t, s.mismatch, "the projection names other bytes than the installed applier writes")
		require.Zero(t, s.claimRefused, "the projection gives content for a patch that the installed applier refuses")
	}

	require.GreaterOrEqual(t, float64(randomStats.exact), differentialMinExact*float64(differentialRandomCases),
		"too few random cases project exactly; the check would not compare content")
}

// compareDifferential projects one case in root and counts the outcome
// against what the applier did. It reports the details of each mismatch
// and of each projection for a refused patch.
func compareDifferential(t *testing.T, root string, c differentialCase, got contentCase, stats *differentialStats) {
	t.Helper()

	accepted := got.ExitStatus == 0

	ch, ok := projectDifferential(t, root, c)

	switch {
	case !ok && accepted:
		stats.unknownAccepted++
	case !ok:
		stats.unknownRefused++
	case !accepted:
		stats.claimRefused++
		t.Errorf("%s: content for a refused patch\nbefore: %q\npatch: %q\nstderr: %q", c.name, c.before, c.patch, got.Stderr)
	case afterState(got.After, "f.txt") != projectedState(ch):
		stats.mismatch++
		t.Errorf("%s: mismatch\nbefore: %q\npatch: %q\napplier: %q", c.name, c.before, c.patch, got.After["f.txt"])
	default:
		stats.exact++
	}
}

// projectDifferential writes the before content of one case to f.txt in
// root and projects the patch. ok is false when the decoder refuses the
// patch or the change has no projection: the guard then denies.
func projectDifferential(t *testing.T, root string, c differentialCase) (lessoncheck.Change, bool) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(root, "f.txt"), []byte(c.before), 0o644))

	ops, err := applyPatchOps(c.patch)
	if err != nil {
		return lessoncheck.Change{}, false
	}

	op := lessoncheck.Operation{Client: CodexID, Tool: codexApplyPatch, Ops: ops, PathsComplete: true}

	snap, err := lessoncheck.ProjectOperation(context.Background(), root, op, lessoncheck.Limits{})
	require.NoError(t, err)
	require.Len(t, snap.Changes, 1, "%s: a generated patch updates f.txt only", c.name)

	ch := snap.Changes[0]

	return ch, projected(ch)
}

// differentialWords are the line texts of the random cases: plain
// words, empty lines, lines with leading or trailing whitespace, and
// lines with characters that the normalization pass maps.
var differentialWords = []string{
	"a", "b", "c", "x", "y", "", "func f", "  x", "x  ", "\tx", "a \u2013 b", "a - b",
	"\u201cq\u201d", `"q"`, "a\u00a0b", "a b", "}", "{", "return",
}

// randomUpdateCases returns n seeded Update cases. Each before file has
// up to eight lines with LF, CRLF, or mixed line ends, sometimes without
// a final newline. Each patch has one to three chunks with context,
// deleted, and added lines that often differ from the file in
// whitespace or punctuation, an optional anchor, an optional end-of-file
// marker, and sometimes CRLF line ends.
func randomUpdateCases(rng *rand.Rand, n int) []differentialCase {
	pick := func(list []string) string { return list[rng.IntN(len(list))] }

	// variant returns a line as the patch writes it: often the same text,
	// else with an added space, trimmed, with ASCII punctuation, or
	// another word.
	variant := func(s string) string {
		switch r := rng.Float64(); {
		case r < 0.6:
			return s
		case r < 0.7:
			return s + " "
		case r < 0.8:
			return strings.TrimSpace(s)
		case r < 0.9:
			return strings.NewReplacer("\u2013", "-", "\u201c", `"`, "\u201d", `"`, "\u00a0", " ").Replace(s)
		default:
			return pick(differentialWords)
		}
	}

	cases := make([]differentialCase, 0, n)

	for c := range n {
		lines := make([]string, rng.IntN(9))
		for i := range lines {
			lines[i] = pick(differentialWords)
		}

		var file strings.Builder

		mode := rng.Float64()

		for i, line := range lines {
			end := "\n"

			switch {
			case mode < 0.6:
			case mode < 0.8:
				end = "\r\n"
			default:
				end = pick([]string{"\n", "\r\n"})
			}

			if i == len(lines)-1 && rng.Float64() < 0.25 {
				end = ""
			}

			file.WriteString(line + end)
		}

		var body []string

		pos := 0

		for h := range []int{1, 1, 1, 2, 2, 3}[rng.IntN(6)] {
			var (
				anchor string
				hunk   []string
				eof    bool
			)

			if len(lines) == 0 || rng.Float64() < 0.2 {
				for range 1 + rng.IntN(2) {
					hunk = append(hunk, "+"+pick(differentialWords))
				}
			} else {
				i := len(lines) - 1
				if pos < len(lines) {
					i = pos + rng.IntN(len(lines)-pos)
				}

				j := min(len(lines), i+1+rng.IntN(3))

				for _, line := range lines[i:j] {
					switch r := rng.Float64(); {
					case r < 0.4:
						hunk = append(hunk, " "+variant(line))
					case r < 0.7:
						hunk = append(hunk, "-"+variant(line))
					default:
						hunk = append(hunk, "-"+variant(line), "+"+pick(differentialWords)+"!")
					}
				}

				if rng.Float64() < 0.3 {
					hunk = append(hunk, "+"+pick(differentialWords))
				}

				if rng.Float64() < 0.1 {
					hunk = append(hunk, " ")
				}

				if i > 0 && rng.Float64() < 0.25 {
					anchor = variant(lines[rng.IntN(i)])
				}

				eof = (j == len(lines) && rng.Float64() < 0.4) || rng.Float64() < 0.05
				pos = j
			}

			switch {
			case h == 0 && anchor == "" && rng.Float64() < 0.3:
				// The first chunk may omit its @@ line.
			case anchor == "":
				body = append(body, "@@")
			default:
				body = append(body, "@@ "+anchor)
			}

			for _, line := range hunk {
				if line == " " && rng.Float64() < 0.5 {
					line = "" // an empty raw line is empty context
				}

				body = append(body, line)
			}

			if eof {
				body = append(body, "*** End of File")
			}
		}

		patch := "*** Begin Patch\n*** Update File: f.txt\n" + strings.Join(body, "\n") + "\n*** End Patch"
		if rng.Float64() < 0.1 {
			patch = strings.ReplaceAll(patch, "\n", "\r\n")
		}

		cases = append(cases, differentialCase{name: fmt.Sprintf("random-%04d", c), before: file.String(), patch: patch})
	}

	return cases
}

// grammarTokens are the section lines of the grammar cases: @@ lines
// with and without an anchor or space, end-of-file lines with spaces,
// change lines, empty and blank lines, and header-like text.
var grammarTokens = []string{
	"@@", "@@ ", "@@  ", "@@ a", "@@ x", "@@x", "@@@", " @@", "*** End of File", "*** End of File ", " *** End of File",
	" a", " b", " x", "-a", "-b", "-x", "+N", "+", "-", " ", "", "   ", "\ta", "***x", " ***x", "+*** End of File", "-@@", " @@ a",
}

// grammarUpdateCases returns n seeded Update cases whose sections are
// one to seven random grammar tokens, over files of up to five lines.
func grammarUpdateCases(rng *rand.Rand, n int) []differentialCase {
	cases := make([]differentialCase, 0, n)

	for c := range n {
		var file strings.Builder

		for range rng.IntN(6) {
			file.WriteString([]string{"a", "b", "x", "@@ a", ""}[rng.IntN(5)] + "\n")
		}

		body := make([]string, 1+rng.IntN(7))
		for i := range body {
			body[i] = grammarTokens[rng.IntN(len(grammarTokens))]
		}

		patch := "*** Begin Patch\n*** Update File: f.txt\n" + strings.Join(body, "\n") + "\n*** End Patch"
		cases = append(cases, differentialCase{name: fmt.Sprintf("grammar-%04d", c), before: file.String(), patch: patch, grammar: true})
	}

	return cases
}
