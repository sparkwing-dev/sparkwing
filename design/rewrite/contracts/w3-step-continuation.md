# W3 step persistence continuation

<!-- design:begin -->

W3 remains incomplete. This continuation adds step persistence to the common
executor. The [previous assignment audit](w3-execution-continuation.md) remains
the disposition for work not changed here. The worktree started clean after the
lead committed that continuation and completed its merge.

## Implemented

The invariant is that a body starts only after its fenced running step record
commits. Every started operation is joined before a job result or retry. Step
records retain the job's exact holder, capability prefix, generation and attempt.
Their error text passes through the shared masker and error bound.

`NodeTransitionStore` now requires C1's `RecordStep`. The executor writes running,
passed, failed, skipped and cancelled states. Blocked steps receive terminal
records without SDK invocation. Finally keeps its bounded parent context after
an attempt timeout. Fail-fast records cancelled siblings as cancelled. Retries
write new attempt rows rather than replacing the previous attempt.

A refused step write stops new author code, cancels and joins parallel work,
and returns an error even for an Optional step. Persistence failure outranks an
application error, so a failed body cannot turn that refusal into a retry.
The tests reproduced both missing writes and the combined-failure case before
their fixes. Step writes are serialized by the scheduler, while bodies still
run concurrently.

## Evidence and retained ports

`lifecycle-transition-replay-and-fencing/hermetic-store-step-records` has six
SQLite cases. They cover success, retries, refused before hooks, skipped bodies,
failed prerequisites with Finally, and a transaction refusing a terminal step
write. The last leaves its step running, fails the job and returns the refusal.
SDK execution, acquisition, delegation and engine verification are fakes.

The native Go SDK test now checks terminal step records and required/forbidden
body output for eleven jobs. It covers ordering and result reads, parallel
steps, fail-fast, a transitive Finally chain, collect-all, ContinueOnError,
Optional, SkipIf, a throwing predicate, a panicking body and dry-run. Its store
is a contract fake. This ports the stored-step and dry-run assertions from
`TestHostDrivesSteps`. That old test remains because its step-window and host
diagnostic assertions still need their final port.

The coordinated refusal test starts a parallel body, refuses its sibling's
terminal write and proves the first body is joined. No sleep selects the order.
The fuzz property generates claim identities, ordinals and raw/encoded secret
failures. Reports must retain the fence and contain no registered failure text.

## Assignment audit and contract changes needed

| Assignment | Disposition |
| --- | --- |
| A1-18, A1-51, retained test handoff 1 | Stored step state now follows final attempts and native execution. Diagnostic events, step windows and old runtime deletion remain. |
| A1-01/02/05/21-25 and router handoff 2 | Existing service handlers remain. C2's PolicyRouter is still a panic stub. Native authentication and delegated process authority remain open. |
| A1-03/04/06/07, A1-10/11/12 | Existing error, record bound, protocol cancel, concurrency and source integrations remain. W8's selected installer remains an integration point. |
| A1-13-17, A1-36 | Existing reconciliation, failure, storage and replay components remain. Restricted remote snapshots, result/reuse transport and concrete owner policies remain open. |
| A1-19, A1-38/39, identity handoff 3 | Existing constructor and claim host remain. W8/W9 still need native award, worker and pod assembly. |
| A1-44-48/50, handoffs 7/9 | Existing sealed settings, alias, environment and cleanup projections remain. Native CLI/placement assembly remains open. |
| A1-53/54, handoffs 4/5/6 and concurrency codec | Prior component evidence remains. Approval policy edges and full native placement cells remain open. |

The W3-local contract change is the required `RecordStep` method. C1's method
and data shape are unchanged. No other area's contract is edited.

W9 still needs a typed step-report body/client adapter for the already declared
C2 start/finish/skip routes. The shared Controller interface currently has no
step writer. The earlier restricted-snapshot, JobResult.Failure and terminal
ReuseSource gaps also remain.

W6/W9 need a sealed representation for retained step-window selections.
`runspec.Options` has no StartAt, StopAt or Only field. W3 cannot recover them
from mutable flags or environment after acceptance. This piece is left open
without changing another area's contract.

C1 approval admission/bypass edges, opening time and resolution identity remain
as listed in the previous audit. The lead/W4 concurrency-mismatch question also
remains. No new architecture decision or customer restriction is introduced.

## Files and deletion

Production changes stay in `internal/nodehost/executor.go`,
`executor_process.go` and `executor_steps.go`. The changed W3 test files are
`executor_test.go`, `executor_step_records_test.go` and
`executor_settlement_unix_test.go`.

Shared fixture changes are `internal/acceptance/host_events.go`,
`internal/acceptance/host_executor.go` and `tests/acceptance/skeleton_test.go`.
The four changed contract documents record this work and its evidence.
No external production caller or API is removed. Legacy runtime deletion is
zero lines. Session, Server, old layers and orchestrator callers remain open.

The added mechanism is mandatory fenced step persistence using the existing C1
writer. Keeping it optional would silently discard step state on one placement.
The smaller alternative cannot satisfy the retained stored-step assertions.
Reverting it requires removing the interface method and its executor calls.

## Verification

Commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, `-p 4`,
and caches/temp directories under `/tmp/sw-astra-W3`. The directory is deleted
before the report.

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for nodehost, orchestrator, source, lease and both acceptance packages.
- Complete source, lease and both acceptance suites pass.
- The current-host selection in the previous continuation passes, including
  all new step tests, native SDK tests and fuzz seeds.
- Race checks pass for node executor, constructors, owner, lifecycle acceptance
  and the new fuzz seeds. The combined failure test initially failed under race
  as well as normally. Both pass after persistence failure takes precedence.
- The five-second, two-worker step fence/mask fuzz run passes 1,859 executions.
- Commentcheck against main is clean across 1,013 files.
- Whitespace checks pass. Git configuration has no local user.name or
  core.worktree override. HEAD remains `eec3dc1a2`.

Focused reproduction uses the same environment prefix:

```text
go test -p 4 ./internal/nodehost -run '^(TestNodeExecutor|FuzzExecutorStep)' -count=1 -timeout 2m
go test -p 4 ./tests/acceptance -run '^TestLifecycle/lifecycle-transition-replay-and-fencing/hermetic-store-step-records' -count=1 -timeout 2m
go test -race -p 4 ./internal/nodehost ./tests/acceptance -run '^(TestNodeExecutor|TestNewNodeInput|TestOwner|TestLifecycle|FuzzExecutorStep)' -count=1 -timeout 3m
go test -p 4 ./internal/nodehost -run '^$' -fuzz '^FuzzExecutorStepRecordFenceAndMask$' -fuzztime=5s -parallel=2 -timeout 1m
go run ./internal/commentcheck -base main .
```

Run these from the worktree with `go -C` or `env -C` as required by its guidance.
The reproducible current-host selection remains in the previous continuation.

No Sparkwing executable, pipeline, installer, Git write, deployment or release
runs in this continuation. Full legacy nodehost/orchestrator suites and native
HTTP/api.sock/platform acceptance remain unverified. Their earlier failures are
not represented as fixed by this step-persistence change.

<!-- design:end -->
