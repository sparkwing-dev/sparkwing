<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing daemon

Every `sparkwing daemon` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing daemon`

Inspect or refresh the local admission daemon

The admission daemon starts on demand when a pipeline needs it. Status never starts one. Restart replaces only an answering daemon with this installed build, using the same drain, durable lease, and reattachment path as automatic version takeover; a stopped daemon stays stopped.

### Subcommands

- `status` -- Report whether wingd is running and which build it serves
- `restart` -- Refresh an answering wingd to this installed build
- `recover-state` -- Preserve unreadable daemon state after guarded commands stop

### Examples

```sh
# Machine-readable status
sparkwing daemon status -o json

# Refresh only if already running
sparkwing daemon restart
```

## `sparkwing daemon recover-state`

Preserve unreadable daemon state after guarded commands stop

Fail-closed recovery for a daemon that cannot parse its durable state. The unreadable bytes may describe guarded commands that are still running, so first stop or verify those commands, then pass --yes. Recovery holds the daemon election lock, moves state.json to a state.json.corrupt-<time> forensic copy, and never discards readable state.

### Flags

| Flag | Description |
|---|---|
| `--home DIR` | Sparkwing home whose unreadable daemon state should be preserved |
| `--yes` | Confirm every guarded command described by the unreadable state has stopped (required) |

### Examples

```sh
# Recover only after verifying guarded commands stopped
sparkwing daemon recover-state --home /path/to/home --yes
```

## `sparkwing daemon restart`

Refresh an answering wingd to this installed build

Refresh an answering daemon when its build differs from this installed build. With --force, drain and replace an answering daemon even when the builds match. Existing holders reconnect and reattach through durable leases. If no daemon is running, nothing is started.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty\|json\|plain (default: pretty on TTY, json when piped) |
| `--home DIR` | Sparkwing state directory |
| `--force` | Replace the daemon even when it already serves this build |

### Examples

```sh
# Refresh only if already running
sparkwing daemon restart

# Replace an answering daemon
sparkwing daemon restart --force

# Machine-readable result
sparkwing daemon restart -o json
```

## `sparkwing daemon status`

Report whether wingd is running and which build it serves

Read-only daemon status. An absent daemon is a healthy stopped state and exits zero. An unreachable socket fails instead of pretending the admission queue is empty. The JSON running_revision identifies the exact source build when available. It also reports missing_requirements: the schema requirements this home's store records and the daemon's binary does not understand. A daemon behind the store by additive migrations alone keeps serving, so only a non-empty missing_requirements marks it unhealthy, and the report then names the remedy that works, which is not a restart when the installed sparkwing is the same build. daemon_schema_version and store_schema_version report the two schema numbers, and a daemon too old to advertise its requirements falls back to comparing them. A store that exists but cannot be read is reported as store_schema_error and also marks the daemon unhealthy, rather than passing as an absent store. daemon_store_ready and daemon_store_error report the daemon's own view of the handle it holds: a daemon whose store will not open keeps admitting runs and names the reason here, and a daemon too old to answer reports neither.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty\|json\|plain (default: pretty on TTY, json when piped) |
| `--home DIR` | Sparkwing state directory |

### Examples

```sh
# Machine-readable status
sparkwing daemon status -o json
```
