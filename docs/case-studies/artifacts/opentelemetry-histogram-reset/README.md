# OpenTelemetry histogram-reset evidence

This directory is the reviewed, public evidence bundle for
[Teaching an agent an OpenTelemetry invariant](../../opentelemetry-histogram-reset.md).
The experiment used these immutable OpenTelemetry-Go revisions:

- learning: `0a1d12fb662e80de7f6f17128efc3960c6dc121b`;
- replay: `0eb89a5210e64df2f38611b95d1ae0afd6b88fd7`.

The files preserve the publication distillation and the first replay attempt:

| File | Evidence |
| --- | --- |
| `00-environment.txt` | Tool versions and immutable source revisions |
| `01-distill-preflight.txt` | Evidence boundary and effective distillation command |
| `02-distill-run.txt` | First publication distillation output |
| `03-proposal-ledger.txt` | Proposal before the apply decision |
| `04-accepted-lessons.yaml` | Reviewed lesson copied into the replay |
| `05-project-settings.sanitized.json` | Reviewed project hooks used by the replay |
| `06-replay-command.txt` | Agent command used in the replay |
| `07-replay-task.txt` | Exact task given to the agent |
| `08-trigger-preview.txt` | Pre-session confirmation that the lesson matched the intended path |
| `09-agent-result.txt` | Agent-authored completion message |
| `10-replay-status.txt` | First-attempt worktree state after the agent finished |
| `11-replay.diff` | Focused patch left by the agent |
| `12-package-test.txt` | Captured aggregate-package test result |
| `13-independent-checks.txt` | Exact explicit, exponential-delta, and exponential-cumulative behavior checks |
| `14-lesson-stats.txt` | Delivery totals and context size |
| `15-lesson-audit.jsonl` | Raw injection and suppression records |
| `SHA256SUMS` | Digests for the files above |

The completion message records what the coding agent reported. The package
test, independent checks, patch, and lesson audit are separate evidence and do
not rely on that report. In particular, this bundle does not independently
capture the agent's stated full `make precommit` run; it captures the narrower
package and behavior checks listed above.

Absolute operator paths and setting values were removed or redacted before
publication. Provider transcripts and unrelated local diagnostics are not
included.
