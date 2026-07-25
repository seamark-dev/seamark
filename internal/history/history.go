// Package history mines the git log into the history layer of the graph:
// commits become Decision rows, and file co-occurrence across commits becomes
// CoChange pairs with lift.
//
// It shells out to git rather than using a Go git implementation: git is
// always present where a repo is, and `git log --numstat` is far faster than
// in-process history walking on large histories (RFC-001 §7.1).
package history

import (
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
)

// Separators for the pretty format. NUL is the one byte git guarantees can
// never appear inside a commit object, so the header is wrapped in NULs
// (emitted via %x00): splitting the output on NUL yields a strict
// header/numstat alternation that no commit message can forge. The field
// separator CAN legally occur in messages; it sits after the trusted
// fields (SHA, author, timestamp) and parsing validates each record,
// skipping non-conforming ones instead of trusting the split.
const (
	recordSep = "\x00" // wraps each header: %x00 header %x00 numstat …
	fieldSep  = "\x02" // between header fields
)

// shaRe matches full SHA-1/SHA-256 object names, anchoring each record.
var shaRe = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// Options tune the mining pass.
type Options struct {
	// MaxCommits bounds the mined window. 0 means the default (5000).
	// Co-change lift is computed over this window, so it reflects recent
	// coupling rather than all-time archaeology.
	MaxCommits int
	// MaxFilesPerCommit drops bulk commits (formatting sweeps, vendoring,
	// mass renames) from co-change counting; they say nothing about
	// intentional coupling. 0 means the default (30). Dropped commits are
	// still recorded as decisions.
	MaxFilesPerCommit int
	// MinTogether is the minimum number of shared commits for a pair to be
	// stored. 0 means the default (2).
	MinTogether int
}

func (o Options) withDefaults() Options {
	if o.MaxCommits == 0 {
		o.MaxCommits = 5000
	}

	if o.MaxFilesPerCommit == 0 {
		o.MaxFilesPerCommit = 30
	}

	if o.MinTogether == 0 {
		o.MinTogether = 2
	}

	return o
}

// excludedPath reports paths whose co-changes are noise: lockfiles,
// vendored/generated trees, build outputs. Kept as a function (not config)
// until real-world tuning demands otherwise.
func excludedPath(p string) bool {
	base := path.Base(p)

	switch base {
	case "go.sum", "go.mod", "package-lock.json", "yarn.lock", "pnpm-lock.yaml",
		"Cargo.lock", "poetry.lock", "uv.lock", "Gemfile.lock", "composer.lock":
		return true
	}

	if strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".pb.go") ||
		strings.HasSuffix(base, "_generated.go") || strings.HasSuffix(base, ".gen.go") {
		return true
	}

	for _, dir := range []string{"vendor/", "node_modules/", "dist/", "build/", "third_party/"} {
		if strings.HasPrefix(p, dir) || strings.Contains(p, "/"+dir) {
			return true
		}
	}

	return false
}

// Mine runs git log in repoRoot and returns decisions (all mined commits)
// plus co-change pairs (from commits passing the size filter). A repository
// with no commits yet yields empty results, not an error.
func Mine(repoRoot string, opts Options) ([]model.Decision, []model.CoChange, error) {
	opts = opts.withDefaults()

	if !hasHead(repoRoot) {
		return nil, nil, nil
	}

	commits, err := gitLog(repoRoot, opts.MaxCommits)
	if err != nil {
		return nil, nil, err
	}

	return commits, coChange(commits, opts), nil
}

// hasHead reports whether the repository has at least one commit. `git log`
// exits non-zero on a freshly initialized repo, which is an empty history,
// not a mining failure.
func hasHead(repoRoot string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "HEAD")
	cmd.Dir = repoRoot

	return cmd.Run() == nil
}

