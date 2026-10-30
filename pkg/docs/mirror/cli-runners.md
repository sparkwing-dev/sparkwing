<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing runners

Every `sparkwing runners` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing runners`

Manage runners

### Subcommands

- `add` -- Register a runner
- `list` -- List runners and their capacity
- `remove` -- Remove a runner

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing runners add`

Register a runner

All runner enrollment requires an independent account session through --session-stdin. For headless setup, run sparkwing-controller admin session in the controller pod as an existing team administrator. Shared runner registration requires team administration. --personal registers capacity for only your runs and requires --session-stdin with an existing independent account session. Members may enroll personal machines when the team's members_can_add_machines setting is true (the default). When false, enrollment requires team administration. Turning it off ends personal machines that members without administration enrolled, and turning it back on does not revive them. The session and its CSRF proof stay in memory. The CLI attempts to log out the supplied session on success or failure. Use a dedicated session because logout ends it for every client. --reserved-for requires team-admin runner registration support. --trust sets which runs a shared runner takes (verified-default or any); unset, a launcher holding a pool with a reserved secret takes only verified default-branch work, including unmodified admin-armed default-branch schedules. --launcher enrolls only claims.launch for a shared reserved pool and skips the persistent runner service. Verified-default pools and pools holding reserved secrets require launcher credentials, including with --trust=any. --repos accepts repeatable, comma-separated patterns. A runner serving several repositories lets a job from one act as the runner and claim another repository's jobs in the team, with their secrets; scope --repos to one repository, or to repositories that trust each other, to isolate them. JSON reports personal scope, credential metadata and service state without the bearer. Plain emits the runner name.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--runner RUNNER` | Runner name or selection. (required) |
| `--personal` | Offer this machine only to your runs, including Cloud overflow. |
| `--session-stdin` | Read an independent account session from stdin without saving it and attempt logout when enrollment finishes; required for shared, personal and launcher enrollment. |
| `--repos PATTERN` | Repository patterns this runner takes jobs for; a job on a runner serving several repositories can claim the others' jobs and their secrets, so scope it to repositories that trust each other. Repeatable; comma-separated values are accepted. |
| `--label LABEL` | Labels this runner advertises, matched against the labels jobs ask for or prefer. Repeatable; comma-separated values are accepted. |
| `--max-concurrent N` | Most jobs this runner holds at once, written to its config. Must be at least 1. |
| `--config PATH` | Machine config file to write the runner section into, the standard Sparkwing config file by default. |
| `--force` | Replace an existing object. |
| `--no-service` | Write the runner config and credential without installing or starting the runner service. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |
| `--reserved-for POOL` | Reserved pools this shared runner holds, which lets a --launcher runner take work and secrets restricted to those pools; a persistent runner holding a pool that a reserved secret names takes no work, and it requires team administration. Repeatable; comma-separated values are accepted. |
| `--trust CLASS` | Which runs this shared runner takes: verified-default takes only verified default-branch work (signed pushes to the default branch and unmodified fires of admin-armed default-branch schedules), and any takes every run; a runner holding a pool with a reserved secret, or a verified-default runner with any pool, takes nothing unless enrolled with --launcher, and unset such a launcher takes only verified default-branch work. Values: verified-default, any. |
| `--platform PLATFORM` | Record a shared Kubernetes runner at admin enrollment; a runner in a pod stops at its first poll without it. Values: kubernetes. |
| `--launcher` | Enroll only claims.launch for a shared reserved pool and skip the persistent runner service. |

## `sparkwing runners list`

List runners and their capacity

Read the same runner details from the local controller or a selected profile. JSON retains granted identity, reservations, labels, capacity, active jobs and headroom. Plain emits names; quiet JSON emits name objects.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |
| `-q, --quiet` | Print only runner names. |

## `sparkwing runners remove`

Remove a runner

Require the enrolled controller, optional runner name and explicit runner credential kind before stopping the service and revoking. JSON retains confirmed revocation. Plain emits the runner name.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--config PATH` | Machine config file holding the runner section to remove, the standard Sparkwing config file by default. |
| `--no-service` | Revoke the runner credential without stopping the installed runner service. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |
| `--runner RUNNER` | Runner name or selection. |
