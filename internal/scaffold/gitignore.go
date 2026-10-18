package scaffold

import (
	"slices"
	"strings"
)

// gitignoreBlock is appended verbatim when no carve-outs are present. A
// .gitignore from an older seamark gets only the lines it does not have.
const gitignoreBlock = `
# Seamark: the index and audit log stay local; the policy-as-code
# overlays belong in review.
.seamark/*
!.seamark/policy.yaml
!.seamark/effects.yaml
!.seamark/lessons.yaml
!.seamark/config.yaml
`

// carveoutLines returns the pattern lines of gitignoreBlock, without the
// comments and blank lines. composeGitignore checks and repairs these
// lines one by one, so a new overlay file added to the block also
// reaches repositories that ran an older setup.
func carveoutLines() []string {
	var lines []string

	for _, ln := range strings.Split(gitignoreBlock, "\n") {
		if ln != "" && !strings.HasPrefix(ln, "#") {
			lines = append(lines, ln)
		}
	}

	return lines
}

// composeGitignore returns .gitignore with the seamark carve-outs. A
// file that holds them all returns unchanged. A file without any
// carve-out gets the commented block; an older block gets only its
// missing lines. The lines go at the end, because every `!` re-include
// must come after `.seamark/*` for gitignore precedence.
func composeGitignore(existing []byte, _ bool) ([]byte, error) {
	present := map[string]bool{}
	for _, ln := range strings.Split(string(existing), "\n") {
		present[strings.TrimSpace(ln)] = true
	}

	lines := carveoutLines()

	var missing []string

	for _, ln := range lines {
		if !present[ln] {
			missing = append(missing, ln)
		}
	}

	if len(missing) == 0 {
		return existing, nil
	}

	block := gitignoreBlock
	if len(missing) < len(lines) {
		block = "\n" + strings.Join(missing, "\n") + "\n"
	}

	return append(slices.Clone(existing), block...), nil
}
