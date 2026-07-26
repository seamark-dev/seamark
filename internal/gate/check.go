package gate

import (
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/store"
)

// ErrBlocked marks decisions that must stop the caller (enforce mode);
// the CLI maps it to a distinct exit code for hooks and CI.
var ErrBlocked = errors.New("blocked by policy")

var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// EvalDiff computes a unified diff's blast radius and evaluates policy
// over it: changed lines map to symbols, and symbols already carry the
// transitively-propagated effect tags — their union is what this change
// can ultimately reach (RFC-001 §5.5, pre-edit enforcement).
func EvalDiff(p *Policy, st *store.Store, diffText string) (*Decision, error) {
	touched := map[string][][2]uint32{} // file -> [start, end] line ranges

	file := ""

	for line := range strings.SplitSeq(diffText, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			file = diffHeaderPath(line[4:])
		default:
			m := hunkRe.FindStringSubmatch(line)
			if m == nil || file == "" {
				continue
			}

			start, _ := strconv.Atoi(m[1])
			count := 1

			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}

			if count < 1 {
				count = 1 // pure deletions still touch the surrounding area
			}

			touched[file] = append(touched[file],
				[2]uint32{uint32(start), uint32(start + count - 1)})
		}
	}

	files := make([]string, 0, len(touched))
	tagSet := map[string]bool{}

	for f, ranges := range touched {
		files = append(files, f)

		syms, err := st.SymbolsInFile(f)
		if err != nil {
			return nil, err
		}

		for i := range syms {
			sym := &syms[i]

			overlaps := false

			for _, r := range ranges {
				if sym.Span.StartLine <= r[1] && sym.Span.EndLine >= r[0] {
					overlaps = true
					break
				}
			}

			if !overlaps {
				continue
			}

			effs, err := st.EffectsForSymbol(sym.ID)
			if err != nil {
				return nil, err
			}

			for _, e := range effs {
				tagSet[e.Tag] = true
			}
		}
	}

	tags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
	}

	activation := map[string]any{
		"effect":  []string{},
		"command": zeroCommand(),
		"env":     p.DetectEnv(os.Environ()),
		"diff":    map[string]any{"files": files, "effects": tags},
	}

	return p.evaluate(tags, activation)
}

// diffHeaderPath extracts the repo-relative path from a "+++" header
// value. Git appends a TAB to paths containing spaces and C-quotes paths
// with special characters — both must unwrap or the file is silently
// skipped and its blast radius missed.
func diffHeaderPath(v string) string {
	v = strings.TrimSuffix(v, "\t")

	if strings.HasPrefix(v, `"`) {
		if unquoted, err := strconv.Unquote(v); err == nil {
			v = unquoted
		}
	}

	if v == "/dev/null" {
		return ""
	}

	return strings.TrimPrefix(v, "b/")
}

// zeroCommand carries every key command rules may access: CEL map access
// to a missing key is an evaluation error, not false.
func zeroCommand() map[string]any {
	return map[string]any{
		"name":          "",
		"argv":          []string{},
		"raw":           "",
		"is_push":       false,
		"is_force_push": false,
		"target_branch": "",
		"dynamic":       false,
	}
}
