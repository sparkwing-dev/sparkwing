<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing operator

Every `sparkwing operator` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing operator`

Manage deployment operations

### Subcommands

- `teams` -- Create teams under operator authority
- `limits` -- Manage deployment operational ceilings
- `object-store` -- Operate the controller's object-store request budget
- `tokens` -- Manage deployment service credentials

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing operator limits`

Manage deployment operational ceilings

### Subcommands

- `set` -- Set a deployment operational ceiling
- `show` -- Print every compute guard and visible runner usage

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing operator limits set`

Set a deployment operational ceiling

Operational ceilings and signup settings only; values above ceilings are refused, never silently clamped.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--setting GUARD` | setting (required) |
| `--value N` | value (required) |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing operator limits show`

Print every compute guard and visible runner usage

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing operator object-store`

Operate the controller's object-store request budget

### Subcommands

- `reset-breaker` -- Clear a tripped object-store budget or ceiling freeze
- `status` -- Show the controller's object-store request budget

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing operator object-store reset-breaker`

Clear a tripped object-store budget or ceiling freeze

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing operator object-store status`

Show the controller's object-store request budget

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing operator teams`

Create teams under operator authority

### Subcommands

- `create` -- Create a team and issue its first administrator credential

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing operator teams create`

Create a team and issue its first administrator credential

Create a new team and its first team-admin credential atomically under operator authority. Existing or retired team slugs are refused.

JSON includes the one-time capability and account metadata. Plain and pretty print the capability once. This operation is never retried automatically.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--team SLUG` | team (required) |
| `--display-name NAME` | display name |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing operator tokens`

Manage deployment service credentials

### Subcommands

- `create` -- Issue a deployment service credential
- `list` -- List deployment service credentials
- `revoke` -- Revoke a deployment service credential
- `rotate` -- Rotate a deployment service credential

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing operator tokens create`

Issue a deployment service credential

Only deployment service credentials; never console trust, grants, freezes, refunds or account deletion powers. Licensed self-hosters have no deployment-admin token path.

Use the deployment /operator/tokens routes. JSON includes the new bearer and metadata; plain emits the bearer once. TTL accepts nonnegative whole seconds, including zero for no expiry.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--type KIND` | type Values: service, deployment. |
| `--principal NAME` | principal |
| `--scope SCOPE` | Credential scope. Repeatable; comma-separated values are accepted. |
| `--ttl DURATION` | ttl |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing operator tokens list`

List deployment service credentials

Read deployment credential metadata. JSON emits one record per token, with exact scopes and no bearer. Plain emits prefixes. --token selects one prefix.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--token PREFIX` | Token identifier or prefix. |
| `--type KIND` | type Values: service, deployment. |
| `--include-revoked` | include revoked |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing operator tokens revoke`

Revoke a deployment service credential

Revoke through the deployment token route. JSON retains the confirmed revocation; plain emits the prefix.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--token PREFIX` | Token identifier or prefix. (required) |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing operator tokens rotate`

Rotate a deployment service credential

Rotate through the deployment token route. JSON includes the new bearer, metadata and old revocation time; plain emits the new bearer. Grace defaults to 24h. Zero TTL preserves the old remaining lifetime. Durations use nonnegative whole seconds.

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
