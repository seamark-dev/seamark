package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
)

// ovDeletedFake is the populated workspace with n tracked paths that
// are deleted in the working tree, the deletions not staged.
func ovDeletedFake(n int) *fakeServices {
	f := ovPopulated()
	f.index.Status.Coverage.FilesDeleted = n

	return f
}

func TestOverviewIndexCardShowsDeletedPathsMuted(t *testing.T) {
	f := ovDeletedFake(47)
	h := newHarness(t, f, 120, 40)

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "47 deleted, not staged", "the INDEX card names the count")
	assert.Contains(t, frame, "no parse errors", "a deleted path is not a parse error")

	// The line keeps the muted style of "no parse errors", never the
	// attention style of a parse error. Colors make the two differ.
	distWithColors(t)

	o := ovScreen(h)
	th := o.st.theme
	require.NotEqual(t, distColorOf(th.Muted), distColorOf(th.Attention))

	_, lines := o.readyIndexCard(f.index, 25)
	assert.Contains(t, lines, th.Muted.Render("47 deleted, not staged"))
	assert.NotContains(t, lines, th.Attention.Render("47 deleted, not staged"))

	// A narrow card keeps the count and drops the qualifier.
	_, narrow := o.readyIndexCard(f.index, 15)
	assert.Contains(t, narrow, th.Muted.Render("47 deleted"))
}

func TestOverviewIndexCardHasNoDeletedLineWithoutDeletions(t *testing.T) {
	f := ovPopulated()
	o := ovScreen(newHarness(t, f, 120, 40))

	_, lines := o.readyIndexCard(f.index, 25)
	for _, l := range lines {
		assert.NotContains(t, ansi.Strip(l), "deleted")
	}

	for _, it := range o.attention() {
		assert.NotEqual(t, "index:deleted", it.key, "no item without a deleted path")
	}
}

func TestOverviewDeletedPathsAreOptionalNotAction(t *testing.T) {
	o := ovScreen(newHarness(t, ovDeletedFake(47), 120, 40))

	it := ovFind(t, o, "index:deleted")
	assert.Equal(t, ovOptional, it.group, "a deletion is the user's choice, never an action")
	assert.Equal(t, ToneNeutral, it.tone)
	assert.Equal(t, "not staged", it.badge)
	assert.Equal(t, "47 tracked files deleted in the working tree", it.title)
	assert.Equal(t, "git diff-files --name-only --diff-filter=D", it.fix, "the detail shows how to list the paths")
	assert.Contains(t, strings.Join(it.body, " "), "The command below lists the paths.",
		"the body says that the command only lists the paths")
	assert.Equal(t, ovActNone, it.act.kind, "a build does not change the working tree")
	assert.Contains(t, strings.Join(it.body, " "), "they are not parse errors")

	for _, other := range o.attention() {
		assert.NotEqual(t, "index:parse", other.key, "a deleted path adds no parse-error item")
	}

	one := ovDeletedItem(1)
	assert.Equal(t, "1 tracked file deleted in the working tree", one.title)
	assert.Contains(t, strings.Join(one.body, " "), "The deletion is not staged.")
	assert.Contains(t, strings.Join(one.body, " "), "it is not a parse error")
}

func TestOverviewSummaryItemNamesDeletedPaths(t *testing.T) {
	w, hgt := 80, 24
	f := ovDeletedFake(47)
	h := newHarness(t, f, w, hgt)

	ovOpenSummary(t, h)

	flat := ovFlat(ovReadDetail(t, h, w, hgt))
	assert.Contains(t, flat, "Parse errors none")
	assert.Contains(t, flat, "Deleted 47 tracked files, not staged")
}

func TestOverviewBuildResultNamesDeletedPaths(t *testing.T) {
	o := ovScreen(newHarness(t, ovPopulated(), 120, 40))

	o.build = &ovBuild{summary: &index.Summary{
		DBPath: "/work/repo/.seamark/index.db", FilesSeen: 632, FilesParsed: 580, FilesDeleted: 47,
	}}

	body := ansi.Strip(strings.Join(o.resultBody(100), "\n"))
	require.Contains(t, body, "Index built")
	assert.Contains(t, body, "47 tracked files, not staged")
	assert.Contains(t, body, "632 seen")

	o.build.summary.FilesDeleted = 0
	assert.NotContains(t, ansi.Strip(strings.Join(o.resultBody(100), "\n")), "not staged",
		"the row exists only with a count")
}
