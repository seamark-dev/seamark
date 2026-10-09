package tui

import (
	"errors"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// This file holds previewFlow, the shared stages of a flow that reads a
// preview, commits it after y, and shows the result. Four flows use it:
// the decisions of Lessons › Proposals, the retarget and prune of Lessons
// › Pins, the save of Settings, and the evidence refresh preview. The
// read writes nothing. Only y commits, under the mutation slot. The
// evidence preview never commits, because its y starts a run record.
//
// A flow is a holder struct that embeds previewFlow[P]. previewFlow keeps
// the stage, the shown preview or read failure, and the scroll position
// of the modal body. The holder supplies every other part:
//
//   - the request: the fields that the read and the commit need, for
//     example the proposal IDs or the settings group;
//   - the preview type P: the plan or the preview that the read returns;
//   - the revalidation: the holder closes an open preview when a newer
//     state makes it stale, for example a settings save (beforeCommit);
//   - the commit work: a plain command, or registered work (st.goWork)
//     when an exit must wait for it, such as a pin file write before its
//     ledger update;
//   - the result text: the outcome fields and every word of the modals;
//   - the keys: the key switch of each stage and the footer labels.
//
// So a save that writes one file, such as Settings, and a commit that
// writes a file and the ledger, such as Pins, use the same stages.
//
// previewFlow keeps no generation. The owner of the flow keeps it,
// because a pointer holder ends with its flow and the next read needs a
// newer ticket. The owner passes its ticket check to readDone. The run
// records of Distill and Extract keep their own read guard on purpose:
// their read follows a scope form and leads to a run that can stop. The
// first-run guide and the setup wizard also keep their own stage types.
// The guide reads its plan without a ticket, and a ticket would change
// what it does. The wizard has two steps before its preview. All these
// stages use the same words: Preparing, Preview, Committing or Running,
// and Result.
//
// The holder must not declare a field or a method with the name of a
// previewFlow field or method. Go embedding has no virtual dispatch, so
// a previewFlow method never reads the field or calls the method of the
// holder. TestPreviewFlowHoldersShadowNothing checks this rule.

// flowStage is the stage of a previewFlow. The words are the words of
// every preview in the console: Preparing reads the preview, Preview
// shows it, Committing writes it, and Result shows the outcome.
type flowStage int

const (
	// flowClosed shows nothing. It is the zero value, so a new flow is
	// closed. Proposals and Evidence close their pointer holder with nil
	// and never reach it. The Pins modal sets it, so that maintHost
	// drops the modal.
	flowClosed flowStage = iota
	// flowPreparing waits for the preview read. The read writes nothing.
	flowPreparing
	// flowPreview shows the preview or its read failure and waits for
	// the user.
	flowPreview
	// flowCommitting waits for the commit. A commit cannot stop safely,
	// so the keys wait for its result.
	flowCommitting
	// flowResult shows the outcome of the commit.
	flowResult
)

// previewFlow is the stage and the shown preview of one flow. P is the
// type that the preview read returns, for example settings.Plan or
// proposals.Preview.
type previewFlow[P any] struct {
	stage flowStage
	// preview is what the read returned. The Preview stage shows it. err
	// is why the read failed, and the Preview stage then shows err.
	preview *P
	err     error
	// scroll is the position of the modal body. Each new stage starts at
	// the top, except Committing (see startCommit).
	scroll scrollState
}

// active reports an open flow: any stage except flowClosed.
func (f *previewFlow[P]) active() bool {
	return f.stage != flowClosed
}

// startRead moves to flowPreparing and forgets the shown preview. The
// owner starts a ticket of its generation next to it, and the read
// carries that ticket.
func (f *previewFlow[P]) startRead() {
	f.stage = flowPreparing
	f.preview, f.err = nil, nil
	f.scroll = scrollState{}
}

// readDone shows the result of a preview read and reports whether it
// did. current is the ticket check of the owner. A result with an old
// ticket, or a result outside flowPreparing, changes nothing, because the
// user left the read. A read without a preview and without an error
// shows emptyErr as its error. So the Preview stage always shows a text.
// Each flow keeps its own text.
func (f *previewFlow[P]) readDone(current bool, preview *P, err error, emptyErr string) bool {
	if !current || f.stage != flowPreparing {
		return false
	}

	f.stage = flowPreview
	f.preview, f.err = preview, err
	f.scroll = scrollState{}

	if preview == nil && err == nil {
		f.err = errors.New(emptyErr)
	}

	return true
}

// showsPreview reports the Preview stage with a preview, not a read
// failure. Each flow adds its own conditions for y.
func (f *previewFlow[P]) showsPreview() bool {
	return f.stage == flowPreview && f.err == nil && f.preview != nil
}

// beforeCommit reports a flow that reads a preview or shows one. A newer
// state, for example a settings save, can make that preview stale, so
// the holder can close it. A commit and a result stay, because they no
// longer depend on the preview.
func (f *previewFlow[P]) beforeCommit() bool {
	return f.stage == flowPreparing || f.stage == flowPreview
}

// startCommit claims the mutation slot under name and moves to
// flowCommitting. It returns work, or false and no command while another
// mutation runs. The holder then words the refusal. The scroll position
// stays, because the holders keep their own rule: only Pins shows the
// wait from the top.
func (f *previewFlow[P]) startCommit(st *state, name string, work tea.Cmd) (tea.Cmd, bool) {
	if !st.begin(name) {
		return nil, false
	}

	f.stage = flowCommitting

	return work, true
}

// commitDone moves a committing flow to flowResult and reports whether
// it did. A holder that checks the owner of a commit result checks it
// first, and then sets its outcome fields. The holder frees the
// mutation slot in every case, also when commitDone returns false.
func (f *previewFlow[P]) commitDone() bool {
	if f.stage != flowCommitting {
		return false
	}

	f.toResult()

	return true
}

// toResult shows the Result stage. The holder keeps the outcome, so the
// flow forgets the preview. Settings calls it without the stage check of
// commitDone, because no key can leave a running save.
func (f *previewFlow[P]) toResult() {
	f.stage = flowResult
	f.preview, f.err = nil, nil
	f.scroll = scrollState{}
}

// close ends the flow without a change. The owner also cancels its
// generation when a read can still arrive.
func (f *previewFlow[P]) close() {
	f.stage = flowClosed
	f.preview, f.err = nil, nil
	f.scroll = scrollState{}
}

// flowWaitModal is the modal of a stage that waits: a preview read or a
// commit. title names the wait without the ellipsis, and note says what
// Seamark reads or writes. A read offers esc, so cancel adds it. A
// commit offers no key, because it cannot stop safely.
func flowWaitModal(st *state, title, note string, cancel bool, w int) modalOpts {
	t := st.theme

	o := modalOpts{
		Title: t.Accent.Render(t.spinnerFrame(st.tick)) + " " + t.Title.Render(title+t.Glyphs.Ellipsis),
		Body:  propLines(t.Muted, note, w),
	}

	if cancel {
		o.Actions = []key.Binding{withHelp(nkeys.Back, "esc", "cancel")}
	}

	return o
}

// previewFailedModal is the modal of a preview read that failed. what
// names the preview, for example "The apply preview". hint is the next
// step, or "" when the cause is the only advice. A read writes nothing.
// unchanged is the last line. It says in the words of the flow that
// nothing was written, for example "Nothing was written."
func previewFailedModal(t *Theme, what string, err error, hint, unchanged string, actions []key.Binding, w int) modalOpts {
	body := propNote(t, ToneFailure, what+" could not be built: "+errText(err), w)

	if hint != "" {
		body = append(body, "")
		body = append(body, propLines(t.Body, hint, w)...)
	}

	body = append(body, "")
	body = append(body, propLines(t.Muted, unchanged, w)...)

	return modalOpts{Title: t.Failure.Render("Preview failed"), Body: body, Actions: actions}
}
