# Agent orientation

Sparkwing is standalone. Do not assume any other local service or machine-specific agent harness exists.

- Start with `sparkwing info --for-agent`; its output is current context for this wake, not documentation to copy.
- `sparkwing commands` is an index, so search it rather than read it: `sparkwing commands | grep -i status` answers in one call and finds matches across branches that walking `--help` would never reach. Once a command is named, `<command> --help` is its detail page. `-o json` is the same index for a program to parse -- `path`, `synopsis`, `subcommand_count`, nothing else -- so reading it is never a substitute for `--help`. Every list verb's `-o json` is NDJSON, one complete record per line, so `head` and `jq` cut it safely.
- Use `sparkwing pipeline list -o json` to discover repository pipelines.
- Treat the README, embedded docs, CLI help, and tests as product truth. Run the relevant pipeline with `sparkwing run <name>`.
- Read `DELIVERY.md` before landing a change; it names the cheap checks and the decisions that must not be skipped.
- `go.work` is gitignored, so it is whatever the developer left in the tree. One sitting above a worktree -- the main checkout's own, which `.claude/worktrees/` sits under -- lists that checkout's modules, not the worktree's, and every plain `go` command in the worktree fails with "does not contain modules listed in go.work". Run `GOWORK=off go -C <worktree> build ./...`: `-C` on its own still fails, because go reads the workspace from the directory it lands in, so `GOWORK=off` has to travel with it. `sparkwing info --for-agent` prints that line with the paths filled in whenever it applies, and the check scripts inherit the same requirement.
- Update source documentation and its drift check when behavior changes.
- Compile-check with `go build -o /dev/null ./cmd/...` or `go vet ./...`. Building one command on its own drops a binary in the repo root, and `go build ./cmd/sparkwing` refuses outright because the output name collides with the `sparkwing/` SDK directory.

`AGENTS.md` is the repository's canonical harness guidance. `CLAUDE.md` only imports it with `@AGENTS.md`.
