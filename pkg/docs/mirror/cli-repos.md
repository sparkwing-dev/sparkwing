<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing repos

Every `sparkwing repos` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing repos`

The machine's fleet of sparkwing repos and their SDK pins

Lists every repo on this machine that carries sparkwing
pipelines -- derived from the repos this laptop has run pipelines
for, unioned with the explicit repos.yaml registry. No manual
registration: a repo shows up once it has run a pipeline or been
added to repos.yaml.

Each row reports the repo, its .sparkwing SDK pin, the last run
observed, and how many migration guides sit between its pin and
the latest release. Linked git worktrees are folded into their
primary checkout; a worktree pinned differently from its primary
is reported as a detail line, not a separate repo.

Bare 'sparkwing repos' and 'sparkwing repos list' both print this
fleet. Use 'sparkwing repos info' for a single-repo deep dive, and
'sparkwing repos update' to bump the whole fleet in one sitting
with a compiled per-repo verdict.

### Subcommands

- `list` -- List the machine's fleet of sparkwing repos
- `info` -- Inspect repository versions, worktrees, store compatibility, and pipelines
- `update` -- Update repository SDK versions and compare pipeline plans

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |

### Examples

```sh
# List the fleet
sparkwing repos

# Agent-readable record
sparkwing repos -o json
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
This is the same output as 'sparkwing repos'.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |

### Examples

```sh
# List the fleet
sparkwing repos list

# Agent-readable record
sparkwing repos list -o json
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
```
