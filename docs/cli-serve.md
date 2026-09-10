<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing serve

Every `sparkwing serve` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing serve`

Manage the local dashboard + API server

Background lifecycle for the laptop-local dashboard.
'start' spawns a detached server (writes PID + log under
$SPARKWING_HOME), 'stop' stops it, 'restart' replaces it, and 'status' reports readiness.

The server is one Go process that hosts the embedded Next.js SPA,
the JSON API, the log endpoints, and the SQLite store on the same
port.

### Subcommands

- `start` -- Start the dashboard, preserving every running instance
- `stop` -- Stop a running dashboard server
- `restart` -- Replace an owned dashboard and wait for readiness
- `status` -- Report whether the dashboard is running
- `logs` -- Read a bounded dashboard log tail

### Examples

```sh
# Start the dashboard
sparkwing serve start

# Check liveness
sparkwing serve status

# Stop the dashboard
sparkwing serve stop
```

## `sparkwing serve logs`

Read a bounded dashboard log tail

Reads the last 40 lines by default, scanning at most the final 1 MiB. --limit 0 skips history. --follow waits for appended lines until interrupted; log rotation requires restarting the command. Lines larger than 16 KiB are marked truncated.

### Flags

| Flag | Description |
|---|---|
| `--home DIR` | State directory |
| `-o, --output pretty\|json\|plain` | Output format |
| `--limit N` | Last N lines; 0 skips history (default: 40) |
| `--follow` | Follow appended lines until interrupted |

### Examples

```sh
# Read recent log lines
sparkwing serve logs
```

## `sparkwing serve restart`

Replace an owned dashboard and wait for readiness

Stops the verified owned instance, then starts the invoked binary. Preserves effective options unless explicitly overridden. Unknown ownership is refused.

### Flags

| Flag | Description |
|---|---|
| `-o, --output pretty\|json\|plain` | Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped. |
| `--addr HOST:PORT` | Bind address (default: 127.0.0.1:4343) |
| `--allow-remote` | Serve a non-loopback --addr. The API has no authentication, so every host that reaches it can run pipelines and read secrets. |
| `--allow-origin ORIGINS` | Comma-separated browser origins (`https://dash.example`) allowed alongside loopback ones. Needed when --allow-remote serves the dashboard under a name that is not the --addr host. |
| `--home DIR` | State directory (default: $SPARKWING_HOME or ~/.sparkwing) |
| `--profile PROFILE` | Profile from ~/.config/sparkwing/profiles.yaml (uses its log_store + artifact_store) |
| `--log-store URL` | Pluggable log backend URL (fs:///abs/path, s3://bucket/prefix). Overrides --profile. |
| `--artifact-store URL` | Pluggable artifact backend URL (fs:///abs/path, s3://bucket/prefix). Overrides --profile. |
| `--read-only` | Reject writes on /api/v1/* (auth + webhooks remain open) |
| `--no-local-store` | Skip local SQLite; list runs from --artifact-store. Requires --log-store + --artifact-store. |

### Examples

```sh
# Restart with existing options
sparkwing serve restart
```

## `sparkwing serve start`

Start the dashboard, preserving every running instance

Detaches a child process that runs the in-process
dashboard + API + logs server (pkg/localws). PID is written to
$SPARKWING_HOME/dashboard.pid; stdout/stderr are appended to
$SPARKWING_HOME/dashboard.log. Returns once the listener is
confirming an HTTP readiness response from that exact instance.

A running instance is left unchanged, including its effective options.
Use serve restart for replacement. Build identity is reported separately
from readiness; missing artifact evidence is unknown.

The listener accepts loopback Host headers and rejects a browser Origin
that is neither loopback, the --addr host, nor listed in --allow-origin.
--allow-remote widens the Host check only.

### Flags

| Flag | Description |
|---|---|
| `-o, --output pretty\|json\|plain` | Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped. |
| `--addr HOST:PORT` | Bind address (default: 127.0.0.1:4343) |
| `--allow-remote` | Serve a non-loopback --addr. The API has no authentication, so every host that reaches it can run pipelines and read secrets. |
| `--allow-origin ORIGINS` | Comma-separated browser origins (`https://dash.example`) allowed alongside loopback ones. Needed when --allow-remote serves the dashboard under a name that is not the --addr host. |
| `--home DIR` | State directory (default: $SPARKWING_HOME or ~/.sparkwing) |
| `--profile PROFILE` | Profile from ~/.config/sparkwing/profiles.yaml (uses its log_store + artifact_store) |
| `--log-store URL` | Pluggable log backend URL (fs:///abs/path, s3://bucket/prefix). Overrides --profile. |
| `--artifact-store URL` | Pluggable artifact backend URL (fs:///abs/path, s3://bucket/prefix). Overrides --profile. |
| `--read-only` | Reject writes on /api/v1/* (auth + webhooks remain open) |
| `--no-local-store` | Skip local SQLite; list runs from --artifact-store. Requires --log-store + --artifact-store. |

### Examples

```sh
# Start with defaults
sparkwing serve start

# Use an alternate port
sparkwing serve start --addr 127.0.0.1:5000

# Isolate state under a scratch dir
sparkwing serve start --home /tmp/sparkwing-x

# Tail CI runs from S3 (no SQLite)
sparkwing serve start --profile ci-smoke --no-local-store --read-only

# Serve a LAN bind under a browser-facing name
sparkwing serve start --addr 192.168.1.20:4343 --allow-remote --allow-origin http://dashboard.example.com:4343
```

## `sparkwing serve status`

Report whether the dashboard is running

Reports persisted effective options, owned process identity, dashboard/API
URLs, readiness and artifact comparison. Stopped exits 1; unknown ownership
or failed readiness exits 2. Matching hashes establish build equality;
missing artifact evidence is unknown.

### Flags

| Flag | Description |
|---|---|
| `-o, --output pretty\|json\|plain` | Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped. |
| `--home DIR` | State directory (default: $SPARKWING_HOME or ~/.sparkwing) |

### Examples

```sh
# Check liveness
sparkwing serve status
```

## `sparkwing serve stop`

Stop a running dashboard server

Verifies the persisted process birth and boot identity before stopping.
Sends TERM, waits five seconds, then forces that owned process to exit and
waits up to two more seconds. Linux uses a process handle; macOS repeats
identity checks immediately before signaling. Unknown ownership is refused.
An absent service succeeds.

### Flags

| Flag | Description |
|---|---|
| `-o, --output pretty\|json\|plain` | Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped. |
| `--home DIR` | State directory (default: $SPARKWING_HOME or ~/.sparkwing) |

### Examples

```sh
# Stop the dashboard
sparkwing serve stop
```
