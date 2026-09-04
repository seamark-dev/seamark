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

For the two review prompts, apply the naive change first (add
`billingCurrency` to `server/schema.py` and `server/presenters.py` without
regenerating the client), as `prepare: naive` does in the runner. Generate a
fresh directory for every prompt.

| Prompt id | Expect | Date | Codex version | Activated skill | Seamark calls | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| understand-response-pipeline | seamark-understand-repo | | | | | |
| understand-web-client | seamark-understand-repo | | | | | |
| plan-billing-field | seamark-plan-change | | | | | |
| plan-rename-region | seamark-plan-change | | | | | |
| review-before-commit | seamark-review-change | | | | | |
| review-double-check | seamark-review-change | | | | | |
| typo-readme | none | | | | | |
| comment-pagination | none | | | | | |
| symbol-lookup | none | | | | | |
| rename-local-variable | none | | | | | |

Recall per skill: hits over the should-activate prompts of that skill.
False activation: should-not prompts that loaded any skill, over all
should-not prompts. Record both below the table when the run is complete,
with the date and the Codex version.
