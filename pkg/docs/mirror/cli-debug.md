<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing debug

Every `sparkwing debug` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing debug`

Interactive debugging for pipeline runs

### Subcommands

- `attach` -- kubectl exec into a paused job's pod (cluster mode)
- `env` -- Inspect public job, executor and pause diagnostics
- `resume` -- Resume a paused job

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing debug attach`

kubectl exec into a paused job's pod (cluster mode)

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run ID` | Run ID or per-team run number. (required) |
| `--job JOB` | Job within the selected run. (required) |
| `--namespace NAME` | Kubernetes namespace to pass to kubectl exec, overriding the namespace recorded for the job's pod. |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

## `sparkwing debug env`

Inspect public job, executor and pause diagnostics

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run ID` | Run ID or per-team run number. (required) |
| `--job JOB` | Job within the selected run. (required) |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |

## `sparkwing debug resume`

Resume a paused job

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run ID` | Run ID or per-team run number. (required) |
| `--job JOB` | Job within the selected run. (required) |
| `--profile NAME` | Controller connection profile from your config. Omit it to use the local controller. |
