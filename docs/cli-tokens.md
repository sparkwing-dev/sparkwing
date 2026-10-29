<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing tokens

Every `sparkwing tokens` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing tokens`

Manage team credentials

### Subcommands

- `create` -- Mint a new API token
- `list` -- List token prefixes + metadata
- `revoke` -- Mark a token revoked
- `rotate` -- Mint a replacement token with a grace window

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing tokens create`

Mint a new API token

Create a member credential through the team token route. Enroll runners with runners add. The controller derives authority and metering. JSON includes the new bearer and metadata; plain emits the bearer once. Member tokens expire within 90 days and end with their minting session. team.admin requires --allow-team-admin and expires within 30 days.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--type KIND` | Kind of token to create, member by default; runners get credentials from runners add instead. Values: member. |
| `--principal NAME` | Not used for member tokens, which always belong to the signed-in account that mints them; the value is ignored. |
| `--scope SCOPE` | Credential scope. Repeatable; comma-separated values are accepted. |
| `--ttl DURATION` | Lifetime of the new token in whole seconds or a duration such as 7d; omitted or zero uses the longest allowed, 90 days, or 30 days with team.admin. |
| `--allow-team-admin` | Allow the team.admin scope on the new token, which then expires within 30 days; without it a team.admin request is refused. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

## `sparkwing tokens list`

List token prefixes + metadata

JSON emits one metadata record per token with exact scopes and no bearer. Plain emits prefixes. --token selects one prefix. Deployment credentials use operator tokens.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--token PREFIX` | Token identifier or prefix. |
| `--type KIND` | List only tokens of this kind. Values: member, runner. |
| `--include-revoked` | Also list tokens that have been revoked. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

## `sparkwing tokens revoke`

Mark a token revoked

JSON retains the confirmed revocation; plain emits the prefix. A failed or unconfirmed revocation returns an error.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--token PREFIX` | Token identifier or prefix. (required) |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

## `sparkwing tokens rotate`

Mint a replacement token with a grace window

JSON includes the new bearer, metadata and old revocation time; plain emits the new bearer. Grace defaults to 24h. Zero TTL preserves the old remaining lifetime. Durations use nonnegative whole seconds.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--token PREFIX` | Token identifier or prefix. (required) |
| `--grace DURATION` | How long the old token keeps working after rotation, in whole seconds or a duration, 24h by default; zero revokes it at once. |
| `--ttl DURATION` | Lifetime of the replacement token in whole seconds or a duration; omitted or zero keeps the old token's remaining lifetime. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |
