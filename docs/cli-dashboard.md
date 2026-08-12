<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing dashboard

Every `sparkwing dashboard` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing dashboard`

Manage the local dashboard + API server

Background lifecycle for the laptop-local dashboard.
'start' spawns a detached server (writes PID + log under
$SPARKWING_HOME), 'kill' stops it, 'status' reports liveness.

The server is one Go process that hosts the embedded Next.js SPA,
the JSON API, the log endpoints, and the SQLite store on the same
port. There is no separate Node process. The dashboard is purely
for visualization -- everything it shows is reachable from the
CLI as well.

### Subcommands

- `start` -- Spawn the detached dashboard server (replaces any running one)
- `kill` -- Stop a running dashboard server
- `status` -- Report whether the dashboard is running

### Examples

```sh
# Start the dashboard
sparkwing dashboard start

# Check liveness
sparkwing dashboard status

# Stop the dashboard
sparkwing dashboard kill
```

## `sparkwing dashboard kill`

Stop a running dashboard server

Sends SIGTERM to the PID recorded in
$SPARKWING_HOME/dashboard.pid, polls for exit, escalates to SIGKILL
after 5s if necessary, and removes the PID file. No-op (exit 0)
when nothing is running.

### Flags

| Flag | Description |
|---|---|
| `--home DIR` | State directory (default: $SPARKWING_HOME or ~/.sparkwing) |

### Examples

```sh
# Stop the dashboard
sparkwing dashboard kill
```

## `sparkwing dashboard start`

Spawn the detached dashboard server (replaces any running one)

Detaches a child process that runs the in-process
dashboard + API + logs server (pkg/localws). PID is written to
$SPARKWING_HOME/dashboard.pid; stdout/stderr are appended to
$SPARKWING_HOME/dashboard.log. Returns once the listener is
accepting TCP connections so callers can immediately curl it.

Replaces any resident dashboard: a live server on file is drained
and a fresh one takes its place. It refuses only when the resident
dashboard is a newer version than this CLI.

### Flags

| Flag | Description |
|---|---|
| `--addr HOST:PORT` | Bind address (default: 127.0.0.1:4343) |
| `--home DIR` | State directory (default: $SPARKWING_HOME or ~/.sparkwing) |
| `--profile PROFILE` | Profile from ~/.config/sparkwing/profiles.yaml (uses its log_store + artifact_store) |
| `--log-store URL` | Pluggable log backend URL (fs:///abs/path, s3://bucket/prefix). Overrides --profile. |
| `--artifact-store URL` | Pluggable artifact backend URL (fs:///abs/path, s3://bucket/prefix). Overrides --profile. |
| `--read-only` | Reject writes on /api/v1/* (auth + webhooks remain open) |
| `--no-local-store` | Skip local SQLite; list runs from --artifact-store. Requires --log-store + --artifact-store. |

### Examples

```sh
# Start with defaults
sparkwing dashboard start

# Use an alternate port
sparkwing dashboard start --addr 127.0.0.1:5000

# Isolate state under a scratch dir
sparkwing dashboard start --home /tmp/sparkwing-x

# Tail CI runs from S3 (no SQLite)
sparkwing dashboard start --profile ci-smoke --no-local-store --read-only
```

## `sparkwing dashboard status`

Report whether the dashboard is running

Reads $SPARKWING_HOME/dashboard.pid, probes the PID
with kill(0), and reports running state + URL. Exit code 0 when
running, 1 when not.

### Flags

| Flag | Description |
|---|---|
| `--home DIR` | State directory (default: $SPARKWING_HOME or ~/.sparkwing) |

### Examples

```sh
# Check liveness
sparkwing dashboard status
```
