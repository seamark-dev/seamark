// Package tui is the experimental `seamark tui` workspace console. It is
// a presentation layer over the shared engines: it reads doctor, status,
// integration, proposal and delivery state through typed services, and
// every write goes through a reviewed preview. It adds no business rule
// and no settings store of its own.
//
// # File map
//
// Each file has one subject, and its name starts with the subject. A
// new file joins a group below, so a reader finds a screen by its name.
// TestDocListsEveryFile fails when a file and this map disagree.
//
// Shell: the program, the frame, and the state that every screen reads.
//
//   - doc.go: the package doc and this file map.
//   - tui.go: Options and Run, the terminal lifecycle and the clipboard.
//   - app.go: the model, the five destinations, the frame, the overlays.
//   - app_run.go: the run line, the quit question, the wait at exit.
//   - state.go: the data sections, the generation tickets, the messages.
//   - keys.go: the global keys, the movement keys, the n/a key helpers.
//
// Drawing primitives: what draws or tracks a position.
//
//   - theme.go: Theme, Glyphs and Tone, the colors and the glyph sets.
//   - box.go: bordered boxes, modals, columns, and the overlay.
//   - widgets.go: layout thresholds, key hints, rows and notes.
//   - text.go: the sanitizers, cell widths, wrap and truncate.
//   - scroll.go: scrollState, textPager and listState.
//   - input.go: lineInput, the one-line text input.
//
// Shared screen types: what several screens embed.
//
//   - listpane.go: listPane and filterInput of Proposals and Pins.
//   - previewflow.go: previewFlow, the stages of a preview and a commit.
//
// Overview (destination 1):
//
//   - overview.go: the screen state, the focus and the keys.
//   - overview_view.go: the layout, the Attention box and the Detail box.
//   - overview_items.go: the attention items and their groups.
//   - overview_cards.go: the summary cards of the wide layout.
//   - overview_summary.go: the summary item of the collapsed layouts.
//   - overview_learn.go: the learning links into Lessons.
//   - overview_build.go: the index build modal and its progress.
//
// Integrations (destination 2) and its setup wizard:
//
//   - integrations.go: the client list, the detail box and the keys.
//   - integrations_detail.go: the capability table and the sections.
//   - integrations_text.go: the line helpers of the boxes and the wizard.
//   - setup.go: the wizard steps, the messages and the keys.
//   - setup_choose.go: the client step and the choice step.
//   - setup_preview.go: the review step, which is the confirmation.
//   - setup_diff.go: the numbered diff of the review step.
//   - setup_result.go: the apply step and the new inspection.
//
// Lessons (destination 3), its container and its five sub-views:
//
//   - lessons.go: the container, the sub-view line and the switch keys.
//   - proposals.go: Proposals, the filters, the marks and the decisions.
//   - proposals_view.go: the Proposals list box and detail box.
//   - proposals_modal.go: the decision previews and their results.
//   - proposals_summary.go: the one-line summary of a review finding.
//   - pins.go: Pins, the installed pins and their keys.
//   - pins_view.go: the Pins list box and detail box.
//   - pins_compare.go: the read-only comparison of similar pins.
//   - pins_modal.go: the retarget and prune preview flow.
//   - pins_modal_view.go: the stages of the retarget and prune preview.
//   - pins_msgs.go: the typed requests between Pins and the shell.
//   - stats.go: Stats, the delivery statistics.
//   - evidence.go: Evidence, the two sources and their actions.
//   - evidence_preview.go: the evidence refresh preview.
//   - evidence_run.go: the evidence run record and its worker.
//   - evidence_run_view.go: the boxes and the verdict of that record.
//   - evidence_words.go: the shared words of the evidence workflow.
//   - run.go: Run, the records of this session and their messages.
//   - run_agent.go: the state that both paid agent run records share.
//   - distill.go: the distillation record and its commands.
//   - distill_keys.go: the keys of a distillation record.
//   - distill_scope.go: the context line and the scope form.
//   - distill_plan.go: the Preview step of a distillation.
//   - distill_run.go: the running distillation and its result.
//   - extract.go: the trigger extraction record and its commands.
//   - extract_view.go: the steps of a trigger extraction record.
//
// Doctor (destination 4), Settings (destination 5) and the first run:
//
//   - doctor.go: the doctor checks and the detail of one check.
//   - settings.go: the screen state, the groups and the keys.
//   - settings_draft.go: the draft, one edit per changed field.
//   - settings_edit.go: the radio editor and the list editor.
//   - settings_save.go: the review and the save of one group.
//   - settings_view.go: the field list and the editor box.
//   - firstrun.go: the guide of a fresh workspace.
//
// Services: the typed engine adapter behind every screen.
//
//   - services.go: the area interfaces and the live engine.
//   - services_learning.go: evidence, refresh and distillation.
//   - services_maint.go: Pins, maintenance and trigger extraction.
//   - services_settings.go: the focused settings reads and patches.
//
// # Rules
//
// Embedding: a holder never declares a field or a method with the name
// of a member of an embedded listPane, filterInput or previewFlow. Go
// embedding has no virtual dispatch, so the shared code would read the
// embedded member and not the holder's one. embedscan_test.go checks
// the rule.
//
// Messages: a worker never changes the model; it returns a message,
// and Update applies it. A screen reaches another screen only through
// a shell message, such as navigateMsg, openRunMsg or openMaintMsg.
// So each screen changes only its own files, and every state change
// happens on the event loop.
package tui
