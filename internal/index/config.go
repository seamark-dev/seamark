package index

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config tunes what the indexer walks (`.seamark/config.yaml`). An absent
// file yields the defaults: generated files are skipped, nothing else is
// excluded. Kept separate from the gate/lessons overlays — this is about
// which files enter the graph at all.
type Config struct {
	Index struct {
		// Generated indexes files carrying a "Code generated … DO NOT
		// EDIT." header. Default false: such files (protoc, stringer,
		// mockgen output) are boilerplate that inflates the graph and
		// pollutes most-called, and are not meant to be hand-navigated.
		Generated bool `yaml:"generated"`
		// Exclude is a list of gitignore-ish globs skipped on top of
		// .gitignore and the built-in skip dirs. Forms: a bare glob
		// (`*.pb.go`, `*_test.go`) matches any basename; a `**/`-prefixed
		// glob matches the basename in any directory; a trailing-slash
		// entry (`internal/gen/`) matches a directory prefix.
		Exclude []string `yaml:"exclude"`
	} `yaml:"index"`
}

// LoadConfig reads <root>/.seamark/config.yaml. A missing file is not an
// error (defaults apply); a malformed one is, so a typo'd exclude is not
// silently ignored.
func LoadConfig(root string) (*Config, error) {
	cfg := &Config{}

	data, err := os.ReadFile(filepath.Join(root, ".seamark", "config.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}

		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("index config: %w", err)
	}

	for _, pat := range cfg.Index.Exclude {
		if err := checkExclude(pat); err != nil {
			return nil, fmt.Errorf("index config: exclude %q: %w", pat, err)
		}
	}

	return cfg, nil
}

// checkExclude rejects at load time the patterns matchExclude would
// silently never apply — the same reasoning that makes a malformed file
// an error: an exclude that quietly matches nothing is worse than no
// exclude at all.
func checkExclude(pattern string) error {
	pattern = strings.TrimPrefix(pattern, "./")

	if pattern == "" {
		return errors.New("empty pattern")
	}

	if strings.HasSuffix(pattern, "/") {
		return nil // a directory prefix is matched literally
	}

	if rest, ok := strings.CutPrefix(pattern, "**/"); ok {
		if strings.Contains(rest, "/") {
			return errors.New("`**/` takes a basename glob, not a path")
		}

		pattern = rest
	}

	if strings.Contains(pattern, "**") {
		return errors.New("`**` is only supported as a leading `**/`")
	}

	_, err := path.Match(pattern, "probe")

	return err // nil, or path.ErrBadPattern for glob syntax errors
}

// excluded reports whether a repo-relative path is dropped by an exclude
// glob.
func (c *Config) excluded(rel string) bool {
	for _, pat := range c.Index.Exclude {
		if matchExclude(pat, rel) {
			return true
		}
	}

	return false
}

// matchExclude implements the small glob subset documented on Exclude —
// deliberately not full gitignore, but enough for the real cases without
// a doublestar dependency.
func matchExclude(pattern, rel string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	if pattern == "" {
		return false
	}

	switch {
	case strings.HasSuffix(pattern, "/"): // directory prefix
		return strings.HasPrefix(rel, pattern)
	case strings.HasPrefix(pattern, "**/"): // basename in any directory
		ok, _ := path.Match(strings.TrimPrefix(pattern, "**/"), path.Base(rel))
		return ok
	case !strings.Contains(pattern, "/"): // bare basename glob
		ok, _ := path.Match(pattern, path.Base(rel))
		return ok
	default: // full-path glob
		ok, _ := path.Match(pattern, rel)
		return ok
	}
}

// generatedRe matches the conventional "Code generated … DO NOT EDIT."
// marker (Go's spec, also emitted by protoc/stringer/mockgen). It must
// precede the package clause, so only a bounded prefix is scanned. Both
// `//` (Go/JS/TS) and `#` (Python) comment leaders are accepted.
var generatedRe = regexp.MustCompile(`^\s*(?://|#|/\*)\s*Code generated .* DO NOT EDIT\.`)

// isGenerated reports whether src begins with a generated-code marker
// within its first lines.
func isGenerated(src []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(src))
	sc.Buffer(make([]byte, 0, 8192), 1<<20)

	for i := 0; i < 40 && sc.Scan(); i++ {
		if generatedRe.MatchString(sc.Text()) {
			return true
		}
	}

	return false
}
