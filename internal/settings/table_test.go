package settings

import (
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
)

// The field table tests. The table is allFields, Field.Group,
// Field.Reader, ownedSections, and the encoders of patch.go. It restates
// the yaml tags of the reader structs by hand. These tests compare the
// table with those tags, so a drift fails here and not in a user's file.

// readerParsers maps each reader name to the bytes entry point of that
// reader. The structs that they return carry the yaml tags that the
// table must match. A test below proves that read pairs the same names
// with the same functions.
var readerParsers = map[string]func([]byte) (any, error){
	ReaderAgent:   func(b []byte) (any, error) { return agent.ParseConfig(b) },
	ReaderDistill: func(b []byte) (any, error) { return distill.ParseConfig(b) },
	ReaderIndex:   func(b []byte) (any, error) { return index.ParseConfig(b) },
}

// readerKey locates one "section.key" path in the struct of a reader.
type readerKey struct {
	reader string
	// index is the field path for reflect.Value.FieldByIndex.
	index []int
	// typ is the field type without a pointer: *int is int.
	typ reflect.Type
}

// yamlName returns the key that yaml.v3 decodes into a struct field, or
// "" when yaml.v3 skips the field. yaml.v3 uses the tag name, else the
// field name in lower case. An inline field fails the test, because the
// table has no path for the keys that it hoists.
func yamlName(t *testing.T, f reflect.StructField) string {
	t.Helper()

	tag := f.Tag.Get("yaml")
	if !f.IsExported() || tag == "-" {
		return ""
	}

	name, opts, _ := strings.Cut(tag, ",")
	require.NotContains(t, strings.Split(opts, ","), "inline", "the walk does not follow the inline field %s", f.Name)

	if name == "" {
		return strings.ToLower(f.Name)
	}

	return name
}

// readerKeys walks the struct that each reader returns. It maps every
// "section.key" path to its reader and its struct field, and every
// section to its reader. The table edits section.key paths only, so a
// section must be a struct.
func readerKeys(t *testing.T) (keys map[string]readerKey, sections map[string]string) {
	t.Helper()

	keys = map[string]readerKey{}
	sections = map[string]string{}

	for reader, parse := range readerParsers {
		cfg, err := parse(nil)
		require.NoError(t, err, "the %s reader rejects an empty file", reader)

		top := reflect.TypeOf(cfg).Elem()

		for i := range top.NumField() {
			sf := top.Field(i)

			section := yamlName(t, sf)
			if section == "" {
				continue
			}

			// Field.Reader maps a section to one reader, so two readers of
			// one section would make the owner of a field ambiguous.
			require.NotContains(t, sections, section, "two readers decode the %s section", section)
			require.Equal(t, reflect.Struct, sf.Type.Kind(), "the %s section is not a mapping; the table has section.key paths only", section)

			sections[section] = reader

			for j := range sf.Type.NumField() {
				kf := sf.Type.Field(j)

				key := yamlName(t, kf)
				if key == "" {
					continue
				}

				typ := kf.Type
				if typ.Kind() == reflect.Pointer {
					typ = typ.Elem()
				}

				keys[section+"."+key] = readerKey{reader: reader, index: []int{i, j}, typ: typ}
			}
		}
	}

	return keys, sections
}

// TestFieldTableMatchesTheReaderStructs ties the field table to the yaml
// tags of the three reader structs, in both directions. Every field names
// a key that its reader decodes. Every key that a reader decodes is a
// field, because Settings shows the stored, default, and effective value
// of each such key. For this reason agent.argv is a field although it
// is read-only: it overrides agent.cli. Phase 4 adds lessons.yaml fields
// through this table, so a drift between the table and a reader must
// fail a test. A key that a later table leaves out on purpose needs an
// explicit exception here, with its reason.
func TestFieldTableMatchesTheReaderStructs(t *testing.T) {
	keys, sections := readerKeys(t)

	t.Run("every field names a key of its reader", func(t *testing.T) {
		for _, f := range allFields {
			k, ok := keys[string(f)]
			if !assert.True(t, ok, "%s is not a yaml key of a reader struct", f) {
				continue
			}

			assert.Equal(t, k.reader, f.Reader(), "%s: Field.Reader names another reader", f)
		}
	})

	t.Run("every key of a reader is a field", func(t *testing.T) {
		var fields []string
		for _, f := range allFields {
			fields = append(fields, string(f))
		}

		assert.ElementsMatch(t, slices.Collect(maps.Keys(keys)), fields)
	})

	// An owned section gets the structural analysis. A decoded section
	// outside the list could hide an alias or a duplicate key from it.
	t.Run("the owned sections are the sections of the readers", func(t *testing.T) {
		assert.ElementsMatch(t, slices.Collect(maps.Keys(sections)), ownedSections)
	})

	// verify compares every section except group.section(), and Editable
	// asks the reader of the first field only. A group must therefore
	// edit one owned section with one reader.
	t.Run("every group edits one owned section", func(t *testing.T) {
		for g := Group(0); g.String() != "unknown"; g++ {
			fields := g.Fields()
			require.NotEmpty(t, fields, "the %s group has no field", g)

			assert.Contains(t, ownedSections, g.section(), "the %s group", g)

			for _, f := range fields {
				assert.Equal(t, g.section(), f.section(), "%s: one plan edits one section", f)
			}
		}

		for _, f := range allFields {
			assert.NotEqual(t, "unknown", f.Group().String(), "%s has no named group", f)
		}
	})

	// A value of the wrong type in one section fails only the reader of
	// that section. This proves that read pairs each reader name with
	// the same parse function as readerParsers.
	t.Run("read runs the same readers", func(t *testing.T) {
		for section, reader := range sections {
			r := read(t.TempDir(), integration.Builtin(), []byte(section+": 5\n"))

			assert.Equal(t, []string{reader}, slices.Collect(maps.Keys(r.errs())), "a wrong %s section", section)
		}

		failed := errors.New("failed")
		names := reading{agentErr: failed, distillErr: failed, indexErr: failed}.errs()

		assert.ElementsMatch(t, slices.Collect(maps.Keys(names)), slices.Collect(maps.Keys(readerParsers)))
	})
}

