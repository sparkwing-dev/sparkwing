# W3 execution diagnostics continuation

<!-- design:begin -->

W3 remains incomplete. This continues the [step persistence work](w3-step-continuation.md).
The [execution audit](w3-execution-continuation.md) still governs unchanged assignments.

## Implemented and ported

The invariant is that step diagnostics follow committed step state, contain no
registered failure text, and retain the committed attempt's identity. A refused
write prevents further author code and every started operation is joined.

`ExecutorDependencies.Log` receives masked host diagnostics with `NodeInput`.
The common executor emits step start/end/skip, dry-run refusal, retry and
fail-fast records. Fail-fast includes its triggering step, sorted cancelled
siblings, their count and cancellation latency. Retry records distinguish the
node-wide ordinal from the application retry budget. These are log records.
The store remains the sole lifecycle event writer.

The timeout tests reproduced a refused step write disappearing into a timeout
and permitting retry. They also reproduced Finally starting after that refusal
when timeout won the context cancellation race. The executor retains write
failure independently of the context's first cause. Timeout cannot erase it or
permit more author code. The coordinated parallel test covers both step-store
and diagnostic failures without sleeps.

`TestHostDrivesSteps`' retained diagnostics move to
`TestProtocol/protocol-step-failure-policies/hermetic-native-go-steps`.
Its eleven cases run real Go SDK processes through the common executor and
strict validator. They cover ordering, result reads, parallel work, fail-fast,
Finally chains, collect-all, ContinueOnError, Optional, SkipIf, predicate/body
panics and dry-run. Store, engine verification, delegation and output publication
are fakes. Native route and placement wiring remain unverified.

`lifecycle-transition-replay-and-fencing/hermetic-store-step-records` now has
seven SQLite cases. Its diagnostic callback reads committed step and attempt
rows before accepting each log. The added refused-log case fails the job,
joins the process and does not retry. A refused step transaction emits no
terminal diagnostic. The SDK and acquisition remain fakes.

The existing step fence/masking fuzz property also checks diagnostic messages
and JSON attributes against raw, JSON-escaped, URL-encoded and base64 failures.

`hermetic-store-foreign-awards` ports the old orchestrator holder-refusal test
through the common claimed host and SQLite. Four cases alter holder, capability,
generation or ordinal. They receive lock_held before renewal, source or SDK
startup. Persisted state and events stay unchanged. The existing
`FuzzClaimedAwardFence` supplies adversarial coverage of the same boundary.

## Deletion and integration

The unused legacy step validator is deleted from `internal/nodehost/steps.go`
(44 lines). The old step test loses 144 net lines of ported assertions and helpers.
Its remaining native cases are named `TestHostStepWindows`, because their
StartAt/StopAt inputs still lack a sealed contract. The native executor test
moves into the shared acceptance driver.
`TestClaimedNodeHost_RefusesANodeAnotherHostHolds` is removed from
`internal/orchestrator/trigger_host_test.go` (27 lines) after its SQLite port. Its old
fixture failed because it still inserted into the deleted tokens table.
No production caller outside W3 changes.

The local contract addition is `ExecutorDependencies.Log`. Placements must
connect it to the bound job-log sink separately from untrusted SDK frame events.
The alternative of routing these records through the SDK would let author code
forge host diagnostics. Reverting this addition removes the callback and its
call sites together. It adds no setting, service or environment variable.

The only shared implementation files changed are
`internal/acceptance/host_events.go`, `internal/acceptance/host_steps.go` and
`tests/acceptance/protocol_test.go` and `tests/acceptance/skeleton_test.go`.
The contract inventory, port inventory and handoff index link this continuation.

## Remaining assignments and contract changes needed

- A1-18/51 and retained-test handoff 1 gain the diagnostic and native step ports.
  Step windows, the complete dispatcher-event port and legacy runtime removal remain.
- A1-01/02/05/21-25 and router handoff 2 retain their implemented service handlers.
  C2's PolicyRouter, shared authentication and delegation functions remain stubs.
  W3 still needs the process authority adapter and native router assembly.
- A1-03/04/06/07/10-17/36/38/39/44-48/50/53/54 and handoffs 3-7/9 retain the
  dispositions in the prior audit. No completed component is reimplemented here.
  Owner policy implementations and runner, local-controller, pod and CLI assembly
  remain open alongside native placement acceptance.
- W9/W2 still need typed fenced step reporting, a restricted execution snapshot,
  persisted JobFailure on JobResult and the terminal result/state on ReuseSource.
- W6/W9 still need sealed StartAt/StopAt/Only selections.
- C1 still needs the owner-policy failure and approval-bypass edges and the
  persisted approval opening time/resolution identity listed in the prior audit.
- The W4 expansion-concurrency mismatch question still needs the lead's decision.

These are unchanged cross-area requests. This continuation changes no other
area's contract and introduces no architecture decision. The area is not ready
to retire Session, Server, the old scheduler/layers or their orchestrator callers.

## Verification

Commands run from the worktree with `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOMAXPROCS=4`, `-p 4` and cache/temp directories under `/tmp/sw-astra-W3`.
The final report records the completed checks and scratch cleanup.

- Whole-repository build and scoped vet pass.
- Current-host tests and the focused orchestrator scope/fuzz selection pass.
- Full source, lease and both acceptance packages pass.
- Race checks pass for executor, lifecycle and the native Go step-policy component.
- The four SQLite foreign-award cases also pass under race.
- The five-second masking/fence fuzz run passes 2,137 executions.
- Commentcheck against main passes. Whitespace checks are clean.

HEAD remains `56cb31a8b`. Git configuration has no local user.name or
core.worktree override. Changes are uncommitted for the lead. The W3 scratch
directory is removed before the final report.

Focused reproduction:

```text
go test -p 4 ./internal/nodehost -run '^(TestNodeExecutor|FuzzExecutorStepRecord)' -count=1 -timeout 2m
go test -p 4 ./tests/acceptance -run '^TestProtocol/protocol-step-failure-policies/hermetic-native-go-steps' -count=1 -timeout 2m
go test -p 4 ./tests/acceptance -run '^TestLifecycle/lifecycle-transition-replay-and-fencing/hermetic-store-step-records' -count=1 -timeout 2m
go test -p 4 ./tests/acceptance -run '^TestLifecycle/lifecycle-transition-replay-and-fencing/hermetic-store-foreign-awards' -count=1 -timeout 1m
go test -p 4 ./internal/nodehost -run '^$' -fuzz '^FuzzExecutorStepRecordFenceAndMask$' -fuzztime=5s -parallel=2 -timeout 1m
```

Full legacy nodehost/orchestrator suites, native HTTP/api.sock, other placements,
TypeScript parity and non-Linux hosts are not established by these checks.
No Sparkwing executable, installer, pipeline, Git write or release runs here.

<!-- design:end -->
