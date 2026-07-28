# Seamark — implementation plan

Seamark indexes a repository into a graph that captures **structure** (symbols,
calls, imports), **history** (which files empirically change together, and why
the code is the way it is, mined from git), and **effects** (which code paths
can reach a database write, infrastructure mutation, secret read, or production
config — propagated transitively along call edges). On top of the graph sits a
policy engine evaluated at edit time and before tool execution. One engine,
three surfaces: LSP, MCP, CLI/hooks.

This document tracks the build order and current status. It is a working plan,
not a spec; sections get checked off and refined as milestones land.

## Principles that shape the build order

- **Detection over instruction** — prefer a post-diff diagnostic to a
  pre-prompt paragraph; rules cost zero tokens until their scope is touched.
- **Depth over breadth** — 5 languages modelled properly (Go, TS/JS, Python,
  SQL migrations, HCL) beats 158 parsed shallowly.
- **One engine, three protocols** — LSP and MCP are both JSON-RPC 2.0 over
  stdio; one transport layer, two protocol adapters over a shared query API.
- **Local, no account, no key.** No telemetry, ever, without opt-in.
- **Every artifact is falsifiable** — every edge traces to a parse, a commit,
  or a policy file. No LLM-summarized "insights" in the index.

## Milestones

### M0 — Foundations (partially deferred)

The original M0 is the release matrix (Zig-CC cross-compilation, static musl
linking, npm `optionalDependencies` skeleton). The parts that gate local
development are done; the CI/release matrix is deferred until there is a
feature worth releasing (tracked below as "Release engineering").

- [x] Go module, project layout, Makefile, Apache-2.0 license
- [x] **FTS5 verified present in `modernc.org/sqlite`** (pure-Go driver, no
      CGO needed for storage; CGO is still required by tree-sitter)
- [x] SQLite schema v1 (see `internal/store/schema.sql`)
- [ ] Release engineering: GitHub Actions matrix (zig cc for linux/windows,
      native macOS runners), static musl linking, npm optionalDeps packages,
      `--provenance` publishing — **deferred until M2 lands**

### M1 — Indexer + history miner (the go/no-go gate) ← current

Prove the history layer tells a human something they did not know. If
`seamark why` on a well-known repo produces nothing surprising, stop and
rethink before building more.

- [x] Store layer: schema migration, batch upserts, FTS5 symbol search
- [x] Go extractor: tree-sitter parse → symbols (functions, methods, types,
      consts/vars) + edges (IMPORTS, CALLS best-effort by name resolution)
- [x] Co-change miner: `git log --numstat`, commit-size and path filters,
      pairwise lift
- [x] Decision mining v0: commits as `decision` rows linked to files
- [x] `seamark index` — build/refresh the index for a workspace
- [x] `seamark why <symbol|file>` — definition, callers, co-change partners,
      recent decisions
- [x] Validate on a real repo — **gate passed 2026-07-25** on trading-tools
      (447 commits, 831 files): filters produced clean ranked lists; top
      findings were genuine cross-language contracts (Python↔TS schema sync
      at 38 shared commits, export-key chain across 4 files, docs coupled to
      code at lift up to 80). Thresholds (30-file bulk cutoff, lift ≥ 3)
      held without tuning. Zero reverts in that history — lesson capture
      (M6) cannot lean on the revert signal alone.
- [x] TypeScript/JavaScript extractor (.ts/.tsx/.js/.jsx + module variants):
      functions incl. arrow/function-expression consts, classes + methods,
      interfaces/type aliases/enums, ES-module imports (named + namespace)
      with per-file module semantics and relative-specifier resolution
- [x] Python extractor: module functions/classes/methods (decorated, async),
      SCREAMING_SNAKE constants, absolute + relative from-imports,
      `__init__.py` package collapse, docstring hashing, and a same-class
      resolution tier (self/cls/this — also wired for TS) shared with the
      other languages
