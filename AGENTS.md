# Agent orientation

Sparkwing is standalone. Do not assume any other local service or machine-specific agent harness exists.

- Start with `sparkwing info --for-agent`; its output is current context for this wake, not documentation to copy.
- Use `sparkwing commands` (a one-line index; `--path` narrows) and `sparkwing pipeline list -o json` to discover the live CLI and repository pipelines. Every list verb's `-o json` is NDJSON -- one complete record per line -- so pair it with `--path` and `head` rather than reading the whole surface: `sparkwing commands --path runs -o json | head -20`. Unfiltered, `commands -o json` is still 200KB.
- Treat the README, embedded docs, CLI help, and tests as product truth. Run the relevant pipeline with `sparkwing run <name>`.
- Read `DELIVERY.md` before landing a change; it names the cheap checks and the decisions that must not be skipped.
- In a git worktree of this repo (including agent worktrees under `.claude/worktrees/`), the checked-in `go.work` resolves the main checkout's modules and breaks every plain `go` command with "does not contain modules listed in go.work". Prefix Go commands with `GOWORK=off`; the check scripts inherit the same requirement.
- Update source documentation and its drift check when behavior changes.

`AGENTS.md` is the repository's canonical harness guidance. `CLAUDE.md` only imports it with `@AGENTS.md`.
