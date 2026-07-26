// Package effects holds the sink catalogue: which calls can ultimately
// write a database, spawn a process, touch the filesystem, or leave the
// network (RFC-001 §5.2). The catalogue is data, not code — the default
// ships embedded, and workspaces extend it with .seamark/effects.yaml.
//
// Detection is deliberately syntactic and matches call REFERENCES, not
// resolved edges: the interesting sinks (database/sql.Exec,
// subprocess.run) live in external dependencies the indexer never
// resolves. Direct tags land on the enclosing declaration; the indexer
// propagates them backwards along CALLS edges to fixpoint.
package effects

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

//go:embed catalog.yaml
var defaultCatalog []byte

// Sink is one catalogue entry; exactly one matcher field is set.
type Sink struct {
	Language string   `yaml:"language"` // family key: go | ecma | python
	Import   string   `yaml:"import"`   // qualified through this import path
	Names    []string `yaml:"names"`    // names for the import matcher
	Method   string   `yaml:"method"`   // any attribute/method call
	Call     string   `yaml:"call"`     // bare call
	Tag      string   `yaml:"tag"`
}

type catalogFile struct {
	Sinks []Sink `yaml:"sinks"`
}

// Catalog is the merged sink list, indexed for matching.
type Catalog struct {
	// byImport: family -> import path \x00 name -> tags
	byImport map[string][]string
	// byMethod: family -> method name -> tags
	byMethod map[string][]string
	// byCall: family -> bare call name -> tags
	byCall map[string][]string
}

func key2(family, a string) string    { return family + "\x00" + a }
func key3(family, a, b string) string { return family + "\x00" + a + "\x00" + b }

// Load returns the embedded catalogue merged with the workspace overlay at
// <root>/.seamark/effects.yaml (missing overlay is fine; a malformed one
// is an error — silently dropping the user's security config is worse).
func Load(root string) (*Catalog, error) {
	var all []Sink

	var base catalogFile
	if err := yaml.Unmarshal(defaultCatalog, &base); err != nil {
		return nil, fmt.Errorf("effects: embedded catalog: %w", err)
	}

	all = append(all, base.Sinks...)

	overlayPath := filepath.Join(root, ".seamark", "effects.yaml")
	if data, err := os.ReadFile(overlayPath); err == nil {
		var overlay catalogFile
		if err := yaml.Unmarshal(data, &overlay); err != nil {
			return nil, fmt.Errorf("effects: %s: %w", overlayPath, err)
		}

		all = append(all, overlay.Sinks...)
	}

	c := &Catalog{
		byImport: map[string][]string{},
		byMethod: map[string][]string{},
		byCall:   map[string][]string{},
	}

	for _, s := range all {
		switch {
		case s.Import != "":
			for _, n := range s.Names {
				k := key3(s.Language, s.Import, n)
				c.byImport[k] = append(c.byImport[k], s.Tag)
			}
		case s.Method != "":
			k := key2(s.Language, s.Method)
			c.byMethod[k] = append(c.byMethod[k], s.Tag)
		case s.Call != "":
			k := key2(s.Language, s.Call)
			c.byCall[k] = append(c.byCall[k], s.Tag)
		}
	}

	return c, nil
}

// MatchImport returns tags for a call to name qualified through (or
// imported by name from) importPath.
func (c *Catalog) MatchImport(family, importPath, name string) []string {
	return c.byImport[key3(family, importPath, name)]
}

// MatchMethod returns tags for an attribute/method call by name.
func (c *Catalog) MatchMethod(family, name string) []string {
	return c.byMethod[key2(family, name)]
}

// MatchCall returns tags for a bare call by name.
func (c *Catalog) MatchCall(family, name string) []string {
	return c.byCall[key2(family, name)]
}