- [x] Indexing config (2026-07-27): committed `.seamark/config.yaml` —
      generated files (`Code generated … DO NOT EDIT.` header) skipped by
      default (`index.generated: true` opts back in), plus `index.exclude`
      globs (basename / `**/`-prefix / dir-prefix / full-path) on top of
      .gitignore. Skips counted in `seamark index` output; a glob that
      could never match fails the load instead of silently no-opping; both
      index-shaping overlays (`effects.yaml`, `config.yaml`) are hashed
      into the freshness fingerprint so an overlay-only edit rebuilds; and
      `seamark init` now repairs an older .gitignore block by appending
      newly-added carve-out lines
- Measured on trading-tools (703 files, 10k symbols, 22k edges, ~3s):
  same-package 3818 / qualified 2799 / same-class 464 / unique-name 658
  CALLS edges. Known noise, by design filterable via `origin`: common
  method names whose only repo declaration is a test stub attract
  unique-name edges (e.g. every `x.set()` → `tests._SyncRedisStub.set`,
  148 edges). Candidate tuning for M2: exclude test-file symbols from the
  unique-name pool, or surface origin in `why` output.

### M2 — LSP server ← current

- [x] `seamark lsp`: hover (sig, caller counts with confidence, co-change
      partners, recent decisions), codeLens (caller counts + file-level
      coupling), co-change omission diagnostics on save (Information
      severity, conservative thresholds: together ≥ 3, lift ≥ 2)
- [x] Transport: hand-rolled JSON-RPC 2.0 over stdio (see decisions log);
      shared framing layer ready for the MCP adapter
- [x] Freshness v0: synchronous full reindex on didSave (~150ms on seamark,
      ~3s on trading-tools); logs to stderr, stdout is protocol-only
- [x] Editor configs: editors/nvim/seamark.lua, editors/vscode shim,
      docs/editors.md
- [x] nvim demo — verified live 2026-07-26 on trading-tools: hover showed
      the cross-language coupling (`auction.py` ↔ `AuctionStateEngine.tsx`,
      lift 2.9) with decision trail; caller lenses rendered
- [x] VS Code demo — verified 2026-07-26: hover merges under pyright's
      docs natively (incl. effects "Reaches" line), lenses render, the
      co-change diagnostic publishes (visibility fixed: full-first-line
      range instead of a zero-width speck). VS Code is the smoother of
      the two demos; nvim's mixed-encoding caveat doesn't apply.
- [ ] Daemonize: `seamarkd` with unix-socket IPC, incremental refresh; LSP
      becomes a thin adapter (deferred until the surface proves out)
- Postponed nvim caveats (2026-07-26): Python-buffer hover breaks when ruff
  attaches with UTF-8 while other clients use UTF-16 (nvim 0.11 mixed-
  encoding buffers; fix = force ruff to utf-16 in its server opts, not a
  seamark issue). Hover slim-down pending a product call: lead with
  co-change + decisions, demote caller counts (gopls wins on structure —
  see the M2 verdict discussion).

### M2.5 — Function-grain history (report, don't extrapolate)

File-level stays the *statistical* layer: functions accumulate history
~10× slower than their files (measured on trading-tools:
`_expected_move_band` touched in 3 commits vs 44 for its file), so
function×function lift would be noise at any honest threshold. But git's
hunk headers name the enclosing function (`@@ … @@ def foo`), which makes
factual function-grain reporting cheap:

- [x] `why` enrichment (2026-07-26): for each co-change partner, name the
      functions the shared commits touched — "`journals.py` 10/399, lift
      7.1 · mostly `get_metrics_for`, `update_trade_for`". Query-time
      (`internal/history/funcgrain.go`): `FileCommits` gets the queried
      file's commit set, `PartnerFunctions` reads the partner's diff
      history once (`git log -U0 -p`, ~40ms/file) and tallies the
      hunk-header function context of the shared commits, distilling each
      to a bare identifier (`class Foo`/`func (x) Run`/`def bar` → the
      name). Measured cost: +0.49s worst case on a 10-partner hot file.
      Best-effort: silently omitted for non-git repos and partners git
      can't resolve a funcname for (generated files, non-code). Grain is
      git's default (enclosing top-level construct / method) — method
      precision via per-language diff drivers is the refinement below.
      Review round fixed before commit: the per-partner `git log -U0 -p`
      buffered a file's whole diff history via cmd.Output() (a
      package-lock.json / generated file = hundreds of MB on the why/MCP
      path) — now a streaming reader (O(one line)), a commit-count cap
      (5000), and a 5s timeout; funcName regex anchored to line start so
      prose containing a keyword isn't mis-read. Known residual (documented
      in code): the shared-commit set is the full co-change window, so a
      bulk commit dropped from lift (>30 files) that touched both files can
      still contribute a name — minor over-count.
- [ ] Better funcname grain via git built-in diff drivers (`diff=python`
      etc. through an injected attr-source) for consistent method-level
      names, not just git's column-0 default.
- [ ] Align the function-grain commit set exactly to the co-change window
      (same MaxFilesPerCommit bulk filter), so the "mostly" names never
      include a commit the lift number excluded.
- [ ] Populate `decision_link` at symbol grain during indexing (schema
      already carries it): map new commits' hunks onto current symbol
      spans so real function-level statistics accumulate as history grows.
- [ ] `seamark why --function`: on-demand deep dive via `git log -L
      :func:file` — one function's true history; too slow to bulk-index,
      fine per query.

### M3 — Rules engine

- [ ] `structural` tier: graph queries (e.g. `domain/` must not import `infra/`)
- [ ] `pattern` tier: tree-sitter `.scm` queries scoped by CEL expressions
      over the graph
- [ ] Diagnostics delivery: LSP for humans, structured tool errors for agents
- [ ] `prose` tier deferred until agent sampling integration exists

### M4 — Effects + security gate ← current

- [x] Sink catalogue as YAML data: embedded default (Go/Python/TS, ~40
      sinks) + additive `.seamark/effects.yaml` workspace overlay; three
      matcher kinds (import-qualified, method-name, bare-call). Detection
      runs on raw call references — the interesting sinks live in external
      dependencies that never resolve to edges.
- [x] Effect propagation backwards along CALLS edges to fixpoint (BFS,
      shortest depth), stored with origin direct/propagated + depth;
      surfaced in `why` ("effects db:write [depth 2]"), LSP hover
      ("Reaches"), and the index summary. Validated on trading-tools:
      598 tagged symbols; route handlers reaching db:write listed with
      depth; propagation travels up to 6 hops.
- Known conservatism: the `execute` method matcher tags every DB-API call
  db:write — Python's cursor API has no syntactic read/write split. Honest
  over-approximation; refine per-driver in the catalogue if it nags.
- [x] Blast radius: per-symbol effect rows ARE the materialized transitive
      closure for effect queries (a symbol's tags = everything it can
      reach); a symbol×symbol closure table is deferred until a consumer
      needs one
- [x] `seamark gate --command`: mvdan/sh parse (pipelines, chains,
      substitutions; unparseable input fails closed), wrapper unwrapping
      (sudo/env), variable-indirection DETECTION (`$X apply` → dynamic,
      flagged — the regex-denylist bypass, surfaced), command-sink section
      in the effects catalogue, force-push + target-branch detection, CEL
      policy (`effect.contains(...)`, env.is_prod from declared vars) with
      warn-default / enforce opt-in; exit 2 on enforced blocks (hook/CI
      ready)
- [x] `seamark check`: unified-diff blast radius (changed lines → symbols
      → union of propagated tags), same policy engine over `diff.*`;
      validated live on trading-tools WIP (db:write+fs:write, allow)
- [x] Audit log: append-only .seamark/audit.jsonl, one entry per decision
- [x] Wired as a Claude Code PreToolUse hook and proven end-to-end
      2026-07-26: .claude/settings.json runs `seamark gate --enforce` on
      every Bash tool call; a fresh Claude session with Bash *permitted*
      attempted `curl` under a deny-egress policy and was blocked before
      execution ("blocked by policy: deny"), with the decision in the
      audit log. Note: Claude Code's auto-mode classifier sits in front
      and catches generic dangers (force-push); seamark adds what a
      generic classifier cannot know — workspace policy, environment
      markers, toolchain effect classification, and the audit trail.
