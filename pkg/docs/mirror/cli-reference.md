<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference

Every `sparkwing` command, flag, and argument, generated from the CLI's own command registry and split into one page per top-level command group. For the conceptual overview -- which binaries exist, the flag-naming rule, and what to reach for when -- see [cli.md](cli.md).

## Command groups

- [`sparkwing cache`](cli-cache.md) -- Inspect or trim the compiled pipeline binary cache
- [`sparkwing commands`](cli-commands.md) -- Search the command index or export the CLI reference
- [`sparkwing completion`](cli-completion.md) -- Emit a shell completion script
- [`sparkwing daemon`](cli-daemon.md) -- Inspect and manage the local controller
- [`sparkwing dashboard`](cli-dashboard.md) -- Manage the local dashboard
- [`sparkwing debug`](cli-debug.md) -- Interactive debugging for pipeline runs
- [`sparkwing docs`](cli-docs.md) -- Embedded user docs (offline)
- [`sparkwing doctor`](cli-doctor.md) -- Inspect local controller and machine diagnostics
- [`sparkwing examples`](cli-examples.md) -- Read complete example pipelines
- [`sparkwing info`](cli-info.md) -- Describe Sparkwing and the selected project
- [`sparkwing operator`](cli-operator.md) -- Manage deployment operations
- [`sparkwing overflow`](cli-overflow.md) -- Manage your Cloud overflow allowance
- [`sparkwing pipeline`](cli-pipeline.md) -- This repo's pipelines
- [`sparkwing pools`](cli-pools.md) -- Manage team runner pools
- [`sparkwing profiles`](cli-profiles.md) -- Manage controller connection profiles
- [`sparkwing queue`](cli-queue.md) -- Inspect queued work and scheduling priority
- [`sparkwing repos`](cli-repos.md) -- The machine's fleet of sparkwing repos and their SDK pins
- [`sparkwing run`](cli-run.md) -- Invoke a pipeline
- [`sparkwing runners`](cli-runners.md) -- Manage runners
- [`sparkwing runs`](cli-runs.md) -- Inspect and control pipeline runs
- [`sparkwing schedules`](cli-schedules.md) -- Manage scheduled pipeline runs
- [`sparkwing secrets`](cli-secrets.md) -- Manage secrets in this machine's local store or on a controller
- [`sparkwing tokens`](cli-tokens.md) -- Manage team credentials
- [`sparkwing update`](cli-update.md) -- Update the CLI binary
- [`sparkwing upgrade`](cli-upgrade.md) -- Upgrade repository SDK pins
- [`sparkwing version`](cli-version.md) -- Inspect versions (CLI, SDK, sparks)

## `sparkwing`

Run and inspect pipelines

Run pipelines locally with run, or submit them to a controller with pipeline trigger. Use commands --search to find commands and --help for their flags.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

### Examples

```sh
# Run a pipeline locally
sparkwing run build

# Inspect a pipeline
sparkwing pipeline describe --pipeline build

# Start the local dashboard
sparkwing dashboard start
```
