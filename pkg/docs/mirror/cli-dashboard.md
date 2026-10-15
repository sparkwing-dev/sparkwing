<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing dashboard

Every `sparkwing dashboard` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing dashboard`

Manage the local dashboard

### Subcommands

- `install` -- Install a dashboard service that starts at login
- `logs` -- Read a bounded dashboard log tail
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
| `--tail N` | tail |
| `-f, --follow` | Stream until the observed work is done. |

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
| `--addr HOST:PORT` | addr |
| `--allow-remote` | allow remote |
| `--allow-origin ORIGIN` | allow origin Repeatable; comma-separated values are accepted. |
| `--read-only` | read only |
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
