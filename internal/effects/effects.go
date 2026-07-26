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
	"strings"

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

// CommandSink classifies a shell command for the gate.
type CommandSink struct {
	Name        string   `yaml:"name"`
	Subcommands []string `yaml:"subcommands"` // empty: any invocation matches
	Tag         string   `yaml:"tag"`
}

type catalogFile struct {
	Sinks    []Sink        `yaml:"sinks"`
	Commands []CommandSink `yaml:"commands"`
}

// Catalog is the merged sink list, indexed for matching.
type Catalog struct {
	// byImport: family -> import path \x00 name -> tags
	byImport map[string][]string
	// byMethod: family -> method name -> tags
	byMethod map[string][]string
	// byCall: family -> bare call name -> tags
	byCall map[string][]string
	// byCommand: command base name -> sink entries
	byCommand map[string][]CommandSink
}

func key2(family, a string) string    { return family + "\x00" + a }
func key3(family, a, b string) string { return family + "\x00" + a + "\x00" + b }

// Load returns the embedded catalogue merged with the workspace overlay at
// <root>/.seamark/effects.yaml (missing overlay is fine; a malformed one
// is an error — silently dropping the user's security config is worse).
func Load(root string) (*Catalog, error) {
	var merged catalogFile

	var base catalogFile
	if err := yaml.Unmarshal(defaultCatalog, &base); err != nil {
		return nil, fmt.Errorf("effects: embedded catalog: %w", err)
	}

	merged.Sinks = append(merged.Sinks, base.Sinks...)
	merged.Commands = append(merged.Commands, base.Commands...)

	overlayPath := filepath.Join(root, ".seamark", "effects.yaml")
	if data, err := os.ReadFile(overlayPath); err == nil {
		var overlay catalogFile
		if err := yaml.Unmarshal(data, &overlay); err != nil {
			return nil, fmt.Errorf("effects: %s: %w", overlayPath, err)
		}

		merged.Sinks = append(merged.Sinks, overlay.Sinks...)
		merged.Commands = append(merged.Commands, overlay.Commands...)
	}

	c := &Catalog{
		byImport:  map[string][]string{},
		byMethod:  map[string][]string{},
		byCall:    map[string][]string{},
		byCommand: map[string][]CommandSink{},
	}

	for _, cs := range merged.Commands {
		c.byCommand[cs.Name] = append(c.byCommand[cs.Name], cs)
	}

	for _, s := range merged.Sinks {
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

// MatchCommand returns tags for a shell command by its base name and argv
// tail. Subcommand candidates are non-flag tokens NOT preceded by a flag
// (a flag's separate value is skipped): `kubectl -n prod delete x` finds
// `delete`, while `terraform plan -out apply` does not find `apply`. Any
// candidate matching counts — for a security classifier a false positive
// beats a false negative.
func (c *Catalog) MatchCommand(name string, args []string) []string {
	candidates := map[string]bool{}

	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}

		if i > 0 && strings.HasPrefix(args[i-1], "-") && !strings.Contains(args[i-1], "=") {
			continue // likely the previous flag's value ("-n prod")
		}

		candidates[a] = true
	}

	var tags []string

	for _, cs := range c.byCommand[name] {
		if len(cs.Subcommands) == 0 {
			tags = append(tags, cs.Tag)
			continue
		}

		for _, want := range cs.Subcommands {
			if candidates[want] {
				tags = append(tags, cs.Tag)
			}
		}
	}

	return tags
}
