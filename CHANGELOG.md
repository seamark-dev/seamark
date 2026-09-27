# Changelog

Notable changes, newest first. Every tagged release attaches
smoke-tested archives for macOS and Linux (amd64/arm64) and a
`SHA256SUMS` file; verify a download with
`sha256sum -c --ignore-missing SHA256SUMS` (on macOS:
`shasum -a 256 -c --ignore-missing SHA256SUMS`).

## Unreleased

- **Agent setup guide and reusable compatibility checks.**
  [docs/agent-integrations.md](docs/agent-integrations.md) explains Codex
  setup and trust, optional skills and tool grants, selecting a CLI for
  inference, API-key authentication, removal, and adding a new adapter.
  `make agents-native-check CLIENT=codex` checks the installed CLI without
  a model: flags, patch parsing, sandbox behavior, final output, provider
  errors, and generated MCP configuration. `make smoke` now checks the
  generated Codex hook commands in CI. Documentation tests check command
  and flag names. The contract tests take the test-only third client
  through the real setup coordinator, the shared delivery service, the
  gate helpers, the inspection, and the distillation pipeline, so a
  contributed adapter is proven against the engines it will run in. `make agents-native-smoke CLIENT=codex` runs one
  bounded distillation with the operator's own login. It passed on
  codex-cli 0.157.0 with GPT-6 Luna / low reasoning; the guide records
  its scope and the remaining native verification gaps.
- **One account of every agent across `init`, `doctor`, and `status`.**
  The three commands now read one registry inspection per client:
  skills, MCP registration, tool grants, and the edit, gate, and reset
  hooks, each with its configuration state (absent, current, partial,
  conflict, unreadable), the project trust seamark can read, and the
  recorded native evidence (verified on a named version and surface,
  pending, or not natively verified). `status --json` adds a `clients`
  array with those typed fields by name and a `distill_client` field
  naming the invoker; every existing field keeps its meaning.
  `status` prints a `clients` block (hooks and registration per client,
  the native evidence, and each limitation the adapter reports, such as
  Codex trust that seamark cannot read and reminders that repeat), and
  its `gate` line covers every client with a gate hook: an enforcing
  Codex hook beside a warn Claude Code hook reads "enforce for codex …
  the claude hook follows policy mode warn". `doctor` reports `hooks`,
  `mcp`, `skills`, and `approvals` per client on the same lines as
  before, prints one line per client with the evidence and limitations
  (`[features] hooks = false` in `.codex/config.toml` is a warning:
  the installed hooks never run), names the invoker on the `agent`
  line, and takes every corrective command from the adapter that
  installs the artifact. A skill directory two clients share is one
  entry that names both consumers everywhere. `seamark init --help`
  lists what seamark supports for each client from the same
  descriptors. Hook evidence is one model for setup and inspection:
  every definition of a hook, owned or wrapped, in the shared file, the
  personal `.claude/settings.local.json`, or the inline Codex
  `[hooks]`, with the tools the agent runs it for, whether the shell
  certainly runs it, and its gate mode; a hook that covers some tools
  is partial wherever it lives, and the action names the file that
  holds it; a wrapped enforcing gate in the personal file enforces, a
  wrapper that runs both modes enforces, and a managed warn hook beside
  an enforcing inline hook reads as enforce everywhere
  (`managed_gate_mode` keeps what setup owns). `init --client claude`
  reports a tool as uncovered only when no definition in either file
  runs the hook for it. A credential inside a hook command is redacted
  before it reaches a detail, a finding, JSON, or the MCP resource.
  `init --client codex` warns when `[features] hooks = false` turns
  the hooks it installs off, and repeats the trust note for kept hooks.
  Inspection stays offline and read-only: no client binary runs and no
  login starts.
- **`seamark init --client <name>`.** Selects the agents to set up, by
  name, and may repeat (`--client claude --client codex`). Each selected
  agent gets what seamark supports for it: its hooks and its MCP server
  registration, with `--skills` and `--approve-tools` still opt-in. For
  Claude Code the registration is a `seamark` entry in `.mcp.json`; for
  Codex it is the `[mcp_servers.seamark]` table without any approval. An
  agent that is not selected is never read or written, a shared
  `.agents/skills` directory never counts as a configured Codex, and a
  skill directory that two selected agents share is written once. What an
  agent does not support yet is reported, and native trust stays the
  user's decision. Without `--client`, init
  output and written files are unchanged. `--client` takes a bare
  `--skills` only.
- **One setup path.** Both init forms now plan every file before the first
  write and apply through one coordinator: a changed input stops the run
  with "run the command again" instead of applying a stale plan, files are
  replaced through a temporary file, and a failed write reports what
  landed and what did not. A re-run converges.
