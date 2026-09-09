# CLI Reference

Sparkwing ships a single `sparkwing` binary. This page is a map of what
each command group is *for*; the complete, auto-generated listing of
every command, flag, and argument lives in
[cli-reference.md](cli-reference.md), one `cli-<group>.md` page per
command group (offline: `sparkwing docs read --topic cli-reference`,
or `--topic cli-<group>` for one group). Treat that generated
reference as authoritative -- when this page and it disagree, it wins.

The CLI compiles pipelines, manages local state, and coordinates admission.
A compiled pipeline binary can also execute on its own; see
[Headless hosts](#headless-hosts).

## Output

Commands with `--output` use `pretty` on stdout terminals and compact JSON
when stdout is redirected. `--output pretty|json|plain` (or `-o`) overrides
the default. JSON reports occupy one line; JSON lists emit one object per
line, with an empty stream for no results. Errors go to stderr.

Help, documentation, onboarding cards and completion scripts emit records
with `kind` and `text` in JSON mode. Help also carries command metadata.
Use explicit plain output when consuming their original text:

```sh
source <(sparkwing completion --shell zsh --output plain)
sparkwing docs read --topic pipelines --output plain | less
sparkwing info --for-agent --output plain
sparkwing commands --format markdown --output plain
```

`commands --format markdown --split-dir DIR` writes reference pages into
files and reports the result in the selected output mode. Supported output
modes are `pretty`, `json`, and `plain`.

Cache JSON reports expose their fields directly through `.entries` and similar
fields. Cache failures return a nonzero status with no stdout record. Plain cache output is the cache directory for `cache info`,
the key for `cache explain`, and the reclaimed entry count for `cache prune`.

## sparkwing run

```
sparkwing run <pipeline> [flags...]
```

Compiles and runs a pipeline from the nearest pipeline directory.
Runner controls use `--sw-*`; an unknown option with that prefix fails before
execution setup. Other arguments pass to the pipeline. Put `--` before
pipeline arguments that resemble runner controls. Every argument after the
separator passes through unchanged.

The runner also consumes `--profile`, `-C`, `-v`, and the explicit
`--dry-run=true` and `--dry-run=false` forms before the separator.
`--target` passes to the pipeline.

`--sw-allow` is enforced by the CLI before it dispatches anything. The
labels you authorize are forwarded to the run as `SPARKWING_ALLOW`
(comma-separated) so the run's own record shows what was
authorized -- setting that variable by hand authorizes nothing, because
the gate has already run by then.

`--profile NAME` selects the storage and dispatch addressing
(state/cache/logs, and any controller auth). Execution still happens
locally; to hand a run to a cluster, use `sparkwing pipeline trigger`
for remote execution.

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
| `profile` | Show which profile would resolve for this invocation, and why (read-only; never prints tokens) |
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

## Conventions

- **Structured output.** List / describe / get verbs accept
  `-o pretty|json|plain` (pretty on terminals, JSON when piped). `-o` / `--output` is the one
  output-format selector across the CLI.
- **List output is one record per line.** A listing's `-o json` is
  NDJSON: one complete JSON object per line, no array and no
  pretty-printing, so `head -5` returns five whole records instead of a
  truncated document that parses as nothing. Read the stream a line at a
  time (`json.Decoder` in a loop, `jq -c .` with no `-s`, `while read
  line`). An empty listing is an empty stream. Describe, get, and status verbs return
  one compact JSON object.
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

Use the command index to find a command, then read its help:

```bash
sparkwing commands --query status
sparkwing runs status --help
sparkwing pipeline list -o json
sparkwing pipeline describe --name fictional-build -o json
sparkwing pipeline discover --query fictional-build -o json
```

Command records carry `path`, `synopsis`, and `subcommand_count`.
Help supplies descriptions, flags, and examples. Hidden commands require
`--include-hidden`. See [Bounded discovery](#bounded-discovery) for pagination.

The describe schema matches `sparkwing.DescribePipeline` plus
`group` / `tags` / `triggers` drawn from the `pipelines:` block in
`.sparkwing/sparkwing.yaml`.

## Headless hosts

A runner host does not need the `sparkwing` CLI. Ship it the compiled
pipeline binary (a plain `go build` of your `.sparkwing/` module),
invoke pipelines by name, and inspect local state through the binary's
own `ops` verbs:

```bash
./pipelines <name>
./pipelines ops queue
./pipelines ops doctor
./pipelines ops stats
./pipelines ops stats-reset
./pipelines ops version
```

The `ops` verbs share the CLI's output conventions -- `-o pretty|json|plain`,
the same JSON shapes as `sparkwing queue` / `sparkwing doctor` -- so a
script written against the CLI works unchanged against the binary. They
are the field-recovery surface for a host with no browser and no CLI:
`ops queue` shows why work is stuck, `ops doctor` clears it, and both are
limited to inspection and abandoned-state repair.

One thing a bare pipeline binary does not do is host the admission
daemon. **The installed Sparkwing distribution owns daemon lifecycle.
Pipeline clients declare required capabilities and use the running
daemon; they never host, replace, or upgrade it.** A run's client spawns
the binary named by `SPARKWING_WINGD_BIN` -- which `sparkwing run` sets
to its own path -- else the `sparkwing` found on PATH.

With neither present, a run says so once and proceeds without host
arbitration -- fine for a host that runs one pipeline at a time.
`.Concurrency()` groups still hold, through the shared store instead of
the daemon. The exception is a pipeline that reserves host capacity with
`.Resources()`: that run fails instead, naming the fix, because CPU and
memory have no fallback arbiter (`SPARKWING_ALLOW_UNADMITTED=1` overrides
it if you know what else runs on the box). Put the CLI on the box when
concurrent runs there should queue against each other -- see
[local-execution.md](local-execution.md#who-hosts-the-daemon).

## Bounded discovery

Use `sparkwing commands --query status` to find a command by path or
synopsis, then read that command's `--help`. The index sorts command paths
lexically and returns at most 40 command records. `--path` restricts a
subtree before query filtering and pagination. Child counts describe the
full visible registry, even when a child is outside the current page.

`docs list --query <topic>` returns at most 40 topic metadata records.
`docs search --query <question>` ranks matching sections before choosing
its 20-result page. Search records include a short snippet, without body
content. Read a selected hit with `docs read --topic <slug> --section
<start_line>`. Section selectors refer to the embedded docs in this binary;
they do not apply to web documents or multi-topic guides. `--body` on search
explicitly includes the bodies of the selected page.

All three indexes emit compact NDJSON by default when piped. A final
`kind: "page"` record reports `total`, `returned`, `limit`, `truncated`, and
`next_cursor` when another page exists. Pass that cursor with `--cursor`,
keeping the same filters and binary version. Command/topic cursors name the
last lexical path/slug; search cursors name the last ranked section as
`slug:start_line`. An unknown cursor fails instead of silently restarting.
Metadata records retain their existing selection fields; readers must
recognize the final page record instead of treat it as a command/topic.

`--limit 0` explicitly returns every remaining match. Plain mode prints
paths or selectors only; continuation information goes to stderr. Pretty
mode includes a short page footer. Explicit `commands --format markdown`
exports the exhaustive reference and rejects query/pagination flags; a
`--path` export still selects the requested subtree. Use `--output plain`
when redirecting the Markdown artifact to a file.
