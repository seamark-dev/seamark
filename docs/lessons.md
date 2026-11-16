# Lessons and proposals

Seamark learns from repository history to help AI coding agents avoid repeating
mistakes. This guide explains the evidence, proposal review, and reminder
settings behind the [first learning workflow](../README.md#learn-from-your-repository).
Lessons are advice; they do not guarantee that an AI coding agent follows them.

## From feedback to reminders

| Term | Meaning |
| --- | --- |
| Finding | One observation from a review comment or fix commit, with its source. |
| Lesson | A recurring pattern in findings for a file or code area. |
| Proposal | A candidate lesson drafted by your AI agent CLI, awaiting your review. |
| Pin | A lesson you accepted or wrote by hand in `.seamark/lessons.yaml`. |

Mining collects evidence. Optional distillation asks your AI agent CLI to
turn findings into proposals. You review the wording, evidence, and scope
before accepting a proposal. Hooks deliver relevant lessons and pins during
supported edits; MCP tools also include them in repository answers.

`seamark lessons --apply p1` prints a YAML block by default. Paste it under
`pin:` in `.seamark/lessons.yaml`, or enable `distill.write` in
`.seamark/config.yaml` before applying to let Seamark edit the file and record
the decision. In print-only mode, the proposal stays pending. See
[configuration](configuration.md) for the setting and durable-state backups.

## What gets mined, and what deliberately does not

`seamark index --reviews` fetches pull-request review comments through
your authenticated `gh` CLI and also mines local fix commits. Use
`seamark index --fixes-only` when only the local, commit-reachable fix source
should be refreshed and no network access is allowed.

Review recurrences are clustered as follows:

- A cited linter code clusters by directory — a habit of an area, not a
  property of one line.
- An un-coded comment clusters by file and issue title; a fingerprint
  recurring across several files widens to their common directory.
- Only patterns that recur (≥2 by default) surface in `why`/`orient`.

Review evidence has a shelf life: comments older than two years are
dropped at mining time (fix commits always had a one-year window), with
one guarantee for slow repositories — the newest 200 comments always
survive, so a repo with three pull requests a year keeps a working
corpus with no tuning. `reviews: {window_days: N}` in config.yaml
adjusts it; `0` means unlimited.

Just as important is what is *not* a lesson. Thread replies are
conversation about a finding, never the finding — mining them once made
an author's "fixed" the top lesson of a real repo. Comments with nothing
actionable ("Very smart!", a bare link) are dropped rather than
clustered. And a linter code only counts when actually cited: not when a
matching token appears in quoted tool output (`rg -A10` once minted a
fake "A10" lesson from a bot's analysis script), and not in a repo that
doesn't contain the linter's language at all.

## Fix commits are findings too

Review quality varies; **fix commits are a signal most repositories
carry.** Both `index --reviews` and `index --fixes-only` mine them whenever
they exist, purely from local git — no GitHub needed at all: commits classified as
fixes by explicit intent
(`fix:` subjects, `fixes #N` links, `Revert` commits; never substring
matches — "prefix" and "fixture" don't count), minus the ones that teach
nothing (typo/lint/CI chores, 30-file bulk refactors), minus cherry-pick
duplicates (patch identity: a backport is the same event) and fixes that
were later reverted. A merge from a `fix/`-named branch whose commits
carry no fix-shaped message of their own counts too (`fix:branch` — the
author declared the fix in the branch name; the finding is the merge's
diff), and in merge-commit workflows every branch commit inherits its
pull request from the merge topology, so a review comment and the fix
commit answering it count as one event. Each surviving fix becomes a finding whose body
carries the commit message *and the patch* — the patch is the signal
that survives useless messages (measured: two anonymous "fix: PR review"
commits still grouped correctly on patch content alone).

Fix findings feed the distiller alongside review findings — a fix and
the review comments on its PR count as one event — and power a
deterministic hotspot line in `why`:

```text
fix density  9 of the last 20 commits to internal/index/index.go were fixes
```

phrased over a recent window so it decays as calmer history accumulates.
The line counts the commits on the whole file, also under a symbol, so
it names the file.

The two sources degrade independently: review mining needs `gh`
authenticated and a github.com remote; fix mining works offline on any
remote. A failed mine (offline, logged out) fails safe — it keeps the
lessons already stored rather than clearing them. `--fixes-only` likewise
preserves existing review rows; use a fresh index when an experiment must
contain only local fixes. Lessons refresh only on an explicit `--reviews` or
`--fixes-only` pass: a normal `seamark index` (and every agent tool call)
leaves them untouched rather than re-hitting the network.

## Tuning what surfaces: lessons.yaml

A committed `.seamark/lessons.yaml` controls what shows — applied at
surface time, so edits take effect with no re-mining:

```yaml
threshold: 2                     # min recurrences to surface (default 2)
pin_budget: 3                    # pins the edit hook injects per edit (default 3)
change_budget: 6                 # lessons one change_set answer carries (default 6)
hook_delivery: once-per-context  # suppress repeats until context compaction
mute:
  - rule: F541                   # hush a noisy rule everywhere
  - region: alembic/versions     # …or every lesson under generated code
pin:                             # your "must not be ignored" list —
  - rule: RUF001                 # surfaced always for its region, even if
    region: scripts              # mining found it once or never
    note: "Keep scripts ASCII — smart quotes have bitten us"
  - rule: validate-at-the-boundary
    region: api                  # a theme living in two places names both:
    regions: [api, db]           # a set beats a repo-wide `*`
    note: "Enforce closed sets and non-null contracts at the edge."
```

`mute` kills noise; `pin` is the escape hatch for a rule you care about
more than the mined frequency implies — written by hand, exactly like
policy.yaml rules (the distiller can draft them, but never has to). Both
flow through `why`, `orient`, and the edit hook via one path, so they
never disagree.

The console (`seamark tui`) edits this file too, always through a
reviewed diff. Settings › Lesson delivery and Lesson mutes change
`threshold`, `pin_budget`, `change_budget`, `hook_delivery`, and
`mute`. Lessons › Pins writes manual pins (see "Pins that a person
writes" below). A save keeps comments, unknown keys, and every
other entry. A Settings reset removes the key line with its comment.
A Settings save refuses to remove a mute entry with a comment after
its text. Two pin writes also delete seamark provenance comments. A
pin removal deletes those of its own entry. A prune (`-` in Lessons ›
Pins) deletes those that name its proposal.

Pins are powerful, so their injection cost is budgeted: the edit hook
carries at most `pin_budget` pins per edit (default 3, and never more
than 8), ranked by
evidence confidence first and region specificity second — a weak pin
must not hold a slot a strong one wants, and among equals a pin on the
file beats its package beats a repo-wide `*` — with a `+N more` pointer
for the rest. Deliberate views (`--file`, `why`, `orient`) apply no
pin budget. `--file` shows every pin that applies and at most 100 mined
lessons, the most recurring first. `why` and
`orient` show the first 6 lessons and a `+N more` line that names where
the rest are: `seamark lessons --file <path>` for `why`;
`.seamark/lessons.yaml` and `seamark lessons --list` for `orient`. Only the
edit hook and the exploration hook have a pin budget; the exploration
hook uses the edit hook's budget and prints no `+N more` pointer.

### Hook delivery modes

Choose one `hook_delivery` value in `.seamark/lessons.yaml`. Omitting the key
is equivalent to `always`.

To deliver matching lessons on every supported edit:

```yaml
# Default: maximize reminder visibility.
hook_delivery: always
```

To avoid sending the same lesson repeatedly while an agent works in one
context window:

```yaml
# Reduce repeated context; reminders return after context compaction.
hook_delivery: once-per-context
```

With `once-per-context`, the first matching edit injects the lesson and later
matching edits in the same provider session stay silent for that lesson.
Different lessons can still be injected, and changing a lesson's region or
symptom gives it a new identity. A delivered pin follows its exact version,
so a mode or check change can also deliver it again. A cosmetic edit of
`lessons.yaml` keeps the version. A mined lesson has no version and keeps its
content key: its region and symptom. `seamark init` installs a `PostCompact`
lifecycle hook that starts a fresh delivery generation after Claude Code
summarizes old context, so delivered reminders can return when they are useful
again. After upgrading Seamark, run init once to install or refresh the hooks.
Configuration is then read on every hook invocation, so switching modes does
not require re-running init.

A pin that the receiving context already has leaves the selection before the
budget. It takes no slot, so a later matching edit reaches the next pin. With
`pin_budget: 3` and four matching pins of equal rank, the first edit sends
three pins and `+1 more`, the second edit sends the fourth pin, and the third
edit sends nothing. After a reset the pins go out again from the top. Here
"reaches" means that the hook emitted the pin. No native recording covers this
case, and an emitted pin does not show that a model read it.

This holds only for a receiver that can reset, with usable state: today the
main Claude Code conversation under `once-per-context`. Under `always`, in a
subagent, on Codex, and when the state fails open, the budget applies to every
matching pin, so the same edit sends the same first pins each time. A pin that
restates a delivered pin stays held back, because the restatement collapse
runs first and ignores what the context has. The exploration hook applies the
budget before its repeat state, so a read does not move to the next pin.
Mined lessons have no version, so this step leaves them alone. They use the
room that pins leave, and the repeat check of a mined lesson runs after the
budget, so a delivered mined lesson can still hold a slot.

The optimization is deliberately fail-open: missing session identity, corrupt
local state, or a state-lock failure causes normal injection rather than
silently hiding a lesson. Only repository-scoped digests of the receiving
context and of the lessons are stored in the gitignored
`.seamark/lessons-hook-state.json`; entries expire after 24 hours.
A reset after a compaction waits up to 2 s for the state lock. When another
hook holds the lock for longer, the context keeps its delivered lessons until
the 24-hour expiry.

A receiving context is the client plus the conversation that the client's
adapter reports as the receiver. Two clients that report the same session
string are two contexts, and a reminder shown to one never hides it from the
other. A client that cannot name the receiver, or has no reset event for it,
gets repeated delivery instead of suppression. For Claude Code the main
conversation is the session, and `PostCompact` resets it. An edit inside a
subagent carries `agent_id` and is its own context: the subagent's context
window never saw what the parent got. No reset event is documented for a
subagent, so a subagent gets repeated delivery, and a reset that fires inside
it never resets the parent. For Codex the adapter names the receiver too: an
edit without `agent_id` belongs to the main session (the root lane), and an
edit that carries `agent_id` belongs to that subagent (a child lane). The
firing log keeps the lane and a digest of the context. Only a manual
compaction of the parent has been observed natively, so no Codex context is
resettable, and Codex reminders repeat until a native check establishes the
reset of each receiver.

The state file carries a version. This release writes version 2. It reads a
version 1 file as empty, because a session-keyed entry does not say which
context got the reminder; the cost is that each lesson already delivered can
be shown once more per context after the upgrade. An older seamark that meets
a version 2 file delivers every reminder and leaves the file alone, and this
seamark does the same with a version it does not know. After a rollback to an
older seamark, delete the file to get suppression back.

## The ledger: lessons --list

You don't have to author the config by hand. `seamark lessons --list`
prints every mined lesson — including the one-off noise that
`why`/`orient` hide — with the exact rule and region values to paste,
and flags anything your config already mutes:

```text
$ seamark lessons --list
review lessons (all mined, strongest first) — 436 total

  ×20   scripts                                        [coderabbit]          E702
  ×3    scripts                                        [coderabbit] (muted)  F541
  ×1    fetcher.py                                     [coderabbit]          bound the target close probe
  …
```

`--region <dir>` narrows the ledger to one area — its lessons and its
ancestors', one-offs included; agents on MCP reach the same view as
`expand lessons:<dir>`, and `why` advertises the ref when it shows
lessons and that view holds a lesson. Reading a package's raw findings side by side is how a pattern
too varied for exact clustering gets spotted (ten differently-worded
"reset pooled state" findings are one lesson to a reader), whether the
reader is you or an agent you point at it — and the ledger's footer
tells that reader what to do with a spotted pattern: propose a pin for
review, never self-add it.

## Distilling patterns: plan → apply

Exact clustering can't see that ten differently-worded findings are one
mistake. `seamark lessons --distill` can: it batches the raw findings
into candidate groups and asks **your own agent CLI** (`claude` by
default, `codex` with `agent.cli: codex`, or any command line under
`agent.argv`) to name recurring patterns and propose pins. Seamark holds
no API keys. See [inference setup and authentication](agent-integrations.md#choose-an-agent-for-inference)
for selecting Codex or using API-key billing. `agent.argv` takes precedence
over `agent.cli`; installing Codex hooks does not select Codex for inference.
Distillation is optional: you can write the same entries by hand without
an agent CLI or a paid model call.

The
[OpenTelemetry histogram-reset case study](case-studies/opentelemetry-histogram-reset.md)
walks through this boundary on a pinned public history: two independent fix
events become a proposal, a maintainer decides what to pin, and a separate
pre-fix worktree demonstrates delivery and auditability. The reusable
[case-study protocol](case-studies/protocol.md) keeps its results separate from
the controlled benchmark.

Before anything is sent, a preflight disclosure prints the exact agent
command line, the group and finding counts, and the estimated token
cost; `--dry-run` stops there ([data-flow.md](data-flow.md) documents
the full payload). Each run reports what it spent:

```text
$ seamark lessons --distill --region v2/pkg/engine
distilling 40 findings (v2/pkg, 1728e3d64e76a8ef)
  42s, ~5.3k tokens sent / ~433 back, 2 proposal(s)
distill plan — 50 groups: 3 read, 40 already distilled, 7 left for another run (raise --limit or drop --region)
agent traffic: ~15.9k tokens sent, ~1.2k received (estimated), 2m6s

proposed pins — distilled from review findings, awaiting YOUR decision

  p6    reset-reused-visitor-state   v2/pkg    2 findings cited [claude/v1]
        When a struct is reused across runs, reset all accumulated
        state — not just the primary field — before reusing it.

decide: `seamark lessons --apply p6` pins a chosen proposal; comma lists and ranges work too; `--dismiss` remembers the no
```

### The economics

Engineered for repeated use: every group's evidence set has a signature,
a distilled signature is **never paid for twice**, and a new finding
reopens exactly its own group. `--limit` (default 10 groups) and
`--region` budget each run — and a budgeted run spends its calls where
they are worth most, reading the groups whose evidence no proposal has
cited yet before the well-mined ones. Nothing is filtered out: coverage
changes the order, never the corpus, because dropping evidence could
starve a genuinely new pattern of the recurrence it needs. Each batch
also arrives knowing the rule *labels* already pinned for its area, so
the call looks past them — labels only, since carrying the notes would
cost more tokens than the duplicates they prevent. Dismissals are
permanent memory; a pattern only returns if its evidence changes.

### "Will distill re-process what it already processed?"

Only where the evidence set changed — and the skip is absolute, not a
discount. The memory keys on a group's **signature: a hash of its
member finding ids**. A group whose signature is already recorded is
skipped with zero tokens (no agent call happens at all), and the plan
line says so: `50 groups: 3 read, 40 already distilled`. Any change to
a group's membership means a new signature, and a re-read sends the
**whole group again**, previously-seen findings included — the model
needs the full group to judge recurrence; there is no incremental
"just the new ones" mode.

What changes membership, in practice:

- **A new finding** re-opens exactly its own group (groups are cut by a
  hash of finding ids, so growth perturbs one batch, not the corpus).
- **A finding aging out** — the mining windows move, and a group that
  loses a member re-reads. A re-mine after a long gap therefore causes
  a one-time bump.
- **Upgrading across a grouping change** re-cuts the affected groups
  once; the changelog calls it out when a release does this.

Three things keep a re-read bump from being waste. `--distill
--dry-run` prices the exact plan first — every group that would be
sent, with its ~token estimate — and sends nothing; `--limit` and
`--region` spread the bump across runs. Re-reads cannot re-propose what
you already have: every reply is checked against your pins and every
decided proposal (dismissals included, by cited-evidence identity as
well as wording), so the tokens buy only genuinely new patterns. And
because *decided* proposals are permanent dedup memory while *pending*
ones whose group churns are pruned and re-derived, deciding what is
pending **before** a re-mine or upgrade preserves those calls instead
of paying for them twice.

### One theme, one pin

Candidate groups are read independently, so a repo-wide mistake shows up
in several of them — and a distiller with no memory would re-propose it
under a new name every time (measured on a real repo before this check:
65 applied pins carried only 50 distinct themes). Every distilled
pattern is therefore compared against what is already captured — your
pins, hand-written ones included, and every proposal already pending,
applied, or **dismissed** — and a restatement is dropped before it
reaches the ledger. The check is deterministic and costs nothing.

`seamark lessons --proposals` audits the pins you already have the same
way: it names each near-duplicate cluster, suggests which entry to keep
(the one resting on the most evidence), and hands you the command —
`seamark lessons --prune p16,p45` — to retire the rest. Pruning is not
dismissal: the theme stays pinned by its survivor and the distiller
still counts it as known, where a dismissal would suppress it.

### Where a pin points: region sets

A proposal first gets fallback coverage computed from its cited evidence,
never from the model's path arithmetic — and it is a **set** (`region: api` plus
`regions: [api, db]`, at most three directories, depth at most three),
chosen to cover at least 80% of the cited *events*. Events vote, not
findings: six comments on one pull request are one voice. Test and doc
paths abstain (a fix's test usually out-churns the fix — the vote
belongs to the code it corrected; a lesson whose entire evidence is
test files keeps its test region), root-level files never drag a theme
repo-wide, and when the voting evidence is genuinely scattered the
honest answer stays `*`. Measured on the two development corpora, this
cut repo-wide pins from 35 of 65 to 3 — every one of which used to tax
the injection budget of every edit in the repository. A verified trigger can
then replace that broad fallback with the more precise delivery set described
next.

### Where the mistake is made: trigger paths

Evidence lives where reviewers commented — the *repair* site. Some
mistakes are made somewhere else: a "regenerate the client" lesson
lands on the generated TypeScript file, while the author who forgets
is editing the backend model, where a pin scoped to the generated file
can never fire. Distillation therefore requires **trigger paths** — up
to three files or directories an author edits when *making* the
mistake. A trigger may be one of the cited evidence paths when another
citation is the companion repair site. Every named path must parse as
repo-relative and exist in the working tree. Seamark then accepts it when it
is an exact cited production path or that path's immediate parent, or when
co-change history confirms it against the cited evidence. Accepted triggers
become the precise delivery set—even an individual file. Evidence
coverage remains the fallback when no trigger verifies, and an
unverified name never moves delivery. The distill plan says whether
each trigger was directly cited, confirmed by co-change, unconfirmed,
or no longer deliverable. The ledger and report retain the durable scope,
drift, and blocked-trigger state; the creation-time plan additionally shows
positive direct and co-change confirmations.

Proposals distilled before trigger extraction existed are upgraded
with `lessons --extract-triggers`: small batched agent calls ask the
one question per proposal (rule, note, evidence paths, one capped
excerpt — a preflight discloses the cost, `--dry-run` stops there).
Every answer records the semantic question version, "no trigger" included, so
re-running is free. A changed question re-asks legacy negative answers once
without disturbing existing positive trigger paths.
Each answer saves its paths, its stamp, and a pending scope change together,
and only while the proposal is still as the run read it. A proposal that
another command applies, dismisses, retargets, or answers meanwhile keeps
its state: stderr names it (`conflict: p1 changed during the agent call — …`),
the run sends no further batch, and re-running resumes.
Pending proposals adopt their verified trigger scopes in place; applied pins
only store the triggers and surface a `regions now:` drift line —
delivery of an installed pin never changes without an explicit
`--retarget` or a scope edit in the console. Hand-pruned pins are skipped: there is no delivery to
retarget. When the note itself names a real outside path and co-change
agrees, the ledger additionally flags the pin — `delivery may miss the
trigger: …` — with a tail block naming every flagged id.

### How much to trust a pin: confidence

Every distilled pin carries a tier — **strong / fair / weak** —
recomputed on each read from what its citations still support: distinct
events (a review comment and the fix commit answering it count once),
source corroboration (review AND fix beats either alone), recency, and
whether the cited files still exist in the workspace. In a copied or
moved checkout whose index still records the original root, a
preparation and `--file` describe the checkout, so they look for the
cited files there. The other surfaces look in the root that the index
records. Nothing is stored
and nothing is model-scored, so a pin's tier honestly decays as its
evidence does. Confidence is not decoration: pins compete for the
hook's injection budget by tier before specificity, a weak pin that
does surface is tagged (`weak evidence: 1 event(s), review, newest
50d`), and deliberate views (`why`, `--file`) print every matched pin's
tier with the facts behind it — as an argument you can check, never a
bare score.

### Revalidating old decisions: the ledger and --retarget

`seamark lessons --proposals` re-judges every pending and applied
proposal under *today's* rules, for free: its confidence facts, a note
when it was distilled under an older prompt (before the recurrence
rules tightened), dead citations, the outcome verdict for applied pins
(the same sentence `--stats` prints — see "Do pins actually change
behavior?"), and — when current inference disagrees with the stored
regions — a `regions now:` line with one command covering every
drifted pin:

```text
  p65   train-serve-parity                 *
        fair — 2 event(s), fix, newest 65d
        regions now: workers
        working — flagged 4× in ~120 region-commits before exposure; 0× in 31 since (fired 9×)
  p47   leak-exception-to-client           svc/api
        not landing — recurred 2× since exposure (fired 12×)

not landing — p47 fires but the mistake recurs; escalation is yours:
reword the note, raise the pin, or graduate it to a check

retarget: `seamark lessons --retarget p65,p62,…` updates those pins to the recomputed regions (lessons.yaml and the ledger together; needs distill.write, else it prints the block)
```

`--retarget` is the upgrade path for pins distilled before region sets
or trigger extraction existed. "Regions now" is one shared recompute:
verified trigger scopes when available, evidence coverage otherwise.

A person can choose the regions of a linked pin in the console. Such a row
keeps its set until a person reviews a replacement. Its `regions now:` line
reads as a suggestion and names the date of the choice:

```text
  p65   train-serve-parity                 workers, jobs
        regions now: workers (a suggestion; a person chose the stored regions on 2026-11-01)

chosen scope: p65 — a person chose these regions; review each advisory above in `seamark tui`, Lessons › Pins
```

The `retarget:` and `trigger scope:` lines leave that row out, because the
command does not change it. The `chosen scope:` line appears when the row has
a `delivery may miss the trigger` advisory. `lessons --retarget` refuses a
chosen row and names the console. It refuses the row when the computed scope
differs, and `--confirm-checks` does not change that. The row prints its
reason, and the other rows of the run continue. A chosen row with a current
scope or with aged-out evidence keeps its usual line. The reason names `w`,
the console key that reviews the computed scope. When another pin or row owns
the computed scope, or the pin file blocks the change, the console does not
offer `w`. The reason then names what stops it, and it names `i`.

In `seamark tui`, the preview of `w` in Lessons › Pins says that it replaces
the choice before you confirm it.
Retarget runs on the engine of the console's Pins view: it changes only
the `region` and `regions` lines of each entry, in place, so the rule
spelling, a hand-edited note, comments, unknown keys, and the position
of the entry stay. The file is replaced atomically first; the ledger
rows then update in one transaction that checks each row again. When
the ledger update fails, seamark puts the earlier file back, but only
while the file still holds exactly the bytes that it wrote, and the
error says `lessons.yaml restored` only after that restore succeeded.
Otherwise the error names the state and the way back: undo the change
(`git diff .seamark/lessons.yaml` shows it) and run the same command
again. A hard crash between the two writes leaves the file ahead of the
ledger. Seamark does not guess that the moved pin is its own, so the
next `--retarget` reports the proposal as not found; restore the earlier
lines and run it again. Re-running is otherwise safe: "regions already
current" is the steady state.

The same ledger also flags near-duplicate clusters among applied pins
and hands you the `--prune` command to retire the redundant entries —
see "One theme, one pin" above for how that audit works and why
pruning is not dismissal.

### Proposal-only by construction

The model must cite the finding ids behind every pattern (uncited
patterns are dropped — it cannot invent evidence), every pattern must
give an explicit trigger-path answer, and named paths affect delivery
only after tree and evidence/history validation. Nothing that the model
drafts reaches `.seamark/lessons.yaml` without an explicit step. One
step is an apply of selected proposals (`--apply <ids>`, or `a` in the
console). The other is a retarget of applied pins to the recomputed
regions (`--retarget <ids>`, or `w` in the console). A retarget can
write trigger scopes that the model drafted and seamark verified. Even
then,
seamark edits the file itself only if `config.yaml` opts in
(`distill: {write: true}`) — otherwise apply prints the pin block for
you to paste. Applied entries are inserted at the head of your
existing `pin:` list, at the indentation of that list, with provenance
comments; everything
hand-written stays byte-for-byte. Without a `pin:` section, apply adds
one at the end of the file (before a `...` end marker). The write uses
the console's pin file editor: it proves the result first (the file
still loads, every other setting and pin is unchanged), replaces the
file atomically, keeps its mode and its CRLF line breaks, and refuses a
symbolic link, a read-only file, a flow-style `pin: []`, more than one
YAML document, and mixed line breaks, with the reason. Apply never adds
a second entry with the same rule and region set: a proposal whose pin
the file already holds is recorded as applied without a write (left out
of the printed block). Apply refuses two selected proposals with one
rule and region set (`p1 and p2 share rule and regions; apply one and
dismiss the other`), because the console's Pins view blocks a pin that
two applied proposals record. For the same reason, it refuses a
proposal whose rule and region set an applied proposal already has
(`p1 is already applied with this rule and regions; dismiss p2`), also
when the file holds that pin. So the record without a write covers
only a pin that no applied proposal records, for example a
hand-written one. It also refuses a proposal whose stored
region fields disagree: the pin that it would write has other regions
than the stored ones, so no reader links that pin to it. `--prune` and
`--retarget` obey the same write gate and use the same editor. Prune
removes the selected entry and the seamark provenance comment that
names its proposal, and nothing else; retarget changes only the region
keys of the entry. Both leave a proposal that they cannot change as it
is and print why: a pin not in the file, two entries with the same rule
and region set (remove the duplicate by hand first), stored region
fields that disagree, or a target rule and region set that another pin
or proposal already has. They refuse a symbolic link and a read-only
file at the pin file path, as apply does. They leave every pin of a
file that the editor blocks as a whole, for example a file with more
than one YAML document or, unlike apply, a file with CRLF line breaks
(the package doc of `internal/pinfile` lists these layouts). With the
write gate off, they still print the change to make by hand for such a
file, without a diff. Their last line then names the refusal that the
gate cannot lift, also for a link or a read-only file. They write only
a result that still loads with every other pin intact.

In a copied or moved checkout, the index still records the original
root until an index run in the new checkout. `--apply`, `--retarget`,
and `--prune` then refuse before they open the index. In a copy, the
pin file lies under the recorded root and the ledger lies in this
database, so one decision would change two repositories. After a move,
the recorded root no longer exists, and a write would create it again.
The refusal writes nothing, prints no change to make by hand, also
with `distill.write` off, and exits 1. Run `seamark index` in the new
checkout, then the command again.

A copy:

```text
$ seamark lessons --apply p1
seamark: the index of this checkout records another repository (<original>), so this command would change the pin file there and the ledger in this database; nothing changed; run `seamark index` in <copy> first
```

A move:

```text
$ seamark lessons --apply p1
seamark: the index of this checkout records a directory that no longer exists (<original>), and a write would recreate it; nothing changed; run `seamark index` in <moved> first
```

A `--db` of another repository follows the rule that the
[console guide](tui.md#versions-and-limits) gives for the same commands.

### Pins that a person writes

You can write a pin by hand in `.seamark/lessons.yaml` or in the
console (`seamark tui`, Lessons › Pins). In the console, `n` drafts a
new pin. `i` edits and `d` removes the pin under the cursor, never the
marked pins.
A new pin is manual, and so is a pin that no proposal records: no
proposal stands behind it. For every pin, the console writes no provenance
comment, and it never creates or links a proposal row. It changes a
proposal row in one case: a scope edit of a linked pin.

- A new pin goes after the last pin of the `pin:` list. Apply still
  inserts its pins at the head of the list. So a manual pin never moves
  the pins before it.
- A pin that no applied proposal records has no evidence to doubt, so
  it ranks as strong evidence. Under `pin_budget` it comes before a fair
  or weak applied pin. The file order decides only among pins with
  equal rank and specificity.
- Without a `pin:` key, the console adds one at the end of the file. A
  missing file gets only the `pin:` key and the new pin.
- An edit changes only the rule, note, regions, mode, or check that you
  edit. A removal deletes one entry and the seamark provenance
  comments directly above it, because they would read as the
  provenance of the next pin. A free comment above the entry stays.
  It then reads as the comment of the next pin. Every other line stays
  byte for byte.
- A linked pin (one applied proposal records it) allows note, mode,
  check, and region edits. Its rule stays read-only, and its removal is
  `-` (prune). Its scope has two routes. `w` (retarget) sets the
  recomputed regions while `w` offers them. `i` sets the regions that you
  choose, through a scope preview, unless the form cannot hold the stored
  regions. While `w` reads `n/a`, Pins gives the reason and a next
  action. The proposal keeps its
  recorded rule, note, and evidence. A changed note makes Pins show
  `edited`; trigger extraction still asks about the recorded note.
  Mode and check edits have their own review notes and change the pin
  version, not the proposal evidence.
- A scope edit writes the pin file and the ledger, and every other
  edit writes the pin file only. The row records that a person chose the
  regions and the date, so inference no longer replaces them unasked.
  Its rule, note, and evidence stay. No command makes this edit, and a
  region change by hand unlinks the pin. Only the console in its own
  recorded root can save it, so a copied checkout refuses it.
- The same `distill.write` gate applies. With the gate off, the review
  shows the exact diff and writes nothing.
- A save needs a readable index of this workspace, because the
  relationship of a pin to the proposals comes from the ledger.
- Unlike apply, these writes refuse a file with CRLF line breaks and a
  file with more than one hard link. The route is an edit by hand.

An edit of the wording or the scope can give the pin a new delivery
identity, and the review says whether it does. A context that already
saw the old wording can then receive the pin again, also in
`once-per-context` mode. Earlier deliveries stay recorded under the
earlier wording and scope; nothing is reset or moved. A new order of the
same regions keeps the identity and the version.

A mode edit, a check edit, or a note edit of letter case alone keeps the
delivery identity but gives the pin a new version. The review says so,
and a context that already saw the pin can receive it again, also in
`once-per-context` mode. A renamed check with the same definition keeps
the version. Mined lessons keep their content key.

### Remove, Prune, Mute, and the other filters

Several actions make a lesson or a pin reach an agent less often. They
change different things:

| Term | What it does | What it changes |
| --- | --- | --- |
| Remove | Deletes one unlinked pin entry from the file. | `.seamark/lessons.yaml` only; the proposal ledger does not change. Console: `d` in Lessons › Pins. |
| Prune | Removes a linked pin and marks its proposal superseded. | The pin file and the ledger. `seamark lessons --prune <ids>`, or `-` in Lessons › Pins. |
| Mute | Hides mined lessons from selection; never pins. A rule matches in any case; a region matches that region and every region under it. A region with a trailing "/" (`api/`) matches only the regions under it, not `api` itself. | The `mute:` list. The mined lessons stay in the index, and `lessons --list` marks them `(muted)`. |
| Threshold | The minimum mined recurrences. Mined lessons only; never pins. | The `threshold:` key. |
| Budget | A cap on one injection or one answer. `pin_budget` caps the pins of one edit-hook or exploration injection (at most 8 lessons in total per injection). `change_budget` caps one `change_set` answer, one preparation, or one `check` advisory. The checks that a preparation lists have no budget. | The budget keys. `--file` and `why` apply no pin budget and keep every restatement. `--file` shows every pin that applies and at most 100 mined lessons, the most recurring first. `why` shows at most 6 and counts the rest. |
| Repetition | Once-per-context suppression of repeats in one agent context (`hook_delivery: once-per-context`). Edit hook only. The exploration hook keeps its own repeat state, which `hook_delivery` does not configure. | Delivery state in `.seamark/lessons-hook-state.json` (exploration: `.seamark/lessons-explore-state.json`); `change_set`, `lessons --prepare`, `check`, `why`, and the CLI views always show their selection. |
| Sample | The local selection for explicit paths, before suppression, with no record. Console only: `s` in a pin review or a lessons Settings review. | Nothing: no file, no firing record, no delivery state. |

A mute, the threshold, and a budget act when a surface selects lessons,
so the next read of the file applies them without re-mining. Remove and
Prune change the pin list itself.

## The moment of change: prepare, change_set, hook, check

A lesson can reach an agent before the agent builds a change, or when the
built change runs. Only the first moment can shape the change. The edit
hook is a pre-tool hook: it runs after the agent has written the
arguments of the edit call, so its reminder reaches a call that already
exists. The reminder can inform the next step, not the edit it arrives
with. When one response or one generated program holds several edits,
the reminder of the first edit cannot inform the later edits either.
All of these edits come from one model request. The agent reads a hook
reminder only in its next model request, together with the tool result.

Preparation is the earlier moment. `seamark lessons --prepare <path>...`
and the MCP tool `change_set` return the guidance for the paths that an
agent plans to change, before it builds the change: one file, several
files, or a path that does not exist yet. Preparation is advisory. It
authorizes no write, and it does not show that a model read it. A program
that prepares and then applies a patch it already built gains nothing:
a later step must build the change. Preparation never touches a repeat
state, so a pin that a preparation returned still reaches the edit hook
and the exploration hook at the next matching edit or read.

Each surface below is budgeted (an injection is someone else's tokens)
and deduplicated — a mined lesson whose whole cluster an applied pin
already cites is suppressed rather than said twice, and two pins wording
one theme never spend two slots:

- **Preparation** (`seamark lessons --prepare <path>...`, no agent
  call): the pins and mined lessons of the paths under `change_budget`
  (default 6), each pin with its pin-ref, its exact version, its mode, and
  the paths it applies to; with no budget, every pin in scope whose mode
  is `check` or `enforce`, and every advisory pin with a `check` (listed
  as not run; under an invalid check configuration every listed check is
  `does not run (configuration error)` with `runs` false, because no
  checker runs then); and each pin or lesson that the budget or the restatement
  collapse left out, with the `seamark lessons --file <path>` command that
  shows it. The text lists the first five omissions and then a `+N more`
  line for the rest; `--json` lists every one. A relative path starts at
  the current directory, and a new
  path need not exist. Without an index the result holds the pins of
  `lessons.yaml` (`mined lessons no-index`). The command refreshes no
  index: when the index no longer matches the workspace, one guidance
  line says that the mined lessons come from an index that does not match
  the workspace now, and when another seamark version built the index,
  the line says that instead. `--json` prints the
  versioned `lesson-preparation/1` document. In it, `guidance` can carry
  three keys beside `mined_status: ok`. Each one is absent when there is
  nothing to qualify. `mined_freshness` is `stale` here and
  `not-refreshed` from `change_set`. Beside `stale`,
  `mined_stale_reason` says why: `workspace-changed`, or
  `other-version` when another seamark version built the index. `mined_root`
  names the root that the index records when that root is another
  repository. In a copied or moved checkout whose index still records the
  original root, the command prepares for the `-C` checkout, as
  `change_set` does: a relative or an absolute path of the checkout
  resolves there, the pins come from its own `.seamark/lessons.yaml`, and
  the firing record goes to its log. The mined lessons come from the
  index, so `mined_root` names the original root, and the text says that
  the mined lessons come from an index that records it. A path inside the
  original root is outside the workspace. The `seamark lessons --file
  <path>` command of each omission follows the same rule, so it shows the
  pins of the checkout: there, `--file` reads a relative path, or an
  absolute path of the checkout, against the checkout and its
  `lessons.yaml`. `why` in such a checkout describes the original root
  and names an absolute path there, so `--file` reads an absolute path
  inside the original root against that root. `lessons --stats` counts these
  records after `seamark index` runs in the checkout. A `--db` that
  records another repository gives the same preparation for the `-C`
  checkout. `change_set`
  refreshes the index first, and that refresh rebuilds a copied default
  index, so its document then names no `mined_root`. The command exits 0 also
  when no pin applies, and 1 when it gets no path, when no path is inside
  the workspace, or when another mode flag is given; it never exits 2,
  because it blocks nothing. Preparation keeps no delivery state: every
  call returns the current selection. Prepare again when the file set
  grows, when
  `.seamark/lessons.yaml` changes (the guidance digest in the result
  changes with it), or when another agent or subagent will build the
  change. The result names its receiver `unknown`, because a command or
  an MCP call does not say which agent context reads the answer.
- **The edit hook** (`lessons --hook`, wired by `seamark init`): per
  file, at most `pin_budget` pins (default 3, confidence-ranked, a
  `+N more` pointer for the rest) plus the file's recurring mined
  lessons. Offline, silent when there is nothing to say. A reminder
  that shows a pin, or points at held-back pins, also names where to
  inspect the pins: Lessons › Pins in `seamark tui`, or
  `.seamark/lessons.yaml`. It names no command that changes a pin. With
  opt-in `hook_delivery: once-per-context`, already-delivered lessons stay silent
  until Claude Code compacts the current session, and a pin that the context
  already has leaves the selection before the budget, so a later matching
  edit reaches the next pin. Only files inside the
  workspace count: a path outside it, or behind a symbolic link that
  leaves it, gets no reminder. A new file counts by its path. Without an
  index the hook still delivers the pins of `lessons.yaml`, and it never
  creates a database. For Codex (`lessons --hook --client codex`, wired by
  `seamark init --client codex`) the unit is the patch, not the file:
  Codex reports every edit as one `apply_patch`, and the hook reads its
  complete file set — adds, updates, deletes, and both ends of a move —
  without running anything. The whole patch gets one reminder under the
  same budget, each line tagged with its region. A patch the hook cannot
  read completely gets no reminder rather than a guessed one, and Codex
  reminders always repeat: the adapter derives a receiving context, but
  Codex child-lane attribution and context reset remain unverified
  (see [Hook delivery modes](#hook-delivery-modes)).
- **`change_set` (MCP)**: before an edit of one or more files, the
  union of the files' lessons under `change_budget` (default 6) — merged
  by identity, ranked by confidence across the whole set, regions shown
  as the map back onto the files. New files count: a brand-new file in a
  pinned region gets its guidance before its first line exists. This text
  stays the first part of the answer. When the preparation of the same
  files has a pin, a check, an omission, or a rejected path, or when
  `.seamark/lessons.yaml` does not read or decode or its checks are
  invalid, a second text part lists the pin-refs, versions, modes,
  checks, and omissions with their inspect commands, without the lesson
  text again. That part names an invalid file or invalid checks in the
  words of `lessons --prepare`, for every protocol version, because the
  first part then shows no pin and no note. This part
  lists the first five omissions and then a `+N more` line, and
  `structuredContent` lists every one. A client
  that negotiated MCP protocol `2025-06-18` or later also gets the whole
  `lesson-preparation/1` document as `structuredContent` under
  `lesson_preparation`, and the first text part again, byte for byte,
  under `change_set_report`, because a client can show
  `structuredContent` in place of the text parts. When the index refresh
  of the call fails or is refused, the document says
  `mined_freshness: not-refreshed`, and when the index records another
  repository, `mined_root` names it. The second part says so in one line,
  so a client that reads only the document still sees the note that opens
  the first part. A failed preparation
  has no `structuredContent`, so the text parts stay the whole answer. A
  relative path starts at the repository root.
- **The exploration hook** (`seamark lessons --explore --hook`, Claude
  Code only): after the `Read` tool reads a file, the hook gives the
  agent that read it the file's pins and mined lessons, under the edit
  hook's budget, as lessons for a file "which you just read". It runs no
  check and blocks nothing. It keeps its own repeat state in
  `.seamark/lessons-explore-state.json`, keyed by the receiving context,
  its generation, and the pin version, so a changed pin is new again.
  The main conversation gets each lesson once until Claude Code compacts
  it, whatever `hook_delivery` says; a subagent, or an event without a
  session, gets every lesson each time. Like the edit hook's state, this
  state fails open and its entries expire after 24 hours. The
  `PostCompact` hook that `seamark init` installs resets this state
  together with the edit state. The exploration hook never reads or
  writes the edit hook's state, so an edit right after a read still gets
  its reminder. Claude Code puts the content of the file in the `Read`
  event, so a read of a large file, an image, a PDF, or a notebook with
  outputs can make the event larger than 1 MiB. Such an event gets no
  lessons and no record, also with the recording switch on: the hook never
  decodes a part of an event, so it fails open. Codex has no structured
  read event, so `--client codex` writes nothing. Install it explicitly
  with `seamark init --client claude --lesson-exploration on`; turn it
  off with `--lesson-exploration off`. Omission preserves the installed
  choice. A recording of Claude Code 2.1.319 shows the exploration reply
  in the next model request, in the main conversation and in a subagent.
  The capability report still reads the exploration path unverified,
  because its evidence names no client version.
- **`check`** (CLI and MCP): after the policy verdict, the touched
  files' lessons as a clearly-marked advisory — printed even when the
  verdict blocks, because a deny is exactly when they matter; `--json`
  stays verdict-shaped. When a pin in mode `check` or `enforce` covers
  a changed file and the call did not ask for lesson verification, the
  text ends with `lesson checks: not requested — 1 executable pin covers
  this diff; run with lesson_checks: true (CLI: seamark check --lessons)`.
  An invalid `lessons.yaml` gives the same line with the words
  `lessons.yaml is invalid`, because the count is unknown. Without such
  a pin the line is absent.

Each surface records what it surfaced to the firing log, tagged by
surface (`prepare`, `change_set`, the edit hook, `explore`, `check`).
The first line of `--stats` counts each surface apart. The ranking, the
never-fired list, and the pin outcomes count the firings of all surfaces
together. A firing shows that a surface returned or injected a lesson, not
that an agent read it.

## Executable lessons: advisory, check, and enforce

A pin's mode is a request, not proof that a client runs its hooks:

| Mode | Behavior |
| --- | --- |
| `advisory` (default) | Show guidance. An attached check is listed but not run by verification. |
| `check` | Evaluate the check during explicit verification and installed post-edit hooks. Do not request a pre-write deny. |
| `enforce` | Also evaluate the proposed edit in a separate installed guard. Request denial for a violation or an unknown result. |

Checks are hand-authored predicates, not executable interpretations of
arbitrary prose. Define them in `.seamark/lessons.yaml` with
`lesson_checks_version: 1`, then bind a pin to an existing check:

```bash
seamark lessons --verify                        # lists current pin references too
seamark lessons --set-mode check --pin <pin-ref> --check <check-id>
seamark lessons --set-mode enforce --pin <pin-ref> --check <check-id>
seamark lessons --set-mode advisory --pin <pin-ref>
```

In the console, Lessons › Pins edits the mode and the check of the
cursor pin with `i`, opens a check definition with `M`, and runs the
default verification with `V`
([console guide](tui.md#check-definitions-and-verification)).

A mode change validates the configuration and follows `distill.write`.
With writing off it refuses the write and prints the change to make by
hand. It does not install hooks or grant trust. The impact view states
requested mode and active state per client. Note, mode, check definition
and effective scope changes can change the exact pin version; earlier
versions keep their own history.

The supported predicates are deliberately narrow:

- **`forbid-path-change/1`** reports a violation for any observed change
  in scope: add, content change, delete, mode/type change, or either end
  of a move. It reads no content. No touching change gives
  `not-applicable`, not `pass`; incomplete paths can give `unknown`.
- **`no-added-pattern/1`** matches a Go regular expression against each
  added text line in scope. A new file adds every line. Moving a file
  into scope adds its content; moving it out removes it. A deletion or
  mode-only change adds no line. It does not scan unchanged lines for
  old violations. A match gives `violation`; otherwise incomplete,
  binary, unreadable or unprojectable content gives `unknown`. A fully
  checked touching change without a match gives `pass`.

See [configuration](configuration.md#executable-lesson-configuration)
for keys and bounds. A detected violation stays visible beside unknown
paths; an incomplete evaluation never becomes a pass.

### Verify the work, not just one proposed call

```bash
seamark lessons --verify
seamark lessons --verify --file path/to/file --file path/to/other
seamark lessons --verify --json
seamark check --lessons
```

Default verification compares the current working tree with `HEAD`
(the empty tree before the first commit). It includes staged and
unstaged tracked changes plus nonignored untracked files. Repeated
`--file` selects only those paths, including ignored files. A relative
path starts at the process directory. The result names the snapshot,
configuration and checker versions, selection, omissions and freshness.
Exit 0 means passed, no applicable checks, or no executable checks.
Exit 1 means that a result is unknown, that the files changed during
the check, or that the configuration is invalid. Exit 2 means a
violation. This does not certify
the whole repository against every lesson.

Default selection leaves out exactly the untracked lesson-check audit,
its rotated generation and its lock. Tracked files and explicit
`--file` selections keep them.
[Configuration](configuration.md#verification-state-and-report-inputs)
names the paths and other limits.

`check --lessons` runs the existing policy check, then verifies the
working tree independently of the supplied policy diff. Its JSON is a
`check-lessons/1` envelope with both results and their combined state.
Plain `check --json` stays verdict-shaped. MCP `check` accepts
`lesson_checks: true` for the same combined reading; omission or false
keeps its existing policy/advisory behavior.

The text form opens with the combined line, then a blank line, the plain
check text, a blank line, and the lesson report. Over MCP, a note on the
root that the index records comes before the combined line, because it
qualifies the whole answer (see [repository history](repository-history.md#how-much-can-the-answers-be-trusted)):

```text
combined: fail (policy deny, not blocking in warn mode; lessons violation)

verdict  deny (mode: warn)
...
lesson checks: violation
...
```

MCP adds the exit code that the CLI gives (`; CLI exit 2`). The policy
clause reads `policy pass` for an allow, `policy blocked` for a verdict
that blocks under enforcement, and names a deny or `require_approval`
that warn mode lets through. The combined state and the exit code do not
change with that clause: warn mode never blocks. In the JSON,
`combined.policy` says only whether the policy blocks (`pass` or
`blocked`), and `combined.policy_verdict` holds the verdict.

The lesson checks read the working tree, not a supplied diff. When a
diff on stdin (MCP: the `diff` argument) names a changed file that the
verified snapshot does not hold, the line after the combined line says
so, for example `note: the lesson checks read the working tree, not the
supplied diff; 1 file of the diff is not in the verified snapshot
(src/todo.go)`. The note names three files and counts the rest; the
JSON lists every path in `combined.diff_paths_outside_snapshot`. The
diff of `git diff HEAD` gets no note, and neither does a repository with
no executable check (`lessons no-executable-checks`), because then no
check read the working tree. No applicable check keeps the note.

A failed audit append never changes the verdict or the exit code. When
the lesson-check audit record is not written and the command exits 0,
`check --lessons` prints `seamark: lesson-check audit: <error>` on
stderr, as `lessons --verify` does. MCP `check` ends its text with one
`note:` line per record that was not written, the policy audit record
and the lesson-check audit record.

A pre-write guard checks one native edit. A post-edit hook checks the
observed edit against its saved baseline; missing or mismatched evidence
stays qualified. Neither replaces the completion check above. Install
checks separately with `seamark init --client claude --lesson-checks check`
or the selected client's `enforce` mode. The advisory edit hook remains
fail-open and never becomes a guard.

### The lesson-check hooks

`seamark init --lesson-checks check` installs two handlers beside the
advisory edit hook: a baseline handler before each covered edit and a
post-edit handler after it. `--lesson-checks enforce` installs the guard
in place of the baseline handler. For Claude Code the handlers run on
`Edit`, `Write`, and `MultiEdit`, and the post-edit handler also runs on
`PostToolUseFailure`. For Codex they run on `apply_patch` with
`--client codex`. Each handler reads one native event from stdin, makes
no agent call, and uses no network.

| Command | When it runs | What it does | Exit status |
| --- | --- | --- | --- |
| `seamark lessons --baseline --hook` | before an edit, in `check` mode | stores the state of the edited paths before the edit in `.seamark/checks/pending/`; it never denies the edit | always 0 |
| `seamark lessons --guard --hook` | before an edit, in `enforce` mode | checks the proposed edit against the enforce pins, and stores the baseline when it allows the edit | 0 allows; 2 denies on a violation, on an edit that the check cannot evaluate, or on an invalid `lessons.yaml` |
| `seamark lessons --verify --hook` | after an edit | reads the baseline of the same tool call, checks the result on disk against every `check` and `enforce` pin in scope, reports the findings to the agent, and deletes the baseline; it never undoes the edit | always 0 |

A valid configuration without an enforce pin never makes the guard deny.
A tool outside the covered list passes the guard, and its record names
the cause. `--guard` and `--baseline` need `--hook` and combine with no
other `lessons` mode. `--verify --hook` takes no `--file` and no
`--json`, because it reads its paths from the event. Setup gives the
baseline handler and the guard a 10 s hook timeout, and the post-edit
handler 15 s.

A deny prints the reason on stderr, as the gate does. This is a guard run
on a Claude Code `Write` event that adds a forbidden marker:

```text
$ seamark lessons --guard --hook < write-event.json
seamark: blocked by policy: lesson check no-debug (no-added-pattern/1) of pin e16bd32b164e "keep-debug-markers-out": Write adds a line matching the pattern at api/a.go:3: // DEBUG_ONLY
$ echo $?
2
```

The post-edit handler gives its findings to the agent as
`additionalContext` in the hook reply on stdout. For the same edit, after
the write, the text reads:

```text
seamark post-edit lesson checks after this Write call: violation (1 evaluated pin: 1 violation, 0 unknown; snapshot eab4d48e46b5)
- violation: pin e16bd32b164e, check no-debug (no-added-pattern/1): api/a.go:3 adds a line that matches the pattern
The files stay as written; repair the violation in a later edit. Check again: seamark lessons --verify --file <root>/api/a.go
```

Each run appends one record to `.seamark/lesson-check-audit.jsonl`: a
`baseline`, `guard`, or `post-edit` record. The next handler run removes
a pending baseline that is older than one hour, and writes a
`baseline-discarded` record for it. The records hold digests of the session and
tool-call IDs, never the raw IDs.

The guard requests denial. Reviewed recordings verify native denial of
Claude Code `Write` and `Edit` (Claude Code 2.1.319) and of Codex
`apply_patch` (codex-cli 0.169.0) in the root and child contexts, with
one limit: the client lets the write run after its 10 s hook timeout.
Seamark cannot read the Codex trust record, so a Codex path still reads
`active-unverified`. Native denial of `MultiEdit` and the post-edit
reply remain unverified. Codex Update content is
currently unprojectable for a content predicate, so an armed guard denies that
unknown result. Verified Update projection and native evidence are
required before v0.9.0 can ship. See the
[integration guide](agent-integrations.md#lesson-checks-and-exploration-unreleased).

## Lesson delivery: lessons --delivery

This model-free reading asks which exact pin version Seamark delivered,
through which surface, and what the lesson checks recorded for it. It
reads the audit logs, not a conversation, so it does not judge whether
the pin works:

```bash
seamark lessons --delivery
seamark lessons --delivery --pin <pin-ref>
seamark lessons --delivery --pin <pin-ref> --json
```

JSON uses `delivery/1`. The overview has one reading for each pin entry
of the current `lessons.yaml`. A reading counts the records of that exact
pin version in `.seamark/lessons-audit.jsonl`, and its check and guard
records in `.seamark/lesson-check-audit.jsonl`. `--pin` selects one pin
by its pin-ref and lists its version; versions are never pooled. A
missing, ambiguous or duplicated reference fails instead of selecting
another pin. An absent, unreadable or over-limit audit is report data,
not a zero count. The report writes no durable state and never calls a
model; SQLite can still create empty WAL/SHM sidecars when it opens the
index read-only.

A record shows what Seamark emitted or left out. It does not show what a
model saw, or that the pin came before the agent built its call.

The text shows what the audit logs give. The JSON keeps the full
`delivery/1` field set, and the fields without a source in this build
hold fixed values. So every build writes the same fields:

- `capture` reads `unavailable`, and `as_of.archive` reads `absent`, each
  with a fixed message.
- `historical`, `unlinked` and `expanded.occurrences` are empty. The
  occurrence counts of each pin stay at zero, and its timing, facts,
  reasons, limitations and next steps are empty.
- In `coverage`, only `unknown_lane_records`, `events_without_pins` and
  `denied_call_injections` come from the audit. The other counts stay at
  zero.
- Each delivered session opens an observation window for that version.
  Every window reads `incomplete` with the fixed reason
  `session-not-captured`, because this build reads no evidence after the
  delivery. So `linked` and `no_linked_correction` stay at zero.

A fixed value is empty, zero, `unknown` or a fixed state, so it never
reads like a result. An incomplete window is not a success or prevention
rate, or proof that the agent obeyed.

The report keeps these units separate:

| Unit | Meaning |
| --- | --- |
| Records | Physical firing-log lines; one edit can write more than one. |
| Events | Surface invocations with a distinct event identity. |
| Delivered sessions | Sessions with an injected hook/exploration record of this version, excluding calls the guard denied. |
| Check operations | Retained verify, post-edit and guard audit records. |
| Pin evaluations | Executable pins evaluated inside those operations. |
| Guard attempts | Pre-write decisions, not edits or corrections. |

The text prints these six units. The JSON unit table also defines the
units of the fixed fields.

A requested deny is not a prevented write. The report separately shows
requested denies, later execution, no execution observed and verified
native paths. `prevented` additionally needs a written deny reply, no
later execution record and a verified native deny path. Today only the
Claude Code `Write` and `Edit` paths (from a recording of Claude Code
2.1.319) and the Codex `apply_patch` paths (from a recording of
codex-cli 0.169.0) are verified, so a deny of `MultiEdit` never counts
as prevented. These recordings also show the limit of these paths: the
client lets the write run after its 10 s hook timeout.

After the totals, the report states what selection applied:

```text
Selection budgets (current lessons.yaml; a past count can come from another budget):
  pin cap: requested 16, effective 8 pins per injection; pin_budget cannot raise the pin cap above the fixed total
  total cap: fixed 8 lessons per injection
  change budget: requested 6, effective 6 lessons per answer
  evidence read
  These counts show what Seamark emitted or left out. They do not show what a model saw, or that a pin came before an action.
```

"Requested" is the number that `lessons.yaml` asks for, or the default.
"Effective" is the number that selection applies. The budgets describe the
`lessons.yaml` of this read. A record stores no budget, so a past count can
come from another budget. With an invalid `lessons.yaml` the requested
value prints as `unknown`, and the effective value is the default.

Each pin version adds a split of its held-back records:

```text
held back by cause (records): hook/over-pin-cap 3, hook/restated 1
```

The keys are surface and cause. `restated` means that the restatement
collapse dropped the pin, `over-pin-cap` means a `pin_budget` below the fixed
total, and `over-total` means the fixed total on `hook` and `explore`, or
`change_budget` on `change_set`, `prepare` and `check`. A record with an empty
or unknown cause counts under `unknown`. `suppressed-repeat` and `emit-failed` are delivery
values with their own counts, not held-back causes. The counts are records,
and `held-back` equals the sum of the split. JSON holds the same reading in
`selection` and `held_back_causes`, both optional under `delivery/1`.

On the edit hook, a pin that the delivered set of the context names reads
`suppressed-repeat`, with no cause, and never a budget cause. A budget cause
still does not show that the receiver lacks the pin: an event with no delivery
state does not read that set, and an older record can read `held-back` for a
pin that the context already had.

When the delivery audit is absent, unreadable or over its limit, a zero
would state nothing. The evidence reading, the delivery totals and each
pin's `delivery records:` line then print `unknown (audit <state>)` and no
count, and the split is absent. The budgets stay, because they read
`lessons.yaml`. In an audit that reads, a zero means that no readable record
names the version. The `delivery audit:` line at the top names the state.
There is no per-recipient view.

## Is it working? lessons --stats

Every ambient surface appends a line to `.seamark/lessons-audit.jsonl`
when it surfaces a lesson — the impact/decay counterpart to the gate's
audit log. Edit-hook records also carry the rendered context byte count,
delivery status, context generation, and repository-scoped SHA-256 digests of
the provider session and tool-use match; raw provider identifiers are never
persisted or made correlatable across repositories. These fields make repeated
delivery and opt-in suppression measurable. Current records also name the
`client`, the `mechanism` that carries the reminder (`pre-tool-use-context`
for the edit hook, `post-tool-use-context` for the exploration hook), and a
digest of the receiving context when the client reports one. A record
written before these fields existed names no client, and
the statistics keep it that way instead of guessing: a repeat is counted only
among records of one client and one receiving context, never across the
records from before and after these fields. An edit operation on
several files is one record with every path and one byte total. A record says
that a reminder was emitted, never that the agent followed it. The log is
owner-only (0600), and the writer refuses a symbolic link at the log or at
`.seamark` ([Configuration](configuration.md#verification-state-and-report-inputs)).

This release writes record version 2 (`schema_version: 2`). The new keys
follow the earlier keys, which keep their names, order, and meaning, so an
older seamark and `lessons --stats` keep their counts of the earlier record
kinds; the new `explore` and `prepare` surfaces add counts of their own. One
run of a surface is one event with a random `event_id`, and every record of
the event carries it. A record names the digest and status of the
`lessons.yaml` that it evaluated and every matched pin by its identity digest and
exact version, with what happened to the pin: `injected`, `returned`,
`suppressed-repeat`, `held-back` (with the cause: restated, over the pin
budget, or over the total), or `emit-failed`. Edit-hook and exploration
records add a digest of the turn, the lane (`root`, `child`, or `unknown`),
and whether the receiving context is resettable; preparation, `change_set`,
and `check` records name no receiver. Two record kinds need the recording
switch: the hook process must run with `SEAMARK_RECORD_EVALUATIONS=1` in its
environment. The native recording script (`make native-record`) sets
it for its paid runs. A hook process sees it only when the client that
starts the hook passes its own environment on. The two kinds are an
`evaluation` record when a
hook emitted nothing (no match, a rejected path, a failed selection or reply,
or, for a read, only lessons that the context already got), and a `reset`
record when `PostCompact` resets a receiving context. Without the switch,
the hooks write the same record kinds as before, with the new keys.
Records written with the switch on say so (`record_evaluations`), because a
missing `reset` record shows that no compaction happened only between two
such records. Raw session, agent, tool-use, and turn identifiers are never
stored.

`seamark lessons --stats` turns
the log into which lessons the surfaces delivered, split by surface in
the first line (a
`change_set` plan, a preparation, and a CI `check` are exposure, not edits
reminded), and which
*would* surface but never have (a lesson whose region no edit touches is a
pruning candidate). The first line names the `explore` and `prepare` counts
only when the log holds such records, so the line of an older log stays the
same. For
instrumented hook records it also reports injected context bytes, repeated
in-session injections, and suppressed repeats. Per-lesson rankings keep actual
delivery and matching opportunities separate: after one injection and three
suppressed edits, a lesson reads `×1 delivered / ×4 matched` rather than looking
as cold as a lesson that matched only once. Rows that tie on matches, deliveries,
and the last firing time print by region, then symptom, so two runs on one log
print the same order:

```text
$ seamark lessons --stats
lesson firings — 128 hook reminders, 31 change_set, 12 check — across 24 files

hook delivery — instrumented: 128 injected (84 repeated), 0 suppressed; context: 57344 bytes
  claude via pre-tool-use-context: 96 injected (61 repeated), 0 suppressed; context: 43008 bytes
  no client recorded: 32 injected (23 repeated), 0 suppressed; context: 14336 bytes

most surfaced
  ×41  scripts                                  last 2026-07-26  E702
  ×18  scripts                                  last 2026-07-26  RUF001
  …
never fired — 7 lessons in regions no edit has touched (decay candidates)
  tests                                    E741
  …

pin outcomes — 3 measured: 1 working, 1 not landing, 1 untested
  p47   leak-exception-to-client           not landing — recurred 2× since exposure (fired 12×)
  p16   pooled-state-reset                 working — flagged 10× in ~200 region-commits before exposure; 0× in 84 since (fired 41×)
  p9    cap-per-request-query              untested — 3 region-commits since exposure (fired 5×)

mcp tool usage — calls: 142, failed: 3 (counts calls, not whether a call helped)
  change_set   claude-code          calls: 88, failed: 1; latency median 120 ms, largest 900 ms; reply median 5120 bytes
  why          claude-code          calls: 54, failed: 2; latency median 40 ms, largest 310 ms; reply median 1890 bytes
```

The `mcp tool usage` section reads `.seamark/mcp-usage.jsonl` and its one
rotated file, in the workspace that `seamark mcp` serves with the same `-C`
and `--db`. A linked worktree thus shows its own calls, also when its index
records the main checkout. The MCP server appends one line per `tools/call`:
the tool name, the client name from `initialize`, the time, the latency, the
size of the text parts of the reply, and whether the call worked. A line
holds no argument, path, symbol, query, or reply text, because a query can
hold private text. A tool name that the server does not know reads `unknown`,
and so does a client without a name. Recording is best effort: a failed
append changes no reply, and the server creates no `.seamark` directory for
it. The server writes one note about failed appends to stderr after its last
reply, because a stderr write during a session can stop the server. The file
rotates at 5 MiB or 30 days, as `.seamark/audit.jsonl` does, so the section
covers the retained calls only. Without a file the section does not
print and the rest of the output stays the same. A damaged line is skipped
and counted, and a file that does not read prints `unknown`, not zero. A
count shows calls. It does not show that a call helped, and no hook reads
this file.

### Do pins actually change behavior? The outcome loop

The finding-based outcome below is independent of lesson delivery.
The outcome proves no cause.

Firing counts measure *exposure*: a surface returned or injected the pin.
The `pin outcomes` block measures *effect*: did the pin's mistake recur
after its first firing? For every applied pin, seamark joins three
things it already stores — the firing log (the first firing of the pin
on any surface), the finding table (did the same mistake get flagged or
fixed again after that), and mined history (how many commits touched
the pin's regions since, so silence in a quiet region is never read as
success). Everything is recomputed on each run; there is no model
call, no score, and no new state.

With `once-per-context`, outcome sentences continue to start exposure at the
first delivered firing, but also show total matches when repeats were suppressed
(for example, `delivered 1×; matched 4×`). This keeps “delivered” distinct
from “the edited region matched the pin.”

Three verdicts, each a sentence you can check against your own repo:

- **`not landing`** — the pin fires and the mistake recurred anyway.
  This is the actionable class: the ledger suggests rewording the
  note, pruning, or promoting the pin toward a machine-checked rule.
- **`working`** — flagged N times before exposure, zero since, across
  enough region commits to mean something. Validation, not a removal
  signal: the pin may be the reason the mistake stopped.
- **`untested`** — no verdict yet, and the sentence says why: the pin
  never fired, too few commits touched its regions (fewer than 5),
  the finding corpus was not re-mined after exposure, or every cited
  finding has aged out of the mining window. For stale evidence, a
  `seamark index --reviews` run whose GitHub fetch succeeds ends the
  state, because the older of the review and fix mine times bounds the
  evidence. When origin names no GitHub repository, seamark mines no
  review findings, so `seamark index --fixes-only` or `--reviews` ends
  it, and the verdict rests on fix findings alone.

The exposure clock starts at a pin's **first firing** on any surface,
not its apply date: a pin that no surface ever returned cannot have
changed behavior. The edit hook and the exploration hook inject a pin
into an agent context. Preparation, `change_set`, and `check` return it
and name no receiver. So a CI `check` or a `lessons --prepare` run that
no agent reads also starts the clock, and a pin that the edit hook never
delivered can read `working` or `not landing`. The `fired N×` count adds
the firings of every surface. Rewording
a pin's note restarts its clock (a different reminder is a different
treatment); reordering its regions or retouching case does not.

## Reviewing it all: seamark report

Agents read the compact text reports over MCP. The person who has to
decide what the repo should actually *remember* needs a different
surface — everything at once, in one place:

```bash
seamark report            # → .seamark/report.html
seamark report --open     # ...and open it
seamark report -o - > /tmp/audit.html
```

One self-contained HTML file — no server, no assets, nothing fetched
when it is opened — so it can be attached to a pull request or read on a
machine with no network. Four sections:

- **Decision queue** — every proposal awaiting `--apply`/`--dismiss`,
  with the findings it cited (click through to the original review
  comment), the commands that decide it, and each card's evidence
  health: the confidence tier with its facts, the prompt-era note, the
  outcome verdict for applied pins (`not landing` highlighted — it is
  the one that needs action), and a `regions now:` drift line with the
  `--retarget` command when today's inference disagrees. A row whose
  regions a person chose reads that line as a suggestion with the date of
  the choice, and it prints no command. The header
  stats row carries the aggregate: how many pins were measured, and
  how many are not landing. The evidence-mix line says what it all
  rests on: *469 review · 34 fix:conventional · 15 fix:subject*.
- **Near-duplicate pins** — which *applied* pins restate a theme already
  pinned. This is the audit that found 17 redundant pins out of 65 in a
  real repo, and the reason `lessons --prune` exists.
- **Hotspot map** — the files seamark parsed, grouped by directory: area
  is how much history a file carries, colour is what share of its recent
  commits were corrections. Same fix-density definition `why` prints, so
  the two surfaces never disagree. Click a cell to filter the lessons
  below to that region.
- **Lessons** — every mined lesson including the one-offs, filterable
  and sortable. The raw material for tuning `lessons.yaml`.

The page is a snapshot: it stamps the index state it was built from,
warns inside the file when the workspace has moved on since, and says so
whenever a list was truncated. All quoted text — review comments, commit
subjects, model-written notes — is escaped, never rendered as markup.
