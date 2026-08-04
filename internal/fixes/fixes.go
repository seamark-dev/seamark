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
	"sort"
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
	merges := listMerges(root, since)
	prMap := prMapOf(merges)

	var out []model.Finding

	kept := 0

	for _, c := range commits {
		if c.source == "" || reverted[c.sha] || dupes[c.sha] {
			continue
		}

		f, ok := buildFinding(root, c, prMap)
		if !ok {
			continue
		}

		out = append(out, f)
		kept++
	}

	// Branch-name tier: a merge from fix/… whose commits carried no
	// fix-shaped message of their own is a fix the author declared only
	// in the branch name — without this, that pull request contributes
	// nothing. Strictly additive by construction: any classified commit
	// inside means the fix is already mined, and this tier stays out.
	// The other exclusions the commit tier enforces hold here too: a
	// chore-shaped branch (fix/lint) or a chore commit inside teaches
	// nothing, a reverted member means the fix was at least partly
	// undone, and a backport merged through a second branch is the same
	// event (patch identity, oldest merge kept).
	bySha := make(map[string]commit, len(commits))
	for _, c := range commits {
		bySha[c.sha] = c
	}

	seenMergePatch := map[string]bool{}

	// Oldest first, so the ORIGINAL merge survives patch-identity
	// dedup and a new backport cannot change the surviving sha — the
	// same stability argument as duplicatePatchShas.
	for i := len(merges) - 1; i >= 0; i-- {
		m := merges[i]

		if !fixBranchRe.MatchString(m.branch) || choreRe.MatchString(m.branch) ||
			reverted[m.sha] || len(m.commits) == 0 {
			continue
		}

		excluded := false

		for _, s := range m.commits {
			c := bySha[s]

			if c.source != "" || choreRe.MatchString(c.subject) || reverted[s] {
				excluded = true

				break
			}
		}

		if excluded {
			continue
		}

		if id := mergePatchID(root, m.sha); id != "" {
			if seenMergePatch[id] {
				continue
			}

			seenMergePatch[id] = true
		}

		f, ok := buildMergeFinding(root, m)
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

var (
	// mergeSubjectRe reads the pull-request number and head ref off a
	// merge commit's subject — GitHub's default wording.
	mergeSubjectRe = regexp.MustCompile(`^Merge pull request #(\d+) from (\S+)`)
	// mergeBranchRe reads the branch off a plain `git merge` subject —
	// the local-workflow twin, which carries no PR number.
	mergeBranchRe = regexp.MustCompile(`^Merge branch '([^']+)'`)
	// fixBranchRe recognizes a branch name that declares a fix.
	fixBranchRe = regexp.MustCompile(`^(?:fix|bugfix|hotfix)/`)
)

// maxPRCommits bounds one merge's branch walk: past this, it is a
// long-lived integration branch, not a reviewable pull request, and
// attributing its commits to one PR would be noise.
const maxPRCommits = 200

// mergeCommit is one merge parsed from the log: what it merged, from
// which branch, for which pull request.
type mergeCommit struct {
	sha     string
	ts      int64
	subject string
	body    string
	pr      int      // 0 for local merges
	branch  string   // head ref minus the owner segment; "" when unparseable
	commits []string // the branch commits it brought in; nil when capped
}

// listMerges parses the windowed merge log. Squash-merge repos have no
// merges — this is one cheap git call there. rev-list per merge names
// exactly the commits the merge brought in, capped at maxPRCommits
// (past the cap commits stays nil — the merge is an integration event,
// not a reviewable change).
func listMerges(root, since string) []mergeCommit {
	out, err := gitOut(root, "log", "--merges", since, "--format=%H%x00%at%x00%s%x00%b%x1e")
	if err != nil {
		return nil
	}

	var merges []mergeCommit

	for rec := range strings.SplitSeq(string(out), "\x1e") {
		parts := strings.SplitN(strings.TrimLeft(rec, "\n"), "\x00", 4)
		if len(parts) != 4 {
			continue
		}

		ts, _ := strconv.ParseInt(parts[1], 10, 64)
		m := mergeCommit{sha: parts[0], ts: ts,
			subject: parts[2], body: strings.TrimSpace(parts[3])}

		if g := mergeSubjectRe.FindStringSubmatch(m.subject); g != nil {
			m.pr, _ = strconv.Atoi(g[1])
			// The head ref is owner/branch; the branch may itself
			// contain slashes (owner/fix/holiday-ux).
			if _, branch, found := strings.Cut(g[2], "/"); found {
				m.branch = branch
			}
		} else if g := mergeBranchRe.FindStringSubmatch(m.subject); g != nil {
			m.branch = g[1]
		} else {
			continue // not a shape we can attribute
		}

		branch, err := gitOut(root, "rev-list", "--no-merges",
			fmt.Sprintf("--max-count=%d", maxPRCommits+1), m.sha+"^1.."+m.sha+"^2")
		if err != nil {
			continue // an octopus or shallow edge; other merges still map
		}

		if shas := strings.Fields(string(branch)); len(shas) <= maxPRCommits {
			m.commits = shas
		}

		merges = append(merges, m)
	}

	return merges
}

// prMapOf recovers each branch commit's pull request from merge
// topology: squash-merge repos carry the PR in every subject; merge-
// commit repos carry it nowhere else. Measured on a real repo, every
// fix finding had pr=0, so a review comment and the fix commit
// answering it counted as two independent events — recurrence inflated
// exactly where evidence was weakest.
//
// listMerges walks newest-first and later assignments overwrite, so a
// commit reachable from several merges (a criss-cross) settles on the
// OLDEST merge — the pull request that first landed it.
func prMapOf(merges []mergeCommit) map[string]int {
	prBySha := map[string]int{}

	for _, m := range merges {
		if m.pr == 0 {
			continue
		}

		for _, s := range m.commits {
			prBySha[s] = m.pr
		}
	}

	return prBySha
}

// mergePatchID computes the stable patch identity of a merge's diff
// against its first parent — how two backport merges of one change are
// recognized as one event. "" on any failure: dedup degrades to
// per-merge findings rather than aborting the tier.
func mergePatchID(root, sha string) string {
	diffCmd := exec.Command("git", "diff", sha+"^1", sha)
	diffCmd.Dir = root

	idCmd := exec.Command("git", "patch-id", "--stable")
	idCmd.Dir = root

	pipe, err := diffCmd.StdoutPipe()
	if err != nil {
		return ""
	}

	idCmd.Stdin = pipe

	var id bytes.Buffer

	idCmd.Stdout = &id

	if err := diffCmd.Start(); err != nil {
		return ""
	}

	if err := idCmd.Run(); err != nil {
		_ = diffCmd.Wait()

		return ""
	}

	_ = diffCmd.Wait()

	if fields := strings.Fields(id.String()); len(fields) > 0 {
		return fields[0]
	}

	return ""
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
// prMap supplies the merge-topology PR fallback; an explicit reference
// in the message always wins over inference.
func buildFinding(root string, c commit, prMap map[string]int) (model.Finding, bool) {
	stat, err := gitOut(root, "show", "--format=", "--numstat", c.sha)
	if err != nil {
		return model.Finding{}, false
	}

	paths, files := fixFiles(string(stat))
	if len(paths) == 0 || files > maxFilesPerFix {
		return model.Finding{}, false
	}

	patch, err := gitOut(root, "show", "--format=", "--unified=1", c.sha)
	if err != nil {
		return model.Finding{}, false
	}

	return model.Finding{
		ID:        shaID(c.sha),
		Path:      paths[0],
		Paths:     paths,
		PR:        commitPR(c, prMap),
		Reviewer:  "human",
		Body:      assembleBody(c.sha, c.subject, c.body, string(patch)),
		URL:       "",
		CreatedAt: c.ts,
		Source:    c.source,
	}, true
}

// buildMergeFinding assembles the finding for a branch-declared fix:
// the merge's whole diff against its first parent — what the pull
// request changed — under the same caps as a single commit's patch.
func buildMergeFinding(root string, m mergeCommit) (model.Finding, bool) {
	stat, err := gitOut(root, "diff", "--numstat", m.sha+"^1", m.sha)
	if err != nil {
		return model.Finding{}, false
	}

	paths, files := fixFiles(string(stat))
	if len(paths) == 0 || files > maxFilesPerFix {
		return model.Finding{}, false
	}

	patch, err := gitOut(root, "diff", "--unified=1", m.sha+"^1", m.sha)
	if err != nil {
		return model.Finding{}, false
	}

	return model.Finding{
		ID:        shaID(m.sha),
		Path:      paths[0],
		Paths:     paths,
		PR:        m.pr,
		Reviewer:  "human",
		Body:      assembleBody(m.sha, m.subject, m.body, string(patch)),
		URL:       "",
		CreatedAt: m.ts,
		Source:    model.SourceFixBranch,
	}, true
}

// assembleBody renders a finding's stored text: header, message,
// touched functions, bounded patch excerpt. Redaction runs before the
// cap — commit messages and patches carry the credentials being
// removed ("-DATABASE_URL=postgres://u:pass@…"), and a cap cut must
// never expose the tail of a scrubbed secret.
func assembleBody(sha, subject, msg, patch string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "fix commit %s\nsubject: %s\n", sha[:8], subject)

	if msg != "" {
		fmt.Fprintf(&b, "%s\n", msg)
	}

	if funcs := touchedFunctions(patch, 5); len(funcs) > 0 {
		fmt.Fprintf(&b, "functions: %s\n", strings.Join(funcs, ", "))
	}

	fmt.Fprintf(&b, "patch:\n%s", trimPatch(patch, patchCap))

	body := redact.Secrets(b.String())
	if len(body) > bodyCap {
		body = body[:bodyCap]
	}

	return body
}

// skipDirs mirrors the indexer's never-worth-parsing set (kept local:
// importing internal/index here would cycle once index mines fixes).
var skipDirs = map[string]bool{
	"vendor": true, "node_modules": true, "testdata": true,
	"dist": true, "build": true,
}

// maxFootprintPaths bounds a finding's stored path list: enough for
// region inference to see the real change, without a wide fix bloating
// the row.
const maxFootprintPaths = 10

// changedFile is one --numstat row that survived the skip list.
type changedFile struct {
	path  string
	churn int
}

// fixFiles parses --numstat output into the commit's code footprint —
// non-skipped files by churn (ties by path, so the order is stable) —
// and reports the total file count for the bulk-commit cutoff.
//
// The FIRST entry is the semantic home: the most-changed file that is
// neither a test nor documentation. Churn alone routinely elects the
// test (a fix's test grows more lines than the fix — measured: every
// fix-sourced proposal on a real repo carried a tests/ evidence path
// and landed at region `*` because of it). Tests and docs stay in the
// footprint as evidence; they just never lead it. When the commit
// touches only tests and docs, the churn winner leads — a test-only
// fix is legitimately about the tests.
func fixFiles(numstat string) (paths []string, files int) {
	var changed []changedFile

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

		changed = append(changed, changedFile{path: parts[2], churn: add + del})
	}

	sort.Slice(changed, func(i, j int) bool {
		if changed[i].churn != changed[j].churn {
			return changed[i].churn > changed[j].churn
		}

		return changed[i].path < changed[j].path
	})

	lead := -1

	for i, c := range changed {
		if !model.IsTestPath(c.path) && !model.IsDocPath(c.path) {
			lead = i

			break
		}
	}

	if lead > 0 {
		home := changed[lead]
		changed = append(changed[:lead], changed[lead+1:]...)
		changed = append([]changedFile{home}, changed...)
	}

	for _, c := range changed {
		if len(paths) >= maxFootprintPaths {
			break
		}

		paths = append(paths, c.path)
	}

	return paths, files
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

// commitPR resolves a commit's pull request: the message's own
// reference first (explicit beats inferred), then the merge-topology
// map.
func commitPR(c commit, prMap map[string]int) int {
	if pr := prNumber(c.subject, c.body); pr > 0 {
		return pr
	}

	return prMap[c.sha]
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
