# CLI Reference

Sparkwing ships two on-path binaries: `sparkwing` (admin / inspection) and `wing` (the pipeline-runner shortcut — a symlink to `sparkwing` that dispatches by invocation name).

The rule across the `sparkwing` tree: **every input is a named flag** — no positional args anywhere. `wing` is the intentional exception: it takes the pipeline name positionally because operators type it all day.

Every leaf command has a `--help` page with flags, examples, and the full description. This reference is a map, not a manual.

## wing

```
wing <pipeline> [flags...]
```

Runs a pipeline from the nearest `.sparkwing/`. Identical in behavior to `sparkwing run <name>`.

Wing-owned flags (consumed before the pipeline sees them):

| Flag | Description |
|---|---|
| `--on <profile>` | Dispatch remotely via the profile's controller instead of running locally |
| `--from <ref>` | Compile from a git ref (branch/tag/SHA) instead of the working tree |
| `--config <preset>` | Apply a named preset from `.sparkwing/config.yaml` |
| `--verbose`, `-v` | Set `SPARKWING_LOG_LEVEL=debug` on the pipeline binary |

All other `--flag value` tokens are forwarded to the pipeline binary and parsed against the pipeline's typed `Args` struct.

## sparkwing

Top-level nouns, in the order `sparkwing --help` lists them:

```
sparkwing info        Agent entrypoint card: what is sparkwing, what's in this repo
sparkwing pipeline    discover / list / describe / run / new / explain / hooks / sparks
sparkwing run         Positional shortcut for `pipeline run`
sparkwing runs        list / status / logs / retry / cancel / approvals / triggers
sparkwing version     Composite: CLI + SDK + sparks pins; `update --cli|--sdk`
sparkwing dashboard   start / kill / status -- detached local dashboard server
sparkwing cluster     status / agents / worker / gc / push / users / tokens / image / webhooks
sparkwing secrets     set / get / list / delete (laptop dotenv or controller-stored with --on)
sparkwing configure   init / profiles / xrepo
sparkwing debug       run / release / attach / env / rerun / replay
sparkwing docs        list / read / all / search (embedded user docs)
sparkwing commands    Full CLI surface as JSON (agent self-discovery)
sparkwing completion  Shell completion (run once)
```

### pipelines

Per-project surface: list / describe / new pipelines plus the SDK pin (version / update). Every entry in `.sparkwing/pipelines.yaml` is a pipeline; entries with an `on:` trigger auto-fire on push / webhook / schedule, entries without are manual-only.

| Command | What |
|---|---|
| `sparkwing pipeline list [-o table\|json\|plain] [--all]` | Enumerate every pipeline in this repo |
| `sparkwing pipeline describe --name NAME [-o ...]` | Full metadata: args, examples, triggers, help |
| `sparkwing pipeline discover --query TEXT [-o ...]` | Fuzzy search across names / descriptions / tags, ranked |
| `sparkwing pipeline new --name NAME [--template minimal\|build-test-deploy] [--group ...] [--hidden] [--short ...]` | Scaffold a new pipeline (refuses to clobber; auto-bootstraps `.sparkwing/` on first use; default template is `minimal` -- pass `--template build-test-deploy` for a build/test/deploy DAG) |
| `sparkwing pipeline explain --name NAME [--flag value ...] [-o ...]` | Render the Plan DAG without running; unknown flags forward to the pipeline |
| `sparkwing pipeline run NAME [--flag value ...]` | Invoke the pipeline (canonical form). `sparkwing run NAME` and `wing NAME` are the positional shortcuts. |
| `sparkwing pipeline hooks {install\|uninstall\|status}` | Git pre-commit / pre-push hooks for triggers |
| `sparkwing pipeline sparks ...` | Manage sparks libraries declared in `.sparkwing/sparks.yaml` (see "sparks" below) |

To inspect or bump the SDK pin in `.sparkwing/go.mod`, use the top-level `sparkwing version` (composite report) and `sparkwing version update --sdk`.

**JSON schema** (from `list` / `describe`): `name`, `group`, `short`, `help`, `hidden`, `tags`, `triggers`, `entrypoint`, `args`, `examples`.

For repo-local bash chores (formatters, port-forwards, the small Makefile-style stuff) use dowing; sparkwing is the Go-pipeline platform.

### runs

Inspect and manage runs. Reads from the local SQLite store by default; `--on <profile>` routes to a remote controller.

