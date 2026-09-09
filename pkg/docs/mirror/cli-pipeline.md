<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing pipeline

Every `sparkwing pipeline` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing pipeline`

This repo's pipelines

Per-project namespace. Every verb here operates on the
nearest .sparkwing/ walking up from the current directory.

Discovery (list / describe / discover / explain) shows what
pipelines this repo defines. 'new' scaffolds a fresh pipeline
(auto-bootstraps .sparkwing/ on first use). 'run' invokes one
(positional name; same as 'sparkwing run <name>'). 'hooks' wires
pipelines to git pre-commit / pre-push / post-commit.
'sparks' manages reusable spark libraries declared in
.sparkwing/sparks.yaml.

The discovery verbs (list / describe / discover / templates)
support -o json so an agent can parse output directly rather
than scraping tab-complete.

To bump the pipeline SDK pin in .sparkwing/go.mod, use
'sparkwing version update --sdk'. To see the current pin, run
'sparkwing version' (composite card).

### Subcommands

- `list` -- Enumerate every pipeline with metadata
- `describe` -- Print one pipeline's full metadata
- `discover` -- Fuzzy search over pipeline names + descriptions + tags
- `new` -- Scaffold a new Go pipeline
- `explain` -- Render the pipeline's Plan DAG without dispatching any jobs
- `lint` -- Check pipeline source for idiomatic anti-patterns (enforced gate)
- `plan` -- Render the runtime-resolved DAG without dispatching any jobs
- `run` -- Invoke a pipeline
- `trigger` -- Submit a pipeline to a profile's controller (remote execution)
- `hooks` -- Install / uninstall git pre-commit + pre-push + post-commit hooks
- `sparks` -- Manage sparks libraries declared in .sparkwing/sparks.yaml

### Examples

```sh
# Machine-readable catalog
sparkwing pipeline list -o json

# One pipeline's details
sparkwing pipeline describe --name fictional-release -o json

# Search by intent
sparkwing pipeline discover --query "tag a release"

# First pipeline in a new repository (auto-bootstraps)
sparkwing pipeline new --name release

# Inspect the DAG before running
sparkwing pipeline explain --name fictional-release

# Run a pipeline
sparkwing pipeline run release
```

## `sparkwing pipeline describe`

Print one pipeline's full metadata

Emits the full record for a single pipeline: kind, group,
description, typed args, examples, triggers, and (for scripts)
frontmatter-declared positional args and flags. Always resolves
hidden entries -- if you're asking for a name explicitly, the
hidden flag shouldn't surprise you.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Pipeline name to describe (required) |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |

### Examples

```sh
# Human-readable
sparkwing pipeline describe --name release

# Agent-readable
sparkwing pipeline describe --name fictional-release -o json
```

## `sparkwing pipeline discover`

Fuzzy search over pipeline names + descriptions + tags

Search the catalog by intent. Every token in --query
must match some haystack field (name / short / help / group /
tags / triggers); matches in the name score higher than matches
in prose so direct hits surface first.

-o json emits {name, kind, group, ..., score} records sorted by
score descending; agents should prefer -o json for consumption.

### Flags

| Flag | Description |
|---|---|
| `--query TEXT` | Search query (one or more tokens, all must hit some field) (required) |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |

### Examples

```sh
# Find release-related pipelines
sparkwing pipeline discover --query release

# Multi-token, all must hit
sparkwing pipeline discover --query "tag release"

# Agent-readable ranked hits
sparkwing pipeline discover --query deploy -o json
```

## `sparkwing pipeline explain`

Render the pipeline's Plan DAG without dispatching any jobs

Compiles the pipeline binary, calls the named pipeline's Plan method, and
prints its nodes, dependencies, and approval gates. Jobs remain unexecuted.

Arguments other than --name, --all, -o/--output, and --help pass to the
pipeline. This previews plans controlled by --env, --version, and similar
inputs. Missing required arguments are tolerated so the plan can be inspected
before every input is supplied.

--all constructs every declared pipeline with no extra arguments and exits
non-zero if any plan fails validation. Validation detects mismatched typed
references, inconsistent declared outputs, duplicate node IDs, and similar
errors.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Pipeline to explain (one of --name or --all required) |
| `--all` | Validate every pipeline in this repo's sparkwing.yaml; non-zero exit on any failure |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |

### Examples

```sh
# Inspect the example release DAG
sparkwing pipeline explain --name fictional-release

