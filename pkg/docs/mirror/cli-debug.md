<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing debug

Every `sparkwing debug` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing debug`

Interactive debugging for pipeline runs

Pause nodes at selected hook points, inspect the paused pod,
drop into a shell, or release the node. Every debug verb is
ephemeral -- pause directives live only on the run they launch,
never in pipeline source. Pipelines stay production-clean.

### Subcommands

- `run` -- Run a pipeline with ephemeral pause directives
- `release` -- Resume a paused node
- `attach` -- kubectl exec into a paused node's pod (cluster mode)
- `env` -- Print a paused node's env + workdir + claim holder
- `rerun` -- Reproduce a node's dispatch frame in an interactive shell
- `replay` -- Re-execute a single node headlessly using its dispatch snapshot

### Examples

```sh
# Pause before the tests node
sparkwing debug run build --pause-before tests

# Resume a paused node
sparkwing debug release --run run-X --node tests
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
sparkwing debug attach --run run-X --node tests --profile prod
```

## `sparkwing debug env`

Print a paused node's env + workdir + claim holder

Inspection-only command: reads the stored node record (env map,
claim holder, current pause state) and prints them to stdout.
Does NOT spawn a shell. If the node is not paused, prints a
warning and exits 0 -- env info is captured at pause time, not
continuously.

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the node (required) |
| `--node NAME` | Node ID to inspect (required) |
| `--profile NAME` | Profile name (cluster mode) |

### Examples

```sh
# Inspect locally
sparkwing debug env --run run-X --node tests
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
sparkwing debug release --run run-X --node tests

# Release in prod
sparkwing debug release --run run-X --node tests --profile prod
```

## `sparkwing debug replay`

Re-execute a single node headlessly using its dispatch snapshot

Mints a new run row linked to the original via replay_of_run_id /
replay_of_node_id, creates a single nodes row for the target, and
exec's the pipeline binary to execute that one node. The
node's input struct is reconstituted from the stored dispatch
snapshot; upstream Refs resolve against the original
run's outputs without re-executing them.

Replay is "what would this node do now, with the same args+env?":
secrets re-resolve fresh through sparkwing.Secret, BeforeRun hooks
re-fire, and any code drift in the registered job struct (renamed
type, removed field) aborts loud rather than silently producing
wrong results.

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
sparkwing debug replay --run run-X --node deploy

# Replay a prod run on your laptop
sparkwing debug replay --profile prod --run run-X --node deploy
```

## `sparkwing debug rerun`

Reproduce a node's dispatch frame in an interactive shell

Reads the dispatch snapshot for the given run/node and reproduces
the env + workdir the orchestrator saw at dispatch time. Local mode
exec's $SHELL with the snapshot env applied and writes upstream Ref
outputs to ~/.sparkwing/rerun/<run>/<node>/refs so they're cat-able
from the shell. Cluster mode pipes a debug-pod manifest to 'kubectl
create' against a runner image (--image or $SPARKWING_RERUN_IMAGE),
carrying the snapshot env on stdin, then attaches to the pod and
deletes it on exit.

Snapshots drop credential-shaped names and values and rewrite the
userinfo of any URL or DSN they keep, and the controller serves the
captured env only to an admin token. The banner names the keys the
snapshot dropped; export those yourself.

Replays do NOT freeze the rest of the cluster: secrets re-resolve
through the standard sparkwing.Secret API on demand, and the runner
image is whatever the cluster runs today. Replay is "what would this
node do now, with the args+env it had then?", not a frozen
reproduction.

Default --seq selects the most-recent attempt for the node; pass
--seq 0 (or another integer) to target a specific attempt index.

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
sparkwing debug rerun --run run-X --node tests

# Rerun a specific attempt
sparkwing debug rerun --run run-X --node tests --seq 1

# Rerun in prod
sparkwing debug rerun --run run-X --node tests --profile prod --image ghcr.io/me/runner:v1
```

## `sparkwing debug run`

Run a pipeline with ephemeral pause directives

Runs the named pipeline exactly as 'sparkwing run <pipeline>' would, with
additional pause hooks the orchestrator honors before and after
each matching node. Directives travel as env vars to the
pipeline binary; they never land in git-tracked code.

--pause-before <node> holds the node BEFORE its Run is invoked.
--pause-after  <node> holds the node AFTER its Run returns
  (success or failure). Both flags are repeatable.
--pause-on-failure holds ANY node whose Run returns a non-nil
  error. Skipped / cancelled / OnFailure-recovered nodes do NOT
  pause -- only honest Run errors.

Paused nodes hold for 30 minutes by default; set
SPARKWING_PAUSE_TIMEOUT=<duration> to change. A timed-out pause
is released with reason 'timeout-released' and surfaces in the
run record.

See 'sparkwing debug release' to resume, 'sparkwing debug env'
to inspect, and 'sparkwing debug attach' (cluster mode) to shell
into the pod holding the paused node.

### Flags

| Flag | Description |
|---|---|
| `--pipeline NAME` | Pipeline (pipeline) name to run under debug supervision (required) |
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
