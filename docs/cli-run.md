<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing run

Every `sparkwing run` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing run`

Invoke a pipeline

Compiles the nearest .sparkwing/ binary and exec's it
with the named pipeline.

Runner options use the --sw- prefix. Unknown --sw- options fail before
execution setup. Other arguments pass to the pipeline. Put -- before
pipeline arguments that resemble runner options; every argument after the
separator passes through unchanged.

For remote execution on a profile's controller, use
'sparkwing pipeline trigger <name> --profile PROF'.

Output: pretty on a terminal, compact NDJSON otherwise. JSON runs
emit a start, at most 20 node completions and five diagnostics, and
a terminal record with status, outcome counts and log commands.
Child output remains in the stored logs. Display strings are capped
at 256 bytes; truncation and omitted event/failure counts are explicit.
Use --sw-verbose for the complete live event stream, or
'sparkwing runs logs --run RUN_ID --follow' to read stored output.

SPARKWING_LOG_FORMAT=pretty|json|quiet overrides terminal detection.
quiet is the human summary used by managed git hooks. Explicit json
uses the same compact records on a terminal; --sw-verbose expands it.

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
prevents duplicate launches after a dropped connection. Reusing a key
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
--profile, --sw-fleet, and the other run-shaping --sw- flags)
is refused with the reason instead of ignored; run those in
the foreground.

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
| `-v, --sw-verbose` | Enable debug logging and the complete live JSON event stream |
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
| `--profile NAME` | Run / read against the named profile from ~/.config/sparkwing/profiles.yaml (default: laptop) |
| `--target TARGET` | Run against the named pipeline deployment target (e.g. dev, prod) |

### Examples

```sh
# Run with no flags
sparkwing run fictional-build

# Pass a typed pipeline arg
sparkwing run fictional-release --version v0.28.1

# Run from a different git ref
sparkwing run fictional-build --sw-ref feature/xyz

# Queue a run that outlives the terminal
sparkwing run nightly-report --sw-detached

# Capture the id for scripting
RUN=$(sparkwing run build --sw-detached --sw-output plain)

# Deduplicate a detached retry
sparkwing run deploy --sw-detached --sw-idempotency-key fictional-deploy-attempt --env staging

# Detach a pipeline from another checkout
sparkwing run lint --sw-detached --sw-cd ~/code/other-project

# Retry a failed run
sparkwing runs retry --run run-fictional --failed

# Submit to a remote controller
sparkwing pipeline trigger deploy --profile prod
```

## `sparkwing run config`

Print a pipeline's declared Secrets with provenance

Lists each declared secret, its source binding, and its resolution status.
Invoke it with 'sparkwing run <pipeline> config'. The pipeline binary
handles this inspection command.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |

### Examples

```sh
# Inspect the declared secrets
sparkwing run fictional-release config

# Agent-readable form
sparkwing run fictional-release config -o json
```
