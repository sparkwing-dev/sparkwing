# Acceptance evidence

Run `go test -v ./tests/acceptance` from the repository root to retain JSON
results for executed assertion leaves and their cells. Empty cells and groups
whose leaves were all filtered out cannot pass. See the
[execution contract](../../design/rewrite/contracts/t1-acceptance.md).

Evidence binds to the worktree digest at run time, the executing platform and
the selected driver. Running a saved binary compiled earlier with `go test -c`
against a changed tree is out of scope. Normal `go test` rebuilds from the
current source.
