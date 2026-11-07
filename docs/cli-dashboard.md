<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing dashboard

Every `sparkwing dashboard` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing dashboard`

Manage the local dashboard

### Subcommands

- `logs` -- Read a bounded dashboard log tail
- `open` -- Print a one-time sign-in link for the local dashboard
- `status` -- Report whether the controller runs and where the dashboard serves

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

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

Print a one-time sign-in link for the local dashboard, starting the local controller when none runs. Every shared local controller serves the dashboard at dashboard.address from config.yaml, 127.0.0.1:4343 by default, from the moment it starts until `sparkwing daemon stop`. The link is minted over the local socket, works once, expires after 60 seconds and works only at the dashboard address it names. Visiting it stores a 30-day session and sets an HttpOnly, SameSite=Strict cookie that survives controller restarts; Log out ends it. Every TCP API request needs that cookie, and every write also its own Origin; the local socket needs neither.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing dashboard status`

Report whether the controller runs and where the dashboard serves

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
