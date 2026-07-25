# Seamark

> A seamark is a navigational marker that shows both the safe channel and the
> hazard — route knowledge left by those who came before, plus a warning about
> what will sink you.

Seamark indexes what your repo's history knows and what your code can actually
do — which files really change together, why the weird parts are weird, and
which paths can reach production. It serves that to your editor as diagnostics
and to your agent as a handful of MCP tools, and it enforces your rules as
checks rather than as paragraphs in a prompt. Mistakes get caught instead of
explained.

**Status: early prototype.** The indexer, git history miner, and `why` query
work; LSP, MCP, effects, and the policy gate are in progress. See
[docs/PLAN.md](docs/PLAN.md) for the roadmap.

## What it models

1. **Structure** — symbols, calls, imports, parsed with tree-sitter.
2. **History** — which files empirically change together (co-change with
   lift), and the commit/PR record of *why*, mined from git.
3. **Effects** *(planned)* — which code paths can write to a database, mutate
   infrastructure, read secrets, or touch production config, propagated
   transitively along call edges.

Everything in the index is falsifiable: every row traces to a parse, a commit,
or a policy file. No LLM-generated "insights" are ever stored.

## Quick start

Requires Go ≥ 1.24 and a C compiler (tree-sitter bindings use CGO).

```sh
make build            # builds ./bin/seamark
./bin/seamark index   # index the current repository
./bin/seamark why Store.UpsertSymbols
./bin/seamark why internal/store/store.go
```

`seamark why` answers: where is this defined, who calls it, which files
change together with it (empirically, not structurally), and which commits
explain it.

## Development

```sh
make test    # run all tests
make lint    # go vet
make fmt     # gofmt
make index   # self-index this repo (builds first)
```

The index lives in `.seamark/index.db` (SQLite, single file). Delete it to
start fresh; `seamark index` rebuilds from scratch.

## License

Apache-2.0. See [LICENSE](LICENSE).
