package yamledit

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// hostileValues hold every character class that changes how YAML reads
// a scalar: flow indicators, comment and mapping indicators, quotes,
// node properties, block indicators, YAML 1.1 booleans, numbers, nulls,
// white space at the ends, escapes, line breaks, and multi-byte text.
var hostileValues = []string{
	"api", "api/", "./api", "internal/store/sql", "a,b", "a, b", "a]b", "[a", "a[b]", "{x}", "a}",
	"a #b", "#x", "a#b", "a: b", "a:b", "a:", ":a", "it's", `"q"`, `a"b`, "'", "*", "*/x", "&x", "!t", "!!str",
	"|", ">", "-", "- x", "-x", "?", "? x", "%", "@", "`", ",", "yes", "no", "on", "off", "y", "null", "Null",
	"~", "123", "1e3", "0x1F", "0o17", ".5", "true", "false", "", " ", " a ", "a ", " a", "a\tb", "\t",
	"é/ü", "日本/語", "a\nb", "\n", "x\x00y", "a\\b", "a\u2028b", "a\u2029b", "a\u0085b", "\ufeffa", "---", "...",
	"--- a", "... a", strings.Repeat("long segment ", 30),
}

func TestBlockScalarReadsBackAsTheSameString(t *testing.T) {
	for _, s := range hostileValues {
		text, err := BlockScalar(s)
		require.NoError(t, err, "value %q", s)
		assert.NotContains(t, text, "\n", "value %q", s)

		var got map[string]any
		require.NoError(t, yaml.Unmarshal([]byte("region: "+text+"  # comment\n"), &got), "value %q as %q", s, text)
		assert.Equal(t, s, got["region"], "value %q as %q", s, text)

		require.NoError(t, yaml.Unmarshal([]byte("regions:\n  - "+text+"\n"), &got), "value %q as %q", s, text)
		assert.Equal(t, []any{s}, got["regions"], "value %q as %q in a block list", s, text)

		_, issue := Parse([]byte("region: " + text + "\n"))
		assert.Nil(t, issue, "the result passes the structural gate: value %q as %q", s, text)
	}
}

func TestFlowScalarReadsBackInsideAFlowList(t *testing.T) {
	for _, s := range hostileValues {
		text, err := FlowScalar(s)
		require.NoError(t, err, "value %q", s)

		var got map[string]any
		require.NoError(t, yaml.Unmarshal([]byte("regions: [first, "+text+", last]   # c\n"), &got), "value %q as %q", s, text)
		assert.Equal(t, []any{"first", s, "last"}, got["regions"], "value %q as %q", s, text)
	}
}

func TestFlowListReadsBackAsTheSameStrings(t *testing.T) {
	text, err := FlowList(hostileValues)
	require.NoError(t, err)
	assert.NotContains(t, text, "\n")

	var got struct {
		Regions []string `yaml:"regions"`
	}

	require.NoError(t, yaml.Unmarshal([]byte("regions: "+text+"   # c\n"), &got))
	assert.Equal(t, hostileValues, got.Regions)

	doc := parsed(t, "pin:\n  - rule: a\n    regions: "+text+"   # c\n")
	_, seq, idx := doc.Section("pin")

	s, ok := doc.KeySpan(seq.Content[0], "regions", doc.SectionEnd(idx)+1)
	require.True(t, ok)
	assert.True(t, s.Inline, "the flow end scan finds the end of the encoded list")
	assert.Equal(t, text, doc.Lines[s.First-1][s.Start:s.End])
}

func TestEncodersQuoteOnlyWhatNeedsIt(t *testing.T) {
	cases := []struct {
		value, block, flow string
	}{
		{"api", "api", "api"},
		{"internal/store", "internal/store", "internal/store"},
		{"*", "'*'", "'*'"},
		{"a,b", "a,b", "'a,b'"},
		{"a]b", "a]b", "'a]b'"},
		{"a #b", "'a #b'", "'a #b'"},
		{"a: b", "'a: b'", "'a: b'"},
		{"on", `"on"`, `"on"`},
		{"123", `"123"`, `"123"`},
		{"", `""`, `""`},
		{"it's", "it's", "it's"},
		{"a\nb", `"a\nb"`, `"a\nb"`},
		{"\n", `"\n"`, `"\n"`},
		{"a\u2029b", `"a\Pb"`, `"a\Pb"`},
		{"a\u2028b", `"a\Lb"`, `"a\Lb"`},
	}

	for _, c := range cases {
		block, err := BlockScalar(c.value)
		require.NoError(t, err)
		assert.Equal(t, c.block, block, "value %q", c.value)

		flow, err := FlowScalar(c.value)
		require.NoError(t, err)
		assert.Equal(t, c.flow, flow, "value %q", c.value)
	}

	list, err := FlowList([]string{"api", "a,b", "*"})
	require.NoError(t, err)
	assert.Equal(t, "[api, 'a,b', '*']", list, "the layout that RenderPins writes")

	for _, empty := range [][]string{nil, {}} {
		list, err = FlowList(empty)
		require.NoError(t, err)
		assert.Equal(t, "[]", list)
	}
}

func TestEncodersRefuseTextThatIsNotUTF8(t *testing.T) {
	_, err := BlockScalar("a\xffb")
	require.ErrorIs(t, err, ErrEncode)

	_, err = FlowScalar("a\xffb")
	require.ErrorIs(t, err, ErrEncode)

	_, err = FlowList([]string{"api", "\xfe"})
	require.ErrorIs(t, err, ErrEncode)
}

func TestReadBackRefusesTextOnSeveralLines(t *testing.T) {
	_, err := readBack("k: a\n  b")
	require.Error(t, err)

	_, err = readBack("k: a\rb")
	require.Error(t, err)

	_, err = readBack("k: a\nj: b")
	require.Error(t, err)

	_, err = readBack("k: [a")
	require.Error(t, err)

	n, err := readBack("k: 'x'")
	require.NoError(t, err)
	assert.True(t, sameString(n, "x"))
}
