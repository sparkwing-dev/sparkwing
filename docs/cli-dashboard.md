<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing dashboard

Every `sparkwing dashboard` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing dashboard`

Manage the local dashboard

### Subcommands

- `install` -- Install a dashboard service that starts at login
- `logs` -- Read a bounded dashboard log tail
- `open` -- Print a one-time sign-in link for the local dashboard
- `start` -- Start the local dashboard and controller on demand
- `status` -- Report whether the dashboard is running
- `stop` -- Stop a running dashboard server
- `uninstall` -- Remove the dashboard login service

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing dashboard install`

Install a dashboard service that starts at login

Install a login service with restart on failure using systemd user or launchd; no socket activation. Refuse Windows with --stay guidance.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--force` | Replace an existing object. |

## `sparkwing dashboard logs`

Read a bounded dashboard log tail

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--tail N` | Number of most recent log lines to print, 40 by default; with --follow, lines before streaming starts. |
| `-f, --follow` | Keep streaming until the observed work finishes. |

## `sparkwing dashboard open`

Print a one-time sign-in link for the local dashboard

Print a one-time sign-in link for the running local dashboard. It is minted over the local socket, works once and expires after 60 seconds; visiting it gives that browser tab a session it sends as an Authorization bearer header, never a cookie. Every TCP API request needs that header; the local socket needs none.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing dashboard start`

Start the local dashboard and controller on demand

Starts the local controller on demand; --stay pins it until stopped. Runs, queued work, held leases and dashboard connections prevent idle exit.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--addr HOST:PORT` | Address the dashboard listens on as HOST:PORT, 127.0.0.1:4343 by default; a non-loopback host also needs --allow-remote. |
| `--allow-remote` | Permit a non-loopback --addr that other hosts can reach; this build refuses the flag because the local controller has no dashboard access policy yet. |
| `--allow-origin ORIGIN` | Browser origin allowed to call the dashboard API besides its own; this build refuses the flag because the local controller has no dashboard access policy yet. Repeatable; comma-separated values are accepted. |
| `--read-only` | Serve the dashboard without changes from the browser; this build refuses the flag because the local controller has no dashboard access policy yet. |
| `--stay` | Keep the local controller running until stopped. |

## `sparkwing dashboard status`

Report whether the dashboard is running

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing dashboard stop`

Stop a running dashboard server

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing dashboard uninstall`

Remove the dashboard login service

Remove the managed login service; Windows uses --stay only.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
