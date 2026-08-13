# Agent orientation

Sparkwing is standalone. Do not assume any other local service or machine-specific agent harness exists.

- Start with `sparkwing info --for-agent`; its output is current context for this wake, not documentation to copy.
- `sparkwing commands` is an index, so search it rather than read it: `sparkwing commands | grep -i status` answers in one call and finds matches across branches that walking `--help` would never reach. Once a command is named, `<command> --help` is its detail page. Reach for `-o json` only when a program consumes the records; unfiltered it is 200KB against the index's 13KB. Every list verb's `-o json` is NDJSON, one complete record per line, so `head` and `jq` cut it safely.
- Use `sparkwing pipeline list -o json` to discover repository pipelines.
- Treat the README, embedded docs, CLI help, and tests as product truth. Run the relevant pipeline with `sparkwing run <name>`.
- Read `DELIVERY.md` before landing a change; it names the cheap checks and the decisions that must not be skipped.
- In a git worktree of this repo (including agent worktrees under `.claude/worktrees/`), the checked-in `go.work` resolves the main checkout's modules and breaks every plain `go` command with "does not contain modules listed in go.work". Prefix Go commands with `GOWORK=off`; the check scripts inherit the same requirement.
- Update source documentation and its drift check when behavior changes.

`AGENTS.md` is the repository's canonical harness guidance. `CLAUDE.md` only imports it with `@AGENTS.md`.
