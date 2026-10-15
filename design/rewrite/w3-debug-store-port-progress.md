# W3 debug store port continuation

<!-- design:begin -->

W3 remains incomplete. This continues [the native store port](w3-native-store-port-progress.md).
The four retained debug roots use the native owner and final SQLite APIs. The
selection root still uses its old fixture because the sealed selection fields
are absent. No acceptance cell was added, unskipped or marked complete.

## Implemented and ported

`OwnerHost.Debug` consumes W3's `DebugBoundaryWaiter` contract. Each request
contains the current owner actor, persisted job reference and generation,
selected reason and sealed timeout. Native assembly refuses a selected pause
without that adapter. The adapter must return only after release or expiry, or
return the cancellation or persistence error.

The owner checks pause-before before owner layers or dispatch. It checks
pause-after and pause-on-failure before downstream scheduling or expansion.
Recovery jobs skip these pauses. Approval jobs retain pause-before and skip
pause-after. Terminal jobs are checked on resume, with released-boundary
idempotency owned by the adapter. Waiting work keeps its owner keeper alive.
The native compute observer remains false while no SDK process is computing.

The owner keeps active work unsettled in its scheduling projection until its
worker returns. Persisted terminal results stay unchanged. Cancellation uses
the actual terminal projection, so it cannot propose another holder cancellation
against an ended job.

The retained debug tests now use Tenant.AdmitRun, AcceptPlan, ClaimUnit,
ReconcileNode, TransitionNode and FinishRunAndUnit through the shared fixture.
Only the pause boundary and SDK execution are fakes. They retain host ordering,
downstream release, gate/recovery exceptions, cancellation and timeout
propagation. The timeout test is renamed because it checks the sealed timeout
sent to the adapter. It does not exercise a real expiry transaction. The
cancellation test preserves terminal absorption instead of overwriting a
successful terminal job with cancelled.

Removed 193 old test/fixture lines, adding 136 replacement lines. Legacy hosting
implementation removed: **0 lines**. Session, Server, the old scheduler/layers
and their production callers remain.

## Contracts and real integration still needed

**W1: debug persistence.** Implement the local `DebugBoundaryWaiter` interface
through authority-bearing tenant transactions. Recheck the live owner lease,
run and job generation on every mutation. Pause-before precedes readiness or
claiming. Pause-after/failure belongs to the terminal generation and cannot
rewrite its result. Use the store clock for expiry. Persist activity and
pause/resume events atomically. A released `(run, job, generation, reason)` never
reopens on replay. Waiting must catch persisted releases without losing a
notification, and end when its context ends. The existing raw CreateDebugPause,
GetActiveDebugPause and ReleaseDebugPause methods take no owner actor or source
generation and cannot serve as this adapter. Actual expiry, activity and event
assertions remain open. No C1 interface or implementation was changed.

**W9: sealed selections, lead decision 1.** `runspec.Options` still has no Only,
StartAt or StopAt. TestOnlyRunsTheMatchedNodesAndWhatTheyNeed remains red with
the legacy args_json fixture. The native port needs the declared seal fields.
No invocation-blob or mutable-flag substitute was added.

**W2/W9: native remote store data.** Controller.ReadSnapshot returns
ScheduleSnapshot, while OwnerStore and ClaimStore require a persisted RunSnapshot
with raw acceptance and execution rows. Controller.ReadRun returns Display input,
not an execution seal. ReconcileJob returns ExecutionJob but cannot supply the
whole accepted snapshot before reconciliation. The remote native adapter still
needs that authorized projection, capacity/adoption and owner-delegation
surfaces. This blocks the trigger/pod caller cut. No foreign contract was changed
and no safe display was reinterpreted as an execution seal.

**W1: canceled snapshot reads.** The existing race failure now reproduces in the
ported pause-after case too. Running `go test -race -p 4 ./internal/nodehost -run
'^TestDebug' -count=3` returned `interrupted (9)` while cancellation ended the
snapshot follower. The prior report has a direct Tenant.ReadSnapshot reproducer.
Normalize an interruption caused by that read's canceled context at the store
boundary. Independent backend/subscription errors must survive. No host retry or
error suppression was added. Race validation is not green.

Priority 2 was already committed before this session. The full nodeproto suite
passes. Its expansion-concurrency test refuses capacity, scope and on_limit
mismatches for both plan and job memberships, permits changed cost and checks
the field path. The conflicting W4 capacity assertion remains removed.

The earlier amendment audit and remaining integration entries in the native
store port remain open. This continuation advances owner debug policy and its
retained tests. Full native placement and production migration are incomplete.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and at most
four package workers, with GOCACHE and GOTMPDIR under `/tmp/sw-astra-W3/`.

- Whole-repository build and scoped nodehost/lease/source vet pass.
- Nodeproto, lease and source suites pass.
- The full nodehost sample has **407 passing roots and 49 failing roots**.
  Forty-eight stop at forbidden sockets. The remaining selection root reaches
  its old fixture. This is not a package-suite pass.
- Debug/native-host/owner-host tests pass ten repeated normal runs.
- Debug race testing reproduces the store interruption above.
- Commentcheck against main and whitespace checks pass. No test sleep was added.

Shared edits are the C3 export inventory and T1 port inventory. No foreign Go
caller or lead-owned amendment/handoff file was changed. No Git writes,
Sparkwing binaries, pipelines, installs, releases or new acceptance cells ran.

The new mechanism is OwnerHost.Debug plus the idempotent boundary interface.
Raw pause writers cannot bind a pause to current owner authority or replay a
terminal generation safely. Removing the field, interface and scheduling calls
undoes this continuation. No setting, environment variable, service or customer
limit was added. Scratch is deleted after checks join.

<!-- design:end -->

STATUS: CONTINUE
