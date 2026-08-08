# The learning pipeline, in depth

How seamark turns review history into rules agents actually follow —
the full detail behind the README's short version. The vocabulary used
here is defined once in the README and holds everywhere: **finding →
lesson → proposal → pin**.

## What gets mined, and what deliberately does not

`seamark index --reviews` fetches pull-request review comments through
your authenticated `gh` CLI and clusters the recurrences:

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
carry.** The same `index --reviews` pass mines them whenever they exist,
purely from local git — no GitHub needed at all: commits classified as
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
fix density  9 of the last 20 commits here were fixes
```

phrased over a recent window so it decays as calmer history accumulates.

The two sources degrade independently: review mining needs `gh`
authenticated and a github.com remote; fix mining works offline on any
remote. A failed mine (offline, logged out) fails safe — it keeps the
lessons already stored rather than clearing them. Lessons refresh only
on the review cadence: a normal `seamark index` (and every agent tool
call) leaves them untouched rather than re-hitting the network.

## Tuning what surfaces: lessons.yaml

A committed `.seamark/lessons.yaml` controls what shows — applied at
surface time, so edits take effect with no re-mining:

```yaml
threshold: 2                     # min recurrences to surface (default 2)
pin_budget: 3                    # pins the edit hook injects per edit (default 3)
change_budget: 6                 # lessons one change_set answer carries (default 6)
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

Pins are powerful, so their injection cost is budgeted: the edit hook
carries at most `pin_budget` pins per edit (default 3), ranked by
evidence confidence first and region specificity second — a weak pin
must not hold a slot a strong one wants, and among equals a pin on the
file beats its package beats a repo-wide `*` — with a `+N more` pointer
for the rest. Deliberate views (`--file`, `why`) always show
everything; only the ambient injection is capped.

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
`expand lessons:<dir>`, and `why` advertises the ref whenever it shows
lessons. Reading a package's raw findings side by side is how a pattern
too varied for exact clustering gets spotted (ten differently-worded
"reset pooled state" findings are one lesson to a reader), whether the
reader is you or an agent you point at it — and the ledger's footer
tells that reader what to do with a spotted pattern: propose a pin for
review, never self-add it.

## Distilling patterns: plan → apply

Exact clustering can't see that ten differently-worded findings are one
mistake. `seamark lessons --distill` can: it batches the raw findings
into candidate groups and asks **your own agent CLI** (`claude` by
default — seamark holds no API keys) to name what recurs, as proposed
pins. It is an optional accelerator, nothing more: every entry it drafts
is one you could write by hand in the same file, and repos without an
agent CLI (or without the appetite for tokens) simply skip it.

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

decide: `seamark lessons --apply p3,p7` (or a range: p1..p9) pins them; `--dismiss` remembers the no
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

A proposal's region is computed from its cited evidence, never taken
from the model's reply — and it is a **set** (`region: api` plus
`regions: [api, db]`, at most three directories, depth at most three),
chosen to cover at least 80% of the cited *events*. Events vote, not
findings: six comments on one pull request are one voice. Test and doc
paths abstain (a fix's test usually out-churns the fix — the vote
belongs to the code it corrected; a lesson whose entire evidence is
test files keeps its test region), root-level files never drag a theme
repo-wide, and when the voting evidence is genuinely scattered the
honest answer stays `*`. Measured on the two development corpora, this
cut repo-wide pins from 35 of 65 to 3 — every one of which used to tax
the injection budget of every edit in the repository.

### How much to trust a pin: confidence

Every distilled pin carries a tier — **strong / fair / weak** —
recomputed on each read from what its citations still support: distinct
events (a review comment and the fix commit answering it count once),
source corroboration (review AND fix beats either alone), recency, and
whether the cited files still exist in the workspace. Nothing is stored
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
        working — flagged 4× in ~120 region-commits before exposure; 0× in 31 since (fired 9×)
        regions now: workers
  p47   leak-exception-to-client           svc/api
        not landing — recurred 2× since exposure (fired 12×)

not landing — p47 fire but the mistake recurs; escalation is yours:
reword the note, raise the pin, or graduate it to a check