- **No write through a symbolic link, for the scaffolds too.** init still
  reads `.gitignore` through a link and still keeps an existing
  `.seamark/*.yaml` whatever it is. It now refuses to create a scaffold,
  or to extend `.gitignore`, when a component of the path is a symbolic
  link, the rule the client files already followed: a link committed in a
  cloned repository must not redirect a write outside the tree. A
  repository that links `.seamark/` elsewhere and lacks a starter file
  must create that file by hand.
- **`--client` looks at every hook source.** With `--client claude`, a
  seamark hook that `.claude/settings.local.json` also runs is reported
  as running twice, with the tools it overlaps on and a differing gate
  mode named. The shared `.claude/settings.json` always gets every hook:
  it is the file a team commits, so it never depends on the personal file
  of whoever ran init. An init without `--client` never reads the local
  file, as before.
- **`--skills` reads like a boolean with modes.** `--skills=true` equals a
  bare `--skills`, and `--skills=false` or an empty value installs
  nothing.

- **One delivery path for the edit hook.** `lessons --hook` and
  `lessons --hook-reset` now run through a shared delivery service with a
  Claude Code adapter in front of it: the adapter translates the native
  event and reply, and the service owns selection, once-per-context
  state, emission order, and the firing log. For a Claude Code edit the
  reminder is byte-identical to before; the two entries below change the
  state file and add fields to the log. The
  service already handles one edit operation on several files under one
  budget (`pin_budget` pins, eight lessons in total) with one log record;
  no shipped agent sends such an event yet. Three edge cases change on
  purpose:
  - A file outside the workspace gets no reminder. It used to receive
    the repo-wide (`*`) pins, and its absolute path went to the log. The
    workspace root itself and a path behind an unreadable directory
    count as outside.
  - A relative `file_path` resolves against the event's `cwd`, and a
    path behind a symbolic link resolves to the real file. A link that
    leaves the workspace gets no reminder. A workspace reached through a
    link (`/tmp` on macOS) now gets its real lessons; it used to get the
    repo-wide pins only.
  - The hook no longer creates `.seamark/index.db` in a workspace that was
    never indexed. The pins of `lessons.yaml` are still delivered there.
    A database that exists and cannot be opened still means no reminder.

- **Suppression follows the receiving context.** `hook_delivery:
  once-per-context` now keys its state by the client and the receiving
  context that the client's adapter reports, not by the session string
  alone. Two clients that report one session string no longer share an
  entry, and a client that cannot name the receiver or has no reset event
  for it gets repeated delivery. For Claude Code the main conversation is
  still the session. An edit inside a subagent (the event carries
  `agent_id`) is now its own context, and it has no reset event, so a
  subagent gets the reminder on every matching edit. Before, a lesson that
  the parent conversation already got could stay hidden from its subagent.
  `.seamark/lessons-hook-state.json` moves to version 2. A version 1 file
  reads as empty, so each lesson already delivered can be shown once more
  per context after the upgrade. An older seamark that meets a version 2
  file delivers every reminder and leaves the file alone; after a
  rollback, delete the file to get suppression back.
- **The firing log names the client.** New edit-hook records carry `client`,
  `mechanism`, and `context_sha256` (a repository-scoped digest, never the
  raw identifier). `seamark lessons --stats` adds one line per recorded
  client under the hook-delivery line; records that name no client are
  listed as such and are never assigned to one. Existing fields keep their
  meaning, and an older log renders as before. Repeat counts never join a
  record that names a client with one that does not, so the first reminder
  after the upgrade counts as new in a session that was already running.

- **Codex lesson hook.** `seamark init --client codex` now writes
  `.codex/hooks.json`: a `PreToolUse` hook on `apply_patch` that runs
  `seamark lessons --hook --client codex`. The
  hook reads the patch text for its complete file set (add, update,
  delete, and both ends of a move) and never runs it; a patch it cannot
  read completely gets no reminder instead of a guessed one. One patch is
  one reminder under the hook budget and one log record. Other hooks in
  the file stay. Setup keeps one handler per hook: when a command in
  `hooks.json` or inline `[hooks]` in `.codex/config.toml` already executes
  the seamark hook, setup installs no second handler and names that
  definition. It reads a hook command the way a shell does, so a command
  that only prints the seamark command is no handler; when another program
  gets the seamark command as arguments and setup cannot tell, it installs
  the managed hook and names the command. No context-reset hook is installed: a Codex reset has nothing
  to clear, and a hook without an effect still costs a trust review.
  Codex must still be
  told to trust the project and the hooks (`/hooks`), and setup says so.
  The adapter reads no receiving context from a Codex event yet (the
  event names a subagent by `agent_id`, but a reset inside a subagent is
  unverified), so `once-per-context` does not apply there and reminders
  repeat. `lessons --hook` and `--hook-reset`
  take `--client`; without it the event is a Claude Code event, as before.
  The patch reader follows the parser of codex-cli 0.154.0 and is tested
  against what that parser did with forty patch texts, offline. A patch
  that names an environment (`*** Environment ID:`) gets no reminder until
  a live capture shows where its paths are rooted. A native run of
  codex-cli 0.154.0 (2026-09-21) observed the lesson hook end to end on
  the recorded surface.
