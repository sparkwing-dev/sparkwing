# Acceptance execution evidence

<!-- design:begin -->

Acceptance proofs are ordinary executable tests in `tests/acceptance` and the
packages that own the behavior. `internal/acceptance` contains native Go SDK
process fixtures. Test inputs and expected observations remain independent of
production policy tables.

`TestEnvironment`, `TestStorage`, `TestProtocol` and `TestTypeScriptBoundary`
select concrete executable cells through `runCell`. Each selected callback must
finish its assertions and cleanup successfully. Missing results, skipped
callbacks, failed assertions and failed cleanup fail the selected cell.
A Go test subtest filter selects only matching callbacks.

Successful cells log JSON containing the test identity, revision, platform,
driver and result. The revision is a SHA-256 digest of the sorted tracked and
nonignored untracked worktree paths and their contents, including deletions.
It identifies an uncommitted candidate as well as committed source. Platform is
the executing process's GOOS/GOARCH; driver identifies the in-process, Go SDK,
Node test or native host implementation. Results from another revision,
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
