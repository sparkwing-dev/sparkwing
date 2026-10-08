<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference

Every `sparkwing` command, flag, and argument, generated from the CLI's own command registry and split into one page per top-level command group. For the conceptual overview -- which binaries exist, the flag-naming rule, and what to reach for when -- see [cli.md](cli.md).

## Command groups

- [`sparkwing cache`](cli-cache.md) -- Inspect or trim the compiled pipeline binary cache
- [`sparkwing cloud`](cli-cloud.md) -- Connect this machine to a sparkwing controller
- [`sparkwing cluster`](cli-cluster.md) -- Operate and inspect the sparkwing cluster
- [`sparkwing commands`](cli-commands.md) -- Index of every command: one path and synopsis per line
- [`sparkwing completion`](cli-completion.md) -- Emit a shell completion script (bash\|zsh\|fish)
- [`sparkwing configure`](cli-configure.md) -- Configure laptop-local settings
- [`sparkwing crons`](cli-crons.md) -- Arm, inspect and drive this host's local pipeline schedules
- [`sparkwing daemon`](cli-daemon.md) -- Inspect or refresh the local admission daemon
- [`sparkwing debug`](cli-debug.md) -- Interactive debugging for pipeline runs
- [`sparkwing docs`](cli-docs.md) -- Embedded user docs (offline)
- [`sparkwing doctor`](cli-doctor.md) -- Inspect and repair abandoned local state
- [`sparkwing examples`](cli-examples.md) -- Read complete example pipelines
- [`sparkwing fleet`](cli-fleet.md) -- Configure foreground assisted execution
- [`sparkwing info`](cli-info.md) -- Describe Sparkwing and the selected project
- [`sparkwing pipeline`](cli-pipeline.md) -- This repo's pipelines
- [`sparkwing queue`](cli-queue.md) -- Inspect local admission holders, connections, and waiters
- [`sparkwing repos`](cli-repos.md) -- The machine's fleet of sparkwing repos and their SDK pins
- [`sparkwing run`](cli-run.md) -- Invoke a pipeline
- [`sparkwing runs`](cli-runs.md) -- Inspect and control pipeline runs
- [`sparkwing secrets`](cli-secrets.md) -- Manage secrets in this machine's local store or on a controller
- [`sparkwing serve`](cli-serve.md) -- Manage the local dashboard + API server
- [`sparkwing update`](cli-update.md) -- Update the CLI binary
- [`sparkwing version`](cli-version.md) -- Inspect versions (CLI, SDK, sparks)

## `sparkwing`

sparkwing -- CI/CD pipelines written in Go

Sparkwing is a self-hosted pipeline runner. Pipelines are Go
programs in a repo's .sparkwing/ directory, triggered by git hooks,
webhooks, schedules, or manual invocation. Use 'sparkwing run
<pipeline>' to invoke one; 'sparkwing pipeline list' / 'describe'
for agent-facing discovery.

Three flags go before any verb:
  -C DIR          run as if started in DIR (the .sparkwing search starts there)
  --profile NAME  select a profile; the verb must accept --profile
  -o FORMAT       pretty | json | plain, for verbs that print a document

Without --profile, verbs that read runs fall back to SPARKWING_PROFILE, then
the project's defaults.profile. Verbs that change state elsewhere (secrets,
crons, runs cancel, cluster, and similar) act locally unless --profile names
the controller.

### Examples

```sh
# Run a pipeline (positional shortcut)
sparkwing run fictional-build

# First command an agent should run
sparkwing info --for-agent

# List every invocable (agents)
sparkwing pipeline list -o json

# Inspect one pipeline's full metadata
sparkwing pipeline describe --name fictional-release -o json

# Bootstrap + scaffold your first pipeline in a new repository
sparkwing pipeline new --name release

# Start the local dashboard
sparkwing serve start

# List another checkout's pipelines
sparkwing -C ~/code/other pipeline list
```
