# Lessons benchmark operations

The benchmark runs coding agents in fresh, generated repositories. Source code
is never pasted into the task prompt: the agent receives the frozen task and
reads the fixture through its normal filesystem tools.

## Safe run sequence

Build Seamark and run the no-agent preflight first:

```sh
make build
make lessons-bench BENCH_FLAGS='-preflight-only -model claude-haiku-4-5-20251001'
```

Then calibrate exactly one paired trial:

```sh
make lessons-bench BENCH_FLAGS='-trials 1 -model claude-haiku-4-5-20251001 -max-budget-usd 1'
```

Inspect both transcripts and patches before increasing the trial count. The
default is intentionally one trial per arm, not ten.

The default Claude adapter:

- requires an exact model ID and pinned effort;
- uses only project settings and disables user plugins, MCP servers, skills,
  browser integration, memory, and session persistence;
- exposes only Read, Edit, Write, and Bash;
- enables Claude Code's native OS sandbox with hard failure when unavailable,
  repository-only writes, no unsandboxed escape hatch, and no approved network
  domains for Bash subprocesses;
- moves Go, Python, and Node caches into the trial directory and disables Go
  dependency downloads;
- caps provider spend per session and preserves the complete stream transcript.

Do not use `--bare` or `--safe-mode` in a custom adapter: both disable the
project hook the treatment is meant to measure.

## Included instance

- `python-ts-schema-sync-v1` asks for a Python API response field. The visible
  backend implementation and public tests pass without touching the generated
  TypeScript client; the owner invariant requires the deterministic
  `make sync-api` output to be committed. Its synthetic git history includes a
  prior backend-only schema change and follow-up client refresh, making the
  lesson plausible repository knowledge rather than generic coding advice.
  This instance requires `python3` and `make`; preflight verifies both before
  any agent session is purchased.

The fixture is generated locally from frozen source and history. No private
repository is read, copied, or required at run time.

## Preflight contract

Preflight spends no model tokens. It rejects a run unless:

1. two generated repositories have the same commit hash;
2. the untouched repository passes its public checks but not the task judge;
3. a task-complete naive solution passes public checks and the task judge but
   fails the owner-invariant judge;
4. the external canonical solution passes task and invariant judges;
5. the canonical solution passes all public checks;
6. every repository check has a bounded per-command timeout;
7. the base fixture contains no Seamark or Claude treatment artifacts;
8. hook-off receives common sandbox settings but no hook or lesson; and
9. hook-on receives the exact lesson and hook configuration.

## Result validity

Schema-v4 rows separately record task success and owner-invariant success.
Only rows with both `valid=true` and `pair_valid=true` enter arm tallies.
Provider errors, rate limits, missing structured output, unexpected plugins or
MCP servers, model mismatches, and non-firing treatment hooks are excluded.
For a hooked arm, a firing counts only when the selected lesson identity,
edit-hook surface, edit tool, and configured file region all match. Treatment
or provider failures stop the batch before another paid session is started and
make the command exit unsuccessfully. Agent timeouts and agent-reported budget
or turn-limit errors remain measured outcomes rather than infrastructure
outages.

Token fields are not combined:

- `input_tokens` is fresh input;
- `cache_read_input_tokens` is cached context read again;
- `cache_creation_input_tokens` is context written to cache;
- `context_tokens` is their sum across the session.

Consequently, a large `context_tokens` value does not mean the fixture was sent
as one prompt. It normally represents repeated context traversal over many
agent turns.

Cost estimates only use valid prior rows with the exact same fingerprint. The
fingerprint binds the task, checks, agent command, model, effort, timeout,
runtime identity, exact Seamark binary digest, treatment and placebo content,
generated fixture HEAD, naive and gold patches, judge version, and embedded
benchmark harness sources.