# Preview with args (forwarded to the pipeline)
sparkwing pipeline explain --name example-release --env prod

# Agent-readable JSON
sparkwing pipeline explain --name fictional-release -o json

# Validate every pipeline (CI gate)
sparkwing pipeline explain --all
```

## `sparkwing pipeline hooks`

Install / uninstall git pre-commit + pre-push + post-commit hooks

Writes small git hook scripts into the repo's .git/hooks/
directory that call 'sparkwing run <pipeline>' for every pipeline that
declares pre_commit:, pre_push:, or post_commit: in its
.sparkwing/sparkwing.yaml triggers block.

The post-commit hook is non-blocking: the commit has already
landed, so it runs its pipelines, tolerates failures, and never
aborts. pre-commit and pre-push abort the git action on the first
failing pipeline.

Managed hooks carry an "Installed by sparkwing" marker so
uninstall and status can tell them apart from hand-written
hooks. Existing unmanaged hooks are left alone; install skips
them with a warning.

### Subcommands

- `install` -- Install pre-commit / pre-push / post-commit git hooks from sparkwing.yaml triggers
- `uninstall` -- Remove sparkwing-managed git hooks
- `status` -- Report declared, installed, and missing sparkwing hooks
- `survey` -- Report effective gates for registered repositories
- `fire` -- Make the gate refuse a commit, to see that it can

## `sparkwing pipeline hooks fire`

Make the gate refuse a commit, to see that it can

Attempts a commit with a managed gate instructed to refuse it and reports
whether the gate blocked Git. A control attempt with hooks disabled must
succeed, so an unrelated commit failure cannot count as a passing diagnostic.

The attempts use a temporary detached worktree and index. The source checkout
and its branches remain unchanged. Only managed hooks carrying the diagnostic
guard execute; other hooks are reported as unprovable.

Exits nonzero unless every applicable repository refused the test commit
through its own gate. Repositories without a pre-commit trigger are excluded.
This command verifies pre-commit hooks.

### Flags

| Flag | Description |
|---|---|
| `--repo DIR` | Repo directory (default: discovered via nearest .sparkwing/) |
| `--fleet` | Fire the gate in every registered repo instead of one |
| `-o, --output FMT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Prove this repo's gate refuses a commit
sparkwing pipeline hooks fire

# Prove every registered repo's gate
sparkwing pipeline hooks fire --fleet

# Machine-readable
sparkwing pipeline hooks fire -o json
```

## `sparkwing pipeline hooks install`

Install pre-commit / pre-push / post-commit git hooks from sparkwing.yaml triggers

Installs managed hooks for declared pre_commit, pre_push, and post_commit
triggers. Existing unmanaged hooks are preserved and reported.

Each gate runs successfully before replacement hooks are published. Existing
hooks remain callable during verification. Publication uses atomic rename;
an installation failure restores prior managed hooks, forwarders, modes,
and configuration. --no-prove skips gate execution.

Without --profile, hooks use --sw-local-only. --profile NAME selects shared
storage. --fleet processes registered repositories and distinguishes installed
gates, gates that could not execute, and repositories declaring no blocking
gate.

### Flags

| Flag | Description |
|---|---|
| `--repo DIR` | Repo directory (default: discovered via nearest .sparkwing/) |
| `--fleet` | Install into every registered repo instead of one |
| `--no-prove` | Claim core.hooksPath without running the gate first |
| `--profile NAME` | Pin the hook's runs to this storage profile (default: local-only) |

### Examples

```sh
# Install in the current repo
sparkwing pipeline hooks install

# Install in a different repo
sparkwing pipeline hooks install --repo /path/to/repo

# Arm every registered repo
sparkwing pipeline hooks install --fleet

