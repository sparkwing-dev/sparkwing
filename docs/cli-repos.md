<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
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

- `list` -- List the fleet (the same as bare 'sparkwing repos')
- `info` -- Deep dive on one repo: pin, guides, worktrees, schema, pipelines
- `update` -- Bump every repo's SDK pin with a compiled per-repo verdict

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |

### Examples

```sh
# List the fleet
sparkwing repos

# Agent-readable record
sparkwing repos -o json
```

## `sparkwing repos info`

Deep dive on one repo: pin, guides, worktrees, schema, pipelines

Reports everything worth knowing about one repo without
stitching it together from git, go.mod, and run history by hand. It
defaults to the repo containing the current directory; --repo names
another fleet member by name or checkout path.

It shows the .sparkwing SDK pin (or replace directive) against the
latest release, the migration guides in between with their titles
and summaries, linked worktrees and any that pin a different
version, the working tree's branch, commit, and clean/dirty state,
whether the pin can open the machine's shared state database (a
mismatch is caught here rather than when a run fails), and the
repo's pipelines with their last run time and status. When
something is off it prints one suggested next step.

Read-only: it never builds, bumps, or commits anything.

### Flags

| Flag | Description |
|---|---|
| `--repo NAME_OR_PATH` | Repo by name or checkout path. Default: the repo containing the current directory. |
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Deep dive on the current repo
sparkwing repos info

# Deep dive on a named repo
sparkwing repos info --repo my-app

# Agent-readable record
sparkwing repos info --repo my-app -o json
```

## `sparkwing repos list`

List the machine's fleet of sparkwing repos

Prints the fleet: every repo on this machine that carries
sparkwing pipelines, with its SDK pin, last run, and how many
migration guides sit between its pin and the latest release. This is
the same output as bare 'sparkwing repos'; the explicit verb exists
so the listing has a name alongside 'info' and 'update'.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |

### Examples

```sh
# List the fleet
sparkwing repos list

# Agent-readable record
sparkwing repos list -o json
```

## `sparkwing repos update`

Bump the fleet's SDK pins with a compiled per-repo verdict

Bumps every tracked repo's .sparkwing SDK pin to a target
release and reports a compiled verdict per repo. For each repo with
a clean working tree it bumps the pin, runs go mod tidy, and
plan-constructs every registered pipeline before and after the
bump:

  - clean: the bump compiled and every plan is byte-identical --
    a guaranteed no-behavior-change upgrade.
  - plan-differs: the bump compiled but a plan changed shape; the
    structured node/dep/step diff is shown.
  - broken: the bump failed to apply, compile, or verify; the
    actual error is shown with the crossed migration guides.

Dirty or missing repos are skipped and named rather than guessed
at. Dry-run by default: nothing is written. --apply commits the
bump per repo with a conventional message (no pushes). --verify
additionally runs each repo's pre-commit gate after the bump.
--repo scopes to one repo by name or path.

Because a shared state database refuses an older pin against a
migrated schema, the fleet is meant to move together; the report
leads with that when pins would diverge.

### Flags

| Flag | Description |
|---|---|
| `--version TAG` | Target SDK release (e.g. v0.16.0). Default: latest. |
| `--apply` | Write the bumps and commit per repo (default is a dry run) |
| `--verify` | Run each repo's pre-commit gate after the bump |
| `--repo NAME_OR_PATH` | Scope to a single repo by name or checkout path |
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Preview a fleet-wide bump to latest (dry run)
sparkwing repos update

# Preview a bump to a specific release
sparkwing repos update --version v0.16.0

# Apply the bump and commit per repo
sparkwing repos update --version v0.16.0 --apply

# Scope to one repo and run its gate
sparkwing repos update --repo my-app --verify
```
