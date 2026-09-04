<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
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
- `run` -- Invoke a pipeline (canonical form of `sparkwing run <name>`)
- `trigger` -- Submit a pipeline to a profile's controller (remote execution)
- `hooks` -- Install / uninstall git pre-commit + pre-push + post-commit hooks
- `sparks` -- Manage sparks libraries declared in .sparkwing/sparks.yaml

### Examples

```sh
# Machine-readable catalog
sparkwing pipeline list -o json

# One pipeline's details
sparkwing pipeline describe --name release -o json

# Search by intent
sparkwing pipeline discover --query "tag a release"

# First pipeline in a fresh repo (auto-bootstraps)
sparkwing pipeline new --name release

# Inspect the DAG before running
sparkwing pipeline explain --name release-all

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
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |

### Examples

```sh
# Human-readable
sparkwing pipeline describe --name release

# Agent-readable
sparkwing pipeline describe --name release -o json
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
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |

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

Compiles the nearest .sparkwing/ binary, calls the named
pipeline's Plan method, and prints the resulting DAG (nodes,
dependencies, approval gates) without running a single job.

Any --flag value tokens that are NOT recognized by explain itself
(i.e. anything other than --name / --all / -o/--output / --help) are
forwarded to the pipeline so Plans that branch on --env / --version
/ etc. can be previewed under realistic inputs. Missing required
args are non-fatal here -- explain renders a best-effort plan so
the shape is visible before every flag is provided.

--all sweeps every pipeline in .sparkwing/sparkwing.yaml, runs
Plan() on each with no extra args, and exits non-zero if any
pipeline fails. Designed as a CI gate: a Plan-time validation
mismatch (sparkwing.RefTo[T] type drift, Produces[T] / SetResult
asymmetry, duplicate node ID, etc.) blocks merges before the
pipeline ever runs.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Pipeline to explain (one of --name or --all required) |
| `--all` | Validate every pipeline in this repo's sparkwing.yaml; non-zero exit on any failure |
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Inspect release-all's DAG
sparkwing pipeline explain --name release-all

# Preview with args (forwarded to the pipeline)
sparkwing pipeline explain --name example-release --env prod

# Agent-readable JSON
sparkwing pipeline explain --name release-all -o json

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

Managed hooks carry a "Installed by sparkwing" marker so
uninstall and status can tell them apart from hand-written
hooks. Existing unmanaged hooks are left alone; install skips
them with a warning.

### Subcommands

- `install` -- Install pre-commit / pre-push / post-commit git hooks from sparkwing.yaml triggers
- `uninstall` -- Remove sparkwing-managed git hooks
- `status` -- Report declared, installed, and missing sparkwing hooks
- `survey` -- Report which registered repos git actually runs a gate for
- `fire` -- Make the gate refuse a commit, to see that it can

## `sparkwing pipeline hooks fire`

Make the gate refuse a commit, to see that it can

Stages a file and commits it with the gate told to refuse, then
reports whether git refused the commit and which hook file did it.

A hook directory cannot answer this. A repo whose core.hooksPath points at a
sibling's hooks holds no gate of its own and refuses commits anyway; a repo
whose hooks are shadowed holds a full set and refuses nothing. Both inspect as
something they are not, so survey and status report what is installed and this
reports what happens.

The attempt runs in a throwaway linked worktree with its own index and its own
detached HEAD, so the repo's working tree, index, branches and HEAD are
untouched whatever the gate does. Only a hook sparkwing wrote that carries the
self-test guard is ever executed -- anything else is reported as unprovable
rather than run, because answering a diagnostic question is not a reason to run
an operator's hook.

Every refusal is checked against a control: the same staged change is committed
again with hooks switched off and has to land, so an unrelated failure is not
read as a gate doing its job.

Exits non-zero unless every repo refused the commit with a gate of its own. A
repo that declares no pre-commit trigger has no gate to fire and does not
count. Pre-push gates are not covered -- firing one needs a remote.

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

Discovers the enclosing .sparkwing/sparkwing.yaml, reads
pre_commit / pre_push / post_commit triggers, and writes one hook
file per hook name that fans out to the matching pipelines. Existing
non-sparkwing hooks are skipped so hand-written ones survive.

