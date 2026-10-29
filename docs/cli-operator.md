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
- `secrets` -- Maintain the controller's sealed values
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
| `--setting GUARD` | Name of the operational guard to change, as operator limits show lists it. (required) |
| `--value N` | New value for the guard as a nonnegative integer; a value above the deployment's ceiling is refused, not clamped. (required) |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

## `sparkwing operator limits show`

Print every compute guard and visible runner usage

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

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
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

## `sparkwing operator object-store status`

Show the controller's object-store request budget

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

## `sparkwing operator secrets`

Maintain the controller's sealed values

### Subcommands

- `rotate` -- Reseal every stored secret and key under the current key

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing operator secrets rotate`

Reseal every stored secret and key under the current key

Reseal every team's secrets, git credentials and submitted run environments, and the session signing key, under the controller's current key. Run it after the previous key is mounted beside the new one; remove the previous key only when nothing is reported unreadable.

Repeats safely. JSON retains the counts, previous_key_only and each unreadable row's purpose, team and name; values never appear.

--verify writes nothing and counts the rows that still open only under the previous key; retire the previous key when it reports zero and nothing unreadable.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--verify` | Change nothing and count the rows that still open only under the previous key; retire that key once this reports zero and nothing unreadable. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

## `sparkwing operator teams`

Create teams under operator authority

### Subcommands

- `show` -- Show a team and its protected node label
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
| `--team SLUG` | Slug for the new team; a slug that exists or was retired is refused. (required) |
| `--display-name NAME` | Human-readable name for the new team, the slug by default. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

## `sparkwing operator teams show`

Show a team and its protected node label

Show the team's protected node label and value for its NodePool template.

The value is derived from the deployment's Cloud Job namespace and the team, so two deployments on one cluster never share a team's nodes.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--team SLUG` | Slug of the team to show. (required) |
| `--job-namespace NAMESPACE` | Kubernetes namespace this deployment runs Cloud Jobs in; the team's node label value is derived from it and the team. (required) |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

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

Only deployment service credentials; never console trust, grants, freezes or account deletion powers. Licensed self-hosters have no deployment-admin token path.

Use the deployment /operator/tokens routes. JSON includes the new bearer and metadata; plain emits the bearer once. TTL accepts nonnegative whole seconds, including zero for no expiry.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--type KIND` | Kind of credential to issue, service by default; deployment is refused by this command. Values: service, deployment. |
| `--principal NAME` | Name of the service the credential is issued to; the controller requires it. |
| `--scope SCOPE` | Credential scope. Repeatable; comma-separated values are accepted. |
| `--ttl DURATION` | Lifetime of the new credential in whole seconds or a duration; omitted or zero issues a credential that never expires. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

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
| `--type KIND` | List only credentials of this kind. Values: service, deployment. |
| `--include-revoked` | Also list credentials that have been revoked. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

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
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

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
| `--grace DURATION` | How long the old credential keeps working after rotation, in whole seconds or a duration, 24h by default; zero revokes it at once. |
| `--ttl DURATION` | Lifetime of the replacement credential in whole seconds or a duration; omitted or zero keeps the old credential's remaining lifetime. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |
