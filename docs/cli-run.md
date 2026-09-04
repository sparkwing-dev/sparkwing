<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing run

Every `sparkwing run` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing run`

Invoke a pipeline

Compiles the nearest .sparkwing/ binary and exec's it
with the named pipeline.

The pipeline name is the only positional in the sparkwing
surface -- a deliberate exception, kept short because run is
typed many times a day. Every other input is a named flag.

Any flag not recognized by run itself is forwarded to the
pipeline binary, e.g. 'sparkwing run release --version
v1.2.3' passes --version through to the pipeline's Args.

For remote execution on a profile's controller, use
'sparkwing pipeline trigger <name> --profile PROF'.

Output: a human-readable per-node summary when stdout is a
terminal, line-delimited JSON otherwise (so piped/agent/CI
consumers get a stable JSONL stream). Force a format with
SPARKWING_LOG_FORMAT=pretty|json|quiet. quiet collapses the
run to a progress line plus a one-line pass/fail status with
the run id, surfacing the failing step only on failure; it is
the default for managed git hooks.

### Subcommands

- `config` -- Print a pipeline's declared Secrets with provenance

### Arguments

- `<pipeline>` (required) -- Pipeline name registered in .sparkwing/sparkwing.yaml

### Flags

| Flag | Description |
|---|---|
| `-C, --sw-cd PATH` | Run as if started in PATH |
| `--sw-ref REF` | Run the pipeline at REF (branch/tag/SHA) instead of the working tree |
| `-v, --sw-verbose` | Enable debug logging |
| `--sw-start-at STEP` | Start the run at STEP |
| `--sw-stop-at STEP` | Stop the run after STEP |
| `--sw-only GLOB` | Run only jobs whose ID matches GLOB (plus their Needs ancestors) |
| `--sw-no-cache` | Ignore cached per-node results (writes still happen) |
| `--sw-local-only` | Force local secrets, state, cache, and logs for this run; ignore any configured shared backends |
| `--sw-fleet` | Let explicitly enrolled helpers execute nodes under this foreground process's authority |
| `--sw-dry-run` | Run each step's dry-run probe instead of its real action |
| `--sw-allow LABEL[,LABEL...]` | Authorize risk-labeled steps (repeatable) |
| `--sw-index PATH` | Judge the git index at PATH instead of the repository's own (prints an index_bound event naming it) |
| `--sw-run-handle-file PATH` | Atomically publish the accepted run's machine-readable handle to PATH |
| `--sw-isolated-home DIR` | Keep this run's state and config under DIR, so it hosts an admission daemon from the sparkwing you invoked instead of joining the machine's |
| `--profile NAME` | Run / read against the named profile from ~/.config/sparkwing/profiles.yaml (default: laptop) |
| `--target TARGET` | Run against the named pipeline deployment target (e.g. dev, prod) |

### Examples

```sh
# Run with no flags
sparkwing run build-test-deploy

# Pass a typed pipeline arg
sparkwing run release --version v0.28.1

# Run from a different git ref
sparkwing run build-test-deploy --sw-ref feature/xyz

# Retry a failed run
sparkwing runs retry RUN_ID --failed

# Submit to a remote controller
sparkwing pipeline trigger deploy --profile prod
```

## `sparkwing run config`

Print a pipeline's declared Secrets with provenance

Pure inspection: lists every Secret the pipeline
declares, each with its source binding and resolution status when a
source is configured -- useful before driving destructive runs to
confirm you'd hit the right vault. No Plan() runs, nothing
dispatches, nothing mutates.

Invocation: `sparkwing run <pipeline> config` -- the
pipeline binary handles the subverb directly.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Inspect the declared secrets
sparkwing run release config

# Agent-readable form
sparkwing run release config -o json
```