Before a gate can fire, install runs it once. While a repo's hooks are inert
a gate that cannot execute looks the same as one that passes, and arming it
turns every commit into a failing one. Every proof finishes before candidate
hook filenames or core.hooksPath are published, so prior hooks remain callable
throughout a proof and unchanged if it fails. Complete replacements publish by
atomic rename; a later installation error restores every prior managed hook,
global-hook forwarder, file mode, and config value. No partial set is armed.
--no-prove arms anyway.

Hooks installed without --profile prove and run their pipelines with
--sw-local-only. Pass --profile NAME when the gate should use shared storage.

--fleet counts as armed only the repos a gate now fires in. A repo whose gates
could not run is named as left ungated, and one that declares no pre_commit or
pre_push trigger is counted apart: nothing there can refuse a commit, so there
was never a gate to arm.

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

Lists every managed hook file under .git/hooks/ along with the pipelines it invokes. Declared hooks that are missing, shadowed, or borrowed are named with the command that repairs them.

### Flags

| Flag | Description |
|---|---|
| `--repo DIR` | Repo directory (default: discovered via nearest .sparkwing/) |

### Examples

```sh
# Show hook status
sparkwing pipeline hooks status
```

## `sparkwing pipeline hooks survey`

Report which registered repos git actually runs a gate for

Classifies every repo in the local registry by what git does
with the hooks its pipelines declare: armed (a gate runs), shadowed (a gate is
installed but core.hooksPath sends git elsewhere), uninstalled (a declared
hook was never written), or undeclared (no pipeline asks for one).

The repos are the ones repos.yaml reaches: what sparkwing configure xrepo add
registered, plus any fallback_paths it scans. A checkout it does not list is
not surveyed, so register it before reading a clean survey as a clean
machine.

A registry it cannot read is an error, not an empty fleet: the survey names
the file and exits non-zero rather than printing the output of a machine with
nothing registered.

--ungated lists the repos a commit or a push goes through unchecked in. Only
pre-commit and pre-push count, since a post-commit hook runs after the commit
has landed and cannot refuse one.

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

Deletes every file under .git/hooks/ that carries the "Installed by sparkwing" marker. Hand-written hooks are left alone.

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
and exits non-zero on any violation. Unlike 'explain' (which
builds and runs Plan to validate the resulting DAG), 'lint' reads
the Go source with go/ast -- it never compiles or runs anything,
so it works against a pinned-SDK .sparkwing/ tree.

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
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
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
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
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

Creates a stub pipeline in the nearest .sparkwing/:
jobs/<snake>.go plus a sparkwing.yaml entry. Auto-bootstraps
.sparkwing/ on first use, so a fresh repo's first scaffold sets
up the package skeleton too -- no separate init step, no
sample pipeline you didn't ask for.

--template takes a shape, not a task: it picks the DAG. --on picks
what fires the pipeline, independently. Every combination runs green
in any repo before you edit it, because the Run bodies are echoes.

  --on pull_request   opened / synchronize / reopened
  --on push           any branch
  --on schedule       cron, 09:00 UTC daily
  --on manual         no trigger; runs only when invoked

One pipeline can declare several: --on push,pull_request, or repeat
the flag. 'manual' is the opt-out and cannot be combined with the rest.

Omit --on and the shape's own default applies (below). A trigger is
declarative -- the controller dispatches whichever pipeline its webhook
names -- so it changes nothing about 'sparkwing run <name>' locally,
and the scaffolded block carries a comment naming the filter it does
not have. Edit the 'on:' block in .sparkwing/sparkwing.yaml to change
any of it.

New to authoring? 'sparkwing docs read --guide authoring' returns the
DAG model, the idioms the linter enforces, how a pipeline fires, and
the sparkwing.yaml schema in one call -- the four pages you would
otherwise open one at a time.

Pass --sw-cd/-C to scaffold into a repo other than the current
directory (the .sparkwing search re-anchors there).

Shapes:
  - minimal (default): single-node Plan with a stubbed Run.
    Smallest viable shape; the editor's first move is replacing
    the placeholder Info() line with real logic.
  - build-test-deploy: three-node Plan (build -> test -> deploy)
    with echo Run bodies that print a placeholder line on each step.
    The canonical CI shape; first 'sparkwing run <name>' surfaces three
    exec banners + three echoed lines so the structure is
    visible end-to-end.
  - ci-pr-check: 3 nodes. lint and test run in parallel and a final
    gate job depends on both, so the pipeline is green only when every
    check passes. test Prefers a CI runner label. Defaults to
    '--on pull_request'.
  - release: linear version-bump -> changelog -> publish flow. The
    canonical release shape; publish Prefers a release runner label.
  - scheduled-report: 5 nodes. One collect job seeds three parallel
    gatherers (metrics, errors, usage) and publish-report converges
    them. Defaults to '--on schedule'.

The other three shapes default to no trigger. Any shape takes any
--on, so "PR-triggered single check" is
'--template minimal --on pull_request' rather than a three-node gate
with two nodes deleted.

Every shape scaffolds a pipeline that compiles, renders clean under
'pipeline explain', and passes 'pipeline lint': pure Plan(), runner-label
preferences over host branching, echo Run bodies so the first
'sparkwing run <name>' succeeds end-to-end.

For how a real pipeline is written -- container deploys, migrations,
canary rollouts -- read a worked one: 'sparkwing docs search -q <task>',
then 'sparkwing examples --name <name> --body'. Those are for reading,
not scaffolding; --template will not take one.

Refuses to clobber: if the name already exists in sparkwing.yaml
the command fails before writing anything.

Supply --hidden to hide from default listings; --short to pre-fill
the description.