- [x] Security review round (2026-07-26) closed the classifier bypasses:
      interpreter payloads (`bash -c "…"`, `eval`) recurse into the gate
      (non-literal or over-deep payloads flag as dynamic), xargs/timeout/
      nice/watch/stdbuf added as wrappers with value-flag handling
      (`sudo -u root …`), backslash-escaped names unescape (`\rm`),
      command sinks match past global flag values (`kubectl -n prod
      delete`), `git push origin HEAD` resolves to the current branch,
      the freshness fingerprint is content-sensitive (already-dirty file
      edits register; `.seamark/` state excluded, the `effects.yaml` and
      `config.yaml` overlays hashed in), diff headers with spaces/quoted
      paths parse, and the
      hook reads PreToolUse JSON natively (`--hook`) failing CLOSED under
      enforce (malformed/empty payload, policy/CEL errors → exit 2)
- [ ] Audit log hardening: size-based rotation for audit.jsonl and an
      advisory-lock note (concurrent writers on NFS may interleave >4k
      entries); known limit — `bash script.sh` file payloads are not
      read, so script contents classify only when invoked inline

### Freshness & incremental indexing

Measured on trading-tools (831 files): full index 3.46s = history mining
0.58s + parse/graph/SQLite ~2.9s; fingerprint check 0.03s.

- [x] Level 0 — freshness fingerprint (HEAD + porcelain status): `why`
      warns when stale, `check` self-repairs, `index` no-ops in <1s when
      the workspace is unchanged (`--force` overrides); LSP no-change
      saves become free
- [~] Level 1 — incremental inputs, exact outputs.
  - [x] Per-file parse cache (2026-07-26): content-hashed FileResults
        gob-blobbed in a `parse_cache` table, reused when a file's bytes
        are unchanged; only changed files re-run tree-sitter. Persisted in
        the Rebuild transaction (never disagrees with the graph), pruned
        for deleted files, version-guarded (`parse_cache_version`, with a
        decode-failure fallback), bypassed by `--force`. Measured on
        trading-tools: a one-file edit reindexes in **1.3s vs 3.2s cold**,
        result byte-identical to a full rebuild (asserted in tests via
        Stats equality). Phase breakdown that drove this: parse 1.8s,
        history 0.7s, write 0.4s, resolution 0.003s — parse was 60%.
        Resolution/propagation/write stay full (cheap + global exactness).
  - [ ] History watermark: `git log <last-sha>..HEAD`, counter-based
        co-change updates (needs windowed eviction via decision_file) —
        removes the remaining 0.7s.
  - Deferred cache optimizations (from the review; correctness unaffected):
    LoadParseCache pulls every blob into memory each run (~tens of MB at
    10k files — load (file,hash) first, fetch data on a hash hit); and
    PruneParseCache is SELECT-then-N-DELETEs (batch to a single
    `DELETE WHERE file NOT IN kept`). Both are scale niceties, not needed
    at current sizes.
- [ ] Level 2 — `seamarkd` + fsnotify: debounced incremental refresh,
      delta DB writes, LSP as a thin client (RFC architecture)
- [ ] Level 3 — incremental resolution with dependency tracking; only
      worth it at 100k-file scale, and subtle: the unique-name tier is
      nonlocal (declaring a second method named X anywhere invalidates
      an edge elsewhere), so invalidation must be tracked per name

### M5 — MCP server ← complete 2026-07-26

- [x] Five tools: `orient`, `change_set`, `why`, `check`, `expand` —
      hand-rolled newline-delimited JSON-RPC stdio transport (same call
      as the LSP: no SDK dependency for a protocol subset this small).
      Reports shared with the CLI via the extracted internal/report
      package; new `seamark orient` CLI command rides along