# Pin the gate's runs to one store
sparkwing pipeline hooks install --profile bucket
```

## `sparkwing pipeline hooks status`

Report declared, installed, and missing sparkwing hooks

Lists every managed hook file under .git/hooks/ along with the pipelines it
invokes. Declared hooks that are missing, shadowed, or borrowed are named with
the command that repairs them.

### Flags

| Flag | Description |
|---|---|
| `-o, --output pretty\|json\|plain` | Pretty on a terminal, NDJSON otherwise. Plain prints hook names. |
| `--repo DIR` | Repo directory (default: discovered via nearest .sparkwing/) |

### Examples

```sh
# Show hook status
sparkwing pipeline hooks status
```

## `sparkwing pipeline hooks survey`

Report effective gates for registered repositories

Reports declared hooks for every registered repository as armed, shadowed,
uninstalled, or undeclared. A shadowed hook is installed but core.hooksPath
selects another location.

Coverage includes registered repositories and configured fallback paths.
Register other checkouts before expecting them in the report. An unreadable
registry produces an error.

--ungated selects repositories where commits or pushes run without a gate.
Only pre-commit and pre-push hooks can block those operations.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FMT` | Output format: pretty\|json\|plain |
| `--ungated` | List only the repos git runs no gate for |

### Examples

```sh
# Survey the fleet
sparkwing pipeline hooks survey

# Just the ungated repos
sparkwing pipeline hooks survey --ungated

# Machine-readable
sparkwing pipeline hooks survey -o json
```

## `sparkwing pipeline hooks uninstall`

Remove sparkwing-managed git hooks

Deletes every file under .git/hooks/ that carries the "Installed by sparkwing"
marker. Hand-written hooks are left alone.

### Flags

| Flag | Description |
|---|---|
| `--repo DIR` | Repo directory (default: discovered via nearest .sparkwing/) |

### Examples

```sh
# Uninstall in the current repo
sparkwing pipeline hooks uninstall
```

## `sparkwing pipeline lint`

Check pipeline source for idiomatic anti-patterns (enforced gate)

Statically analyzes pipeline source for the anti-patterns
that make a Plan() non-deterministic, impure, or misconfigured,
and exits non-zero on any violation. It inspects the Go syntax tree without
compilation or execution, including source pinned to another SDK version.

Only the Plan() body is inspected; code inside job/step closures
and SkipIf / BeforeRun bodies runs at dispatch, so I/O and
environment reads there are idiomatic and never flagged.

The rule set (see --rules for each rule's charter):
  plan-io              I/O (shell, exec, file, http) in Plan()
  plan-runtime-branch  os.Getenv / runtime.GOOS / IsLocal branching in Plan()
  runner-label         blank runner labels; Inline + Requires on one job
  unused-ref           a RefTo result discarded into _ or a bare statement
  guard-misuse         pipeline guards that can never be satisfied together

With no target it sweeps every pipeline in .sparkwing/sparkwing.yaml
and exits non-zero if any violates a rule -- designed as a CI gate
alongside 'explain --all'. --all says the same thing explicitly.
--name lints a single pipeline. Source defaults to <.sparkwing>/jobs;
override with --dir.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Pipeline to lint (default: every pipeline) |
| `--all` | Lint every pipeline in this repo's sparkwing.yaml; the default, non-zero exit on any violation |
| `--rules` | Print each rule's charter (what it forbids and why) and exit |
| `--dir DIR` | Directory of pipeline source to scan (default: <.sparkwing>/jobs) |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
| `-C, --sw-cd DIR` | Operate as if started in this directory (re-anchors the .sparkwing search) |

### Examples

```sh
# Lint one pipeline
sparkwing pipeline lint --name release

# Lint every pipeline (CI gate)
sparkwing pipeline lint --all

# Agent-readable findings
sparkwing pipeline lint --all -o json

# Show the rule set
sparkwing pipeline lint --rules
```

## `sparkwing pipeline list`

Enumerate every pipeline with metadata

Walks up from the current directory to locate .sparkwing/,
merges sparkwing.yaml entries with the describe cache's typed
metadata, and prints a grouped aligned table.

-o json emits structured records instead; agents should prefer
-o json since tab-complete / table output is for human reading.

--all includes entries marked 'hidden: true'. By default they're
omitted.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
| `--all` | Include entries marked hidden |

### Examples

```sh
# Human-readable table
sparkwing pipeline list

# Agent-readable catalog
sparkwing pipeline list -o json

# Include hidden entries
sparkwing pipeline list --all
```

## `sparkwing pipeline new`

Scaffold a new Go pipeline

Creates a pipeline source file and registers its name. Creates the pipeline
module when the repository has none. An existing pipeline name is refused
before files are written.

--template selects the dependency graph. --on selects triggers independently:
  pull_request   opened, synchronize, and reopened events
  push           any branch
  schedule       daily at 09:00 UTC
  manual         explicit invocation only

Repeat --on or separate events with commas. 'manual' must stand alone.
Edit the generated trigger configuration to add filters.

Templates:
  minimal            one node with a placeholder action; no trigger
  build-test-deploy  sequential build, test, and deploy; no trigger
  ci-pr-check        parallel lint and test, followed by a gate; pull_request
  release            version, changelog, and publish sequence; no trigger
  scheduled-report   collect, parallel gatherers, then publish; schedule

Generated actions print placeholder output. Replace them with the work the
pipeline should perform. Use 'sparkwing docs read --guide authoring' for
pipeline authoring guidance and 'sparkwing examples' for complete examples.

--sw-cd/-C selects another repository. --hidden hides the entry from default
listings. --short sets its description.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | New pipeline's kebab-case name (a-z, 0-9, -) (required) |
| `-C, --sw-cd DIR` | Scaffold as if started in this directory (re-anchors the .sparkwing search) |
| `--template SHAPE` | DAG to scaffold: minimal (1 node) \| build-test-deploy (3) \| ci-pr-check (3) \| release (3) \| scheduled-report (5) (default: minimal) |
| `--on EVENT` | Trigger(s) to declare: pull_request \| push \| schedule \| manual (repeatable or comma-separated) (default: the shape's own) |
| `--hidden` | Mark the entry hidden in default tab-complete menus |
| `--short TEXT` | Pre-fill the ShortHelp / desc line |

### Examples

```sh
# Single-node pipeline (default shape)
sparkwing pipeline new --name release

# Build/test/deploy DAG (three-node)
sparkwing pipeline new --name fictional-release --template build-test-deploy

# Pull-request gate (lint + test -> gate)
sparkwing pipeline new --name pr-check --template ci-pr-check

# Scheduled fan-out report
sparkwing pipeline new --name daily-report --template scheduled-report

# One job, fired by pull requests
sparkwing pipeline new --name pr-test --template minimal --on pull_request

# Fired by both push and pull requests
sparkwing pipeline new --name ci --template ci-pr-check --on push,pull_request

# A gate you invoke by hand, not on every PR
sparkwing pipeline new --name gate --template ci-pr-check --on manual
```

## `sparkwing pipeline plan`

Render the runtime-resolved DAG without dispatching any jobs

Compiles the pipeline and evaluates its plan under the supplied arguments.
Each step reports would_run or would_skip with its reason. Step actions are
not executed.

Skip reasons:
  user_skipif  the SkipIf predicate matches
  range_skip   the step falls outside --start-at and --stop-at

Dynamic fan-out counts remain unresolved when they require execution.
Skipping a state-loading step with --start-at leaves that state empty;
downstream predicates are evaluated with the resulting state.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Pipeline to plan |
| `--start-at STEP` | Skip every WorkStep upstream of STEP in the resulting plan |
| `--stop-at STEP` | Skip every WorkStep downstream of STEP in the resulting plan |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |

### Examples

```sh
# Resolve the example cluster DAG with supplied arguments
sparkwing pipeline plan --name fictional-cluster

# Preview a resume-from-step
sparkwing pipeline plan --name fictional-cluster --start-at fictional-install

# Agent-readable JSON for diff against expectations
sparkwing pipeline plan --name fictional-release -o json
```

## `sparkwing pipeline run`

Invoke a pipeline

Compiles and runs the named pipeline from the nearest pipeline directory.
This command has the same behavior as 'sparkwing run <pipeline>'.

Runner options use the --sw- prefix. Unknown --sw- options fail before
execution setup. Other arguments pass to the pipeline. Put -- before
pipeline arguments that resemble runner options; every argument after the
separator passes through unchanged.

### Arguments

- `<pipeline>` (required) -- Pipeline name registered in .sparkwing/sparkwing.yaml

### Flags

| Flag | Description |
|---|---|
| `-C, --sw-cd PATH` | Run as if started in PATH |
| `--sw-ref REF` | Run the pipeline at REF (branch/tag/SHA) instead of the working tree |
| `--sw-detached` | Queue the run for this machine's resident consumer and print its handle instead of executing here; the run outlives the terminal |
| `--sw-idempotency-key KEY` | Detached only: deduplication token; a repeat carrying this key returns the original run instead of starting a second one |
| `--sw-request-id ID` | Detached only: tracing identifier recorded on the run; never affects deduplication |
| `--sw-consumer-idle DUR` | Detached only, and only if this starts a consumer: how long it stays alive with no work (default 5m) |
| `--sw-consumer-claim-lease DUR` | Detached only, and only if this starts a consumer: the lease it stamps on each claimed run, renewed while the run executes (default 3m) |
| `--sw-output FORMAT` | Detached only: run-handle format, pretty\|json\|plain (default: pretty on a TTY, json when piped) |
| `-v, --sw-verbose` | Enable debug logging and the complete live JSON event stream |
| `--sw-start-at STEP` | Start the run at STEP |
| `--sw-stop-at STEP` | Stop the run after STEP |
| `--sw-only GLOB` | Run only jobs whose ID matches GLOB (plus their Needs ancestors) |
| `--sw-no-cache` | Ignore cached per-node results (writes still happen) |
| `--sw-priority VALUE` | Local admission priority: an integer, or front/back for one step past the queue as it stands; overrides the plan's own Priority |
| `--sw-local-only` | Force local secrets, state, cache, and logs for this run; ignore any configured shared backends |
| `--sw-fleet` | Let explicitly enrolled helpers execute nodes under this foreground process's authority |
| `--sw-dry-run` | Run each step's dry-run probe instead of its real action |
| `--sw-allow LABEL[,LABEL...]` | Authorize risk-labeled steps (repeatable) |
| `--sw-index PATH` | Judge the git index at PATH instead of the repository's own (prints an index_bound event naming it) |
| `--sw-run-handle-file PATH` | Atomically publish the accepted run's machine-readable handle to PATH |
| `--sw-isolated-home DIR` | Keep this run's state and config under DIR, so it hosts an admission daemon from the sparkwing you invoked instead of joining the machine's |
| `--profile NAME` | Run / read against the named profile from ~/.config/sparkwing/profiles.yaml (default: laptop) |
| `--target TARGET` | Run against the named pipeline deployment target (e.g. dev, prod) |

### Examples

```sh
# Run with no flags
sparkwing pipeline run fictional-build

# Pass a typed pipeline arg
sparkwing pipeline run fictional-release --version v0.28.1

# Run from a different git ref
sparkwing pipeline run fictional-build --sw-ref feature/xyz

# Dispatch remotely
sparkwing pipeline trigger deploy --profile prod
```

## `sparkwing pipeline sparks`

Manage sparks libraries declared in .sparkwing/sparks.yaml

Sparks libraries are Go modules that add opinionated helpers
(Docker builds, GitOps deploys, ECR auth, language-specific
checks) on top of the unopinionated SDK. Consumers declare
which libraries they want live-tracked in
.sparkwing/sparks.yaml; the resolver writes an overlay modfile
at .sparkwing/.resolved.mod that the compile step uses via
'go build -modfile='. The consumer's tracked go.mod is
never modified.

See docs/sparks.md for the full spec (spark.json schema,
sparks.yaml shape, resolution rules, warmup).

### Subcommands

- `list` -- Show declared sparks libraries and their resolved versions
- `lint` -- Validate a spark.json library manifest
- `resolve` -- Resolve versions and materialize the overlay modfile
- `update` -- Re-resolve one or all libraries
- `add` -- Add a library to sparks.yaml
- `remove` -- Remove a library from sparks.yaml
- `warmup` -- Pre-compile pipeline binaries after a sparks release
- `inflate` -- Copy a spark library's source into this repo so you can edit it

### Examples

```sh
# List declared sparks libraries
sparkwing pipeline sparks list

# Validate a library's spark.json
sparkwing pipeline sparks lint ~/code/fictional-sparks

# Re-materialize the overlay modfile
sparkwing pipeline sparks resolve

# Add a library pinned to latest
sparkwing pipeline sparks add example.com/fictional/sparks
```

## `sparkwing pipeline sparks add`

Add a library to sparks.yaml

Appends a new entry to .sparkwing/sparks.yaml. Defaults the
version to 'latest' when --version is omitted. Refuses to add
a duplicate (same source or same name).

### Flags

| Flag | Description |
|---|---|
| `--source PATH` | Go module path (required) |
| `--version VER` | Declared version ('latest', exact tag, or semver range) |
| `--name NAME` | Short library name (default: last path segment of --source) |
| `--sparkwing-dir DIR` | Path to .sparkwing/ (default: <cwd>/.sparkwing) |

### Examples

```sh
# Add a library pinned to latest
sparkwing pipeline sparks add --source example.com/fictional/sparks

# Add with a semver range
sparkwing pipeline sparks add --source example.com/fictional/sparks --version "^v0.10.0"
```

## `sparkwing pipeline sparks inflate`

Copy a spark library's source into this repo so you can edit it

Copies a library from the Go module cache into the pipeline's local sources,
adds a module replacement pointing at that copy, and runs 'go mod tidy'.
Imports retain their module paths.

--module accepts a sparks-core module name or a full module path. The version
comes from the pipeline's required modules, or resolves to latest when absent.
The destination must be unused. To undo the copy, remove its directory and
module replacement.

### Flags

| Flag | Description |
|---|---|
| `--module NAME` | Sparks-core module name or full module path (required) |
| `--sparkwing-dir DIR` | Path to .sparkwing/ (default: <cwd>/.sparkwing) |
| `-o, --output FMT` | Output format: pretty\|json |

### Examples

```sh
# Inflate the sparks-core templates module
sparkwing pipeline sparks inflate --module templates

# Inflate any spark library by module path
sparkwing pipeline sparks inflate --module github.com/example/my-sparks
```

## `sparkwing pipeline sparks lint`

Validate a spark.json library manifest

Loads spark.json from the given path (or the current directory
if omitted) and checks: required fields (name, description,
author), that the manifest declares exactly one non-empty
entry array -- packages[] for a library that is one Go module,
modules[] for a monorepo of independently tagged modules --
that each entry path exists as a directory under the manifest
root and describes itself, that a modules[] entry names the Go
module its directory's go.mod declares, that stability values
are valid, and that paths are not duplicated. Unknown fields
are a soft warning, not an error. Exits non-zero on any hard
failure.

### Arguments

- `[path]` (optional) -- Library directory or spark.json path, when --path is not supplied

### Flags

| Flag | Description |
|---|---|
| `--path PATH` | Library directory or direct spark.json path. Positional fallback accepted. (default: .) |

### Examples

```sh
# Lint the library in the current directory
sparkwing pipeline sparks lint

# Lint a sibling library by path
sparkwing pipeline sparks lint --path ~/code/fictional-sparks

# Lint a multi-module monorepo
sparkwing pipeline sparks lint ~/code/fictional-sparks
```

## `sparkwing pipeline sparks list`

Show declared sparks libraries and their resolved versions

Reads .sparkwing/sparks.yaml and prints one row per declared
library with its declared constraint and the resolved tag
(found via the module proxy). Use --no-resolve to skip the
proxy calls when offline.

### Flags

| Flag | Description |
|---|---|
| `--sparkwing-dir DIR` | Path to .sparkwing/ (default: <cwd>/.sparkwing) |
| `-o, --output FMT` | Output format: pretty\|json\|plain |
| `--no-resolve` | Skip module-proxy lookups; print declared versions only |

### Examples

```sh
# Table output
sparkwing pipeline sparks list

# JSON for scripting
sparkwing pipeline sparks list -o json

# Offline (no proxy calls)
sparkwing pipeline sparks list --no-resolve
```

## `sparkwing pipeline sparks remove`

Remove a library from sparks.yaml

Removes the entry matching NAME (or matching its source path).

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Library name or source path to remove (required) |
| `--sparkwing-dir DIR` | Path to .sparkwing/ (default: <cwd>/.sparkwing) |

### Examples

```sh
# Remove by short name
sparkwing pipeline sparks remove --name fictional-sparks

# Remove by source path
sparkwing pipeline sparks remove --name example.com/fictional/sparks
```

## `sparkwing pipeline sparks resolve`

Resolve versions and materialize the overlay modfile

Resolves declared libraries through the Go module proxy and writes the
module overlay used for pipeline builds. Prints 'up-to-date' when the
overlay already matches. The repository's module file stays unchanged.

### Flags

| Flag | Description |
|---|---|
| `--sparkwing-dir DIR` | Path to .sparkwing/ (default: <cwd>/.sparkwing) |
| `-q, --quiet` | Suppress the 'up-to-date' message |

### Examples

```sh
# Resolve and write the overlay
sparkwing pipeline sparks resolve

# Quiet mode for scripts
sparkwing pipeline sparks resolve -q
```

## `sparkwing pipeline sparks update`

Re-resolve one or all libraries

Re-runs resolution for every declared library (or a single
named one) and re-materializes the overlay modfile. For a
range or 'latest' constraint this picks up any new tag from
the module proxy; for an exact pin it is a no-op.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Restrict update to one library (name or source); omit to update all |
| `--sparkwing-dir DIR` | Path to .sparkwing/ (default: <cwd>/.sparkwing) |

### Examples

```sh
# Update every declared library
sparkwing pipeline sparks update

# Update one by name
sparkwing pipeline sparks update --name fictional-sparks
```

## `sparkwing pipeline sparks warmup`

Pre-compile pipeline binaries after a sparks release

Resolves libraries, compiles the pipeline binary, and uploads it to the
binary cache. Subsequent runs with matching build inputs can reuse it.
Warmup uses the same compilation path and cache key as 'sparkwing run'.

### Flags

| Flag | Description |
|---|---|
| `--sparkwing-dir DIR` | Path to .sparkwing/ (default: <cwd>/.sparkwing) |
| `--clear-cache` | Delete the local pipeline binary cache before compiling |

### Examples

```sh
# Warm up the current repo's pipelines
sparkwing pipeline sparks warmup

# Force a fresh compile
sparkwing pipeline sparks warmup --clear-cache
```

## `sparkwing pipeline trigger`

Submit a pipeline to a profile's controller (remote execution)

Submits a trigger to the controller defined by --profile and
follows the remote run until it reaches a terminal state.

When the profile defines a logs URL, the follow streams full log
output; otherwise it shows node-status updates from the
controller. --detach skips the follow and prints the run id once
the trigger is registered (the trigger POST itself always
completes before the command exits, so the run is guaranteed
queued).

A follow exits on the run's outcome, matching a local run:
0 when the run succeeded, 1 when it failed or was cancelled,
and 3 when the follow ended without a readable terminal status
(the run may still be in progress -- re-check it with
'sparkwing runs status --run <id> --profile <p>'). The status
block and failing-node errors print to stderr on either follow
mode, so redirecting stdout still shows why a run failed.
--detach exits 0 once the trigger is queued -- it reports
submission, not outcome.

Any flag not recognized here is forwarded to the pipeline as a
typed argument. For example, 'sparkwing pipeline trigger release --profile
prod --version v1.2.3' passes --version through to the trigger
payload -- same shape as 'sparkwing run'.

--working-tree freezes tracked changes and untracked non-ignored
files into an immutable Git snapshot, uploads it before admission,
and runs that exact snapshot without pushing to the origin. It
requires a complete SHA-1 repository; shallow and SHA-256 checkouts
fail before upload.

Requires a profile with controller: set. For local execution
against a profile's storage, use 'sparkwing run --profile X'.

### Arguments

- `<pipeline>` (required) -- Pipeline name registered on the controller

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile (from ~/.config/sparkwing/profiles.yaml) whose controller runs the pipeline (required) |
| `--detach` | Return once the trigger is registered (print the run id); don't follow |
| `--working-tree` | Run tracked changes and untracked non-ignored files from an immutable remote snapshot |

### Examples

```sh
# Submit and follow
sparkwing pipeline trigger fictional-release --profile prod --version v1.2.3

# Fire-and-forget; print run id and exit
sparkwing pipeline trigger fictional-release --profile prod --detach

# Run the current dirty tree remotely
sparkwing pipeline trigger test --profile gaming --working-tree
```
