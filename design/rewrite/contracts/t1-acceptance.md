# Acceptance execution evidence

<!-- design:begin -->

Acceptance proofs are ordinary executable tests in `tests/acceptance` and the
packages that own the behavior. `internal/acceptance` contains native Go SDK
process fixtures. Test inputs and expected observations remain independent of
production policy tables.

`TestEnvironment`, `TestStorageBoundaries`, `TestStorage`, `TestProtocol` and
`TestTypeScriptBoundary`
select concrete executable cells through `runCell`. Each assertion-bearing
leaf starts with `recordLeaf` and records its result
after its assertions and cleanup pass. A selected cell passes only with at
least one matching leaf result and no failed or skipped leaf. Empty callbacks
and groups whose leaves were all filtered out produce no passing evidence.
A Go test subtest filter selects only matching callbacks.

Successful cells log JSON containing the test identity, revision, platform,
driver and result. The revision is a SHA-256 digest of the sorted tracked and
nonignored untracked worktree paths and their contents, including deletions.
It identifies an uncommitted candidate as well as committed source at run time.
A saved binary compiled earlier with `go test -c` and executed against a changed
worktree is outside this contract. Normal `go test` rebuilds from the current
source. Platform is the executing process's GOOS/GOARCH. The driver identifies
the in-process, Go SDK, Node test or native host implementation. Results from another revision,
platform, driver or cell are rejected. `TestExecutionRequiresMatchingSuccessfulResult`
and `TestExecutionRejectsSkippedFailedAndCleanupFailedCallbacks` guard this contract.

Run `go test -v ./tests/acceptance` to retain per-cell JSON results in the test
output. Missing native tools or inaccessible required services fail execution.
The suite requires an isolated test environment and never supplies a user's
cluster, cloud account or credentials by default.

The product requirements in [t1-invariants.md](t1-invariants.md) remain broader
than these executable proofs. A passing component test supplies evidence only
for its actual driver and platform. Cross-platform, cluster, hosted and complete
product-path claims require their own successful executions. Requirements and
historical port destinations do not count as execution evidence.

<!-- design:end -->
