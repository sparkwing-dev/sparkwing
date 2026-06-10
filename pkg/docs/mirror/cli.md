# CLI Reference

Sparkwing ships a single `sparkwing` binary. This page is a map of what
each command group is *for*; the complete, auto-generated listing of
every command, flag, and argument lives in
[cli-reference.md](cli-reference.md) (and offline via
`sparkwing docs read --topic cli-reference`). Treat that generated
reference as authoritative -- when this page and it disagree, it wins.

The rule across the whole tree: **every input is a named flag**. The one
intentional exception is the pipeline name on `sparkwing run <pipeline>`
(and its `sparkwing pipeline run <pipeline>` long form), which is
positional because operators type it all day.

## sparkwing run

```
sparkwing run <pipeline> [flags...]
```

Compiles and runs a pipeline from the nearest `.sparkwing/`, locally.
`sparkwing run` owns a set of control flags prefixed `--sw-*` (plus
`--profile` and `--target`); everything else on the line is forwarded to
the pipeline binary and parsed against the pipeline's typed Inputs. The
`--sw-*` prefix keeps those control flags from colliding with
pipeline-defined flags -- see the flag-namespace section of
[sdk.md](sdk.md#typed-inputs) for the full list and the forwarding rules.

`--profile NAME` selects the storage and dispatch addressing
(state/cache/logs, and any controller auth). Execution still happens
locally; to hand a run to a cluster, use `sparkwing pipeline trigger`
instead of `sparkwing run`.

## Command groups

Top-level groups, each with its own `--help` and a full entry in
[cli-reference.md](cli-reference.md):

| Group | For |
|---|---|
| `info` | Agent entrypoint card: what sparkwing is, what's in this repo, what to run next |
| `pipeline` | This repo's pipelines: list / describe / discover / new / explain / run / trigger / hooks / sparks |
| `run` | Shortcut for `pipeline run` (the positional form) |
| `runs` | Inspect and manage runs: list / status / logs / retry / cancel, plus `approvals` and `triggers` |
| `profile` | Show which profile would resolve right now, and why (read-only; never prints tokens) |
| `version` | Composite CLI + SDK + sparks version card; `version update --sdk` bumps the pinned SDK |
| `update` | Self-update the `sparkwing` CLI binary |
| `dashboard` | Detached local dashboard server: start / kill / status |
| `cluster` | Cluster ops against a profile's controller: status / agents / worker / gc / users / tokens / image / webhooks / concurrency |
| `secrets` | Secrets, laptop dotenv or controller-stored with `--profile`: set / get / list / delete |
| `configure` | Laptop-local config: init / profiles / xrepo |
| `debug` | Interactive run debugging: run / release / attach / env / rerun / replay |
| `docs` | The embedded copy of this doc tree: list / read / all / search |
| `commands` | The full CLI surface as JSON (agent self-discovery) |
| `completion` | Shell completion script (`--shell bash\|zsh\|fish`) |

For repo-local shell chores (formatters, port-forwards, Makefile-style
glue) use dowing; sparkwing is the Go-pipeline platform.

## Conventions

- **Named flags only.** Every input is `--flag value`; the pipeline name
  on `sparkwing run` is the sole positional.
- **Structured output.** List / describe / get verbs accept
  `-o pretty|json|plain` (default `pretty`). `-o` / `--output` is the one
  output-format selector across the CLI.
- **Profile addressing.** `--profile NAME` picks the storage/dispatch
  profile. Absent, commands read local state (SQLite under `~/.sparkwing/`).
  `sparkwing run` always executes locally; `sparkwing pipeline trigger` is
  the verb for remote (cluster) execution.
- **Required flags.** Marked `[required]` in `--help`; missing ones fail
  before any side effect.
- **Hidden entries.** Pipelines marked `hidden: true` don't appear in
  `pipeline list` or tab-complete but stay invocable by exact name. Pass
  `--all` to `pipeline list` to see them.

## Agent discovery

Agents should read the catalog as JSON rather than scraping help text:

```bash
sparkwing pipeline list -o json                 # every invocable with metadata
sparkwing pipeline describe --name X -o json    # one pipeline's full metadata
sparkwing pipeline discover --query TEXT -o json # ranked fuzzy search
sparkwing pipeline explain --name X -o json     # Plan DAG before running
sparkwing commands                              # the entire CLI surface as JSON
```

The describe schema matches `sparkwing.DescribePipeline` plus
`group` / `tags` / `triggers` drawn from the `pipelines:` block in
`.sparkwing/sparkwing.yaml`.
