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

Reads the last 40 lines by default, scanning at most the final 1 MiB. --limit 0 skips history. --follow waits for appended lines until interrupted; log rotation requires restarting the command. Lines larger than 16 KiB are marked truncated. A requested history exceeding the byte window reports an error. Follow retains incomplete lines until a newline arrives.

### Flags

| Flag | Description |
|---|---|
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

Stops the verified owned instance, then starts the invoked binary. Preserves effective options unless explicitly overridden. Address and storage URL syntax are checked before stopping; a valid replacement can still fail during startup. Unknown ownership is refused.

### Flags

| Flag | Description |
|---|---|
| `-o, --output pretty\|json\|plain` | Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped. |
| `--addr HOST:PORT` | Bind address (default: 127.0.0.1:4343) |
| `--allow-remote` | Serve a non-loopback --addr. Every host that reaches it can try the serve token, and a holder of the token can run pipelines and list, overwrite and delete this machine's local secrets. It never serves a masked value. |
| `--allow-origin ORIGINS` | Comma-separated browser origins (`https://dash.example`) allowed alongside loopback ones, as Origin and as Host. Needed when a same-host proxy or --allow-remote serves the dashboard under a name that is not the --addr host. |
| `--profile PROFILE` | Profile from ~/.config/sparkwing/config.yaml (uses its logs + cache surfaces) |
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

Every API request except GET /api/v1/version and signed POST /webhooks/
needs a bearer: the token in serve-token under the Sparkwing home, which
only your account can read, or a browser session derived from it. The CLI
sends the token. A browser opens the dashboard link start and status
print once; its #code= fragment is a single-use sign-in code the page
trades for a session kept in that origin's localStorage. No credential
rides a cookie or a URL a server sees.

The listener accepts loopback Host headers and the host of each
--allow-origin entry, and rejects a browser Origin that is neither a
loopback origin on the served port, the --addr host, nor listed in
--allow-origin; a pnpm dev server on port 3100 needs --allow-origin
http://localhost:3100. A browser write with a body must send
application/json. --allow-remote widens the Host check only.

### Flags

| Flag | Description |
|---|---|
| `-o, --output pretty\|json\|plain` | Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped. |
| `--addr HOST:PORT` | Bind address (default: 127.0.0.1:4343) |
| `--allow-remote` | Serve a non-loopback --addr. Every host that reaches it can try the serve token, and a holder of the token can run pipelines and list, overwrite and delete this machine's local secrets. It never serves a masked value. |
| `--allow-origin ORIGINS` | Comma-separated browser origins (`https://dash.example`) allowed alongside loopback ones, as Origin and as Host. Needed when a same-host proxy or --allow-remote serves the dashboard under a name that is not the --addr host. |
| `--profile PROFILE` | Profile from ~/.config/sparkwing/config.yaml (uses its logs + cache surfaces) |
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
SPARKWING_HOME=/tmp/sparkwing-x sparkwing serve start

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

### Examples

```sh
# Stop the dashboard
sparkwing serve stop
```
