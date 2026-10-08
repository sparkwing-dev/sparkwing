<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing repos

Every `sparkwing repos` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing repos`

The machine's fleet of sparkwing repos and their SDK pins

'list' shows registered repositories and repositories with recorded pipeline
runs, with their SDK pins; 'info' inspects one; 'update' validates or applies
SDK upgrades. 'add', 'remove' and 'prune' edit the registry of checkouts.

The registry maps pipeline names to local checkouts so
cross-repo RunAndAwait calls resolve without hardcoded WithFreshRepo
annotations. Auto-populated when you run 'sparkwing run <pipeline>'
in a .sparkwing/-bearing repo (set SPARKWING_NO_AUTO_REGISTER=1 to
disable).

The registry is the repos section of config.yaml: $SPARKWING_CONFIG
(if set), else $XDG_CONFIG_HOME/sparkwing/config.yaml, else
~/.config/sparkwing/config.yaml. SPARKWING_HOME does not move it; it
is the state, cache and logs root, and a registered checkout is a
machine-wide fact that outlives any one home. A write from a command
running under a home of its own is refused rather than sent to the
machine's registry: set SPARKWING_CONFIG to a path inside that home
to keep it there.

### Subcommands

- `list` -- List the machine's fleet of sparkwing repos
- `info` -- Inspect repository versions, worktrees, store compatibility, and pipelines
- `update` -- Update repository SDK versions and compare pipeline plans
- `add` -- Register a checkout
- `remove` -- Remove a registered checkout
- `prune` -- Remove checkouts whose pipeline directory is gone

### Examples

```sh
# List the fleet
sparkwing repos list

# Register the current checkout
sparkwing repos add

# Drop entries whose checkout is gone
sparkwing repos prune
```

## `sparkwing repos add`

Register a checkout

Registers a checkout explicitly. The path defaults to the current directory.

### Arguments

- `[path]` (optional) -- Checkout path; defaults to the current directory

### Examples

```sh
# Register the current checkout
sparkwing repos add

# Register another checkout
sparkwing repos add ../service
```

## `sparkwing repos info`

Inspect repository versions, worktrees, store compatibility, and pipelines

Reports a repository's SDK version, intervening migration guides, linked
worktrees, source revision, uncommitted changes, store compatibility, and
pipeline outcomes. It suggests a next action when a check finds a problem.

Defaults to the enclosing repository. --repo selects another repository by
name or checkout path. The command reads existing state.

### Flags

| Flag | Description |
|---|---|
| `--repo NAME_OR_PATH` | Repo by name or checkout path. Default: the repo containing the current directory. |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |

### Examples

```sh
# Deep dive on the current repo
sparkwing repos info

# Deep dive on a named repo
sparkwing repos info --repo fictional-app

# Agent-readable record
sparkwing repos info --repo fictional-app -o json
```

## `sparkwing repos list`

List the machine's fleet of sparkwing repos

Lists registered repositories and repositories with recorded pipeline runs.
Each row shows the SDK version, last run, and intervening migration guides.
Linked worktrees appear under their primary checkout, with differing SDK
versions reported separately.

--checkouts lists the registry itself instead: each registered checkout, its
status, and the pipelines it provides (--pipelines=false skips the per-repo
describe call).

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
| `--checkouts` | List registered checkouts and their pipelines instead of SDK pins |
| `--pipelines` | With --checkouts, include pipeline names (default: true) |

### Examples

```sh
# List the fleet
sparkwing repos list

# Agent-readable record
sparkwing repos list -o json

# Registered checkouts and their pipelines
sparkwing repos list --checkouts

# Skip pipeline discovery
sparkwing repos list --checkouts --pipelines=false
```

## `sparkwing repos prune`

Remove checkouts whose pipeline directory is gone

Removes registered checkouts that no longer contain a .sparkwing directory.

### Examples

```sh
# Remove stale registry entries
sparkwing repos prune
```

## `sparkwing repos remove`

Remove a registered checkout

Removes every registry entry matching a path or basename.

### Arguments

- `<path-or-basename>` (required) -- Registered path or basename to remove

### Examples

```sh
# Remove a checkout by basename
sparkwing repos remove service
```

## `sparkwing repos update`

Update repository SDK versions and compare pipeline plans

Previews SDK updates across tracked repositories. For each repository with
no uncommitted changes, compares pipeline plans before and after the update
and reports one result:
  clean         compiled and compared plans are byte-identical
  plan-differs  compiled, with a difference in a compared plan
  broken        update, compilation, or verification failed

Plan equality covers the compared structure; execution behavior still needs
verification. Plans that already failed before the update are reported as
not compared. Repositories with uncommitted changes or missing directories
are skipped and named.

--apply writes and commits updates per repository. --verify also runs each
repository's pre-commit gate. --repo selects one repository.

--in-place updates the checkout you stand in (or the one -C names) instead:
native go get for the resolved release, then go mod tidy, with no plan
comparison and no commit. Go keeps its toolchain selection, module
verification and dependency rules. --in-place --check reads the pin and the
release metadata without changing anything: exit 0 means current or ahead, 1
means an update is available, and 2 means unknown, diverged or a check
failure; a local SDK replacement reports unknown. Output is one update_check
record for a check and one update receipt for an update.

Progress goes to stderr. The first interrupt allows the active repository to
restore module files and prints completed results. A second interrupt exits
immediately. The report also identifies divergent SDK pins that may conflict
with the shared store schema.

### Flags

| Flag | Description |
|---|---|
| `--version TAG` | Target SDK release (vX.Y.Z). Default: latest. |
| `--apply` | Write the bumps and commit per repo (default is a dry run) |
| `--verify` | Run each repo's pre-commit gate after the bump |
| `--repo NAME_OR_PATH` | Scope to a single repo by name or checkout path |
| `--in-place` | Bump this checkout's pin with go get and go mod tidy; no plan comparison, no commit |
| `--check` | With --in-place, compare the pin with the release without changing it |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |

### Examples

```sh
# Preview a fleet-wide bump to latest (dry run)
sparkwing repos update

# Preview a bump to a specific release
sparkwing repos update --version v0.16.0

# Apply the bump and commit per repo
sparkwing repos update --version v0.16.0 --apply

# Scope to one repo and run its gate
sparkwing repos update --repo fictional-app --verify

# Bump this checkout's SDK pin
sparkwing repos update --in-place

# Check this checkout's SDK pin
sparkwing repos update --in-place --check
```
