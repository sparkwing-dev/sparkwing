<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference

Every `sparkwing` command, flag, and argument, generated from the CLI's own command registry and split into one page per top-level command group. For the conceptual overview -- which binaries exist, the flag-naming rule, and what to reach for when -- see [cli.md](cli.md).

## Command groups

- [`sparkwing cache`](cli-cache.md) -- Inspect or trim the compiled pipeline binary cache
- [`sparkwing cluster`](cli-cluster.md) -- Operate and inspect the sparkwing cluster
- [`sparkwing commands`](cli-commands.md) -- Index of every command: one path and synopsis per line
- [`sparkwing completion`](cli-completion.md) -- Emit a shell completion script (bash\|zsh\|fish)
- [`sparkwing configure`](cli-configure.md) -- Configure laptop-local settings
- [`sparkwing daemon`](cli-daemon.md) -- Inspect or refresh the local admission daemon
- [`sparkwing dashboard`](cli-dashboard.md) -- Manage the local dashboard + API server
- [`sparkwing debug`](cli-debug.md) -- Interactive debugging for pipeline runs
- [`sparkwing docs`](cli-docs.md) -- Embedded user docs (offline)
- [`sparkwing doctor`](cli-doctor.md) -- Diagnose and safely repair local state
- [`sparkwing examples`](cli-examples.md) -- Worked pipelines to read, not starting points to scaffold
- [`sparkwing fleet`](cli-fleet.md) -- Configure foreground assisted execution
- [`sparkwing info`](cli-info.md) -- Self-describe sparkwing + the current project (agent entrypoint)
- [`sparkwing pipeline`](cli-pipeline.md) -- This repo's pipelines
- [`sparkwing profile`](cli-profile.md) -- Show which profile sparkwing would use right now, and why
- [`sparkwing queue`](cli-queue.md) -- The truthful view of local admission: holders, connections, waiters, and why
- [`sparkwing repos`](cli-repos.md) -- The machine's fleet of sparkwing repos and their SDK pins
- [`sparkwing run`](cli-run.md) -- Invoke a pipeline
- [`sparkwing runs`](cli-runs.md) -- Inspect and control pipeline runs
- [`sparkwing secrets`](cli-secrets.md) -- Manage secrets (local dotenv or controller-stored)
- [`sparkwing update`](cli-update.md) -- Self-update the CLI binary
- [`sparkwing version`](cli-version.md) -- Show + update versions (CLI, SDK, sparks)

## `sparkwing`

sparkwing -- CI/CD pipelines written in Go

Sparkwing is a self-hosted pipeline runner. Pipelines are Go
programs in a repo's .sparkwing/ directory, triggered by git hooks,
webhooks, schedules, or manual invocation. Use 'sparkwing run
<pipeline>' to invoke one; 'sparkwing pipeline list' / 'describe'
for agent-facing discovery.

### Examples

```sh
# Run a pipeline (positional shortcut)
sparkwing run build-test-deploy

# First command an agent should run
sparkwing info --for-agent

# List every invocable (agents)
sparkwing pipeline list -o json

# Inspect one pipeline's full metadata
sparkwing pipeline describe --name release -o json

# Bootstrap + scaffold your first pipeline in a fresh repo
sparkwing pipeline new --name release

# Start the local dashboard
sparkwing dashboard start
```
