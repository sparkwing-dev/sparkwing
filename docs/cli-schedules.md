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
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |
| `--schedule SCHEDULE` | Schedule name. Repeatable; comma-separated values are accepted. |
| `--unpinned` | Arm schedules to compile the checkout on every fire instead of pinning a proved binary; with --profile, follow the checked-out branch. |
| `--no-prove` | Arm local schedules without compiling their pipelines first, which also leaves them unpinned; ignored with --profile. |
| `--no-timer` | Skip installing the native timer that fires local schedules; ignored with --profile. |
| `--all-repos` | Install local schedules for every registered repository instead of only the current one; cannot be combined with --profile or --schedule. |

## `sparkwing schedules list`

List the schedules armed on this host

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |
| `--all` | Include entries that are hidden by default. |
| `--timer` | Report the health of the schedule timer instead of listing schedules, exiting with failure when it is unhealthy. |
| `--next N` | List the next N fire times across all schedules instead of the schedule table. |

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
| `--profile PROFILE` | Controller connection profile from your config. Omit it to use the local controller. |

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
| `--profile PROFILE` | Controller connection profile from your config. Omit it to use the local controller. |

## `sparkwing schedules run`

Launch a schedule's pipeline now

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |
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
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |
| `--cron EXPR` | Override the schedule's cron expression. |
| `--tz ZONE` | Override the schedule's time zone with an IANA zone name; an empty value selects the default zone. |
| `--overlap POLICY` | Override what happens when a fire comes due while the previous scheduled run is still running. Values: skip, queue. |
| `--catch-up DUR` | Override how long after its due time a late fire may still run. |
| `--arg K=V` | Replace the schedule's pipeline arguments with these KEY=VALUE pairs; a single empty value replaces them with none. Repeatable; comma-separated values are accepted. |
| `--reset` | Drop every override so the repository's declared schedule applies again; must be used alone. |
| `--pin` | Pin a local schedule to a freshly compiled binary of its checkout, so later checkout changes do not affect its runs; must be used alone. |
| `--unpin` | Let a local schedule compile its checkout on every fire instead of running a pinned binary; must be used alone. |
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
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |
| `--fires N` | Number of recent fires to include in the detail, 10 by default. |
| `--next N` | Print the next N fire times for each selected schedule instead of its detail. |
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
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |
| `--schedule SCHEDULE` | Schedule name. Repeatable; comma-separated values are accepted. |
| `--all-repos` | Remove every local schedule on this host from all repositories, then remove the timer when none remain. |