// fullConfig sets every field of the table, so a reset of each field has
// a key to remove and the view has a stored value to show.
const fullConfig = `agent:
  cli: claude
  argv: ["my-llm", "--print"]
distill:
  write: true
reviews:
  window_days: 90
index:
  generated: true
  exclude:
    - vendor/
`

// settable holds one valid value for every field that the form can set.
// Each value differs from the value in richConfig, so each save changes
// the file.
var settable = map[Field]Edit{
	AgentCLI:       {Field: AgentCLI, String: "codex"},
	DistillWrite:   {Field: DistillWrite, Bool: true},
	ReviewWindow:   {Field: ReviewWindow, Days: 30},
	IndexGenerated: {Field: IndexGenerated, Bool: true},
	IndexExclude:   {Field: IndexExclude, List: []string{"*.pb.go"}},
}

// readOnly lists the fields whose only edit is their removal.
var readOnly = []Field{AgentArgv}

// carried returns the value that an edit carries for a reader field of
// type typ. Edit has one value field per kind of setting, so the kind of
// the reader field selects it.
func carried(t *testing.T, e Edit, typ reflect.Type) any {
	t.Helper()

	switch typ.Kind() {
	case reflect.String:
		return e.String
	case reflect.Bool:
		return e.Bool
	case reflect.Int:
		return e.Days
	case reflect.Slice:
		if typ.Elem().Kind() == reflect.String {
			return e.List
		}
	}

	require.FailNow(t, "no Edit value carries this type", "%s", typ)

	return nil
}

// decoded returns the value that the owning reader decodes at the path
// of k from text.
func decoded(t *testing.T, k readerKey, text string) reflect.Value {
	t.Helper()

	cfg, err := readerParsers[k.reader]([]byte(text))
	require.NoError(t, err)

	return reflect.ValueOf(cfg).Elem().FieldByIndex(k.index)
}

// TestEveryFieldHasAnEncoderAndAReader proves that each field of the
// table has a working write path and a working view. A set goes through
// validate, the splice encoder, and verify, and the owning reader then
// decodes the value of the edit. A reset removes the key. The view finds
// every stored key and fills its default and effective text.
func TestEveryFieldHasAnEncoderAndAReader(t *testing.T) {
	keys, _ := readerKeys(t)

	t.Run("every field is settable or read-only", func(t *testing.T) {
		listed := append(slices.Collect(maps.Keys(settable)), readOnly...)

		assert.ElementsMatch(t, allFields, listed)
	})

	for _, f := range allFields {
		k, ok := keys[string(f)]
		require.True(t, ok, "%s is not a yaml key of a reader struct", f)

		t.Run("set "+string(f), func(t *testing.T) {
			e, ok := settable[f]
			if !ok {
				_, err := PlanPatch(inspect(t, withConfig(t, richConfig)), integration.Builtin(), []Edit{{Field: f}})
				require.ErrorIs(t, err, ErrInvalidEdit, "a read-only field refuses a value")

				return
			}

			root := withConfig(t, richConfig)
			save(t, root, e)

			v := decoded(t, k, readConfig(t, root))
			if v.Kind() == reflect.Pointer {
				require.False(t, v.IsNil(), "the reader decodes no value")

				v = v.Elem()
			}

			assert.Equal(t, carried(t, e, k.typ), v.Interface())
		})

		t.Run("reset "+string(f), func(t *testing.T) {
			root := withConfig(t, fullConfig)
			save(t, root, Edit{Field: f, Reset: true})

			text := readConfig(t, root)

			key, _ := parsed(t, text).Child(f.section(), f.key())
			assert.Nil(t, key, "the key is gone")
			assert.True(t, decoded(t, k, text).IsZero(), "the reader decodes its default")
		})
	}

	t.Run("the view reads every field", func(t *testing.T) {
		snap := inspect(t, withConfig(t, fullConfig))
		require.Len(t, snap.Fields, len(allFields))

		for _, st := range snap.Fields {
			assert.NoError(t, st.Err, "%s", st.Field)
			assert.True(t, st.Stored.Present, "%s: the view finds no stored key", st.Field)
			assert.NotEmpty(t, st.Default, "%s has no default text", st.Field)
			assert.NotEmpty(t, st.Effective, "%s has no effective text", st.Field)
		}
	})
}