| Command | What |
|---|---|
| `sparkwing runs list` | Recent runs, filterable by `--pipeline` / `--status` / `--tag` / `--since` |
| `sparkwing runs status --run ID` | Rich status summary for one run |
| `sparkwing runs logs --run ID [--node NODE] [--tail N] [--grep STR] [--tree]` | Stream or tail logs for a run / node / subtree |
| `sparkwing runs errors --run ID` | Just the failure records + messages |
| `sparkwing runs failures [--since DUR] [--group-by step\|node]` | Recent failures, clusterable |
| `sparkwing runs stats [--since DUR]` | Aggregate outcomes over a window |
| `sparkwing runs last [--pipeline NAME] [--watch]` | Most recent run (optionally tail forever) |
| `sparkwing runs tree --run ID` | Plan DAG with per-node status |
| `sparkwing runs wait --run ID` | Block until the run reaches a terminal state |
| `sparkwing runs find --pipeline NAME [--status ...] [--since DUR]` | Search runs; exit non-zero if none match |
| `sparkwing runs get --run ID [-o json]` | Full run record as JSON |
| `sparkwing runs retry {--failed\|--all} --run ID` | Re-run a failed / cancelled run; `--failed` reuses passed nodes, `--all` re-executes everything |
| `sparkwing runs cancel --run ID` | Cancel a running run |
| `sparkwing runs prune [--older-than DUR] [--dry-run]` | Delete runs + their log files |

Nested surfaces under `runs`:

| Command | What |
|---|---|
| `sparkwing runs approvals list [--run ID]` | Pending approval gates (or one run's history) |
| `sparkwing runs approvals approve --run ID --node ID [--comment STR]` | Resolve a gate as approved |
| `sparkwing runs approvals deny    --run ID --node ID [--comment STR]` | Resolve a gate as denied |
| `sparkwing runs triggers list` | Pending / claimed / done triggers |
| `sparkwing runs triggers get --id ID` | One trigger's metadata |

### cluster

Operate + inspect a sparkwing cluster. Profile via `--on NAME` picks which cluster.

| Command | What |
|---|---|
| `sparkwing cluster status [--on NAME]` | Roll-up: controller health + fleet + queue + recent runs |
| `sparkwing cluster agents [--on NAME]` | Fleet-view detail (GET /api/v1/agents) |
| `sparkwing cluster worker [--on NAME]` | Laptop-side queue drainer against a remote cluster |
| `sparkwing cluster gc [--on NAME]` | Sweep stale warm-PVC state |
| `sparkwing cluster push [--on NAME]` | Publish HEAD to the profile's gitcache |
| `sparkwing cluster users {add\|list\|delete} [--on NAME]` | Dashboard login users stored on the controller |
| `sparkwing cluster tokens {create\|list\|revoke\|lookup\|rotate} [--on NAME]` | Controller API tokens |
| `sparkwing cluster image rollout --image NAME --tag TAG --on NAME [--wait] [--dry-run]` | Bump a gitops image tag, push, optionally sync + wait |
| `sparkwing cluster webhooks {list\|deliveries\|replay} [--on NAME]` | GitHub webhook debug (wraps `gh api`) |

For the laptop-local dashboard server, see `sparkwing dashboard` (next).
Secrets moved out of `cluster` to top-level `sparkwing secrets`.

### dashboard

Background lifecycle for the laptop-local dashboard. One Go process
hosts the embedded Next.js SPA, the JSON API, the log endpoints, and
the SQLite store on the same port (default `http://127.0.0.1:4343`).

| Command | What |
|---|---|
| `sparkwing dashboard start` | Spawn the detached server (idempotent; re-running prints the URL if already up) |
| `sparkwing dashboard kill` | Stop a running dashboard server |
| `sparkwing dashboard status` | Report whether the dashboard is running and its URL |

### secrets

Top-level since secrets straddle laptop dotenv (default) and
controller-stored (`--on PROF`) and are referenced constantly.

| Command | What |
|---|---|
| `sparkwing secrets set --name K --value V [--on NAME] [--plain]` | Store a secret (laptop or controller) |
| `sparkwing secrets get --name K [--on NAME]` | Print a secret's raw value to stdout |
| `sparkwing secrets list [--on NAME]` | List secret names + metadata (never values) |
| `sparkwing secrets delete --name K [--on NAME]` | Remove a secret |

### configure

Laptop-local config: laptop bootstrap (`init`), connection profiles
(`profiles`), and the cross-repo registry (`xrepo`).

| Command | What |
|---|---|
| `sparkwing configure init` | Set up `~/.config/sparkwing/` and report laptop config status (idempotent) |
| `sparkwing configure profiles list` | Show all profiles (default marked) |
| `sparkwing configure profiles add --name NAME --controller URL [--token TOKEN] [--logs URL] [--gitcache URL] [--default]` | Register a new profile |
| `sparkwing configure profiles show --name NAME` | One profile's full record |
| `sparkwing configure profiles use --name NAME` | Set the active default |
| `sparkwing configure profiles remove --name NAME` | Delete a profile |
| `sparkwing configure profiles duplicate --name SRC --to DST` | Clone a profile under a new name |
| `sparkwing configure profiles set --name NAME [--controller URL] [--token ...] [--logs ...] [--gitcache ...]` | Update fields on an existing profile |
| `sparkwing configure profiles test --name NAME` | Probe controller / auth / logs / gitcache |
| `sparkwing configure xrepo {list\|add\|remove\|prune}` | Cross-repo registry of local sparkwing checkouts |

### spark

Sparks-library management. Reads `.sparkwing/sparks.yaml`.

| Command | What |
|---|---|
| `sparkwing pipeline sparks list` | Declared libraries + resolved versions |
| `sparkwing pipeline sparks lint [--path DIR]` | Validate a spark.json manifest |
| `sparkwing pipeline sparks resolve` | Resolve versions + materialize the overlay go.mod |
| `sparkwing pipeline sparks update [--name NAME]` | Re-resolve one or all libraries |
| `sparkwing pipeline sparks add --source PATH [--version VER] [--name NAME]` | Append a library to sparks.yaml |
| `sparkwing pipeline sparks remove --name NAME` | Remove a library from sparks.yaml |
| `sparkwing pipeline sparks warmup` | Pre-compile pipeline binaries + upload to gitcache |

### debug

Interactive debugging for pipeline runs. Ephemeral — pause directives live only on the run they launch, never in pipeline source.

| Command | What |
|---|---|
| `sparkwing debug run --pipeline NAME [--pause-before NODE] [--pause-after NODE] [--pause-on-failure]` | Run a pipeline with pause hooks the orchestrator honors |
| `sparkwing debug release --run ID --node ID` | Resume a paused node |
| `sparkwing debug attach --run ID --node ID` | `kubectl exec` into a paused node's pod (cluster mode) |
| `sparkwing debug env --run ID --node ID` | Print a paused node's env + workdir + claim holder |
| `sparkwing debug rerun --run ID --node ID` | Reproduce a node's dispatch frame and drop into a shell |
| `sparkwing debug replay --run ID --node ID` | Headlessly re-execute a single node from a prior run |

### info

```
sparkwing info [--json]
```

Agent entrypoint card: a short tour of what sparkwing is, what's in
this repo's `.sparkwing/`, and what to run next. Designed as the first
command an LLM agent should call when dropped into a new repo.

### docs

The `docs/` tree (this site) is also embedded in the binary so the CLI
can read it offline.

| Command | What |
|---|---|
| `sparkwing docs list` | Index of available topics |
| `sparkwing docs read --topic SLUG` | Render one topic to stdout |
| `sparkwing docs all` | Concatenate every topic for piping into an LLM |
| `sparkwing docs search --query TEXT` | Keyword search across embedded docs |

### completion

```
sparkwing completion --shell bash|zsh|fish
```

Emits a shell completion script on stdout. Source it in your rc:

```bash
source <(sparkwing completion --shell zsh)
```

zsh + fish get per-entry descriptions; bash is name-only (compgen limitation).

### version

| Command | What |
|---|---|
| `sparkwing version [--offline]` | Composite: CLI + latest release + this project's SDK + sparks pins |
| `sparkwing version update --cli [--version VER]` | Self-update the CLI binary |
| `sparkwing version update --sdk [--version VER]` | Bump the pipeline SDK pinned in `.sparkwing/go.mod` |

`--cli` and `--sdk` are mutually exclusive; one is required.

## Conventions

- **No positional args on `sparkwing`.** Every input is `--flag value`. `wing` is the exception.
- **Structured output.** Every list / describe / get verb accepts `-o table|json|plain` (default `table`). `--json` is a hidden alias for `-o json`.
- **Remote dispatch.** `--on NAME` picks a profile. Absent, commands read local state (SQLite under `~/.sparkwing/`).
- **Required flags.** Marked `[required]` in `--help`. Missing required flags fail before any side effect.
- **Hidden entries.** Pipelines marked `hidden: true` (yaml) or `# hidden: true` (scripts) don't appear in `pipelines list` / tab-complete. Still invocable by exact name. Pass `--all` to `pipelines list` to see them.

## Agent discovery

Agents should read the catalog via JSON:

```bash
sparkwing pipeline list --json           # every invocable with metadata
sparkwing pipeline describe --name X --json
sparkwing pipeline discover --query TEXT --json
sparkwing pipeline explain --name X --json    # Plan DAG before running
sparkwing run X --flag value  # invoke
```

The JSON schema matches `sparkwing.DescribePipeline` plus `kind` / `group` / `tags` / `triggers` drawn from `pipelines.yaml`.