- **`agent.cli: codex`.** Both inference consumers, `lessons --distill`
  and `lessons --extract-triggers`, can run Codex: `codex exec --ephemeral
  --sandbox read-only -C <root> --skip-git-repo-check --ignore-rules -c
  features.hooks=false -`, the prompt on stdin, the final message as the
  reply, proposals marked `codex/…` in provenance. `--ignore-rules` keeps
  an inherited execpolicy allow rule from lifting a command out of the
  read-only sandbox. A failed Codex run reports the error after its
  version banner, not the banner alone. The command is resolved
  through the client registry, so `--dry-run`, `doctor`, and `status`
  disclose it without the binary and never start it; a missing binary is
  an error before any work. `agent.argv` still wins and keeps the caller's
  working directory; `claude` stays the default. The preset has not run
  in a live Codex session yet.
- **The Codex command gate.** `seamark init --client codex` also writes a
  `PreToolUse` hook on Codex's `Bash` tool that runs
  `seamark gate --hook --client codex`. It is the same gate: warn until
  `--gate-mode enforce`, exit 2 with the reason on stderr to block, fail
  closed under `--enforce` on a malformed payload or a broken policy, and
  the policy file's own `mode: enforce` blocks through a warn hook. A
  Codex `apply_patch` event that a widened matcher sends to the gate is
  refused and never parsed as a shell command; under `--enforce` the
  refusal blocks and names the tool. A `require_approval` verdict blocks
  like a deny, because Codex parses a native "ask" reply and does not
  support it yet. Without `--gate-mode`, each selected client keeps its
  own installed mode; the run's gate line takes enforce when any selected
  client's hook enforces, and names a selected hook that still runs
  without the flag, also one setup does not manage. A hook definition
  counts as a handler only where the client runs it for the hook's tool:
  a gate command under another event, or under a matcher that never fires
  for `Bash`, no longer stops the install of the managed gate hook. The
  plain `seamark gate --command` and the Claude Code hook are unchanged.
  The generated Codex hook has not blocked a command in a live session
  yet.

- **Setup no longer takes a wrapped hook for its own.** A hook command such
  as `/opt/wrapper /usr/local/bin/seamark gate --hook`, or a shell condition
  in front of the seamark path, ends like seamark's own command. Setup
  rewrote it to the bare command and the wrapper was gone, without a word.
  Setup now owns only a command that is one seamark executable plus its
  arguments. It keeps any other form as written and reports it. In
  `.claude/settings.json` the managed hook is still added, the report says
  that the handler runs twice, and a wrapped gate hook that enforces shows
  in the gate line of the run.

## v0.6.0 — 2026-09-05

- **The MCP server states judgment rules, not a ritual.** The `initialize`
  instructions and the `onboard` prompt now say when each tool earns its
  cost: `change_set` before editing more than one file or an unfamiliar area,
  `why` for a load-bearing symbol, `orient` only when the area is unfamiliar,
  `check` on the diff (new files staged first) before reporting completion,
  `expand` only for a needed ref. Co-change means "usually", and missing or
  unindexed evidence never means safe.
- **Agent skills, opt-in.** Three Agent Skills (`seamark-understand-repo`,
  `seamark-plan-change`, `seamark-review-change`) ship inside the binary and
  install with `seamark init --skills` into `.claude/skills/` and, when
  `.agents/` exists, `.agents/skills/`. The installer refreshes only
  directories that carry seamark's ownership marker, never writes through a
  symlink, and previews with `--print`. `seamark doctor` and `seamark status`
  report installed, stale, foreign, and unreadable copies; the release smoke
  proves the embedded tree ships; `make skills-validate` runs Claude Code's
  validator locally.
- **`seamark init --approve-tools`.** Merges exact Claude Code allow rules
  for the five MCP tools and the three skills into `.claude/settings.json`,
  because a skill's own `allowed-tools` grant lasts one turn and, in the
  Claude Code version tested (2.1.257), applied only when the user invoked
  the skill by name, although the documentation says it covers both. For
  Codex it appends the `seamark mcp` registration and `approval_mode =
  "approve"` for exactly the five tools to `.codex/config.toml`, preserving
  every existing byte and reporting conflicts instead of replacing them.
  Additive, idempotent, previewable with `--print`, and independent of
  `--skills`. `seamark doctor` and `seamark status` report both clients'
  approval configuration. The Claude Code rules are spelled with the server
  name `.mcp.json` registers, a server-wide `mcp__seamark` rule counts for
  every tool, and a rule under `permissions.deny` or `permissions.ask` is a
  reported conflict, never counted as approved. Codex approvals follow the
  `.codex/` directory or an explicit `--skills=codex`, with or without
  `--skills`; a `[mcp_servers.seamark]` table left without its `command`
  is completed in place instead of being reported as another command,
  unless it carries an explicit setting such as `enabled = false`; a
  header-shaped line inside a nested array no longer hides an inline
  server table from the layout check; a registration with zero approvals
  is partial for both clients, with the re-run hint; and an unparseable
  `.mcp.json` is reported on the approvals line, because the server name
  in it spells every rule.
