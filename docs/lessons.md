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

Just as important is what is *not* a lesson. Thread replies are
conversation about a finding, never the finding — mining them once made
an author's "fixed" the top lesson of a real repo. Comments with nothing
actionable ("Very smart!", a bare link) are dropped rather than
clustered. And a linter code only counts when actually cited: not when a
matching token appears in quoted tool output (`rg -A10` once minted a
fake "A10" lesson from a bot's analysis script), and not in a repo that
doesn't contain the linter's language at all.

## Fix commits are findings too

Review quality varies; **fix commits exist in every repository.** The
same `index --reviews` pass also mines them, purely from local git — no
GitHub needed at all: commits classified as fixes by explicit intent
(`fix:` subjects, `fixes #N` links, `Revert` commits; never substring
matches — "prefix" and "fixture" don't count), minus the ones that teach
nothing (typo/lint/CI chores, 30-file bulk refactors), minus cherry-pick
duplicates (patch identity: a backport is the same event) and fixes that
were later reverted. Each surviving fix becomes a finding whose body
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
mute:
  - rule: F541                   # hush a noisy rule everywhere
  - region: alembic/versions     # …or every lesson under generated code
pin:                             # your "must not be ignored" list —
  - rule: RUF001                 # surfaced always for its region, even if
    region: scripts              # mining found it once or never
    note: "Keep scripts ASCII — smart quotes have bitten us"
```

`mute` kills noise; `pin` is the escape hatch for a rule you care about
more than the mined frequency implies — written by hand, exactly like
policy.yaml rules (the distiller can draft them, but never has to). Both
flow through `why`, `orient`, and the edit hook via one path, so they
never disagree.

Pins are powerful, so their injection cost is budgeted: the edit hook
carries at most `pin_budget` pins per edit (default 3), most specific
region first — a pin on the file beats its package beats a repo-wide
`*` — with a `+N more` pointer for the rest. Deliberate views (`--file`,
`why`) always show everything; only the ambient injection is capped.

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

### Proposal-only by construction

The model must cite the finding ids behind every pattern (uncited
patterns are dropped — it cannot invent evidence), regions are computed
from the cited files, and nothing reaches `.seamark/lessons.yaml` except
through an explicit `--apply` of explicit ids. Even then, seamark edits
the file itself only if `config.yaml` opts in (`distill: {write: true}`)
— otherwise apply prints the pin block for you to paste. Applied entries
are inserted under your existing `pin:` section with provenance
comments; everything hand-written stays byte-for-byte. `--prune` obeys
the same write gate, removes each entry with its provenance comment and
nothing else, and refuses to write at all unless the result still parses
with every other pin intact.

## Is it working? lessons --stats

The edit hook appends a line to `.seamark/lessons-audit.jsonl` each time
it reminds an agent — the impact/decay counterpart to the gate's audit
log. `seamark lessons --stats` turns that into which lessons actually
reach agents, and which *would* surface but never have (a lesson whose
region no edit touches is a pruning candidate):

```text
$ seamark lessons --stats
lesson firings — 128 edits reminded across 24 files

most surfaced
  ×41  scripts                                  last 2026-07-26  E702
  ×18  scripts                                  last 2026-07-26  RUF001
  …
never fired — 7 lessons in regions no edit has touched (decay candidates)
  tests                                    E741
  …
```

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
  comment) and the commands that decide it. The header line says what
  the evidence rests on: *469 review · 34 fix:conventional · 15
  fix:subject*.
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
