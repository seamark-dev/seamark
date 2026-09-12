package history

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Function grain reads git's hunk-header function context to report which
// functions moved together (RFC-001 §2.5). It is bounded on every axis
// that could hurt the interactive/MCP `why` path: a commit-count window,
// a wall-clock timeout, and a streaming reader so a giant generated file's
// diff history is never buffered whole.
const (
	grainMaxCommits = 5000
	grainTimeout    = 5 * time.Second
	grainLineCap    = 8 << 20 // a single diff line over this is skipped
)

// gitScan runs git and calls onLine for each output line, streaming — so
// memory stays O(one line) regardless of history size. Errors (not a repo,
// timeout, git missing) yield no lines, so callers degrade to no output.
// The scan stops at the caller's deadline or after grainTimeout, whichever
// comes first, so a batch of scans can share one budget.
func gitScan(ctx context.Context, dir string, stdin io.Reader, onLine func(string), args ...string) {
	ctx, cancel := context.WithTimeout(ctx, grainTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdin = stdin

	out, err := cmd.StdoutPipe()
	if err != nil {
		return
	}

	if err := cmd.Start(); err != nil {
		return
	}

	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), grainLineCap)

	for sc.Scan() {
		onLine(sc.Text())
	}

	// Drain any unread tail so git can exit instead of blocking on a full
	// pipe (e.g. after an over-long line stopped the scanner).
	_, _ = io.Copy(io.Discard, out)
	_ = cmd.Wait()
}

// FileCommits returns the set of commit hashes that touched a file, capped
// to the mining window. It is the left side of a co-change intersection:
// the commits shared with a partner are those in BOTH files' sets. Empty
// (nil) when git is unavailable, so callers degrade to no enrichment.
func FileCommits(ctx context.Context, repoRoot, file string) map[string]bool {
	set := map[string]bool{}

	gitScan(ctx, repoRoot, nil, func(line string) {
		if h := strings.TrimSpace(line); h != "" {
			set[h] = true
		}
	}, "-c", "core.quotePath=off", "log", "--no-renames",
		"-n", strconv.Itoa(grainMaxCommits), "--format=%H", "--", file)

	if len(set) == 0 {
		return nil
	}

	return set
}

// hunkFunc matches a unified-diff hunk header and captures git's function
// context — the text after the second "@@", which git fills with the
// enclosing function/class/type it detected in that revision.
var hunkFunc = regexp.MustCompile(`^@@ .* @@\s*(.+)$`)

// PartnerFunctions reports which functions of partner were most often
// touched in the commits it shares with another file (shared). It asks git
// for the diff of those commits only, with `--no-walk --stdin`, so the cost
// grows with the shared set and not with the partner's whole history: a
// lockfile or a generated client with thousands of commits stays cheap.
// The hashes go over stdin because a large set would overflow argv. This
// is a factual report of what moved together — never a statistical claim.
// NOTE: shared is the co-change window's commit set; a bulk commit dropped
// from the lift metric (too many files) that still touched both files can
// contribute here — a known, minor over-count.
func PartnerFunctions(ctx context.Context, repoRoot, partner string, shared map[string]bool, limit int) []string {
	if len(shared) == 0 {
		return nil
	}

	counts := map[string]int{}
	order := map[string]int{} // first-seen index, for stable tie-breaks
	next := 0

	hashes := make([]string, 0, len(shared))
	for h := range shared {
		hashes = append(hashes, h)
	}

	sort.Strings(hashes)

	// %x00%H frames each commit with a NUL + hash that patch text can never
	// forge (every diff body line starts with +/-/space/\); -U0 keeps only
	// hunk headers and changed lines. The pathspec keeps only the commits
	// that touched the partner, so every hunk seen belongs to a shared one.
	gitScan(ctx, repoRoot, strings.NewReader(strings.Join(hashes, "\n")+"\n"), func(line string) {
		if strings.HasPrefix(line, "\x00") {
			return
		}

		m := hunkFunc.FindStringSubmatch(line)
		if m == nil {
			return
		}

		if name := funcName(m[1]); name != "" {
			if _, seen := counts[name]; !seen {
				order[name] = next
				next++
			}

			counts[name]++
		}
	}, "-c", "core.quotePath=off", "log", "--no-renames", "--no-walk", "--stdin",
		"-U0", "-p", "--format=%x00%H", "--", partner)

	return topByCount(counts, order, limit)
}

// declKeyword strips a leading declaration keyword and an optional Go
// receiver, so "class TradeCreate(BaseModel):" → "TradeCreate",
// "func (x *W) Run() {" → "Run", "def greet(self):" → "greet". Anchored
// near line start (after optional modifiers) so prose that merely contains
// a keyword — "return interface value" — is not mistaken for a decl.
var declKeyword = regexp.MustCompile(
	`^\s*(?:export\s+|public\s+|private\s+|static\s+|async\s+)*` +
		`(?:class|def|func|interface|type|struct|enum|function)\b\s*` +
		`(?:\([^)]*\)\s*)?([A-Za-z_]\w*)`)

// funcName distills a hunk's function-context line to a bare identifier;
// it returns "" for contexts that carry no recognizable declaration
// (anonymous funcs, `interface{}`, prose).
func funcName(ctx string) string {
	if m := declKeyword.FindStringSubmatch(ctx); m != nil {
		return m[1]
	}

	return ""
}

// topByCount returns the limit most-frequent names, strongest first, with
// first-seen order breaking ties for determinism.
func topByCount(counts, order map[string]int, limit int) []string {
	if len(counts) == 0 {
		return nil
	}

	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}

	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}

		return order[names[i]] < order[names[j]]
	})

	if limit > 0 && len(names) > limit {
		names = names[:limit]
	}

	return names
}
