<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing debug

Every `sparkwing debug` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing debug`

Interactive debugging for pipeline runs

### Subcommands

- `attach` -- Attach to a running Kubernetes target (Cloud jobs unsupported)
- `show` -- Inspect public job, executor and pause diagnostics
- `resume` -- Resume a paused job

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing debug attach`

Attach to a running Kubernetes target (Cloud jobs unsupported)

Attach a shell to a running job's Kubernetes target. Cloud jobs do not support shell attachment.

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
| `--profile NAME` | Controller connection profile from your config. Without it, or with --profile=, the command uses the local controller. |

## `sparkwing debug resume`

Resume a paused job

Resume a paused job in the selected run.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run ID` | Run ID or per-team run number. (required) |
| `--job JOB` | Job within the selected run. (required) |
| `--profile NAME` | Controller connection profile from your config. Without it, or with --profile=, the command uses the local controller. |

### Examples

```sh
# Resume a paused job
sparkwing debug resume --run RUN_ID --job build
```

## `sparkwing debug show`

Inspect public job, executor and pause diagnostics

Show a job's public execution details and current pause state.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--run ID` | Run ID or per-team run number. (required) |
| `--job JOB` | Job within the selected run. (required) |
| `--profile NAME` | Controller connection profile from your config. Without it, or with --profile=, the command uses the local controller. |

### Examples

```sh
# Inspect one job's public diagnostics
sparkwing debug show --run RUN_ID --job build
```
