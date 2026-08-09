package cli

import "strings"

// Starter files written by `seamark init`. The policy ships with example
// rules active but in WARN mode by default, so verdicts surface without
// blocking anything — enforcement is an explicit opt-in
// (`init --gate-mode enforce`), never something init turns on by itself.

// The two spellings of the policy mode line. starterPolicy embeds the
// warn line; starterPolicyFor swaps in the enforce line, so the swap can
// never miss (the template literally contains the constant it replaces).
const (
	policyModeLineWarn    = `mode: warn   # verdicts are reported, nothing blocks; "enforce" exits 2`
	policyModeLineEnforce = `mode: enforce   # deny/require_approval verdicts exit 2; "warn" only reports`
)

const starterPolicy = `# Seamark gate policy (RFC-001 §5.5). Evaluated before an agent runs a
# command and over a diff's blast radius. Mode "warn" reports verdicts
# without blocking; "enforce" makes deny/require_approval verdicts exit 2.
#
# Rules are CEL expressions over:
#   effect   list of tags the command classifies to ("infra:mutate" …)
#   command  {name, argv, raw, is_push, is_force_push, target_branch, dynamic}
#   env      {is_prod, is_dev, detected: {VAR: value…}}
#   diff     {files, effects}   (populated by ` + "`seamark check`" + `)

` + policyModeLineWarn + `

environment:
  detect: [AWS_PROFILE, KUBECONFIG, DATABASE_URL, ENV, ENVIRONMENT, DEPLOY_ENV]
  prod_markers: [prod, production, live]

# Decision log (.seamark/audit.jsonl: 0600, rotated by size and age,
# gitignored). Each entry stores the normalized command names, a SHA-256
# of the input, the verdict and the policy hash — never the raw command
# line, which frequently carries tokens, passwords and connection strings.
# audit:
#   raw: true   # ALSO persist the input line. Secret-shaped patterns are
#               # redacted best-effort, but treat the log as sensitive.

deny:
  - id: no-prod-infra-mutation
    when: 'effect.contains("infra:mutate") && env.is_prod'
    message: infrastructure mutation against a production environment requires a human

  - id: no-force-push-default-branch
    when: 'command.is_force_push && command.target_branch in ["main", "master"]'
    message: force-push to the default branch

require_approval:
  - id: prod-db-write
    when: 'effect.contains("db:write") && env.is_prod'
    message: database write against a production environment

  - id: dynamic-argv0-prod
    when: 'command.dynamic && env.is_prod'
    message: command name comes from a variable — effects cannot be classified

  - id: diff-reaches-infra
    when: 'diff.effects.contains("infra:mutate")'
    message: this change can reach infrastructure mutation
`

// starterPolicyFor returns the policy scaffold for a gate mode. The
// template is authored in warn; enforce swaps the one mode line, so an
// `init --gate-mode enforce` writes a policy file that agrees with the
// hook it installs instead of contradicting it.
func starterPolicyFor(gateMode string) string {
	if gateMode != gateModeEnforce {
		return starterPolicy
	}

	return strings.Replace(starterPolicy, policyModeLineWarn, policyModeLineEnforce, 1)
}

const starterConfig = `# Indexing options — what enters the graph at all.
# Committed and reviewed like the other .seamark overlays.

index:
  # Index files carrying a "// Code generated ... DO NOT EDIT." header
  # (protoc, stringer, mockgen, go generate …). Default false: generated
  # code is boilerplate that inflates the graph and pollutes most-called.
  generated: false

  # Extra paths to skip, on top of .gitignore and the built-in skip dirs.
  #   *.pb.go        a basename glob, any directory
  #   **/*_test.go   the same, explicit
  #   internal/gen/  a directory prefix
  exclude:
    # - "**/*_test.go"   # uncomment for a production-only graph

# Which agent CLI seamark may shell out to for inference-backed features
# (lesson distillation). Seamark holds no credentials: the CLI is one
# you already run and have already authenticated.
# agent:
#   cli: claude                  # the built-in preset (default)
#   argv: ["my-llm", "--print"]  # or any CLI reading a prompt on stdin

# Distillation plan/apply. write lets "lessons --apply" insert accepted
# pins into .seamark/lessons.yaml itself; without it, apply prints the
# block for you to paste. Applying is always an explicit human command
# naming explicit proposal ids — this only gates who edits the file.
# distill:
#   write: true
`

const starterLessons = `# Tune how mined review lessons surface (RFC-001 §5.4). Committed and
# reviewed like policy.yaml — applied at surface time, so edits take
# effect immediately without re-mining. An absent file means "defaults":
# nothing muted, nothing pinned, threshold 2.
#
# Lessons come from ` + "`seamark index --reviews`" + ` (PR review comments) and
# appear in ` + "`why <file>`" + `, ` + "`orient`" + `, and the PreToolUse edit hook.
# Run ` + "`seamark lessons --list`" + ` to see every mined lesson and the exact
# rule/region values to paste below.

# Minimum recurrences before a mined lesson surfaces. A single comment is
# noise; a pattern needs repetition.
threshold: 2

# Ambient edit-hook delivery. "always" preserves the original behavior.
# "once-per-context" emits each lesson once per provider session, then allows
# it again after Claude Code compacts that session's context.
# hook_delivery: once-per-context

# Hush noise. Match by rule code and/or region (a file or directory
# prefix). An entry with both narrows to that rule in that region.
mute:
  # - rule: F541                 # f-strings-without-placeholders, don't care
  # - region: alembic/versions   # generated migrations — ignore lessons here

# Your exceptions that must NOT be ignored: surfaced always, for their
# region, even if mining found them once or never. The note is shown to
# the agent verbatim. Write pins by hand, or let ` + "`lessons --distill`" + `
# draft them — same entries, same review, distill is optional.
pin:
  # - rule: RUF001
  #   region: scripts
  #   note: "Keep scripts ASCII — smart quotes from chat have bitten us"

# How many pins the EDIT HOOK injects per edit (most specific region
# first; the rest are one pointer line away). Deliberate views (--file,
# why) always show everything. Default 3 — every applied pin taxes every
# future edit, so spend consciously.
# pin_budget: 3
`
