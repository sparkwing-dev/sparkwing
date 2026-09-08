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

--sw-detached queues the run instead of executing it here. It
returns as soon as the run is durable, and a resident consumer
process on this machine owns it from then on: close the
terminal, drop the ssh session, log out -- the run keeps going.
The acknowledgment is a run id and the directory its logs land
in. Address the run by that id afterwards:

  sparkwing runs status --run RUN_ID
  sparkwing runs logs   --run RUN_ID --follow
  sparkwing runs cancel --run RUN_ID

Five flags are read only by a detached launch and are refused
without --sw-detached: --sw-idempotency-key, --sw-request-id,
--sw-consumer-idle, --sw-consumer-claim-lease, and --sw-output,
which picks the acknowledgment's format (pretty on a TTY, json
when piped; plain prints the bare id for scripting).

--sw-idempotency-key deduplicates on key plus pipeline: a repeat
carrying a key an earlier launch used returns the original run
id and its current status and creates nothing, which is what
makes a retry after a dropped connection safe. Reusing a key
with different arguments is refused, because a key names one
intent and different arguments are a different request.
--sw-request-id is tracing only and never affects deduplication.

--sw-ref and --sw-priority both work detached. The ref resolves
to a commit when you launch, so the run executes that commit
even if the ref moves first; the consumer executes the worktree
and removes it when the run ends. 'front' and 'back' stay
unresolved until the consumer launches the run, so 'front' means
ahead of the queue the run actually joins.

A flag a detached run cannot carry (--sw-index, --sw-dry-run,
--profile, --sw-fleet, --sw-isolated-home, and the other
run-shaping --sw- flags) is refused with the reason rather than
ignored; run those in the foreground.

PIPELINE resolves against the checkout you are standing in (or
--sw-cd PATH) first, then the repo registry, and the chosen
checkout is recorded on the run. A detached run executes with an
allow-listed snapshot of the launching environment -- SPARKWING_*,
GITHUB_*, PATH, HOME, HOSTNAME, and KUBERNETES_SERVICE_HOST, minus
every credential-shaped name -- widened by naming variables in
SPARKWING_SUBMIT_ENV_ALLOW. A consumer starts automatically if none
is running and exits after five idle minutes; see
'sparkwing runs consumer'.

### Subcommands

- `config` -- Print a pipeline's declared Secrets with provenance

### Arguments

- `<pipeline>` (required) -- Pipeline name registered in .sparkwing/sparkwing.yaml

### Flags

| Flag | Description |
|---|---|
| `-C, --sw-cd PATH` | Run as if started in PATH |
| `--sw-ref REF` | Run the pipeline at REF (branch/tag/SHA) instead of the working tree |
| `--sw-detached` | Queue the run for this machine's resident consumer and print its handle instead of executing here; the run outlives the terminal |
| `--sw-idempotency-key KEY` | Detached only: deduplication token; a repeat carrying this key returns the original run instead of starting a second one |
| `--sw-request-id ID` | Detached only: tracing identifier recorded on the run; never affects deduplication |
| `--sw-consumer-idle DUR` | Detached only, and only if this starts a consumer: how long it stays alive with no work (default 5m) |
| `--sw-consumer-claim-lease DUR` | Detached only, and only if this starts a consumer: the lease it stamps on each claimed run, renewed while the run executes (default 3m) |
| `--sw-output FORMAT` | Detached only: run-handle format, pretty\|json\|plain (default: pretty on a TTY, json when piped) |
| `-v, --sw-verbose` | Enable debug logging |
| `--sw-start-at STEP` | Start the run at STEP |
| `--sw-stop-at STEP` | Stop the run after STEP |
| `--sw-only GLOB` | Run only jobs whose ID matches GLOB (plus their Needs ancestors) |
| `--sw-no-cache` | Ignore cached per-node results (writes still happen) |
| `--sw-priority VALUE` | Local admission priority: an integer, or front/back for one step past the queue as it stands; overrides the plan's own Priority |
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

# Queue a run that outlives the terminal
sparkwing run nightly-report --sw-detached

# Capture the id for scripting
RUN=$(sparkwing run build --sw-detached --sw-output plain)

# Make a detached retry safe to repeat
sparkwing run deploy --sw-detached --sw-idempotency-key deploy-2026-08-11-a --env staging

# Detach a pipeline from another checkout
sparkwing run lint --sw-detached --sw-cd ~/code/other-project

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
