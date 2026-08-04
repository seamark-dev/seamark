// Package fixes mines fix commits into findings — the lesson source
// that exists in every repository, review culture or none. A fix commit
// is a correction the author themselves labeled: the patch shows the
// mistake being undone, which makes it distillation material even when
// the commit message says nothing useful (validated empirically: two
// anonymous "fix: PR review" commits grouped correctly on patch content
// alone).
//
// Classification is tiered and precise over complete — a miss degrades,
// a false positive corrupts — and every finding declares its derivation
// in Source, like every call edge declares its resolution. Duplicates
// are removed by patch identity (cherry-picks and backports are one
// event), and fixes that were later reverted are excluded: a fix bad
// enough to undo teaches the wrong lesson.
package fixes

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/redact"
)

// Options bounds a fix-mining pass.
type Options struct {
	// Window is how far back to mine. Staleness defense: a years-old fix
	// pattern on since-rewritten code must not enter the corpus. Zero
	// means DefaultWindow.
	Window time.Duration
	// Logf receives progress; nil discards it.
	Logf func(format string, args ...any)
}

// DefaultWindow is the mining recency window — measured on the test
// repos as the span that keeps a meaningful corpus (~50-70 findings)
// without resurrecting ancient history.
const DefaultWindow = 365 * 24 * time.Hour

// Tunables, shared rationale with the co-change miner and findingBody.
const (
	maxFilesPerFix = 30   // above this it's a refactor, not a discrete mistake
	bodyCap        = 4096 // stored finding body bound
	patchCap       = 3000 // patch excerpt inside the body
)

// choreRe matches fixes that are real but teach nothing transferable:
// lint, CI, formatting, typos. They stay out of the corpus entirely.
var choreRe = regexp.MustCompile(`(?i)fix[^a-z]*(ci|lint|typo|fmt|format|imports?|build|spelling)`)

var (
	conventionalRe = regexp.MustCompile(`^(?i)fix[(:!]`)
	subjectRe      = regexp.MustCompile(`^Fix `)
	issueLinkRe    = regexp.MustCompile(`(?i)\b(fixes|closes|resolves) #\d+`)
	revertRe       = regexp.MustCompile(`^Revert "`)
	revertShaRe    = regexp.MustCompile(`This reverts commit ([0-9a-f]{40})`)
	prRefRe        = regexp.MustCompile(`\(#(\d+)\)\s*$`)
	issueNumRe     = regexp.MustCompile(`(?i)\b(?:fixes|closes|resolves) #(\d+)`)
	hunkFuncRe     = regexp.MustCompile(`(?m)^@@ .* @@ (.+)$`)
)

// Classify returns the finding source for a commit subject+body, or ""
// when it is not a fix worth mining. Exported for the fix-density
// report line, which classifies decision titles the same way.
func Classify(subject, body string) string {
	switch {
	case revertRe.MatchString(subject):
		return model.SourceRevert
	case choreRe.MatchString(subject):
		return "" // real fix, no transferable lesson
	case conventionalRe.MatchString(subject):
		return model.SourceFixConventional
	case issueLinkRe.MatchString(subject) || issueLinkRe.MatchString(body):
		return model.SourceFixIssueLink
	case subjectRe.MatchString(subject):
		return model.SourceFixSubject
	default:
		return ""
	}
}

// commit is one candidate parsed from the log.
type commit struct {
	sha     string
	author  string
	ts      int64
	subject string
	body    string
	source  string
}

// Result carries one mining pass. Mined distinguishes "git answered,
// zero fixes" — swapping the stored set to empty is then correct, stale
// findings age out — from "no git here", where stored findings must be
// preserved. The same contract as reviews.Result.Fetched.
type Result struct {
	Findings []model.Finding
	Mined    bool
}

