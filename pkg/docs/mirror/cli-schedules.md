<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing schedules

Every `sparkwing schedules` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing schedules`

Manage scheduled pipeline runs

### Subcommands

- `install` -- Arm a repo's declared schedules on this host and install the OS timer
- `list` -- List the schedules armed on this host
- `pause` -- Pause a schedule
- `resume` -- Resume a paused schedule
- `run` -- Launch a schedule's pipeline now
- `set` -- Override a declared cadence on this host
- `show` -- Show one schedule's full record and its recent fires
- `uninstall` -- Disarm a repo's schedules, and remove the timer when nothing is left

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing schedules install`

Arm a repo's declared schedules on this host and install the OS timer

Capture the installing environment; native timers on Linux/macOS invoke __tick. Windows schedules are daemon-run.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
| `--schedule SCHEDULE` | Schedule name. Repeatable; comma-separated values are accepted. |
| `--unpinned` | unpinned |
| `--no-prove` | no prove |
| `--no-timer` | no timer |
| `--all-repos` | all repos |

## `sparkwing schedules list`

List the schedules armed on this host

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
| `--all` | Include entries normally hidden. |
| `--timer` | timer |
| `--next N` | next |

## `sparkwing schedules pause`

Pause a schedule

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--schedule SCHEDULE` | Schedule name. Repeatable; comma-separated values are accepted. (required) |
| `--profile PROFILE` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing schedules resume`

Resume a paused schedule

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--schedule SCHEDULE` | Schedule name. Repeatable; comma-separated values are accepted. (required) |
| `--profile PROFILE` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing schedules run`

Launch a schedule's pipeline now

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
| `--schedule SCHEDULE` | Schedule name. Repeatable; comma-separated values are accepted. (required) |

## `sparkwing schedules set`

Override a declared cadence on this host

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
| `--cron EXPR` | cron |
| `--tz ZONE` | tz |
| `--overlap POLICY` | overlap Values: skip, queue. |
| `--catch-up DUR` | catch up |
| `--arg K=V` | arg Repeatable; comma-separated values are accepted. |
| `--reset` | reset |
| `--pin` | pin |
| `--unpin` | unpin |
| `--schedule SCHEDULE` | Schedule name. Repeatable; comma-separated values are accepted. (required) |

## `sparkwing schedules show`

Show one schedule's full record and its recent fires

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
| `--fires N` | fires |
| `--next N` | next |
| `--schedule SCHEDULE` | Schedule name. Repeatable; comma-separated values are accepted. (required) |

## `sparkwing schedules uninstall`

Disarm a repo's schedules, and remove the timer when nothing is left

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
| `--schedule SCHEDULE` | Schedule name. Repeatable; comma-separated values are accepted. |
| `--all-repos` | all repos |
