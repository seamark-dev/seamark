package distill

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/redact"
)

const (
	promptFixHunkCap       = 800
	promptPathItemsPerFact = 10
)

// promptBodyCapFor shares the historical worst-case body budget across a
// group. Small groups can show enough of each fix to expose parallel
// implementations; max-sized groups cost no more than before.
func promptBodyCapFor(findings int) int {
	if findings <= 0 {
		return promptBodyMax
	}

	return max(promptBodyMin, min(promptBodyMax, promptBodyBudget/findings))
}

// relevantFindingPaths returns every non-document path represented by a
// finding, primary first. A multi-file fix must not look like a one-file event
// to the distiller merely because storage chose one semantic home.
func relevantFindingPaths(f model.Finding) []string {
	paths := append([]string{f.Path}, f.Paths...)
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))

	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || model.IsDocPath(p) {
			continue
		}

		if _, ok := seen[p]; ok {
			continue
		}

		seen[p] = struct{}{}
		out = append(out, p)
	}

	return out
}

// promptFindingEvidence shares one per-finding slice of the group evidence
// budget between path metadata and the body. The primary path is mandatory;
// additional paths are admitted only while both the item and serialized-byte
// limits allow them.
func promptFindingEvidence(f model.Finding, maxBytes int) (paths []string, body string) {
	paths = promptFindingPaths(f, maxBytes)
	encoded, _ := json.Marshal(paths)
	bodyBudget := max(0, maxBytes-len(encoded))
	body = promptFindingBody(f, bodyBudget)

	return paths, body
}

func promptFindingPaths(f model.Finding, maxBytes int) []string {
	candidates := relevantFindingPaths(f)
	primary := strings.TrimSpace(f.Path)

	if primary != "" && (len(candidates) == 0 || candidates[0] != primary) {
		candidates = append([]string{primary}, candidates...)
	}

	if len(candidates) == 0 || maxBytes <= 0 {
		return nil
	}

	// The primary identifies the finding and is never traded away for
	// secondary footprint entries. Mined footprints themselves are capped;
	// this second cap also protects imported or synthetic findings.
	out := []string{candidates[0]}

	for _, candidate := range candidates[1:] {
		if len(out) >= promptPathItemsPerFact {
			break
		}

		trial := append(append([]string(nil), out...), candidate)
		encoded, _ := json.Marshal(trial)
		if len(encoded) > maxBytes {
			continue
		}

		out = trial
	}

	return out
}

func promptFindingBody(f model.Finding, maxChars int) string {
	body := redact.Secrets(f.Body)

	if !isFixFinding(f.Source) {
		return truncatePromptText(body, maxChars)
	}

	return compactFixEvidence(body, maxChars)
}

// compactFixEvidence keeps the commit message and samples distinct code
// hunks before tests or documentation. The stored body stays verbatim; this is
// only the bounded model view.
func compactFixEvidence(body string, maxChars int) string {
	marker := "\npatch:\n"
	patchAt := strings.LastIndex(body, marker)

	if patchAt < 0 {
		return truncatePromptText(body, maxChars)
	}

	head := body[:patchAt]
	patch := body[patchAt+len(marker):]

	head = compactFixHeader(head)
	if len(head) >= maxChars {
		return truncatePromptText(head, maxChars)
	}

	var b strings.Builder
	b.WriteString(head)
	b.WriteString("\npatch:\n")

	hunks := splitPatchHunks(patch)
	if len(hunks) == 0 {
		return truncatePromptText(head, maxChars)
	}

	sort.SliceStable(hunks, func(i, j int) bool {
		return fixHunkRank(hunks[i]) < fixHunkRank(hunks[j])
	})

	for _, hunk := range hunks {
		remaining := maxChars - b.Len()

		if remaining <= 0 {
			break
		}

		piece := truncatePromptText(strings.TrimSpace(hunk), min(promptFixHunkCap, remaining))
		if piece == "" {
			continue
		}

		b.WriteString(piece)
		b.WriteByte('\n')
	}

	return strings.TrimSpace(truncatePromptText(b.String(), maxChars))
}

func compactFixHeader(head string) string {
	var out strings.Builder

	for line := range strings.Lines(head) {
		line = strings.TrimSpace(line)

		lower := strings.ToLower(line)

		switch {
		case line == "", line == "---------":
			continue
		case strings.HasPrefix(lower, "co-authored-by:"),
			strings.HasPrefix(lower, "signed-off-by:"):
			continue
		case strings.HasPrefix(line, "functions:"):
			line = compactFunctionLine(line)
		}

		if line != "" {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}

	return strings.TrimSpace(out.String())
}

func compactFunctionLine(line string) string {
	parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "functions:")), ", ")
	kept := make([]string, 0, len(parts))

	for _, part := range parts {
		if codeDeclaration(part) {
			kept = append(kept, part)
		}
	}

	if len(kept) == 0 {
		return ""
	}

	return "functions: " + strings.Join(kept, ", ")
}

func splitPatchHunks(patch string) []string {
	var (
		hunks   []string
		current strings.Builder
		file    string
	)

	for line := range strings.Lines(patch) {
		if filePath, ok := strings.CutPrefix(line, "file: "); ok {
			file = strings.TrimSuffix(strings.TrimSuffix(filePath, "\n"), "\r")
			continue
		}

		if strings.HasPrefix(line, "@@") {
			if current.Len() > 0 {
				hunks = append(hunks, current.String())
				current.Reset()
			}

			if file != "" {
				fmt.Fprintf(&current, "file: %s\n", file)
			}
		}

		if current.Len() > 0 || strings.HasPrefix(line, "@@") {
			current.WriteString(line)
		}
	}

	if current.Len() > 0 {
		hunks = append(hunks, current.String())
	}

	return hunks
}

var testHunkRE = regexp.MustCompile(`(?i)(^|[^a-z0-9])(?:test|tests|testing|benchmark|bench)[_a-z0-9]*\b`)

func fixHunkRank(hunk string) int {
	first, rest, _ := strings.Cut(hunk, "\n")
	context := first

	if file, ok := strings.CutPrefix(first, "file: "); ok {
		file = strings.TrimSpace(file)

		if model.IsTestPath(file) {
			return 2
		}

		if model.IsDocPath(file) {
			return 3
		}

		context, _, _ = strings.Cut(rest, "\n")
	}

	lower := strings.ToLower(context)

	switch {
	case testHunkRE.MatchString(lower):
		return 2
	case codeDeclaration(context):
		return 0
	case strings.Contains(lower, "changelog"), strings.Contains(lower, "semantic version"):
		return 3
	default:
		return 1
	}
}

func codeDeclaration(text string) bool {
	lower := strings.ToLower(text)

	// Parenthesized hunk context covers methods and functions across
	// Go, Python, JavaScript/TypeScript, Rust, Java, C#, Ruby, and C/C++
	// without maintaining a language-specific parser here. Declaration
	// keywords also retain useful type-level hunks with no parameter list.
	return strings.Contains(text, "(") ||
		strings.Contains(lower, "func ") ||
		strings.Contains(lower, "fn ") ||
		strings.Contains(lower, "function ") ||
		strings.Contains(lower, "def ") ||
		strings.Contains(lower, "type ") ||
		strings.Contains(lower, "class ") ||
		strings.Contains(lower, "struct ") ||
		strings.Contains(lower, "interface ")
}

func truncatePromptText(text string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}

	if len(text) <= maxChars {
		return text
	}

	const suffix = " …[truncated]"

	cut := maxChars
	if maxChars > len(suffix) {
		cut -= len(suffix)
	}

	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}

	truncated := strings.TrimSpace(text[:cut])
	if maxChars <= len(suffix) {
		return truncated
	}

	return truncated + suffix
}
