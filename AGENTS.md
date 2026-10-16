# Agent orientation

Sparkwing is standalone. Do not assume any other local service or machine-specific agent harness exists.

- Start with `sparkwing info --for-agent`; its output is current context for this wake, not documentation to copy.
- Search the command index with `sparkwing commands --search status`; narrow it with `--path "sparkwing pipeline"`. Read a named command with `<command> --help`. `-o json` emits index records (`path`, `synopsis`, `subcommand_count`) followed by a page record. Commands default to 40 matches; use a positive `--limit` and continue with `--cursor <next_cursor>` using the same filters. Search documentation with `sparkwing docs search --search <topic>` and read a hit with `sparkwing docs read --topic <slug> --section <start_line>`. Discovery JSON is NDJSON, one complete record per line.
- Use `sparkwing pipeline list -o json` to discover repository pipelines.
- Treat the README, embedded docs, CLI help, and tests as product truth. Run the relevant pipeline with `sparkwing run <name>`.
- Before selecting checks or changing hook behavior, read [DELIVERY.md](DELIVERY.md) for tier membership, budgets, coverage limits, and measured costs. `sparkwing pipeline hooks status` verifies which hooks are armed in this checkout.
- Read `DELIVERY.md` before landing a change; it names the cheap checks and the decisions that must not be skipped.
- `go.work` is gitignored, so it is whatever the developer left in the tree. One sitting above a worktree -- the main checkout's own, which `.claude/worktrees/` sits under -- lists that checkout's modules, not the worktree's, and every plain `go` command in the worktree fails with "does not contain modules listed in go.work". Run `GOWORK=off go -C <worktree> build ./...`: `-C` on its own still fails, because go reads the workspace from the directory it lands in, so `GOWORK=off` has to travel with it. `sparkwing info --for-agent` prints that line with the paths filled in whenever it applies, and the check scripts inherit the same requirement.
- Lint the way the gate does: `GOWORK=off golangci-lint run --allow-serial-runners ./...` from each committed module, with `GOLANGCI_LINT_CACHE` pointing inside the worktree. `.golangci.yml` judges only code new since `origin/main`, so a checkout that cannot resolve that ref reports the whole tree. A run that exits 4 saying "Timeout exceeded" stopped at `run.timeout` with packages unjudged: raise the budget and run again rather than fixing what printed.
- Update source documentation and its drift check when behavior changes.
- Compile-check with `go build -o /dev/null ./cmd/...` or `go vet ./...`. Building one command on its own drops a binary in the repo root, and `go build ./cmd/sparkwing` refuses outright because the output name collides with the `sparkwing/` SDK directory.

`AGENTS.md` is the repository's canonical harness guidance. `CLAUDE.md` only imports it with `@AGENTS.md`.
