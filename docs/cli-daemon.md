<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing daemon

Every `sparkwing daemon` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing daemon`

Inspect or refresh the local admission daemon

The admission daemon starts on demand when a pipeline needs it. Status never
starts one. Restart replaces only an answering daemon with this installed
build, using the same drain, durable lease, and reattachment path as automatic
version takeover; a stopped daemon stays stopped. Stop drains an answering
daemon and launches no successor.

### Subcommands

- `status` -- Report whether wingd is running and which build it serves
- `restart` -- Refresh an answering wingd to this installed build
- `stop` -- Drain an answering wingd and leave it stopped
- `recover-state` -- Preserve unreadable daemon state after its holders stop

### Examples

```sh
# Machine-readable status
sparkwing daemon status -o json

# Refresh only if already running
sparkwing daemon restart

# Stop it and leave it stopped
sparkwing daemon stop
```

## `sparkwing daemon recover-state`

Preserve unreadable daemon state after its holders stop

Fail-closed recovery for a daemon that cannot parse its durable state. The
unreadable bytes may describe leases whose runs still hold host capacity, so
first stop or verify those runs, then pass --yes. Recovery holds the
daemon election lock, moves state.json to a state.json.corrupt-<time> forensic
copy, and never discards readable state.

### Flags

| Flag | Description |
|---|---|
| `--home DIR` | Sparkwing home whose unreadable daemon state should be preserved |
| `--yes` | Confirm every run described by the unreadable state has stopped (required) |

### Examples

```sh
# Recover only after verifying the described runs stopped
sparkwing daemon recover-state --home /path/to/home --yes
```

## `sparkwing daemon restart`

Refresh an answering wingd to this installed build

Refresh an answering daemon when its build differs from this installed build.
With --force, drain and replace an answering daemon even when the builds
match. Existing holders reconnect and reattach through durable leases. If no
daemon is running, nothing is started.

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

Reports daemon reachability and source build identity. An absent daemon exits
zero. An unreachable socket returns an error.

running_revision identifies the build. missing_requirements lists store
requirements the binary cannot interpret. Additive migrations permit older
binaries to keep serving. Older daemons use daemon_schema_version and
store_schema_version for comparison.

store_schema_error reports a store the CLI could not read. daemon_store_ready
and daemon_store_error describe the daemon's own store handle. The daemon
evicts runs whose terminal state it cannot check. store_path identifies the
store it tried to open.

daemon_store_skew identifies an incompatible store schema. Pipeline runs use
standalone storage for that mismatch and fail for other store errors. Older
daemons omit fields they cannot report.

api_socket names the controller API socket. api_ready reports whether it is
bound, and api_error explains a binding failure. A daemon that supports the
API but cannot bind its socket is unhealthy. Older daemons omit api_ready.

artifact_store_error reports failure to open the configured cache. The daemon
continues serving with artifact routes disabled.

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

## `sparkwing daemon stop`

Drain an answering wingd and leave it stopped

Drains an answering daemon through the same wire request a restart uses, then
waits for its admission socket to go quiet and its election lock to be
released. No successor is launched, and the supervisor exits with the worker it
started, so the pair stays down until the next run needs a daemon. An absent
daemon is a no-op and exits zero.

A run still holding admission finishes against the store it already opened.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty\|json\|plain (default: pretty on TTY, json when piped) |
| `--home DIR` | Sparkwing home whose daemon should stop |

### Examples

```sh
# Stop this machine's daemon
sparkwing daemon stop

# Machine-readable result
sparkwing daemon stop -o json
```