See also:
  If your pipeline is a single linear shell sequence with no DAG,
  retry, or cross-runner concerns, a plain shell-script runner
  (e.g. just / make / a wrapper over ./bin/*.sh) is probably a
  better fit -- it skips the compile cycle.

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
sparkwing pipeline new --name release-all --template build-test-deploy

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

Compiles the nearest .sparkwing/ binary, calls the named
pipeline's Plan method, and prints the runtime-resolved DAG --
the same structure 'explain' shows plus a per-step decision
("would_run" / "would_skip <reason>") evaluated under the
supplied args and --start-at / --stop-at bounds. NO step bodies
execute.

Skip reasons surface their cause:
  - user_skipif    : a SkipIf predicate would match at run time
  - range_skip     : item is outside the --start-at..--stop-at window

For SpawnNodeForEach generators (dynamic fan-out), cardinality is
reported as "unresolved" with a pointer to the source item -- the
honest answer when the count depends on a runtime value.

State-loading caveat: if a step normally populates in-memory state
that downstream steps consume, --start-at past it leaves state
empty. The plan output reflects this honestly (downstream steps
show "would_run") but operators should design step bodies to
lazy-load when state isn't populated, so resume-from-step is safe.

Like 'explain', this is the read-only pre-flight surface; pair
with 'sparkwing run <name>' to actually dispatch.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Pipeline to plan |
| `--start-at STEP` | Skip every WorkStep upstream of STEP in the resulting plan |
| `--stop-at STEP` | Skip every WorkStep downstream of STEP in the resulting plan |
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Resolve cluster-up's DAG with current args
sparkwing pipeline plan --name cluster-up

# Preview a resume-from-step
sparkwing pipeline plan --name cluster-up --start-at install-argocd

# Agent-readable JSON for diff against expectations
sparkwing pipeline plan --name release-all -o json
```

## `sparkwing pipeline run`

Invoke a pipeline (canonical form of `sparkwing run <name>`)

Compiles the nearest .sparkwing/ binary and exec's it
with the named pipeline. Identical to the top-level shortcut
'sparkwing run <name>'.

The pipeline name is the only positional in the sparkwing
surface -- a deliberate exception, kept short because run is
typed many times a day.

Any flag not recognized by run itself is forwarded to the
pipeline binary, e.g. 'sparkwing pipeline run release
--version v1.2.3' passes --version through to the pipeline's
Args.

### Arguments

- `<pipeline>` (required) -- Pipeline name registered in .sparkwing/sparkwing.yaml

### Flags

| Flag | Description |
|---|---|
| `-C, --sw-cd PATH` | Run as if started in PATH |
| `--sw-ref REF` | Run the pipeline at REF (branch/tag/SHA) instead of the working tree |
| `-v, --sw-verbose` | Enable debug logging |
| `--sw-start-at STEP` | Start the run at STEP |
| `--sw-stop-at STEP` | Stop the run after STEP |
| `--sw-only GLOB` | Run only jobs whose ID matches GLOB (plus their Needs ancestors) |
| `--sw-no-cache` | Ignore cached per-node results (writes still happen) |
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
sparkwing pipeline run build-test-deploy

# Pass a typed pipeline arg
sparkwing pipeline run release --version v0.28.1

# Run from a different git ref
sparkwing pipeline run build-test-deploy --sw-ref feature/xyz

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
'go build -modfile='. The consumer's git-tracked go.mod is
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
sparkwing pipeline sparks lint ~/code/sparks-core

# Re-materialize the overlay modfile
sparkwing pipeline sparks resolve

# Add a library pinned to latest
sparkwing pipeline sparks add github.com/sparkwing-dev/sparks-core
```

## `sparkwing pipeline sparks add`

Add a library to sparks.yaml

Appends a new entry to .sparkwing/sparks.yaml. Defaults the
version to 'latest' when --version is omitted. Refuses to add
a duplicate (same source or same name).

### Flags

| Flag | Description |
|---|---|
| `--source PATH` | Go module path (e.g. github.com/user/sparks-lib) (required) |
| `--version VER` | Declared version ('latest', exact tag, or semver range) |
| `--name NAME` | Short library name (default: last path segment of --source) |
| `--sparkwing-dir DIR` | Path to .sparkwing/ (default: <cwd>/.sparkwing) |

### Examples

```sh
# Add a library pinned to latest
sparkwing pipeline sparks add --source github.com/sparkwing-dev/sparks-core

# Add with a semver range
sparkwing pipeline sparks add --source github.com/sparkwing-dev/sparks-core --version "^v0.10.0"
```

## `sparkwing pipeline sparks inflate`

Copy a spark library's source into this repo so you can edit it

Inflates a spark library: copies its source out of the Go
module cache into .sparkwing/sparks/<name>/, then adds a
'replace <module> => ./sparks/<name>' directive to
.sparkwing/go.mod and runs 'go mod tidy'.

--module takes a sparks-core block name (e.g. 'templates',
which resolves to github.com/sparkwing-dev/sparks-core/templates)
or a full module path for any other spark library.

The version is read from .sparkwing/go.mod's require list, or
'latest' when the module is not yet required.

Because the replace directive points at the copied tree, your
import paths do not change and transitive dependencies keep
resolving -- the code is simply yours now, editable in place.
The command refuses to overwrite an existing destination. To
undo, delete .sparkwing/sparks/<name>/ and drop the replace
directive.

### Flags

| Flag | Description |
|---|---|
| `--module NAME` | Sparks-core block name (e.g. templates) or a full module path (required) |
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
sparkwing pipeline sparks lint --path ~/code/sparks-core

# Lint a multi-module monorepo
sparkwing pipeline sparks lint ~/code/sparks-core
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
sparkwing pipeline sparks remove --name sparks-core

# Remove by source path
sparkwing pipeline sparks remove --name github.com/sparkwing-dev/sparks-core
```

## `sparkwing pipeline sparks resolve`

Resolve versions and materialize the overlay modfile

Runs the same pipeline as 'sparkwing run <name>' takes before compile:
load sparks.yaml, resolve each entry against the module proxy,
and write .sparkwing/.resolved.mod + .resolved.sum. Idempotent
-- a second run with no upstream change is a fast no-op that
prints 'up-to-date'. Never modifies the git-tracked go.mod.

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
sparkwing pipeline sparks update --name sparks-core
```

## `sparkwing pipeline sparks warmup`

Pre-compile pipeline binaries after a sparks release

Post-release optimization: resolve the latest versions, compile
the pipeline binary for the current .sparkwing/ tree, and
upload to gitcache so the next 'sparkwing run' in-cluster or on a
fresh laptop gets a cache hit instead of paying the full
compile cost.

Uses the exact same build path as 'sparkwing run', so the cache key
matches. Warmup is optional -- pipelines always resolve on
build -- it just removes the first-run compile cost after a
new sparks version is published.

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
typed Arg, e.g. 'sparkwing pipeline trigger release --profile
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
sparkwing pipeline trigger release --profile prod --version v1.2.3

# Fire-and-forget; print run id and exit
sparkwing pipeline trigger release --profile prod --detach

# Run the current dirty tree remotely
sparkwing pipeline trigger test --profile gaming --working-tree
```
