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

Print a one-time sign-in link for the local dashboard, starting the local controller when none runs. Every shared local controller serves the dashboard at dashboard.address from config.yaml, 127.0.0.1:4343 by default, from the moment it starts until `sparkwing daemon stop`. The link is minted over the local socket and carries a one-time code in its fragment that works once, expires after 60 seconds and only at the dashboard address it names. The page exchanges it for a 30-day session token kept in localStorage and sent as an Authorization bearer header; it survives controller restarts and Log out revokes it. No cookie is set or accepted; every write also needs the dashboard's own Origin. The local socket needs no session.

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