// gitLog reads the commit log with per-commit file lists.
// core.quotePath=off keeps non-ASCII paths raw instead of C-quoted, so the
// history layer stores the same file names the parse layer produces.
func gitLog(repoRoot string, maxCommits int) ([]model.Decision, error) {
	format := "%x00%H" + fieldSep + "%an" + fieldSep + "%at" + fieldSep + "%s" + fieldSep + "%b%x00"

	cmd := exec.Command("git", "-c", "core.quotePath=off", "log", "--numstat", "--no-renames",
		"-n", fmt.Sprint(maxCommits), "--pretty=format:"+format)

	cmd.Dir = repoRoot

	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("history: git log: %s", strings.TrimSpace(string(ee.Stderr)))
		}

		return nil, fmt.Errorf("history: git log: %w", err)
	}

	// Splitting on NUL yields ["", header1, numstat1, header2, numstat2, …]:
	// the alternation is structural (git emits exactly two NULs per commit,
	// messages cannot contain them), so headers and file lists can never be
	// spoofed by message content.
	records := strings.Split(string(out), recordSep)

	var decisions []model.Decision

	for i := 1; i < len(records); i += 2 {
		numstat := ""
		if i+1 < len(records) {
			numstat = records[i+1]
		}

		fields := strings.SplitN(records[i], fieldSep, 5)
		if len(fields) != 5 || !shaRe.MatchString(fields[0]) {
			continue
		}

		ts, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			// A field separator inside the author name shifted the split
			// (possible only in hand-crafted commit objects); skip the
			// record rather than abort the whole layer.
			continue
		}

		d := model.Decision{
			Kind:   model.DecisionCommit,
			Ref:    fields[0],
			TS:     ts,
			Author: fields[1],
			Title:  fields[3],
			Body:   strings.TrimSpace(fields[4]),
		}

		if strings.HasPrefix(d.Title, "Revert ") || strings.HasPrefix(d.Title, "Revert:") {
			d.Kind = model.DecisionRevert
		}

		for line := range strings.SplitSeq(numstat, "\n") {
			// numstat lines: "<added>\t<deleted>\t<path>"; the counters are
			// digits or "-" (binary). Validation is belt and braces on top
			// of the structural framing above.
			parts := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 3)
			if len(parts) != 3 || parts[2] == "" ||
				!numstatCount(parts[0]) || !numstatCount(parts[1]) {
				continue
			}

			d.Files = append(d.Files, parts[2])
		}

		decisions = append(decisions, d)
	}

	return decisions, nil
}

// coChange computes pairwise co-occurrence with lift over the mined window.
//
//	lift(a,b) = P(a,b) / (P(a)·P(b)) = together·N / (n(a)·n(b))
//
// Lift rather than raw counts keeps hub files (touched in half the commits)
// from dominating: a pair must co-occur beyond what its members' individual
// churn predicts.
func coChange(commits []model.Decision, opts Options) []model.CoChange {
	type pair struct{ a, b string }

	fileCount := map[string]int{}
	pairCount := map[pair]int{}
	total := 0

	for _, c := range commits {
		files := make([]string, 0, len(c.Files))

		for _, f := range c.Files {
			if !excludedPath(f) {
				files = append(files, f)
			}
		}

		if len(files) < 1 || len(files) > opts.MaxFilesPerCommit {
			continue
		}

		total++
		sort.Strings(files)

		for i, a := range files {
			fileCount[a]++
			for _, b := range files[i+1:] {
				pairCount[pair{a, b}]++
			}
		}
	}

	if total == 0 {
		return nil
	}

	var out []model.CoChange

	for p, together := range pairCount {
		if together < opts.MinTogether {
			continue
		}

		lift := float64(together) * float64(total) /
			(float64(fileCount[p.a]) * float64(fileCount[p.b]))
		out = append(out, model.CoChange{
			FileA: p.a, FileB: p.b,
			Together: together, Total: total, Lift: lift,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Together != out[j].Together {
			return out[i].Together > out[j].Together
		}
		if out[i].FileA != out[j].FileA {
			return out[i].FileA < out[j].FileA
		}
		return out[i].FileB < out[j].FileB
	})

	return out
}

// numstatCount reports whether s is a valid numstat counter.
func numstatCount(s string) bool {
	if s == "-" {
		return true
	}

	if s == "" {
		return false
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

// IsRepo reports whether dir is inside a git work tree.
func IsRepo(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	out, err := cmd.Output()

	return err == nil && strings.TrimSpace(string(out)) == "true"
}
