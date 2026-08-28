# CLI Reference

Sparkwing ships a single `sparkwing` binary. This page is a map of what
each command group is *for*; the complete, auto-generated listing of
every command, flag, and argument lives in
[cli-reference.md](cli-reference.md), one `cli-<group>.md` page per
command group (offline: `sparkwing docs read --topic cli-reference`,
or `--topic cli-<group>` for one group). Treat that generated
reference as authoritative -- when this page and it disagree, it wins.

**Sparkwing does not require sparkwing.** The CLI is a developer
convenience, not a dependency: a host that only ships the pinned pipeline
binary -- no CLI installed -- still runs pipelines and stays operable.
What the CLI adds at runtime is coordination: it hosts the per-machine
admission daemon, so multiple runs on one box queue against each other
instead of oversubscribing it. A pipeline binary alone runs
uncoordinated. See [Headless hosts](#headless-hosts).

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

`--sw-allow` is enforced by the CLI before it dispatches anything. The
labels you authorize are forwarded to the run as `SPARKWING_ALLOW`
(comma-separated) purely so the run's own record shows what was
authorized -- setting that variable by hand authorizes nothing, because
the gate has already run by then.

`--profile NAME` selects the storage and dispatch addressing
(state/cache/logs, and any controller auth). Execution still happens
locally; to hand a run to a cluster, use `sparkwing pipeline trigger`
instead of `sparkwing run`.

## Command groups

Top-level groups, each with its own `--help` and a full per-group page
indexed in [cli-reference.md](cli-reference.md):

| Group | For |
|---|---|
| `info` | Agent entrypoint card: what sparkwing is, what's in this repo, what to run next |
| `pipeline` | This repo's pipelines: list / describe / discover / new / explain / run / trigger / hooks / sparks |
| `run` | Shortcut for `pipeline run` (the positional form) |
| `runs` | Inspect and manage runs: list / status / logs / retry / cancel, plus `approvals` and `triggers` |
| `repos` | The machine's fleet of sparkwing repos and their SDK pins: list / info / update |
| `queue` | Local admission: holders, connections, waiters, capacity |
| `daemon` | The local admission daemon: status / restart |
| `profile` | Show which profile would resolve right now, and why (read-only; never prints tokens) |
| `version` | Composite CLI + SDK + sparks version card; `version update --sdk` bumps the pinned SDK |
| `update` | Self-update the `sparkwing` CLI binary |
| `dashboard` | Detached local dashboard server: start / kill / status |
| `doctor` | Diagnose and repair local state, including unsafe private-home permissions and provably-dead records |
| `cluster` | Cluster ops against a profile's controller: status / agents / worker / gc / users / tokens / image / webhooks / concurrency |
| `secrets` | Secrets, laptop dotenv or controller-stored with `--profile`: set / get / list / delete |
| `configure` | Laptop-local config: init / profiles / xrepo |
| `debug` | Interactive run debugging: run / release / attach / env / rerun / replay |
| `docs` | The embedded copy of this doc tree: list / read / all / search |
| `examples` | The worked-pipeline registry; `--name <example> --body` prints the source |
| `commands` | The full CLI surface as JSON (agent self-discovery) |
| `completion` | Shell completion script (`--shell bash\|zsh\|fish`) |

One-shot repo-local shell chores -- formatters, port-forwards,
Makefile-style glue -- belong in whatever task runner you already use;
sparkwing is the Go-pipeline platform.

## Conventions

- **Named flags only.** Every input is `--flag value`; the pipeline name
  on `sparkwing run` is the sole positional.
- **Structured output.** List / describe / get verbs accept
  `-o pretty|json|plain` (default `pretty`). `-o` / `--output` is the one
  output-format selector across the CLI.
- **List output is one record per line.** A listing's `-o json` is
  NDJSON: one complete JSON object per line, no array and no
  pretty-printing, so `head -5` returns five whole records instead of a
  truncated document that parses as nothing. Read the stream a line at a
  time (`json.Decoder` in a loop, `jq -c .` with no `-s`, `while read
  line`). An empty listing is an empty stream, not `[]`. Describe / get
  / status verbs answer with one object and stay pretty-printed.
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
sparkwing commands                              # one-line index of every verb
```

The list verbs stream NDJSON, so an agent that cannot afford a whole
catalog reads a prefix of it: `sparkwing commands --path runs -o json |
head -20` is twenty complete command records. `sparkwing commands` with
no flags is the cheapest orientation of all -- one line per verb for the
whole surface.

`sparkwing commands -o json` is an index, not a help dump: each record
carries `path`, `synopsis`, and `subcommand_count` (0 means the verb is
a leaf), and nothing else. Description, flags, and examples belong to
`<path> --help`, which renders them from the same command registry and
cannot go stale against it; duplicating them into the listing cost 190KB
to answer a question the caller had not asked yet. Hidden commands are
dispatchable but omitted from every listing -- their own help names the
supported verb to use instead, so surfacing them in the index would
steer readers at the thing the index exists to steer them away from.
`--include-hidden` lists them, marked `"hidden": true`, and a `--path`
that matches only hidden commands says so rather than reporting an empty
subtree.

The describe schema matches `sparkwing.DescribePipeline` plus
`group` / `tags` / `triggers` drawn from the `pipelines:` block in
`.sparkwing/sparkwing.yaml`.

## Headless hosts

A runner host does not need the `sparkwing` CLI. Ship it the compiled
pipeline binary (a plain `go build` of your `.sparkwing/` module),
invoke pipelines by name, and inspect local state through the binary's
own `ops` verbs:

```bash
./pipelines <name>                # run a pipeline
./pipelines ops queue             # the admission queue: holders, waiters, capacity
./pipelines ops doctor            # repair private-home permissions and provably-dead local state
./pipelines ops stats             # the rolling admission-outcome window
./pipelines ops stats-reset       # clear that window after an incident
./pipelines ops version           # the binary's SDK version
```

The `ops` verbs share the CLI's output conventions -- `-o pretty|json|plain`,
the same JSON shapes as `sparkwing queue` / `sparkwing doctor` -- so a
script written against the CLI works unchanged against the binary. They
are the field-recovery surface for a host with no browser and no CLI:
`ops queue` shows why work is stuck, `ops doctor` clears it, and both are
non-destructive to live runs.

One thing a bare pipeline binary does not do is host the admission
daemon. **The installed Sparkwing distribution owns daemon lifecycle.
Pipeline clients declare required capabilities and use the running
daemon; they never host, replace, or upgrade it.** A run's client spawns
the binary named by `SPARKWING_WINGD_BIN` -- which `sparkwing run` sets
to its own path -- else the `sparkwing` found on PATH.

With neither present, a run says so once and proceeds without host
arbitration -- fine for a host that runs one pipeline at a time.
`.Concurrency()` groups still hold, through the shared store rather than
the daemon. The exception is a pipeline that reserves host capacity with
`.Resources()`: that run fails instead, naming the fix, because CPU and
memory have no fallback arbiter (`SPARKWING_ALLOW_UNADMITTED=1` overrides
it if you know what else runs on the box). Put the CLI on the box when
concurrent runs there should queue against each other -- see
[local-execution.md](local-execution.md#who-hosts-the-daemon).

This is the operational face of *sparkwing does not require sparkwing*:
the pipeline binary is the product, and it stays functional alone -- the
CLI adds the coordination.
