# Install Seamark and connect your AI coding agent

This guide targets **v0.7.0 or newer**. Upgrade an older installation before
using `--client` and the Codex hook integration.

## Requirements

- macOS or Linux, with Git installed.
- A local Git repository. More history gives Seamark more evidence.
- Claude Code or Codex for the integrated AI coding agent workflow. You can
  also use Seamark's CLI without an AI coding agent.

GitHub CLI authentication is needed only to mine GitHub reviews. Model access
is needed for optional lesson generation and for your AI coding agent's own
work; local indexing and history queries do not call a model.

## Install with Homebrew

```bash
brew install seamark-dev/tap/seamark
seamark version
```

Homebrew installs the latest published version, not the development tree.
Existing installations can update with
`brew update` followed by `brew upgrade seamark`.

The [tap](https://github.com/seamark-dev/homebrew-tap) provides bottles for
Apple Silicon macOS and x86_64 Linux. Other platforms, including Intel macOS,
build from source. Shell completions are included.

## Build from source

Install Go 1.25 or newer and a C compiler; the parser uses CGO. The commands
below build the current `main` branch:

```bash
git clone https://github.com/seamark-dev/seamark.git
cd seamark
mkdir -p ~/.local/bin
make install
export PATH="$HOME/.local/bin:$PATH"
seamark version
```

Add `~/.local/bin` to your shell's startup configuration to keep it on `PATH`.
Check that `command -v seamark` points to the build you intend to use, especially
if you also installed a release with Homebrew.

## Install a release archive

Download the archive for your OS and architecture, plus `SHA256SUMS`, from
[Releases](https://github.com/seamark-dev/seamark/releases). In the download
directory, check the archive against the published checksum:

```bash
# Linux
sha256sum -c --ignore-missing SHA256SUMS

# macOS
shasum -a 256 -c --ignore-missing SHA256SUMS
```

Extract the selected archive, then copy its `seamark` binary into a directory
on `PATH`, such as `~/.local/bin`. A matching checksum verifies integrity
against the published list, not publisher identity. Artifact signing is not
yet available. Windows is unsupported.

## Connect an AI coding agent

From the repository you want to work on, choose one command:

```bash
seamark init --client claude --skills --approve-tools
seamark init --client codex --skills --approve-tools
```

To configure both, repeat `--client` in one command:

```bash
seamark init --client claude --client codex --skills --approve-tools
```

Setup connects MCP, installs hooks, and scaffolds `.seamark/` configuration.
`--skills` adds the three workflow skills. `--approve-tools` adds permission
rules for Seamark tools while preserving existing restrictions. Omit either
flag if you do not want that part. With `--client`, use bare `--skills`, not
`--skills=codex`.

Add `--print` to preview the files without changing them. Setup preserves
existing configuration and can be rerun to refresh managed hooks and skills.
A fresh policy uses warn mode; an existing enforcing policy remains in force.
Policies are [experimental](policies.md).

Open or restart the AI coding agent in this repository. For Codex, project
trust and hook trust are separate: accept project trust if appropriate, then
review the hooks with `/hooks` in the Codex CLI. Changed hooks need a new review.
Seamark does not grant trust. See [agent integrations](agent-integrations.md)
for the supported surfaces, optional permissions, and removal instructions.

## Get your first result

```bash
seamark index
seamark orient
seamark doctor
```

`orient` shows the main code areas, frequently called functions, files that
often change together, and recent decisions. `doctor` reports setup problems
and suggests corrective commands. An optional component that is missing may
need attention only if you want to use it; read the individual findings.

Ask your AI coding agent:

> Use Seamark to explain this repository's main areas and recent decisions.
> Which files often change together?

For a specific file, run `seamark why path/to/file` or ask the agent to explain
it using Seamark. A young repository may have little history to report.

## Start learning from past mistakes

Follow [Learn from your repository](../README.md#learn-from-your-repository)
to mine fixes, draft a proposal, review it, and install a lesson. The
[lessons guide](lessons.md) explains tuning and the proposal lifecycle.

Installing hooks does not create lessons. A reminder appears only when there
is a matching lesson. You can inspect a file with
`seamark lessons --file path/to/file` and check delivered reminders with
`seamark lessons --stats`.

## If something is missing

- **The command rejects `--client`:** run `seamark version` and check
  `command -v seamark`. Upgrade to v0.7.0 or newer.
- **The AI coding agent cannot find tools:** restart it from the repository,
  run `seamark doctor`, and check its MCP registration and project trust.
- **Codex has tools but no reminders:** review `/hooks`, check hooks are
  enabled, and verify there is a lesson for the edited file.
- **No findings or proposals:** the history may lack recognizable fixes or
  recurring feedback, or the findings may already have been processed. Inspect
  `seamark lessons --list` and `seamark lessons --proposals`, add GitHub reviews
  if available, or write a lesson by hand. Do not delete the database to retry;
  it contains [durable decisions](configuration.md#durable-state-the-index-is-not-a-throwaway-cache).
- **Codex setup still tries to run Claude for lesson generation:** select
  the [inference agent](agent-integrations.md#choose-an-agent-for-inference)
  separately in `.seamark/config.yaml`.
- **`--apply` did not install a lesson:** by default it prints YAML for you
  to paste. Enable `distill.write` to let it update `lessons.yaml` and the
  proposal ledger automatically when you explicitly apply a proposal.
