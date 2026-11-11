# Agent orientation

Sparkwing is standalone. Do not assume any other local service or machine-specific agent harness exists.

- Start with `sparkwing info --for-agent`; its output is current context for this wake, not documentation to copy.
- Read [design/rewrite/ORIENTATION.md](design/rewrite/ORIENTATION.md) for the owner and guard test of each core rule, the test workflow, known gotchas and the decision index.
- Search the command index with `sparkwing commands --search status`; narrow it with `--path "sparkwing pipeline"`. Read a named command with `<command> --help`. `-o json` emits index records (`path`, `synopsis`, `subcommand_count`) followed by a page record. Commands default to 40 matches; use a positive `--limit` and continue with `--cursor <next_cursor>` using the same filters. Search documentation with `sparkwing docs search --search <topic>` and read a hit with `sparkwing docs read --topic <slug> --section <start_line>`. Discovery JSON is NDJSON, one complete record per line.
- Use `sparkwing pipeline list -o json` to discover repository pipelines.
- Treat the README, embedded docs, CLI help, and tests as product truth. Run the relevant pipeline with `sparkwing run <name>`.
- Before selecting checks or changing hook behavior, read [DELIVERY.md](DELIVERY.md) for tier membership, budgets, coverage limits, and measured costs. `sparkwing pipeline hooks status` verifies which hooks are armed in this checkout.
- Read `DELIVERY.md` before landing a change; it names the cheap checks and the decisions that must not be skipped.
- Submit every agent landing through `bash <main-checkout>/bin/queue-land.sh -C <candidate> --brief '<goal>'`, using an up-to-date main copy of the script. Read `DELIVERY.md`'s queued Land contract for pin refusals and changes to the pipeline itself.
- The committed `go.work` joins the host, Go SDK and pipeline modules. Use this
  worktree's workspace for source builds (`GOWORK=auto` from the worktree).
  Root `go test ./...` skips the nested SDK module, so run
  `GOWORK=off go -C <worktree>/sparkwing test ./...` separately. Workspace-off
  host builds resolve the published SDK requirement. Before the first nested
  SDK tag is published, use a scratch modfile with a local SDK replacement for
  that check. Keep the root manifest free of replacements so versioned
  `go install` works.
- Lint the way the gate does: `GOWORK=auto golangci-lint run --allow-serial-runners ./...` from each committed module, with `GOLANGCI_LINT_CACHE` pointing inside the worktree. `.golangci.yml` judges only code new since `origin/main`, so a checkout that cannot resolve that ref reports the whole tree. A run that exits 4 saying "Timeout exceeded" stopped at `run.timeout` with packages unjudged: raise the budget and run again rather than fixing what printed.
- For release notes, add a unique `docs/changes/<slug>.md` fragment and any breaking guide in `docs/migrations/unreleased/<slug>.md`; read `CHANGELOG-STYLE.md` for the formats. Run `bash bin/sync-docs.sh` and commit the matching fragment mirrors.
- Update source documentation and its drift check when behavior changes.
- Compile-check with `go build -o /dev/null ./cmd/...` or `go vet ./...`. Building one command on its own drops a binary in the repo root, and `go build ./cmd/sparkwing` refuses outright because the output name collides with the `sparkwing/` SDK directory.

`AGENTS.md` is the repository's canonical harness guidance. `CLAUDE.md` only imports it with `@AGENTS.md`.
