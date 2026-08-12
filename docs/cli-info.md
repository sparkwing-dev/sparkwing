<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing info

Every `sparkwing info` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing info`

Self-describe sparkwing + the current project (agent entrypoint)

One command that answers "what is sparkwing, am I in a
project, what should I run next" without prior knowledge. Prints
the CLI version, whether the current directory is inside a
sparkwing project (and how many pipelines it has), whether the Go
toolchain is on PATH, a curated list of next-step commands, and
the docs URL. When a project declares a git hook that is not
firing, repairing it is the first next step.

This is the canonical first command an agent runs after install.
Use -o json for structured output that an agent can parse, or
-o plain to emit one next-step command per line for shell
pipelines (head -n1 yields the most-likely next command).

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `--for-agent` | Emit current discovery context for one agent wake (no ANSI, no extras) |
| `--first-time` | Print the post-install onboarding card (used by install.sh; re-runnable any time) |

### Examples

```sh
# Human-readable card
sparkwing info

# Agent-readable record
sparkwing info -o json

# Load current agent discovery
sparkwing info --for-agent

# Reprint the post-install onboarding card
sparkwing info --first-time
```
