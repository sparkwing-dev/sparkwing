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

Create a member or runner credential through the team token route. The controller derives authority and metering. JSON includes the new bearer and metadata; plain emits the bearer once. TTL accepts nonnegative whole seconds, including zero for no expiry.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--type KIND` | type Values: member, runner. |
| `--principal NAME` | principal |
| `--scope SCOPE` | Credential scope. Repeatable; comma-separated values are accepted. |
| `--ttl DURATION` | ttl |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

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
| `--type KIND` | type Values: member, runner. |
| `--include-revoked` | include revoked |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

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
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

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
| `--grace DURATION` | grace |
| `--ttl DURATION` | ttl |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