- **`check` names the companions the diff left out.** After the verdict,
  `check` (MCP tool and `seamark check`) prints `history suggests also
  reviewing`: the files that usually change with the diff's files and that
  the diff leaves untouched. The first skills cohort showed that a review
  pass could not catch a forgotten companion because nothing in `check`
  looked at co-change; now the omission history can see is on the screen
  where the review happens.
- **Companions come with their reason.** In `change_set` and `check`, each
  suggested partner carries the planned file it shares the most commits
  with and, when history has them, the functions those commits touched
  there and the latest fix recorded on it (`last fix here: <subject>
  (<commit>)`). Ties on shared commits and lift break in favour of the
  partner outside the planned files' directories, the one a plan forgets.
  A bare file name at lift 1.3 was the weakest line on the screen in the
  first cohort; the reason is what makes it a question the agent answers.
  The reasons are computed for the whole list at once under one five-second
  budget, and git diffs only the commits the two files share (`git log
  --no-walk --stdin`), so a lockfile or a generated client with thousands
  of commits no longer costs a full timeout per partner on every
  `change_set` and `check` call.
- **Skills grant the CLI fallback they describe.** `allowed-tools` now
  includes `Bash(seamark lessons --region *)`, the command the reference
  names in place of `expand lessons:<dir>` when the MCP tools are absent; a
  test pins every `seamark` command in a skill body to a grant.
- **Skills rewritten around the cohort's failure points.** The plan skill
  names the task shapes it covers, says that a request for a minimal change
  does not switch it off, and turns "follow every surprise" into a rule
  with an output: open or `why` every partner under `history suggests also
  reviewing` and exclude one only by naming what the shared commits changed
  there. The review skill answers the companions `check` lists. The
  understand skill no longer claims requests to change something, because
  it raced the plan skill for the cohort task on a fresh repository and won
  a quarter of the time. The command-line fallback moved into the shared
  reference as one table, so the skills are shorter.
- **Skills workflow benchmark harness.** `make skills-bench` runs the paired
  experiment the skills were waiting for: an MCP-only arm against an MCP +
  skills arm on co-change variants of the lessons fixtures, whose history
  carries the trigger and companion files together, with the lessons judges,
  sandbox, and preflight discipline. Rows record what the transcript proves
  (`change_set` before the first edit, the companion named and followed,
  `check` after the last edit, activations, calls, cost) in their own schema
  and file. `make skills-activation` replays a checked-in prompt set and
  records which skill loaded. `make skills-bench-report` assesses the frozen
  `bench/workflow-claims.yaml`, which is committed after calibration and
  before the cohort. Both arms switch off Claude Code's built-in skills, so
  a row proves that only the seamark skills were loaded, and the runner
  passes the trial's settings file with `--settings`, because Claude Code
  ignores project allow rules in an untrusted workspace; a refused seamark
  or `Skill` call now invalidates the row. The lessons harness
  sources, arms, claims, rows, and reports are unchanged.
- **The skills preserve the companion-file invariant.** The second cohort
  (2026-09-05, Claude Haiku 4.5 at medium effort, five valid pairs on each
  of three co-change instances, $2.66) passed the frozen claim: MCP + skills
  preserved the owner invariant in 12/15 task-complete sessions versus
  1/15 for the MCP server alone, +60/+80/+80 percentage points per instance
  and +73 on average against the frozen +30, with intervals of 0 to +83 and
  +19 to +96, all 30 tasks completed, and no refused tool call. The first
  cohort on the unrevised harness had measured +6.7 points. The activation
  set passed at 5/5 recall per skill and 0/4 false activations. The cost
  is visible: the skills arm processed about twice the context per
  session. The skills stay opt-in (`seamark init --skills`), because that
  spend is the user's to accept; the report is
  [`bench/skills-report-v2.md`](bench/skills-report-v2.md).

## v0.5.3 — 2026-08-28

This patch makes the benchmark and its test suite reliable on macOS. It does
not change lesson selection or delivery behavior.

- **Trial-local Go telemetry no longer races cleanup.** Benchmark commands now
  disable Go telemetry in the trial cache before they start. This prevents a
  Go telemetry process from writing into a temporary trial after the command
  has finished. Cache setup errors stop the affected check, judge, fixture
  generator, index, or agent launch and are reported as infrastructure
  failures instead of failed benchmark outcomes.
- **Tests no longer depend on the default macOS Go build cache.** `make test`
  and `make test-race` use a writable per-user cache under `TMPDIR`, with a
  safe `/tmp` fallback. Public-repository preparation also keeps command
  stdout separate from stderr, so a harmless macOS or Git diagnostic cannot
  make a clean checkout appear dirty. Regression tests cover both failure
  modes.

## v0.5.0 — 2026-08-27

The cross-file learning line: Seamark can now recover a repository-specific
relationship from local fix history, verify where the mistake can begin, and
deliver the accepted lesson at that trigger. This release also adds
reproducible synthetic and OpenTelemetry-Go evidence for trigger-scoped
delivery.

- **Distillation preserves cross-file owner knowledge and targets its trigger.**
  Prompt v5 discloses every relevant non-document path in a fix finding,
  shares a fixed evidence budget adaptively across each group, and samples
  distinct production hunks before repetitive test noise. It explicitly asks
  for parallel-implementation, layer, generated-artifact, and
  producer/consumer relationships, and every proposed pattern must answer the
  trigger-path question. A trigger that is an exact cited production path or
  its immediate parent is now valid without an unnecessary co-change edge;
  otherwise history must still confirm it. Test-only citations and broad
  ancestors cannot establish direct delivery. Verified triggers become exact,
  bounded delivery scopes, including individual files, while evidence
  coverage remains the safe fallback. This intentionally supersedes v0.4.0's
  widen-only behavior: `--retarget` may now narrow an evidence-scoped pin to
  its verified trigger.
  The corrected extraction question re-asks legacy negative answers once;
  existing positive trigger answers are preserved. State bundles v2 carry
  that question version while imports still accept v1. The preserved first
  OpenTelemetry case-study proposal motivated these changes: it found the
  generic stale-field rule but lost the explicit/exponential relationship and
  returned no trigger. Prompt v6 also reapplies best-effort secret scrubbing
  when stored evidence is dispatched, which protects legacy findings created
  before storage-time redaction. Trigger-question v3 bounds companion path
  metadata per finding and across each extraction batch.
- **Historical distillation can now use a bounded local-fix corpus.**
  `seamark index --fixes-only` refreshes fix commits reachable from the pinned
  `HEAD` without querying GitHub or deleting an existing review corpus.
  Region-scoped distillation filters findings before grouping, fix-miner
  envelope text no longer drives lexical similarity, and weak two-token
  bridges cannot grow unbounded components. The preflight identifies every
  planned finding by source, PR, and path without printing its body. A reduced
  OpenTelemetry histogram-reset corpus locks the intended two-fix group and
  excludes an unrelated same-package fix.
- **A reproducible case study covers the complete learning-to-delivery path.**
  The OpenTelemetry histogram-reset walkthrough starts from a pinned public
  commit, builds a bounded local-fix corpus, previews and runs distillation,
  records the maintainer's proposal decision, and replays the task with the
  accepted lesson installed. The published evidence bundle includes the
  proposal, applied pin, sanitized settings, patch, focused package test,
  independent behavior checks, lesson statistics, hook audit, and checksums.
  It is a worked example of the normal user workflow, not a claim that one
  replay proves causation or general extraction accuracy
  ([case study](docs/case-studies/opentelemetry-histogram-reset.md)).
- **Pinned public-repository lessons benchmark.** The harness can now prepare
  and verify an exact OpenTelemetry-Go commit, including a content-pinned
  vendored dependency tree, before creating fresh network-isolated agent
  worktrees. A historical histogram-reset task compares the same lesson at the
  explicit-histogram trigger and exponential-histogram repair sites. Hidden
  judges distinguish visible task completion from the parallel delta and
  cumulative owner invariant, and a task-complete naive patch proves that
  distinction during preflight. The public matched claim is frozen before its
  first paid run. Paid shell and hook subprocesses isolate home and XDG state
  while the Claude process retains saved authentication, and agents leave trial
  changes uncommitted. The accepted clean cohort passed its frozen threshold:
  trigger-scoped delivery preserved the parallel histogram invariant 5/5
  versus 0/5 without the hook, while the repair-scoped control was 1/5 versus
  0/5. The +80 percentage-point difference-in-differences effect exceeded the
  precommitted +40-point bar, with all 20 visible tasks complete and no harmful
  interference. Raw rows and the generated report are committed under
  `bench/` ([report](bench/otel-report-v7.md)). This is one pinned public task,
  model, and runtime—not broad external validity.
- **Benchmark failures are evidence too.** Calibration exposed two harness
  confounds before the accepted cohort: an agent added uncompilable tests after
  missing OpenTelemetry's nested Go module, and Claude later returned a
  status-less `API Error: Connection closed mid-response`. The shared task now
  names the exact offline module-local test command, and provider API errors
  without an HTTP status are excluded as infrastructure failures while genuine
  budget or turn exhaustion remains a measured agent outcome. Both discarded
  attempts remain calibration, not silently pooled release evidence.
- **Trigger-scoped delivery now has a controlled benchmark.** A matched
  schema-sync fixture compares the same lesson at the backend trigger with its
  former generated-client repair scope. Five clean pairs per variant produced
  a +100 percentage-point difference-in-differences effect: trigger hook-on
  preserved the owner invariant 5/5 versus 0/5 hook-off, while the
  repair-scoped control was 3/5 in both arms and received zero hook exposure.
  All 20 sessions completed the visible task. Result schema v7 records whether
  hook exposure is required or optional and binds intentional variants with a
  shared protocol fingerprint, preventing incompatible cohorts from being
  compared. This is synthetic evidence under one pinned model/runtime, not an
  external-validity claim.

## v0.4.0 — 2026-08-15

The delivery line: a lesson is only as good as where it fires. This
release moves pins from where reviewers commented to where the mistake
is made, verifies every move against the working tree and co-change
history, and gives existing corpora a one-command upgrade.

- **Lessons deliver where the mistake is made.** Distilled pins used to
  scope to where reviewers commented — the repair site. For
  cross-boundary mistakes that is the wrong place: a "regenerate the
  client" lesson scoped to the generated TypeScript file can never
  fire for the author editing the backend model. Distillation now asks
  the agent for trigger paths and verifies every answer in three steps
  — it must parse, it must exist in the working tree, and co-change
  history must confirm it against the cited evidence — and only then
  widens the pin's regions to deliver at the trigger. A new advisory
  ("delivery may miss the trigger") flags mis-scoped pins on the
  `--proposals` ledger, the HTML report, and the distill plan, with a
  tail block so long lists cannot bury it. One shared recompute now
  backs every "regions now" reader, so `--retarget` applies a widening
  and can never strip one.
- **`lessons --extract-triggers` upgrades existing corpora.** Small
  batched agent calls ask the one trigger question per
  already-distilled proposal: a preflight always discloses cost,
  `--dry-run` stops there, live per-batch progress shows during the
  calls, and every answer — including "no trigger" — is stamped so
  re-running is free. Pending proposals widen in place; applied pins
  surface their drift in the ledger for the explicit `--retarget`, so
  an installed pin's delivery never changes without you. Hand-pruned
  pins are skipped: there is no delivery to widen. Field runs on the
  two development corpora: 17 of 53 proposals gained validated
  triggers on a two-language repository (the schema-sync pins now
  deliver at the backend model that triggers them), while a
  single-ecosystem repository correctly produced only same-site
  triggers and no false cross-boundary widenings. Answers travel in
  state bundles: an import fills a local row that never asked, and
  never overwrites a locally paid answer.
