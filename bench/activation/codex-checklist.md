# Codex activation checklist

The activation runner drives Claude Code only. Codex activation is recorded
by hand with this checklist, on the same prompt set (`prompts.yaml`) and the
same fixture, with the skills installed through `seamark init --skills=codex
--approve-tools`. Run each prompt in a fresh Codex session from the fixture
root, note which skill Codex loaded (or none), and commit the filled table
with the cohort. The pass criteria are the ones frozen in
`bench/workflow-claims.yaml`, applied per client.

Prerequisite: a `seamark` binary on `PATH`, for example from `make install`
(it builds into `~/.local/bin/seamark`). The Codex registration that
`--approve-tools` writes runs the bare `seamark` command, so a binary that
is not on `PATH` would leave Codex without the MCP server. Then, from the
seamark source checkout:

```sh
go run ./cmd/skills-bench -instance python-ts-schema-sync-cochange-v1 -generate /tmp/codex-fixture
cd /tmp/codex-fixture && seamark init --skills=codex --approve-tools && seamark index
```

For the review prompts marked `prepare: naive` in `prompts.yaml`, apply the
naive change first (add `billingCurrency` to `server/schema.py` and
`server/presenters.py` without regenerating the client), as the runner does. Generate a
fresh directory for every prompt.

| Prompt id | Expect | Date | Codex version | Activated skill | Seamark calls | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| understand-response-pipeline | seamark-understand-repo | 2026-09-07 | 0.153.1 | seamark-understand-repo | MCP: orient 1, why 5 | — |
| understand-web-client | seamark-understand-repo | 2026-09-07 | 0.153.1 | seamark-understand-repo | CLI: orient 1, why 7, status 1 | — |
| understand-sync-mechanism | seamark-understand-repo | 2026-09-07 | 0.153.1 | seamark-understand-repo | MCP: orient 1, why 3 | — |
| understand-load-bearing | seamark-understand-repo | 2026-09-07 | 0.153.1 | seamark-understand-repo | MCP: orient 1, why 4, expand 4 | — |
| understand-cochange-hubs | seamark-understand-repo | 2026-09-07 | 0.153.1 | seamark-understand-repo | MCP: orient 1, why 8, change_set 1 | Recovered from an initial incorrect skill path. |
| plan-billing-field | seamark-plan-change | 2026-09-07 | 0.153.1 | seamark-plan-change, seamark-review-change | CLI: orient 1, why 6, lessons 5, lessons --help 1, check 1 | Also loaded review skill for the implementation. |
| plan-rename-region | seamark-plan-change | 2026-09-07 | 0.153.1 | seamark-plan-change, seamark-review-change | MCP: orient 1, change_set 1, why 4, check 1 | Also loaded review skill for the implementation. |
| plan-task-minimal | seamark-plan-change | 2026-09-07 | 0.153.1 | seamark-plan-change, seamark-review-change | CLI: status 1, why 3, lessons 5, check 1 | Also loaded review skill for the implementation. |
| plan-task-owner | seamark-plan-change | 2026-09-07 | 0.153.1 | seamark-plan-change, seamark-review-change | MCP: orient 1, change_set 1, why 1, check 1 | Also loaded review skill for the implementation. |
| plan-fix-missing-region | seamark-plan-change | 2026-09-07 | 0.153.1 | seamark-plan-change, seamark-review-change | MCP: change_set 1, check 1 | Also loaded review skill for the implementation. |
| review-before-commit | seamark-review-change | 2026-09-07 | 0.153.1 | seamark-review-change | MCP: check 1 | — |
| review-double-check | seamark-review-change | 2026-09-07 | 0.153.1 | seamark-review-change | CLI: check 1 | — |
| review-task-handover | seamark-review-change | 2026-09-07 | 0.153.1 | seamark-review-change | MCP: check 1, why 1 | — |
| review-before-pr | seamark-review-change | 2026-09-07 | 0.153.1 | review-code, seamark-review-change | CLI: check 1 | Also loaded personal review-code; isolation caveat below. |
| review-what-breaks | seamark-review-change | 2026-09-07 | 0.153.1 | seamark-review-change | MCP: check 1, why 2 | — |
| typo-readme | none | 2026-09-07 | 0.153.1 | none | none | Requested README wording change only. |
| comment-pagination | none | 2026-09-07 | 0.153.1 | none | none | Requested comment only. |
| symbol-lookup | none | 2026-09-07 | 0.153.1 | none | none | Returned (0, 100); no skill or Seamark call. |
| rename-local-variable | none | 2026-09-07 | 0.153.1 | none | none | Requested local rename only. |

Recall per skill: hits over the should-activate prompts of that skill.
False activation: should-not prompts that loaded any skill, over all
should-not prompts. Record both below the table when the run is complete,
with the date and the Codex version.

## Recorded cohort: 2026-09-07

**Local-environment result: passes the frozen activation thresholds.** All 19
sessions completed on `gpt-5.4-mini` at **medium** reasoning effort, using
Codex CLI `0.153.1` and Seamark `v0.5.4-20-g0f0d17a` (source commit
`0f0d17a72c3a`, full SHA in the evidence). No expensive model was used for
benchmark sessions. Each prompt had a fresh fixture and session, with a
five-minute timeout; no session reached that limit.

- `seamark-understand-repo` recall: **5/5 (100%)**, minimum 80%.
- `seamark-plan-change` recall: **5/5 (100%)**, minimum 80%.
- `seamark-review-change` recall: **5/5 (100%)**, minimum 80%.
- False activation: **0/4 (0%)**, maximum 25%.

An activation means the session successfully read the skill's `SKILL.md`;
merely mentioning a skill or listing its directory does not count. Seamark
calls above distinguish MCP from CLI. Additional skills on positive prompts
are listed in the table; they do not enter the negative-prompt denominator.
This checks activation, not complete workflow compliance or task correctness.

**Isolation limitation:** user configuration and plugins were disabled, and
per-skill disable overrides were supplied for personal/system skills. However,
`review-before-pr` also read the personal `review-code` skill, so exclusion was
not complete. The personal skill directories are symlinks; the overrides used
the symlink paths. Treat this as a local-environment cohort, not an isolated
three-skill result. No prompt was rerun to improve its activation outcome.

The fixture received `seamark init --skills=codex --approve-tools`. Because
project-only MCP discovery was unreliable in the local preflight, each
`codex exec` invocation also registered the installed Seamark binary explicitly
with `required=true` and tool approvals. The scaffold was excluded from the
candidate diff; review fixtures started with only the two naive backend edits.

Recorded usage: **2,674,330 input tokens**, including
**2,401,792 cached input tokens**, and **87,343 output tokens**
(including reasoning). These are cumulative model-request counts, not unique
context size. The CLI did not report a dollar charge.

[Recorded evidence](codex-results-2026-09-07.json) contains per-prompt skill
read events, Seamark calls, usage, transcript hashes, configuration, and source
hashes. Full JSONL transcripts, stderr, setup logs, and before/after patches
remain local under `bench/transcripts/codex-activation-2026-09-07-v2/`, following
the repository's ignored-transcript convention. The evidence uses a separate
manual Codex schema and is not input to the Claude activation report command.
