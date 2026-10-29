<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing repos

Every `sparkwing repos` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing repos`

The machine's fleet of sparkwing repos and their SDK pins

### Subcommands

- `add` -- Register a checkout
- `info` -- Inspect repository versions, worktrees, and pipelines
- `list` -- List the machine's fleet of sparkwing repos
- `prune` -- Remove checkouts whose pipeline directory is gone
- `remove` -- Remove a registered checkout
- `update` -- Update repository SDK versions and compare pipeline plans

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing repos add`

Register a checkout

--checkout is required; it must be a local path.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing repos info`

Inspect repository versions, worktrees, and pipelines

--checkout is required; repository identity is distinct from local checkout.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing repos list`

List the machine's fleet of sparkwing repos

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--checkouts` | List each registered local checkout with its status instead of the per-repository version summary. |
| `--pipelines` | With --checkouts, include each checkout's pipeline names, on by default and turned off with --pipelines=false; refused without --checkouts. |

## `sparkwing repos prune`

Remove checkouts whose pipeline directory is gone

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing repos remove`

Remove a registered checkout

--checkout is required; registry basename fallback is refused.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing repos update`

Update repository SDK versions and compare pipeline plans

Apply by default; --dry-run previews. --all-repos requires --yes for bulk changes.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--version TAG` | SDK release tag to move repositories to, latest by default. |
| `--verify` | After the bump, run pre-commit run --all-files and treat a failure as a broken update; refused with --in-place. |
| `--in-place` | Bump the current checkout's SDK pin directly, skipping the before and after plan comparison and the commit. |
| `--dry-run` | Show what would change without changing anything. |
| `--all-repos` | Update every registered repository instead of only the current one; requires --yes unless --dry-run is given. |
| `--yes` | Confirm a destructive change without prompting. |
