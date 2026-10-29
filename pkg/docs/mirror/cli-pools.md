<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing pools

Every `sparkwing pools` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing pools`

Manage team runner pools

### Subcommands

- `allow` -- Allow a repository and branch to use a pool
- `disallow` -- Remove a pool access rule
- `list` -- List runner pools

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing pools allow`

Allow a repository and branch to use a pool

Require independently verified repository authority; a typed repository or branch grants no trust.

Create a rule through the team-admin route with an idempotency key. JSON preserves the rule and replay result; plain emits its ID.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--pool POOL` | Runner pool name. (required) |
| `--repo REPO` | GitHub repositories the rule admits, as owner/name; each must be reachable through the team's GitHub App, and naming one grants nothing on its own. Repeatable; comma-separated values are accepted. (required) |
| `--branch BRANCH` | Branch name patterns the rule admits, with * and ? wildcards that do not cross a slash; omitted, every ref of the repositories qualifies, tags included. Repeatable; comma-separated values are accepted. |
| `--source SOURCE` | Admit only runs that a GitHub App webhook delivery started; omitted, schedules whose commit the GitHub App resolved also qualify, while runs proved by GitHub Actions or fetched by the controller never do. Values: github. |
| `--route` | Also keep runs from the matching repositories and branches that select no runner on this pool's self-hosted runners instead of Sparkwing Cloud. |
| `--profile PROFILE` | Controller connection profile from your config. Omit it to use the local controller. |

## `sparkwing pools disallow`

Remove a pool access rule

Delete the selected pool rule through the team-admin route. JSON retains whether it was deleted or already absent; plain emits the rule ID.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--pool POOL` | Runner pool name. (required) |
| `--rule RULE` | ID of the pool rule to delete, as pools list shows it. (required) |
| `--profile PROFILE` | Controller connection profile from your config. Omit it to use the local controller. |

## `sparkwing pools list`

List runner pools

Read controller-granted reservations and access rules. JSON emits each pool with its runners and rules; plain emits pool names.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile PROFILE` | Controller connection profile from your config. Omit it to use the local controller. |
