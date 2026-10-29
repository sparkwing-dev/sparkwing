# CLI Reference

Sparkwing ships a single `sparkwing` binary. This page is a map of what
each command group is *for*; the complete, auto-generated listing of
every command, flag, and argument lives in
[cli-reference.md](cli-reference.md), one `cli-<group>.md` page per
command group (offline: `sparkwing docs read --topic cli-reference`,
or `--topic cli-<group>` for one group). Treat that generated
reference as authoritative -- when this page and it disagree, it wins.

The CLI inspects pipelines and uses controller APIs for state and admission.
The shared host executes SDK processes. See [Headless hosts](#headless-hosts).

## Output

Commands with `--output` use `pretty` on stdout terminals and compact JSON
when stdout is redirected. `--output pretty|json|plain` (or `-o`) overrides
the default. JSON reports occupy one line; JSON lists emit one object per
line, with an empty stream for no results. Errors go to stderr.

Repository registration uses `repos add --checkout PATH` and
`repos remove --checkout PATH`. Their JSON receipts include the action,
absolute path and affected count. `version hold`, `daemon recover-state` and
`pipeline sparks lint|update` also honor the selected output format.

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

`sparkwing run <pipeline> [flags...]`

Runs a pipeline from the nearest pipeline directory. Engine controls precede
`--`. All pipeline and job arguments follow it:

```bash
sparkwing run deploy --sw-output json -- --environment staging
```

Unknown engine flags fail before execution setup. Local `run` accepts no
connection profile. Use `pipeline trigger --profile NAME` to submit to a remote
controller. The pipeline name selects the deployment shape.

For local execution, `--sw-no-bincache` compiles a fresh Go pipeline executable with debug information.
`--sw-hash-all-files` includes Git-ignored files in binary-cache keys, including
local replacement modules. Both options apply to initial inspection and retained
execution, including detached runs and retries.

Local `--sw-priority front|back` resolves once against the controller's current
machine queue before submission. The sealed request retains the numeric rank.

The CLI resolves the configured pipeline name and SDK entrypoint separately.
The invoked alias remains visible to author code. `--sw-allow-risk` authorizes
declared risk labels. The accepted request retains those permissions, including
through retry. Raw secret arguments never enter previews or public receipts.

`runs retry` requires `--failed` or `--all-jobs` and uses persisted seals. The
caller cannot replace the original source, risk permissions or captured
environment. Local, detached and initial-trigger execution still need their
final native bindings. See [rewrite integration](architecture.md).

## Command groups

Top-level groups, each with its own `--help` and a full per-group page
indexed in [cli-reference.md](cli-reference.md):

| Group | For |
|---|---|
| `info` | Agent entrypoint card: what sparkwing is, what's in this repo, what to run next |
| `pipeline` | This repo's pipelines: list / describe / new / lint / plan / trigger / hooks / sparks |
| `run` | Run a pipeline locally: `sparkwing run <pipeline>` |
| `schedules` | Arm and manage local or controller schedules |
| `runs` | Inspect and manage runs: list / status / logs / stats / retry / cancel / prune, plus `approvals` and `annotations` |
| `repos` | The machine's fleet of sparkwing repos and their SDK pins: list / info / update / add / remove / prune |
| `queue` | `list` reads admission or a selected concurrency group. `priority` changes a queued run's rank. See [Queue views](diagnosing-admission.md#queue-views). |
| `cache` | The compiled pipeline binary cache and the docs cache: info / prune / explain / warm |
| `daemon` | The local controller: status / events / restart / stop / recover-state |
| `version` | Composite CLI + SDK + sparks version card; `upgrade` changes the pinned SDK |
| `update` | Self-update the machine CLI and refresh managed units |
| `upgrade` | Upgrade and verify a repository's pinned SDK with its matching engine |
| `dashboard` | Local controller lifecycle and UI: start / stop / status / logs / install / uninstall |
| `doctor` | Sweep stray local step sessions and inspect controller findings, project toolchains, hooks and competing installs |
| `profiles` | Private machine controller connections and credential files |
| `operator` | Deployment operational limits, object storage and service credentials |
| `tokens` | Team credential metadata, issuance, rotation and revocation |
| `runners` | Runner observations, enrollment and removal |
| `pools` | Team-admin reserved-pool access rules |
| `secrets` | Secrets in the local store, or controller-stored with `--profile`: set / get / list / delete / rotate |
| `debug` | Job inspection and debugging: resume / attach / env |
| `docs` | The embedded copy of this doc tree: list / read / search / migrations |
| `examples` | The worked-pipeline registry; `--example <example> --body` prints the source |
| `commands` | The full CLI surface as JSON (agent self-discovery) |
| `completion` | Shell completion script (`--shell bash\|zsh\|fish\|powershell`) |

## Conventions

- **Structured output.** List / describe / get verbs accept
  `-o pretty|json|plain` (pretty on terminals, JSON when piped). `-o` / `--output` is the one
  output-format selector across the CLI.
- **List output is one record per line.** A listing's `-o json` is
  NDJSON: one complete JSON object per line, no array and no
  pretty-printing, so `head -5` returns five whole records instead of a
  truncated document that parses as nothing. Read the stream a line at a
  time (`json.Decoder` in a loop, `jq -c .` with no `-s`, `while read
  line`, and similar readers). An empty listing is an empty stream. Describe, get, and status verbs return
  one compact JSON object.
- **Root flags.** `-C DIR`, `--profile NAME` and `-o FORMAT` may go before
  the verb, as in `sparkwing -C ~/code/other runs list`. It selects a local checkout before project discovery.
- **Profile addressing.** `--profile NAME` picks a private controller
  connection. Commands with an optional profile use the saved default when
  omitted, then the local controller when no default is selected. Set it with
  `profiles set --profile NAME --default`; `--profile=` selects local for one
  command. Environment and project config cannot choose a default connection.
  `sparkwing run` executes locally and takes no `--profile`.
  `sparkwing pipeline trigger <pipeline> --profile NAME` submits remote work.
- **Required flags.** Marked `[required]` in `--help`; missing ones fail
  before any side effect.
- **Hidden entries.** Pipelines marked `hidden: true` don't appear in
  `pipeline list` or tab-complete but stay invocable by exact name. Pass
  `--all` to `pipeline list` to see them.

Repository updates apply to the selected checkout by default. Use
`repos update --dry-run` to preview and `--all-repos --yes` to apply across the
fleet. With `--in-place`, `--dry-run` reports the SDK pin without editing it.

## Agent discovery

Use the command index to find a command, then read its help:

```bash
sparkwing commands --search status
sparkwing runs status --help
sparkwing pipeline list -o json
sparkwing pipeline describe --pipeline fictional-build -o json
sparkwing pipeline list --search fictional-build -o json
```

Command records carry `path`, `synopsis`, and `subcommand_count`.
Help supplies descriptions, flags, and examples. Hidden commands require
`--all`. See [Bounded discovery](#bounded-discovery) for pagination.

The describe schema uses the shared protocol v2 pipeline description. Project
metadata comes from the `pipelines:` block in `.sparkwing/sparkwing.yaml`.
Named `pipeline describe` reads the selected SDK's current protocol response,
including with `--secrets`. A failed source build or protocol read fails the
command. Pipeline listings can use cached metadata. Named describe always reads
the selected SDK.
Engine selection and `info` read the SDK pin from the pipeline directory's
manifest and lockfile.
The YAML entrypoint selects the SDK registration for flags, help, secret
classification and risks, and planning and execution send it to the SDK. The
run context keeps the invoked pipeline name. Pass required planning inputs after `--`,
for example `sparkwing pipeline plan --pipeline deploy-prod -- --region us-east`.
Previews redact secret arguments while the SDK receives their execution values.

## Headless hosts

SDK programs speak node protocol v2 to a host. They do not run or inspect a
pipeline independently. The host resolves the pinned SDK's verified engine before
installation and planning. A matching node protocol is part of that engine.

The local controller owns daemon lifecycle, state, queue and admission. A
foreground CLI hosts its run. The controller owns detached consumption.
Standalone CI runs the same controller runtime in process with its own home.
An unavailable shared controller does not authorize a second writer to its home.

The controller's four activity holds and explicit stay mode govern idle exit.
Machine updates refresh installed controller and timer units together.
See [local execution](local-execution.md#who-hosts-the-daemon).

## Bounded discovery

Use `sparkwing commands --search status` to find a command by path or
synopsis, then read that command's `--help`. The index sorts command paths
lexically and returns at most 40 command records. `--path` restricts a
subtree before query filtering and pagination. Child counts describe the
full visible registry, even when a child is outside the current page.

`docs list --search <topic>` returns at most 40 topic metadata records.
`docs search --search <question>` ranks matching sections before choosing
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
recognize the final page record separately from command and topic records.

`--limit` requires a positive count. Use `--cursor` to read the next page. Plain mode prints
paths or selectors only; continuation information goes to stderr. Pretty
mode includes a short page footer. Explicit `commands --format markdown`
exports the exhaustive reference and rejects search/pagination flags; a
`--path` export still selects the requested subtree. Use `--output plain`
when redirecting the Markdown artifact to a file.

Misspelled command words suggest the closest command in the same group. Run the
suggested command with `--help` to see its flags. An unknown flag on `runs list`
names the accepted flags and points to `--help`.

`runs status` in plain and pretty formats includes `started_at`, `finished_at`
and `duration` for the run, plus each job's start, finish and duration. Missing
timestamps or a duration without both timestamps appear as `-`. JSON retains
the controller's timestamps.
