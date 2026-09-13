// Package skills is the catalogue of agent skills that ship inside the
// seamark binary. It reads the embedded skills/ tree, parses the SKILL.md
// frontmatter, and owns the marker that tells the installer which
// directories seamark may refresh. init, doctor, and status all go
// through this package, so the marker rule exists once.
package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark"
)

// Root is the embedded directory that holds one subdirectory per skill.
const Root = "skills"

// SkillFile is the file every skill directory carries. Its YAML
// frontmatter is the skill's metadata; the Markdown body is the skill.
const SkillFile = "SKILL.md"

// Ownership marker. A SKILL.md whose metadata carries MarkerKey set to
// MarkerValue belongs to seamark: init refreshes such a directory and
// never touches any other. This mirrors how hooks are recognized by
// their argument tail, with the marker read from frontmatter instead.
const (
	MarkerKey   = "seamark"
	MarkerValue = "managed"
)

// Frontmatter is the YAML header of a SKILL.md. The fields are the Agent
// Skills core set plus allowed-tools. Unknown fields are ignored, so a
// user's own skill still parses when init inspects it for the marker.
type Frontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  string            `yaml:"allowed-tools"`
}

// Names lists the embedded skills in sorted order. A skill is a
// directory under Root that contains SkillFile; other entries, such as
// the tree's README, are not skills.
func Names() ([]string, error) {
	entries, err := fs.ReadDir(seamark.SkillsFS, Root)
	if err != nil {
		return nil, err
	}

	var names []string

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		if _, err := fs.Stat(seamark.SkillsFS, path.Join(Root, e.Name(), SkillFile)); err == nil {
			names = append(names, e.Name())
		}
	}

	sort.Strings(names)

	return names, nil
}

// Files returns every file of one skill keyed by its path relative to
// the skill directory, for example "SKILL.md" and
// "references/interpreting-seamark.md". The map is unordered; a caller
// that writes the files sorts the keys for stable output.
func Files(name string) (map[string][]byte, error) {
	dir := path.Join(Root, name)

	if _, err := fs.Stat(seamark.SkillsFS, path.Join(dir, SkillFile)); err != nil {
		return nil, fmt.Errorf("skills: no skill named %q", name)
	}

	files := map[string][]byte{}

	err := fs.WalkDir(seamark.SkillsFS, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := fs.ReadFile(seamark.SkillsFS, p)
		if err != nil {
			return err
		}

		files[strings.TrimPrefix(p, dir+"/")] = data

		return nil
	})

	return files, err
}

// ParseFrontmatter splits a SKILL.md into its frontmatter and body. The
// file must open with a "---" line and close the header with another;
// the body is everything after the closing fence. YAML syntax errors are
// reported rather than loaded as an empty header, so a malformed shipped
// skill fails the tests instead of failing silently in a client.
func ParseFrontmatter(skillMD []byte) (Frontmatter, string, error) {
	var fm Frontmatter

	header, body, err := splitFrontmatter(skillMD)
	if err != nil {
		return fm, "", err
	}

	if err := yaml.Unmarshal(header, &fm); err != nil {
		return fm, "", fmt.Errorf("skills: frontmatter: %w", err)
	}

	return fm, string(body), nil
}

// IsManaged reports whether the frontmatter carries the ownership marker.
func IsManaged(fm Frontmatter) bool {
	return fm.Metadata[MarkerKey] == MarkerValue
}

// fence is the line that opens and closes the frontmatter.
var fence = []byte("---")

// splitFrontmatter returns the YAML between the two fences and the body
// after the closing one. It scans line by line: the closing fence must
// stand alone on its line, so a "---" inside a YAML value never ends the
// header early.
func splitFrontmatter(skillMD []byte) (header, body []byte, err error) {
	first, rest, found := bytes.Cut(skillMD, []byte("\n"))
	if !found || !bytes.Equal(bytes.TrimRight(first, "\r"), fence) {
		return nil, nil, errors.New("skills: SKILL.md does not open with a --- line")
	}

	offset := 0

	for offset <= len(rest) {
		line, tail, more := bytes.Cut(rest[offset:], []byte("\n"))

		if bytes.Equal(bytes.TrimRight(line, "\r"), fence) {
			return rest[:offset], tail, nil
		}

		if !more {
			break
		}

		offset += len(line) + 1
	}

	return nil, nil, errors.New("skills: SKILL.md frontmatter has no closing --- line")
}