- **The ledger says what it knows.** An applied row whose pin left
  `lessons.yaml` is named plainly ("delivers nothing until it
  returns") instead of receiving region advice a retarget would
  refuse. A confirmed trigger that cannot widen delivery — the region
  set is full, or the path has no region — prints its own line on the
  ledger, the report, and the plan: no drift line and no advisory
  would otherwise mention it.
- **Sharper failures.** Agent CLI complaints that arrive on stdout —
  usage limits, expired OAuth sessions — now surface in the error
  instead of a bare exit status. A JSON reply missing its expected key
  is retried, not recorded: a paid group read or trigger answer is
  never burned on `{}`. An interrupted run reports what completed —
  stamped rows stay done and the next run resumes. Contradictory flag
  combinations (`--dry-run` with a decision flag, `--proposals` with
  `--apply` or `--distill`, spending modes together) are refused
  before dispatch instead of one flag being silently ignored. And
  extraction excerpts pass secret redaction before leaving the
  machine, covering rows mined before store-time scrubbing existed.

Upgrade note: the database upgrades to schema v5 in place on first
open (trigger paths, plus the answered-question stamp that keeps
re-runs free); an older binary then refuses the upgraded database —
the versioning contract. State bundles carry both fields; older
binaries still import the rest of a newer bundle.

## v0.3.0 — 2026-08-10

The outcome line: the system now measures whether its own lessons work —
passively from data every repo already has, and actively with a
controlled, reproducible benchmark — and tunes how lessons are
delivered based on what those measurements showed.

- **The passive outcome loop.** Every applied pin now carries a verdict
  computed from data seamark already holds — the firing log, the
  finding table, and mined history: `working` (flagged N× before
  exposure, none since, across enough region commits), `not landing`
  (the pin fires and the mistake recurs — the ledger names these and
  suggests escalation), or `untested` (with the missing evidence named:
  never fired, quiet region, evidence not mined since exposure,
  citations aged out). Rendered as the same falsifiable sentence in
  `lessons --stats`, the `--proposals` ledger, and the HTML report.
  Deterministic, recomputed per ask, no new stored state. Exposure
  starts at a pin's first firing — a pin no agent ever saw cannot have
  changed behavior — and mining freshness is now stamped so absence of
  recurrence is never claimed from an unmined corpus.
- **A controlled lessons benchmark, with its first passing claim.**
  `make lessons-bench` runs paired headless agent sessions in generated
  fixture repositories built around owner-specific invariants that
  public checks do not cover (a generated TS client, a cache version, an
  async worker registry). Model and effort are pinned, sessions are
  sandboxed and budget-capped, every row carries artifact digests, and
  a no-spend preflight proves the judges discriminate before anything
  is purchased. The first clean cohort (3 instances × 5 valid pairs,
  Haiku, medium effort) passed the pre-frozen claim: hook-on preserved
  the owner invariant 15/15 versus 3/15 hook-off, +80 pp mean lift, no
  visible-task regressions ([bench/lessons-report-v5.md](bench/lessons-report-v5.md)).
  Reproducible controlled evidence under exact conditions — not
  external validation.
- **Once-per-context lesson delivery.** `.seamark/lessons.yaml` now accepts
  `hook_delivery: once-per-context` to inject each matching lesson once in the
  current agent context instead of repeating it after every edit. The default
  remains `always`. Local state contains only repository-scoped session and
  lesson digests, expires after 24 hours, resets after Claude Code compaction,
  and fails open so missing identity, lock contention, or corrupt state never
  hides guidance. Hook audit records carry match and context-generation
  digests; `lessons --stats` distinguishes injections, repeats, suppressions,
  and injected bytes. Per-lesson stats and passive outcome sentences now show
  match-inclusive counts alongside actual deliveries when repeats were
  suppressed, without moving the first-delivery exposure clock.
- **Auditable delivery-cost benchmarks.** Benchmark result schema v6 records
  the selected delivery policy and its hook intensity, keeps policy cohorts in
  separate fingerprints, and labels them in Markdown reports. The first
  two-pair export-registry calibration reduced four matching hook events to one
  injection and three suppressions per treatment session, with zero repeated
  injections and no loss of owner-invariant success. The small dirty-build
  sample does not yet support a token-cost claim.
- **More reproducible benchmark sessions.** The managed Claude adapter now
  strips inherited provider-routing, helper-model, thinking-budget, and prompt-
  caching overrides while preserving existing OAuth and API-key discovery. It
  also rejects unknown hook-delivery evidence during the run, keeps the file-
  only arm byte-stable across hook policies, and aligns the public v6 JSON
  Schema with the semantic validator where standard JSON Schema can express
  the constraint.

Upgrade note: run `seamark init` once after upgrading to install the
`PostCompact` lifecycle hook. Switching `hook_delivery` modes afterward does
not require another init run.

## v0.2.0 — 2026-08-05

The evidence-quality line: pins that say where they apply and how much
their evidence still supports them, and surfaces that inject that
memory at the moment of change.

- **Evidence confidence, everywhere pins compete.** Every distilled pin
  now carries a deterministic tier — strong / fair / weak — computed on
  read from what its citations still support: distinct events, source
  diversity (review+fix), recency, and whether the cited files still
  exist. Weak pins lose injection-budget slots to strong ones and carry
  a "weak evidence" tag in the hook; `why` prints each pin's tier with
  the facts behind it. Nothing is stored, nothing is model-scored.
- **The proposals ledger re-judges old decisions.** `lessons
  --proposals` now shows each pin's evidence health under TODAY'S rules
  — recomputed events, liveness, prompt era — and the regions current
  inference would assign. The new `lessons --retarget p3,p7` applies
  that tightening to lessons.yaml and the ledger together (write-gated
  like `--apply`): the upgrade path for pins distilled before region
  sets. The HTML report's decision cards carry the same health — tier
  badge, facts, era, and the drift line with its retarget command.
- **change_set and check carry the memory.** `change_set` answers now
  end with the budgeted lessons governing the files about to change
  (`change_budget`, default 6), and `check` appends an advisory block
  for the diff's files — clearly marked, never part of the verdict.
  Both record to the firing log with a surface tag, so `--stats`
  reflects all ambient exposure.
- **Review evidence gets a shelf life.** Review mining keeps two years
  of comments (fix mining always kept one) — with the newest 200 always
  surviving, so slow repositories keep a working corpus untuned.
  `reviews: {window_days: N}` adjusts; `0` means unlimited.
- **Region sets replace the repo-wide `*` collapse.** A proposal's
  region is now a small set (≤3 directories, depth ≤3) covering ≥80%
  of its cited *events*: test and doc paths don't vote (test-only
  evidence keeps its test region), root files can't drag a theme to
  `*`, and a theme living in `api` AND `db` says so instead of saying
  "everywhere". Measured on the corpora that motivated it: repo-wide
  proposals drop from 35/65 to 3/65 and 3/27 to 0/27. Applied pins
  carry both `region:` (first entry — what older seamark reads, still
  narrower than the old `*`) and `regions: [a, b]`; the schema
  migrates to v3 (`proposal.regions`, `finding.paths`) automatically.
  Deliberately NOT migrated: existing proposals and their applied pins
  keep the regions they were decided under — rewriting them would
  silently change pin identities behind lessons.yaml's back. New
  distillations get sets immediately; existing pins tighten through
  the upcoming revalidation audit, which shows the recomputed regions
  next to the stored ones with the command to apply them.
