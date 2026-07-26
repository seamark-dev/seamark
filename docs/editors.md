# Running seamark in your editor

Seamark runs as a *secondary* language server alongside your language's
own (gopls, pyright, ts_ls, …). It serves three things from the index:

- **Hover** — the decision layer: signature, caller count (with
  confidence), which files empirically change together, recent commits.
- **Code lenses** — caller counts per function; a file-level lens for
  empirical coupling.
- **Diagnostics on save** — co-change omission: "`schema.ts` changes with
  this file in 38 of 58 commits and is not part of this change."

`seamark lsp` speaks LSP over stdio and builds the index on first start.
All logging goes to stderr; stdout is the protocol channel.

## Neovim (0.9+)

Source [editors/nvim/seamark.lua](../editors/nvim/seamark.lua) from your
config (or copy its contents; for lazy.nvim/LazyVim put it in
`lua/plugins/` and add `return {}` at the end). It attaches seamark to
Go/Python/TS/JS buffers, wires code-lens refresh, and maps `<leader>sm`
to a seamark-only hover — on nvim 0.10+ plain `K` also merges seamark's
answer into the combined hover. Requires `seamark` on `$PATH`, or adjust
`cmd`.

Try it: open a file, `<leader>sm` on a function, save a file that has
strong co-change partners and watch the diagnostics.

## VS Code

VS Code cannot attach arbitrary LSP servers without an extension, so
[editors/vscode](../editors/vscode) contains a ~40-line shim:

```sh
cd editors/vscode
npm install
```

Then open `editors/vscode` in VS Code and press F5 (Extension Development
Host), or package it once with `npx vsce package` and install the .vsix.
Set `seamark.path` if the binary is not on PATH.

## Anything else

Any editor with LSP client support works the same way: command
`seamark lsp -C <workspace-root>`, stdio transport, attach as an
additional server for the languages you care about.