// Mine returns the fix findings for the repository at root. A workspace
// without git yields an unmined result, never an error — the same
// degrade-not-fail contract as review mining.
func Mine(root string, opts Options) (Result, error) {
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	window := opts.Window
	if window == 0 {
		window = DefaultWindow
	}

	since := fmt.Sprintf("--since=%d.days", int(window.Hours()/24))

	commits, mined := listCommits(root, since)
	if !mined {
		return Result{}, nil
	}

	reverted := revertedShas(root)
	dupes := duplicatePatchShas(root, since)

	var out []model.Finding

	kept := 0

	for _, c := range commits {
		if c.source == "" || reverted[c.sha] || dupes[c.sha] {
			continue
		}

		f, ok := buildFinding(root, c)
		if !ok {
			continue
		}

		out = append(out, f)
		kept++
	}

	logf("fixes: %d commits scanned, %d findings kept", len(commits), kept)

	return Result{Findings: out, Mined: true}, nil
}

// listCommits parses the windowed log into candidates, classifying as
// it goes. NUL field / RS record separators survive any message text.
// mined is false when git itself has nothing to say (no repository).
func listCommits(root, since string) (commits []commit, mined bool) {
	out, err := gitOut(root, "log", since, "--no-merges",
		"--format=%H%x00%an%x00%at%x00%s%x00%b%x1e")
	if err != nil {
		return nil, false // not a git repo (or no commits): nothing to mine
	}

	for rec := range strings.SplitSeq(string(out), "\x1e") {
		parts := strings.SplitN(strings.TrimLeft(rec, "\n"), "\x00", 5)
		if len(parts) != 5 {
			continue
		}

		ts, _ := strconv.ParseInt(parts[2], 10, 64)
		c := commit{sha: parts[0], author: parts[1], ts: ts,
			subject: parts[3], body: strings.TrimSpace(parts[4])}
		c.source = Classify(c.subject, c.body)

		commits = append(commits, c)
	}

	return commits, true
}

// revertedShas returns commits later undone, over the whole history: a
// revert outside the window still invalidates a fix inside it.
func revertedShas(root string) map[string]bool {
	out, err := gitOut(root, "log", "--format=%b%x1e", "--grep", `^Revert "`)
	if err != nil {
		return nil
	}

	shas := map[string]bool{}

	for _, m := range revertShaRe.FindAllStringSubmatch(string(out), -1) {
		shas[m[1]] = true
	}

	return shas
}

// duplicatePatchShas maps every sha whose patch already appeared under
// an earlier sha in the window — cherry-picks and backports. One event,
// one finding.
//
// The walk is --reverse (oldest first) so the ORIGINAL commit is the one
// kept and later backports are the duplicates. Newest-first would keep
// whichever backport landed last, so a new backport would change the
// surviving sha — and with it the sha-derived finding id, the group
// signature, and the distillation bill for an unchanged fix.
func duplicatePatchShas(root, since string) map[string]bool {
	logCmd := exec.Command("git", "log", since, "--reverse", "--no-merges", "-p", "--format=commit %H")
	logCmd.Dir = root

	idCmd := exec.Command("git", "patch-id", "--stable")
	idCmd.Dir = root

	pipe, err := logCmd.StdoutPipe()
	if err != nil {
		return nil
	}

	idCmd.Stdin = pipe

	var ids bytes.Buffer

	idCmd.Stdout = &ids

	if err := logCmd.Start(); err != nil {
		return nil
	}

	if err := idCmd.Run(); err != nil {
		_ = logCmd.Wait()

		return nil
	}

	_ = logCmd.Wait()

	seen := map[string]bool{} // patch-id -> already seen
	dupes := map[string]bool{}

	sc := bufio.NewScanner(&ids)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}

		if seen[fields[0]] {
			dupes[fields[1]] = true
		}

		seen[fields[0]] = true
	}

	return dupes
}