- **Fix findings point at the code, not the churn.** A fix's primary
  file is its most-changed non-test, non-doc file (tests routinely
  out-churn the fix they cover), and the finding stores the commit's
  full code footprint for region inference.
- **Merge-commit workflows get PR attribution.** Branch commits inherit
  their pull request from merge topology (`Merge pull request #N` +
  rev-list), so a review comment and the `fix: PR review` commit
  answering it finally count as one event in repos that don't squash.
  Explicit `(#N)` / `fixes #N` references still win. A merge from a
  `fix/`-named branch whose commits carry no fix-shaped message becomes
  one `fix:branch` finding (the merge's diff) — the tier only fires
  where there was no signal at all, and the source label shows in every
  evidence header.
- **Captured themes surface once.** A mined lesson stops surfacing in
  `why` and the edit hook when an applied pin cites every finding in
  its cluster AND that pin is currently present in lessons.yaml — the
  file stays the source of truth, so a hand-pruned pin resurfaces its
  lesson, a partially-cited cluster keeps surfacing (one comment can
  flag two mistakes), and a recurrence arriving after the pin was
  applied re-opens the lesson. The ledger (`lessons --list`,
  `--region`) still shows every raw lesson.
- **Secret redaction in mined text.** Review-comment bodies and
  fix-commit patches are scrubbed of secret-shaped values (connection
  strings, tokens, password assignments) at mining time, with the same
  patterns the gate's raw audit log uses — a credential a reviewer
  quoted once is not re-broadcast into agent context on every edit.
  Already-stored findings keep their text until the next
  `seamark index --reviews` re-mines them.
- **Compact hook injection.** The edit-hook reminder drops the
  terminal-table padding, per-line regions, and reviewer names for
  `- [pin]` / `- [×N]` lines: the reader is a model, and the tokens now
  go to the guidance. Deliberate views keep the full table.
- **Proposal dedup by evidence.** A distilled pattern citing exactly the
  same findings as one already proposed, applied, dismissed, or pinned
  is dropped as a re-derivation, whatever its wording (measured: two
  applied pins with identical citations and unrelated names). The
  `lessons --proposals` audit flags such pairs for pruning. Bare
  linter-code pins (RUF001 vs RUF003) never merge on wording; the edit
  hook collapses restated pins before spending its injection budget.
- **Stable distillation batches.** Oversized candidate groups are cut by
  finding-id hash instead of position, so one new finding re-opens one
  batch instead of re-billing the whole component. One-time cost on
  upgrade: existing oversized-group signatures change once, so the next
  `lessons --distill` re-reads those groups (small groups keep their
  signatures; applied and dismissed decisions are unaffected, and
  re-read groups cannot re-propose already-captured themes).

Upgrade notes: databases upgrade to schema v3 in place on first open
(an older binary then refuses them — the versioning contract). Existing
pins keep the regions they were decided under: `lessons --proposals`
shows what today's inference would assign, `lessons --retarget` applies
it. The next `lessons --distill` re-reads once-oversized groups whose
batch signatures changed — the preflight prices that before anything is
sent — and already-stored review text picks up secret redaction on the
next `seamark index --reviews`.

## v0.1.0 — 2026-08-03

The first tagged release: the production-readiness line — everything
between the first prototype and here.

- **Trust baseline.** A default `seamark init` can never block a
  command; gate enforcement is an explicit opt-in (`--gate-mode
  enforce`), and re-running init preserves the installed mode. The audit
  log stores normalized command names and input hashes instead of raw
  command lines (0600, rotated by size and age, flock-serialized,
  symlink-proof); opt-in raw logging redacts secret-shaped values.
- **Durable state.** The store schema is versioned with ordered,
  transactional migrations; an older binary refuses a newer database.
  Rebuilds preserve proposal decisions and distillation memory, and
  `seamark state export|import` makes them portable across clones.
- **Disclosure.** `lessons --distill` prints what would leave the
  machine before any agent call; `--dry-run` stops there. Data flows and
  the threat model are documented (docs/data-flow.md,
  docs/threat-model.md).
- **Health.** `seamark status` reports semantic health (coverage, call
  confidence, history age, integrations) as text, JSON, and an MCP
  resource; `seamark doctor` diagnoses the installation read-only with
  exact corrective actions. `check` exposes coverage blind spots to
  policy (`diff.unindexed_files`) and every surface distinguishes "no
  evidence" from "not indexed".
- **Distribution.** This release pipeline: per-platform native builds,
  end-to-end smoke tests on every artifact, SHA-256 checksums, draft
  releases for maintainer review.

Upgrade notes: a pre-existing hook installed as `gate --enforce --hook`
is preserved by re-running `seamark init` (mode is kept unless
`--gate-mode` says otherwise). Databases from earlier builds upgrade in
place on first open; coverage metadata backfills on the next
`seamark index`.
