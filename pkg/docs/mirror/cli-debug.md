<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing debug

Every `sparkwing debug` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing debug`

Interactive debugging for pipeline runs

Pause nodes at selected execution points, inspect them, open a shell,
or resume execution. Pause settings apply to the launched run.

### Subcommands

- `run` -- Run a pipeline with ephemeral pause directives
- `release` -- Resume a paused node
- `attach` -- kubectl exec into a paused node's pod (cluster mode)
- `env` -- Print a paused node's environment and working directory + claim holder
- `rerun` -- Reproduce a node's dispatch frame in an interactive shell
- `replay` -- Re-execute a single node headlessly using its dispatch snapshot

### Examples

```sh
# Pause before the tests node
sparkwing debug run build --pause-before tests

# Resume a paused node
sparkwing debug release --run run-fictional --node tests
```

## `sparkwing debug attach`

kubectl exec into a paused node's pod (cluster mode)

Looks up the pod holding the paused node's claim-lease from
the controller's node row, then shells out to kubectl exec -it
-- bash. Local mode prints a note that attach does not apply
(the process is already in your current shell's world) and
exits 0.

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the paused node (required) |
| `--node NAME` | Node ID to attach to (required) |
| `--profile NAME` | Profile name (cluster mode) |

### Examples

```sh
# Attach in prod
sparkwing debug attach --run run-fictional --node tests --profile prod
```

## `sparkwing debug env`

Print a paused node's environment and working directory + claim holder

Prints the environment, working directory, process owner, and state
captured when a node paused. If the node is not paused, prints a warning
and exits zero.

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the node (required) |
| `--node NAME` | Node ID to inspect (required) |
| `--profile NAME` | Profile name (cluster mode) |

### Examples

```sh
# Inspect locally
sparkwing debug env --run run-fictional --node tests
```

## `sparkwing debug release`

Resume a paused node

Flips the pause row's released_at timestamp so the
orchestrator's poll loop wakes and continues dispatching past
the pause point. Local and cluster modes share this surface.

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the paused node (required) |
| `--node NAME` | Node ID to release (required) |
| `--profile NAME` | Profile name (cluster mode) |

### Examples

```sh
# Release locally
sparkwing debug release --run run-fictional --node tests

# Release in prod
sparkwing debug release --run run-fictional --node tests --profile prod
```

## `sparkwing debug replay`

Re-execute a single node headlessly using its dispatch snapshot

Mints a new run row linked to the original via replay_of_run_id /
replay_of_node_id, creates a single nodes row for the target, and
exec's the pipeline binary to execute that one node. The
node's input struct is reconstituted from the stored dispatch
snapshot; upstream Refs resolve against the original
run's outputs without re-executing them.

Replay is "what would this node do now, with the same arguments and
environment?":
secrets re-resolve fresh through sparkwing.Secret, BeforeRun hooks
re-fire, and any code drift in the registered job struct (renamed
type, removed field) returns an error.

With --profile PROF, the original run + target node + dep outputs +
dispatch snapshot are first fetched from the named controller via
HTTP and side-loaded into the local store. Replay execution itself
always runs locally because the user's sparkwing binary owns the
registered pipeline factories.

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the original node (required) |
| `--node NAME` | Node ID to re-execute (required) |
| `--profile PROF` | Sideload from this profile's controller before replaying locally |

### Examples

```sh
# Replay a node locally
sparkwing debug replay --run run-fictional --node deploy

# Replay a prod run on your laptop
sparkwing debug replay --profile prod --run run-fictional --node deploy
```

## `sparkwing debug rerun`

Reproduce a node's dispatch frame in an interactive shell

Opens an interactive shell using a node's recorded environment and working
directory. Local execution writes upstream reference outputs beneath the
run's rerun directory. Cluster execution creates a temporary pod using
--image or SPARKWING_RERUN_IMAGE, attaches to it, and deletes it on exit.

Snapshots omit credential names and values and remove URL credentials.
Controller access to the captured environment requires an admin token.
The command lists omitted keys so you can supply required credentials.
Secrets resolve when accessed, and the selected runner image applies.

--seq selects an attempt index; its default selects the latest attempt.

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the node (required) |
| `--node NAME` | Node ID to reproduce (required) |
| `--seq N` | Attempt index; -1 selects most recent |
| `--profile NAME` | Profile name (cluster mode) |
| `--image REF` | Runner image for cluster-mode debug pod (cluster mode) |

### Examples

```sh
# Rerun locally
sparkwing debug rerun --run run-fictional --node tests

# Rerun a specific attempt
sparkwing debug rerun --run run-fictional --node tests --seq 1

# Rerun in prod
sparkwing debug rerun --run run-fictional --node tests --profile prod --image ghcr.io/me/runner:v1
```

## `sparkwing debug run`

Run a pipeline with ephemeral pause directives

Runs the named pipeline exactly as 'sparkwing run <pipeline>' would, with
additional pause hooks the orchestrator honors before and after
each matching node. Directives travel as env vars to the
pipeline binary; they never land in tracked code.

--pause-before <node> holds the node BEFORE its Run is invoked.
--pause-after  <node> holds the node AFTER its Run returns
  (success or failure). Both flags are repeatable.
--pause-on-failure holds ANY node whose Run returns a non-nil
  error. Skipped / cancelled / OnFailure-recovered nodes do not
  pause -- only Run errors.

Paused nodes hold for 30 minutes by default; set
SPARKWING_PAUSE_TIMEOUT=<duration> to change. An expired pause
is released with reason 'timeout-released' and surfaces in the
run record.

See 'sparkwing debug release' to resume, 'sparkwing debug env'
to inspect, and 'sparkwing debug attach' (cluster mode) to shell
into the pod holding the paused node.

### Flags

| Flag | Description |
|---|---|
| `--pipeline NAME` | Pipeline name to run under debug supervision (required) |
| `--pause-before NODE` | Hold NODE before Run (repeatable) |
| `--pause-after NODE` | Hold NODE after Run (repeatable) |
| `--pause-on-failure` | Hold any node whose Run errors |

### Examples

```sh
# Pause before tests
sparkwing debug run --pipeline build --pause-before tests

# Pause on failure
sparkwing debug run --pipeline build --pause-on-failure
```
