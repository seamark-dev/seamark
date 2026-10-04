package yamledit

// Encoders for new value text. Each encoder asks yaml.v3 for the text,
// because concatenation misses the quoting rules: "*" reads as an alias,
// "on" reads as a boolean in YAML 1.1 readers, and "a, b" reads as two
// items inside a flow list. Each encoder then parses its own text again
// and refuses a result that does not read back as the same strings on
// one line.

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrEncode reports a value that has no single-line YAML text that reads
// back as the same string, for example text that is not UTF-8.
var ErrEncode = errors.New("value has no safe single-line YAML text")

// flowList is the shape that makes yaml.v3 write one flow sequence. The
// emitter then quotes each item for flow context, as RenderPins does for
// the regions of a pin.
type flowList struct {
	Items []string `yaml:"items,flow"`
}

// flowPrefix is the key text that yaml.v3 writes before the flow list.
const flowPrefix = "items: "

// BlockScalar returns s as one scalar token for block context, such as
// the text after "region: " or after "- " in a block list. The text is
// the choice of yaml.v3 when it reads back on one line. Else it is one
// double-quoted token with escapes: yaml.v3 writes a line break as a
// block scalar, and U+2028 or U+2029 as a raw line break in quotes.
func BlockScalar(s string) (string, error) {
	for _, v := range []any{s, doubleQuoted(s)} {
		out, err := yaml.Marshal(v)
		if err != nil {
			continue
		}

		text := strings.TrimSuffix(string(out), "\n")

		if got, err := readBack("k: " + text); err == nil && sameString(got, s) {
			return text, nil
		}
	}

	return "", fmt.Errorf("%w: %q", ErrEncode, s)
}

// FlowScalar returns s as the text of one item inside a flow list, such
// as one item of "regions: [api, db]". A plain item ends at ",", "]",
// and the other flow indicators, so an item that holds them is quoted.
// The fallback to a double-quoted token follows the rule of BlockScalar.
func FlowScalar(s string) (string, error) {
	for _, attempt := range []func() ([]byte, error){
		func() ([]byte, error) {
			out, err := yaml.Marshal(flowList{Items: []string{s}})
			text, _ := strings.CutPrefix(string(out), flowPrefix)

			return []byte(text), err
		},
		func() ([]byte, error) {
			return yaml.Marshal(&yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle, Content: []*yaml.Node{doubleQuoted(s)}})
		},
	} {
		out, err := attempt()
		if err != nil {
			continue
		}

		list := strings.TrimSuffix(string(out), "\n")
		if !strings.HasPrefix(list, "[") || !strings.HasSuffix(list, "]") {
			continue
		}

		if got, err := readBack("k: " + list); err == nil && sameStrings(got, []string{s}) {
			return list[1 : len(list)-1], nil
		}
	}

	return "", fmt.Errorf("%w: %q", ErrEncode, s)
}

// FlowList returns items as one flow list on one line, for example
// "[api, 'a,b']". The items are separated by ", ", as yaml.v3 writes
// them. An empty list gives "[]".
func FlowList(items []string) (string, error) {
	texts := make([]string, 0, len(items))

	for _, item := range items {
		text, err := FlowScalar(item)
		if err != nil {
			return "", err
		}

		texts = append(texts, text)
	}

	list := "[" + strings.Join(texts, ", ") + "]"

	if got, err := readBack("k: " + list); err != nil || !sameStrings(got, items) {
		return "", fmt.Errorf("%w: %q", ErrEncode, items)
	}

	return list, nil
}

// doubleQuoted returns a string node that yaml.v3 writes in double
// quotes. In double quotes yaml.v3 escapes every line break.
func doubleQuoted(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: s}
}

// readBack parses text as a one-key document and returns the value node.
// The text must stay on one line: a line break would move content to a
// line that the splice does not own. The text gate of Parse applies too,
// so a result never blocks the next edit of the file.
func readBack(text string) (*yaml.Node, error) {
	if strings.Contains(text, "\n") || textIssue([]byte(text)) != nil {
		return nil, errors.New("the text holds a line break or is not UTF-8 text")
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, err
	}

	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || len(doc.Content[0].Content) != 2 {
		return nil, errors.New("the text is not one value")
	}

	return doc.Content[0].Content[1], nil
}

// sameString reports whether a node is the string s. The tag check
// refuses text that reads as another type, such as !!binary for text
// that is not UTF-8.
func sameString(n *yaml.Node, s string) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!str" && n.Value == s
}

// sameStrings reports whether a node is a flow list of exactly the
// strings items, in order.
func sameStrings(n *yaml.Node, items []string) bool {
	if n.Kind != yaml.SequenceNode || n.Style&yaml.FlowStyle == 0 || len(n.Content) != len(items) {
		return false
	}

	for i, item := range items {
		if !sameString(n.Content[i], item) {
			return false
		}
	}

	return true
}
