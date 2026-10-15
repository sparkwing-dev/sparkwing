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
| `--namespace NAME` | Kubernetes namespace of the runner pods (default: sparkwing) |
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

Paused nodes hold for 30 minutes by default; set debug.pause_timeout
in config.yaml (a duration such as 10m) to change it. An expired pause
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