retarget: `seamark lessons --retarget p65,p62,…` updates those pins to
the recomputed regions (lessons.yaml and the ledger together)
```

`--retarget` is the upgrade path for pins distilled before region sets
existed. On any ordinary failure the two halves move **together or not
at all**: the ledger updates in one transaction, and if either the
file write or that transaction fails, lessons.yaml is restored to its
original bytes. The one exception is a hard crash in the narrow window
between the file write and the ledger transaction — the file can then
be ahead of the ledger, and the next `--retarget` detects exactly that
state and repairs it with a ledger-only update. Re-running is always
safe: "regions already current" is the steady state.

The same ledger also flags near-duplicate clusters among applied pins
and hands you the `--prune` command to retire the redundant entries —
see "One theme, one pin" above for how that audit works and why
pruning is not dismissal.

### Proposal-only by construction

The model must cite the finding ids behind every pattern (uncited
patterns are dropped — it cannot invent evidence), regions are computed
from the cited files, and nothing reaches `.seamark/lessons.yaml` except
through an explicit `--apply` of explicit ids. Even then, seamark edits
the file itself only if `config.yaml` opts in (`distill: {write: true}`)
— otherwise apply prints the pin block for you to paste. Applied entries
are inserted under your existing `pin:` section with provenance
comments; everything hand-written stays byte-for-byte. `--prune` and
`--retarget` obey the same write gate, remove or replace each entry
with its provenance comment and nothing else, and refuse to write at
all unless the result still parses with every other pin intact.

## The moment of change: hook, change_set, check

Three ambient surfaces inject the memory exactly when it can still
change the outcome, each budgeted (an injection is someone else's
tokens) and each deduplicated — a mined lesson whose whole cluster an
applied pin already cites is suppressed rather than said twice, and two
pins wording one theme never spend two slots:

- **The edit hook** (`lessons --hook`, wired by `seamark init`): per
  file, at most `pin_budget` pins (default 3, confidence-ranked, a
  `+N more` pointer for the rest) plus the file's recurring mined
  lessons. Offline, silent when there is nothing to say.
- **`change_set` (MCP)**: before a multi-file edit, the union of the
  files' lessons under `change_budget` (default 6) — merged by
  identity, ranked by confidence across the whole set, regions shown as
  the map back onto the files. New files count: a brand-new file in a
  pinned region gets its guidance before its first line exists.
- **`check`** (CLI and MCP): after the policy verdict, the touched
  files' lessons as a clearly-marked advisory — printed even when the
  verdict blocks, because a deny is exactly when they matter; `--json`
  stays verdict-shaped.

All three record what they surfaced to the firing log, tagged by
surface, so the stats below never conflate a speculative plan with an
actual pre-edit reminder.

## Is it working? lessons --stats

Every ambient surface appends a line to `.seamark/lessons-audit.jsonl`
when it reminds an agent — the impact/decay counterpart to the gate's
audit log. `seamark lessons --stats` turns that into which lessons
actually reach agents, split by surface (a `change_set` plan and a CI
`check` are exposure, not edits reminded), and which *would* surface
but never have (a lesson whose region no edit touches is a pruning
candidate):

```text
$ seamark lessons --stats
lesson firings — 128 hook reminders, 31 change_set, 12 check — across 24 files

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
```

### Do pins actually change behavior? The outcome loop

Firing counts measure *exposure* — a pin reached an agent. The `pin
outcomes` block measures *effect*: did the pin's mistake recur after
agents started seeing it? For every applied pin, seamark joins three
things it already stores — the firing log (when the pin first reached
an agent), the finding table (did the same mistake get flagged or
fixed again after that), and mined history (how many commits touched
the pin's regions since, so silence in a quiet region is never read as
success). Everything is recomputed on each run; there is no model
call, no score, and no new state.

Three verdicts, each a sentence you can check against your own repo:

- **`not landing`** — the pin fires and the mistake recurred anyway.
  This is the actionable class: the ledger suggests rewording the
  note, pruning, or promoting the pin toward a machine-checked rule.
- **`working`** — flagged N times before exposure, zero since, across
  enough region commits to mean something. Validation, not a removal
  signal: the pin may be the reason the mistake stopped.
- **`untested`** — no verdict yet, and the sentence says why: the pin
  never fired, too few commits touched its regions (fewer than 5),
  the finding corpus was not re-mined after exposure (run `seamark
  index --reviews`), or every cited finding has aged out of the
  mining window.

The exposure clock starts at a pin's **first firing**, not its apply
date — a pin no agent ever saw cannot have changed behavior. Rewording
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
  `--retarget` command when today's inference disagrees. The header
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
