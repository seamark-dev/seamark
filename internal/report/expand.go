package report

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/store"
)

// expandLineCap bounds how much source one expand returns: progressive
// disclosure is the point — an agent that wants more asks again with a
// narrower ref, it does not get a whole file by accident.
const expandLineCap = 250

// Expand resolves ref — a symbol name/FQN, "file:start[-end]", or
// "lessons:<dir>" — and writes what it names: source lines, or an
// area's raw review-lesson ledger. This is the second half of every
// other report's contract: they return refs, Expand turns a ref into
// content.
func Expand(w io.Writer, st *store.Store, root, ref string) error {
	if region, ok := strings.CutPrefix(ref, "lessons:"); ok {
		return expandLessons(w, st, root, region)
	}

	if file, start, end, ok := parseSpanRef(ref); ok {
		return writeRegion(w, root, file, start, end)
	}

	syms, err := st.FindSymbols(ref, 2)
	if err != nil {
		return err
	}

	if len(syms) == 0 {
		return fmt.Errorf("nothing in the index matches %q (want a symbol, or file:start-end)", ref)
	}

	sym := syms[0]
	if sym.File == "" {
		return fmt.Errorf("%s is external — no source in this repo", sym.FQN)
	}

	if len(syms) > 1 {
		fmt.Fprintf(w, "note: %q also matched %s — expanding %s\n", ref, syms[1].FQN, sym.FQN)
	}

	return writeRegion(w, root, sym.File, sym.Span.StartLine, sym.Span.EndLine)
}

// expandLessonCap bounds one lessons expand, echoing expandLineCap's
// contract: an agent that wants more narrows the region, it does not
// get the whole repo's ledger by accident.
const expandLessonCap = 200

// expandLessons writes the raw lesson ledger for a region — every mined
// finding in its area, one-offs included. This is the pattern-hunting
// view behind the "reviewers keep flagging" summaries: exact clustering
// cannot merge ten differently-worded findings about one mistake, but a
// reader of the raw set can.
func expandLessons(w io.Writer, st *store.Store, root, region string) error {
	region = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(region), "./"), "/")

	lessons, err := LedgerForRegion(st, region)
	if err != nil {
		return err
	}

	over := 0

	if len(lessons) > expandLessonCap {
		over = len(lessons) - expandLessonCap
		lessons = lessons[:expandLessonCap]
	}

	PrintLessonLedger(w, lessons, loadLessonConfig(w, root), region)

	if over > 0 {
		fmt.Fprintf(w, "… %d more — expand lessons:<deeper-dir> to narrow\n", over)
	}

	return nil
}

// parseSpanRef recognizes "path/file.go:12" and "path/file.go:12-40".
func parseSpanRef(ref string) (file string, start, end uint32, ok bool) {
	file, spanPart, found := strings.Cut(ref, ":")
	if !found || file == "" {
		return "", 0, 0, false
	}

	startStr, endStr, ranged := strings.Cut(spanPart, "-")

	s, err := strconv.ParseUint(startStr, 10, 32)
	if err != nil || s == 0 {
		return "", 0, 0, false
	}

	e := s

	if ranged {
		if e, err = strconv.ParseUint(endStr, 10, 32); err != nil || e < s {
			return "", 0, 0, false
		}
	}

	return file, uint32(s), uint32(e), true
}

// writeRegion prints file lines [start, end] with a header, reading from
// the working tree — expand shows what IS, not what was indexed.
func writeRegion(w io.Writer, root, file string, start, end uint32) error {
	// Clean BEFORE checking: "a/../../x" must not slip past the ".."
	// prefix test. Refs are model-supplied input.
	rel := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(file, "./")))
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("ref must stay inside the workspace: %q", file)
	}

	full := filepath.Join(root, rel)

	// The lexical check above is not enough: a symlink committed inside
	// the workspace can point outside it, and os.ReadFile follows it. An
	// untrusted cloned repo could ship `link -> ~/.ssh` and a model-
	// supplied ref `link/id_rsa`. Resolve symlinks on both sides and
	// require the target to remain within the (also-resolved) root.
	if err := confinedToRoot(root, full); err != nil {
		return err
	}

	data, err := os.ReadFile(full)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}

	// Drop the single trailing newline most files carry, so a whole-file
	// expand does not print a spurious final blank line or over-count.
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if start > uint32(len(lines)) {
		return fmt.Errorf("%s has %d lines, ref starts at %d", file, len(lines), start)
	}

	if end == 0 || end > uint32(len(lines)) {
		end = uint32(len(lines))
	}

	truncated := false

	if end-start+1 > expandLineCap {
		end = start + expandLineCap - 1
		truncated = true
	}

	fmt.Fprintf(w, "%s:%d-%d\n", file, start, end)

	for i := start; i <= end; i++ {
		fmt.Fprintf(w, "%4d\t%s\n", i, lines[i-1])
	}

	if truncated {
		fmt.Fprintf(w, "… capped at %d lines — expand %s:%d-%d for the rest\n",
			expandLineCap, file, end+1, end+expandLineCap)
	}

	return nil
}

// confinedToRoot reports whether target, with all symlinks resolved,
// stays inside root (also resolved — root itself may sit under a
// symlinked prefix, e.g. /tmp -> /private/tmp on macOS). A target that
// does not exist yet is allowed through: it cannot leak content, and
// the caller's read will surface the real "no such file" error.
func confinedToRoot(root, target string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}

	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return nil // non-existent path; the read will fail honestly
	}

	rel, err := filepath.Rel(realRoot, realTarget)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("ref resolves outside the workspace: %q", target)
	}

	return nil
}
