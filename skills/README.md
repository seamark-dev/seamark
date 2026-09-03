# Seamark agent skills

Three [Agent Skills](https://agentskills.io) that teach a coding agent when and how to use the Seamark MCP tools. They encode judgment (blast radius before a multi-file edit, `check` before completion, how to read co-change and coverage honestly), not tool aliases, and they stay silent for small localized edits.

- `seamark-understand-repo`: understand, explain, or get oriented in a codebase, module, or area.
- `seamark-plan-change`: implement, add, change, refactor, or fix something that spans files or an unfamiliar area.
- `seamark-review-change`: review, double-check, or finish a change; "am I missing anything".

Install into a repository with `seamark init --skills`: it writes `.claude/skills/` and, when an `.agents/` directory exists, `.agents/skills/`; `--skills=claude|codex|all` overrides the detection. Install one skill for yourself with `npx skills add seamark-dev/seamark --skill seamark-plan-change`.

Each skill carries the same `references/interpreting-seamark.md` so that it installs on its own; a test keeps the three copies byte-identical. Seamark owns a skill directory whose `SKILL.md` carries `metadata.seamark: managed` and refreshes it on the next `seamark init --skills`; copy a skill under a new name to customize it.