// buildFinding assembles the finding for one classified fix: primary
// file, touched functions from the hunk headers, and a bounded patch
// excerpt — the patch is the signal that survives useless messages.
func buildFinding(root string, c commit) (model.Finding, bool) {
	stat, err := gitOut(root, "show", "--format=", "--numstat", c.sha)
	if err != nil {
		return model.Finding{}, false
	}

	path, files := primaryFile(string(stat))
	if path == "" || files > maxFilesPerFix {
		return model.Finding{}, false
	}

	patch, err := gitOut(root, "show", "--format=", "--unified=1", c.sha)
	if err != nil {
		return model.Finding{}, false
	}

	var b strings.Builder

	fmt.Fprintf(&b, "fix commit %s\nsubject: %s\n", c.sha[:8], c.subject)

	if c.body != "" {
		fmt.Fprintf(&b, "%s\n", c.body)
	}

	if funcs := touchedFunctions(string(patch), 5); len(funcs) > 0 {
		fmt.Fprintf(&b, "functions: %s\n", strings.Join(funcs, ", "))
	}

	fmt.Fprintf(&b, "patch:\n%s", trimPatch(string(patch), patchCap))

	// Redact before capping: commit messages and patches carry the
	// credentials being removed ("-DATABASE_URL=postgres://u:pass@…"),
	// and a cap cut must never expose the tail of a scrubbed secret.
	body := redact.Secrets(b.String())
	if len(body) > bodyCap {
		body = body[:bodyCap]
	}

	return model.Finding{
		ID:        shaID(c.sha),
		Path:      path,
		PR:        prNumber(c.subject, c.body),
		Reviewer:  "human",
		Body:      body,
		URL:       "",
		CreatedAt: c.ts,
		Source:    c.source,
	}, true
}

// skipDirs mirrors the indexer's never-worth-parsing set (kept local:
// importing internal/index here would cycle once index mines fixes).
var skipDirs = map[string]bool{
	"vendor": true, "node_modules": true, "testdata": true,
	"dist": true, "build": true,
}

// primaryFile picks the most-changed code file from --numstat output
// and reports the total file count.
func primaryFile(numstat string) (path string, files int) {
	best, bestChurn := "", -1

	for line := range strings.SplitSeq(numstat, "\n") {
		// Tab-delimited, not space: `1\t0\tpkg/my file.go` is one valid
		// row, and splitting on whitespace would drop every path
		// containing a space — from the churn ranking AND the file count
		// the bulk-commit cutoff reads.
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}

		files++

		if skipDirs[strings.SplitN(parts[2], "/", 2)[0]] {
			continue
		}

		add, _ := strconv.Atoi(parts[0])
		del, _ := strconv.Atoi(parts[1])

		if churn := add + del; churn > bestChurn {
			best, bestChurn = parts[2], churn
		}
	}

	return best, files
}

// touchedFunctions distills the hunk headers' function context.
func touchedFunctions(patch string, limit int) []string {
	var out []string

	seen := map[string]bool{}

	for _, m := range hunkFuncRe.FindAllStringSubmatch(patch, -1) {
		name := strings.TrimSpace(m[1])
		if name == "" || seen[name] {
			continue
		}

		seen[name] = true
		out = append(out, name)

		if len(out) >= limit {
			break
		}
	}

	return out
}

// trimPatch keeps the informative prefix of a patch, dropping the noise
// lines that spend the cap without carrying signal.
func trimPatch(patch string, cap_ int) string {
	var b strings.Builder

	for line := range strings.SplitSeq(patch, "\n") {
		if strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "diff --git") ||
			strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			continue
		}

		if b.Len()+len(line) > cap_ {
			b.WriteString("…[truncated]")

			break
		}

		b.WriteString(line)
		b.WriteByte('\n')
	}

	return b.String()
}

// prNumber extracts the PR (or linked issue) number — the cross-provider
// key: a fix and the review comments on its PR describe one event.
func prNumber(subject, body string) int {
	if m := prRefRe.FindStringSubmatch(subject); m != nil {
		n, _ := strconv.Atoi(m[1])

		return n
	}

	if m := issueNumRe.FindStringSubmatch(subject + "\n" + body); m != nil {
		n, _ := strconv.Atoi(m[1])

		return n
	}

	return 0
}

// shaID derives the stable finding id from the commit sha: the first
// eight bytes, positive. Stability across mines is what the distill
// signature economics stand on.
func shaID(sha string) int64 {
	raw, err := hex.DecodeString(sha[:16])
	if err != nil {
		return 0
	}

	return int64(binary.BigEndian.Uint64(raw) &^ (1 << 63))
}

func gitOut(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root

	return cmd.Output()
}