- [x] Every tool call self-repairs freshness (fingerprint fast path),
      tool failures travel in-band (isError) so the model corrects
      course, expand caps at 250 lines and refuses to leave the
      workspace, orient filters test helpers out of most-called
- [x] MCP resource (seamark://orient) and `onboard` prompt; .mcp.json
      shipped at repo root for Claude Code auto-discovery
- Validated on trading-tools: orient surfaces the api/schemas.py ↔
  web schema.ts coupling as top change hubs; change_set on
  (api/schemas.py, db/models.py) returns the TS schema/client and the
  architecture docs as history-suggested companions
- Validated in a live Claude Code session (2026-07-26): correct tool
  selection from descriptions alone across orient/change_set/why/expand;
  agent's own post-session review confirmed the core bet — co-change/
  lift is "the hardest-to-replicate part and the biggest saving", while
  structure queries are merely a compact substitute for grep. Its
  closing mental model ("seamark for orientation and risk before an
  edit; direct tools for the edit itself") is now encoded in the
  server's initialize instructions, along with the fact that freshness
  is automatic (the agent wrongly assumed the index could be stale).
  Session also exposed the silent-empty-section gap in change_set
  (agent burned probe calls re-checking) — fixed with the `defines` line

### M6 — Lessons loop ← capture + surface (Tier 1) complete 2026-07-26

- [x] Mine PR review comments into recurring "lessons":
      `internal/reviews` fetches `pulls/comments` through an injectable
      Fetcher (default shells out to `gh api --paginate`), parses the
      concatenated-per-page JSON with a streaming decoder, classifies the
      reviewer (coderabbit | copilot | bot | human) and clusters. A cited
      linter code (RUF001, E501, reportArgumentType) clusters by
      (directory, code); an un-coded comment clusters by (file, bold
      issue-title fingerprint). Reviewer-agnostic: bots and humans travel
      the same path. Degrades to a Note (never an error) with no gh / no
      auth / no GitHub remote / no PRs.
      - Store: `lesson` table given reviewer/last_ts/example_url columns;
        `ReplaceLessons` is its own transaction — lessons refresh on the
        review cadence (`seamark index --reviews`), NOT on every
        structural reindex, so the MCP self-repair never triggers network
        fetches. Rebuild leaves `lesson` intact.
      - Surface (Tier 1): region-scoped `reviewers keep flagging` block in
        `why <file>` (occurrences ≥ 2) and repo-wide in `orient` — flows
        through MCP with zero protocol change.
- [x] Live-validated on trading-tools (2026-07-26, real CodeRabbit +
      Copilot history, 477 comments): top clusters are exactly the
      anti-repeat signal — `E702 ×20`, `RUF001 ×6` (the non-ASCII case),
      `BLE001`, `reportArgumentType`, `B008`, `E741`. Real data drove
      three fixes the fixtures missed: gh's per-page array concatenation,
      clustering on CodeRabbit's severity boilerplate (fixed by
      extracting the bold issue title), and `(root)` cross-contamination
      (root-level comments now cluster file-scoped, not lumped).
- [x] Push-based awareness — the PreToolUse edit hook. `seamark lessons
      --hook` reads tool_input.file_path from an Edit/Write payload and
      emits the file's lessons as `additionalContext`; wired in
      settings.json on `Edit|Write|MultiEdit`. Read-only, offline, silent
      when a file has no lessons. PROVEN 2026-07-26: a headless agent
      given a normal edit task in scripts/ with NO seamark mention
      received the lessons and named all seven flagged codes in its
      answer — vs. the identical pre-hook test where it named none. This
      is what closes "available" → "aware without being told"; MCP alone
      is pull-only and an unprompted agent does not consult it.
- [x] Configurable, gate-style: committed `.seamark/lessons.yaml`
      overlay applied at surface time (no re-mining) — `threshold`
      override, `mute` (by rule and/or region prefix, kills the
      generated-code/`(root)` noise), and `pin` (curated lessons that
      surface always for their region, even if never mined — the "must
      not be ignored" list, with a human note). One `Config.Surface` path
      shared by why, orient, and the hook so they never disagree.
- [x] Adversarial review round (2026-07-26) fixed before commit: (1)
      data-loss — a transient mining failure (offline, gh logged out,
      non-GitHub checkout) wiped all stored lessons via
      ReplaceLessons(nil); now Result carries a Fetched flag and the swap
      only happens on a genuine successful fetch (verified: 401 → "kept
      existing", count unchanged). (2) rule-code regex forged codes from
      SHA256/RFC3339/HTTP200/ISO8601; now anchored to a known linter-
      prefix allowlist. (3) concatenated-page decode now tested. (4)
      malformed lessons.yaml degrades why/orient to defaults + a note
      instead of aborting the report (and MCP). (5) dead MaxComments knob
      removed. (6) hook injection framed as untrusted quoted data
      (defense-in-depth vs NL injection; escapes/protocol already safe).
- [x] Onboarding + config ergonomics (2026-07-26): `seamark init`
      scaffolds `.seamark/{policy,lessons}.yaml` starters (warn-mode,
      never clobbering), adds the `.gitignore` carve-outs, and merges the
      gate + lessons PreToolUse hooks into `.claude/settings.json` —
      preserving existing hooks/settings, idempotent, resolved
      `os.Executable()` path, `--print` dry-run. `seamark lessons --list`
      is the config ledger: every mined lesson (incl. below-threshold
      one-offs) with the rule/region to paste and a `(muted)` flag for
      what's already hidden. (Deliberately NOT a YAML-mutating command —
      the config stays hand-owned and reviewable, like policy.yaml.)
      Review round fixed before commit: hook detection was a substring
      match that could clobber a user command containing the marker (and
      mis-place seamark's hook onto the wrong matcher) → now a suffix
      match with the joining space; binary path shell-quoted (spaced
      install paths); a present-but-wrong-typed hooks field errors instead
      of silently overwriting user data; starter policy gained the
      dynamic-argv0-prod rule the embedded default has, and is now
      regression-tested through gate.LoadPolicy + EvalCommand.
- [x] Firing audit log (`.seamark/lessons-audit.jsonl`, append-only, one
      line per hook injection: {ts, file, tool, fired[]}) — the true
      gate-audit analog. Written best-effort by the edit hook (never fails
      the edit), read by `seamark lessons --stats`, which ranks the
      most-surfaced lessons and lists those that WOULD surface but never
      have — the decay signal (a lesson whose region no edit touches is a
      pruning candidate). Verified live on trading-tools. Aggregation
      (reviews.Summarize) is pure + unit-tested; log rotation still
      deferred (shared with the gate audit's unbounded-growth note).
- [ ] Tier 0 (zero-token promotion): N≥3 mechanical recurrences → a
      proposed PostToolUse `ruff check --select <codes>` on edited files,
      as a `.seamark/` diff for human review, never auto-enabled. The
      hook already proves the plumbing; this swaps injected text for the
      linter actually running
- [ ] Decay/watermark: `since`-incremental fetch; hits/last_hit aging
      (currently the mined window IS the recency filter — no all-time
      tally to decay)
- [x] Tier 2 distillation (2026-07-27): `lessons --distill` batches raw
      findings (stored in full behind every lesson) into candidate
      groups — token-overlap themes plus per-directory coverage, capped
      at 40 — and reads each NEW group once through the user's own agent
      CLI (`claude -p` preset; any stdin CLI via `agent.argv`; seamark
      holds no keys). Cite-or-die validation: patterns must cite ≥2 real
      member finding ids, regions computed from citations, never trusted
      from the model. Plan/apply: proposals persist with p-ids;
      `--apply p3,p7` / ranges `p1..p9` (holes tolerated, bare ids
      strict) insert pins into lessons.yaml — only with the
      `distill.write` config opt-in, else printed to paste; `--dismiss`
      is permanent memory. Group signatures (hash of member ids) mean an
      unchanged evidence set is never paid for twice; runs report ~token
      cost. Hook pin injection budgeted (`pin_budget`, default 3,
      deepest region first, "+N more" pointer). Validated on
      graphql-go-tools (14/14 proposals applied by the human) and
      trading-tools (domain-specific pins incl. session-boundary and
      statistical-baseline rules).
- [ ] Non-review failure signals (test pass→fail, reverts) as finding
      sources — the rest of the original Tier 2 idea

findings). Reviewer-agnostic by design: CodeRabbit, Copilot review,
human reviewers — all arrive through the same PR-comment API. Surface by
cost tier, cheapest wins:
  - Tier 0 (zero tokens): N≥3 recurrences of a mechanical rule promote
    to a proposed PostToolUse check (`ruff check --select <codes>` on
    the edited file) — a `.seamark/` diff for human review, never
    auto-enabled. Agent pays tokens only on violation.
  - Tier 1 (bounded, DONE): recurring lessons become one-liners in MCP
    orient/why responses, ranked by occurrences, region-scoped. Never
    CLAUDE.md (always-on cost, grows forever, gets ignored).
  - Tier 2 (DONE, see above): distillation of human threads through the
    user's own agent CLI — plan/apply, cite-or-die, never auto-enabled.

### v1.0

- [ ] `seamark orient` markdown digest + MCP resource
- [x] One-command setup: `seamark init` wires config + agent hooks
- [ ] One-line install (prebuilt binaries); agent auto-detect
- [ ] Audit log surfaced

## Current architecture (as built)

```
cmd/seamark/            entry point
internal/cli/           cobra commands: index, why, version
internal/index/         orchestrator: walk → parse → resolve → store + history
internal/parse/         language registry, tree-sitter extractors (Go first)
internal/history/       git log miner: co-change pairs + decision rows
internal/store/         SQLite (modernc, pure Go) schema + queries + FTS5
internal/model/         shared types: Symbol, Edge, CoChange, Decision
```

Schema notes (deviations from the RFC sketch, all additive):

- `decision_file(decision_id, file)` supplements `decision_link`: M1 mines
  decisions at file grain (commits touch files); symbol-grain links arrive
  when hover/lens needs them.
- `meta(key, value)` for index bookkeeping (schema version, last indexed
  commit, repo root).
- `symbol_fts` is an FTS5 table over (name, fqn, file) kept in sync by the
  indexer, not by triggers, because the indexer rebuilds per-file atomically.

## Decisions log

| Date | Decision |
|---|---|
| 2026-07-25 | Pure-Go SQLite (`modernc.org/sqlite`): FTS5 confirmed working; the `mattn/go-sqlite3` fallback is unnecessary. |
| 2026-07-25 | CGO accepted for tree-sitter (official bindings); we ship precompiled binaries so users never see a compiler. |
| 2026-07-25 | Call edges in M1 are name-resolved best-effort (same package, then unique repo-wide match). Type-accurate resolution is a later refinement; edges record their origin so confidence is queryable. |
| 2026-07-25 | Module path `github.com/seamark-dev/seamark` is final: the `seamark-dev` org and repo exist and are set as origin. |
| 2026-07-25 | LSP transport: hand-rolled JSON-RPC 2.0 over stdio, not `tliron/glsp`. glsp v0.2.2 pulls ~15 modules (websockets, terminal styling, commonlog) for a protocol we use ~6 methods of; a hand-rolled framing layer is ~60 lines, dependency-free, and is the shared transport the MCP adapter reuses (§3: one engine, three protocols). |

## Open questions carried from the RFC

1. Does the co-change signal survive a real monorepo, or does noise swamp it?
   **Answer empirically in M1 before anything else.**
2. Effect catalogue: static seed only, or one-time reviewed LLM pass over the
   dependency surface?
3. Is `orient` genuinely better than an agent reading the README + three
   files? Needs a head-to-head.
4. Consume an existing CodeGraph index when present, or always own the graph?
5. Right failure signal for lesson capture in repos without test coverage?
6. Multi-repo effect graph: v2, or does cross-service reach belong in v1?
