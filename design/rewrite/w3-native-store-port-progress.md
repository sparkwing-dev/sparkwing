# W3 native store test port

<!-- design:begin -->

W3 remains incomplete. This continues `w3-store-admission-progress.md`. The
priority was the nodehost store port, followed by the W4 concurrency correction
and production caller retirement. Lead-owned handoffs and amendments are unchanged.
No acceptance cells were added or unskipped.

## Implemented

`NewNativeHost` discovers `CapacityStore` on its real tenant store. Each `Run`
binds the current owner claim on a private host copy, so reusing a host does not
reuse another run's owner. Missing coordination refuses concurrency work.
Actual SQLite tests observe the holder during execution and its removal after
terminal finish. Plan holders and queued waiters survive an interrupted owner.
Terminal node/run transitions release them, and promotion observes that release.
Local resource and semaphore admission stays composed around this store adapter.

Real-store admission exposed a C4 stub. `ResolveConcurrency` now resolves omitted
fields to global/1/1/queue, rejects invalid declarations and persists concurrency
in plan, job and expansion settings. Reused group definitions must agree on
capacity, scope and on_limit. Cost may differ. These changes implement the existing
C4 contract without changing its signature.

Real expansion submission exposed another native integration gap. The run owner
credential cannot submit as the terminal source. `OwnerDelegator` resolves the
live owner and issues a narrowed source credential through C1's existing
`DelegateCapability`. It preserves parent expiry, deadline and scope, binds the
source generation separately from the owner lease, and grants only
`expansion.submit`. The host checks the returned identity before submission.
SQLite resume tests now use this real issuance and `AcceptExpansion` path.

Invariant: an expansion may affect only the source generation named by its current
owner-bound authority. `FuzzOwnerExpansionPreservesDelegatedSource` changes returned
source, attempt, owner lease, audience, parent and grants. It passes 1,002 inputs.
The check snapshots the requested binding before calling the delegator because
returned pointer fields may alias the input.

`OwnerHost.Run` also preserves the caller's cancellation cause when a canceled
SQLite query returns only `context canceled`.

## Retained ports and cuts

| Old surface | Retained evidence |
| --- | --- |
| Store admission and plan admission | Eight SQLite capacity tests plus native constructor integration, host reuse, missing-store refusal and stale-owner property seeds. Existing LocalAdmission tests retain resource, priority, queue and lease-ending behavior. |
| Approval scheduler fixtures | Real owner release and typed resolution/timeout transitions cover approve, deny, three timeout policies, cancellation, late human resolution and atomic storage refusal. |
| Attempt and closure runners | Native executor tests retain before/verify/after ordering across successful and exhausted retries, canonical failure input and refused before hook. Native planner tests retain unknown-pipeline refusal. The run admission lease remains live through `FinishRunAndUnit`. |
| Scheduler and lifecycle events | SQLite owner scheduling retains diamond parallelism, failure/recovery propagation, optional verdicts, accepted metadata, durable cancellation, caller cancellation, missing executor and refused output. Events come from the real transitions. |
| Remote dispatch and reattachment | SQLite claims retain persisted terminal results, one award while held, cancellation intent with holder drain, free unstarted requeue and terminal absorption. An owner may no longer finish remote work from a dispatch callback. |
| Manual retry reuse | The owner probes `SourceManualRetry` without execution on a miss. Live and terminal preservation use SQLite. Hit/missing-output responses use the existing reconciler contract fake. Actual authorized output copying remains open. |
| Generators and resume | Native evaluation tests retain original-byte strict validation, combined batches and 256/257 fanout. SQLite tests retain waiter ordering, failure/skip barriers, pending expansion resume, failed-child preservation and complete/empty replay. Native replay fuzz passed 5,401 inputs. |
| Retry and masking | SQLite execution remains running under the same generation at attempt two before its terminal report. Native layer/output refusal tests check masked, bounded returned errors, persisted error text and `job_failed` events. DryRun uses the native environment builder. |

Removed tests for arbitrary `Run.Add`, partially persisted generator batches,
owner-authored remote finishes and obsolete scheduler result/event spellings.
Strict validation and atomic `AcceptExpansion` replace those mechanisms. A skipped
generator source skips its generated consumers and keeps the run green, as frozen
architecture section 3.6 requires.

The tracked test diff removes 3,619 old lines and adds 1,130 replacement lines.
Five new test files add 556 lines. Legacy hosting implementation deleted: **0
lines**. `Session`, `Server`, the old scheduler/layers and their orchestrator
callers remain. Those are required follow-up work, not a completed hard cut.

Lead decision 9 is addressed in `internal/nodeproto/plan_validation_test.go` by
removing the W4 assertion that accepted a conflicting capacity. The existing
`TestExpansionPreservesConcurrencyDeclarations` table verifies refusal for
capacity, scope and on_limit, permits changed cost, and checks the field path.
The full nodeproto package passes.

## Remaining contracts and integration

1. **W1 typed debug operations.** Four retained debug test roots still fail in
   their legacy fixture. Native pause before/after/failure, timeout, cancellation
   and downstream release need an authority-bearing pause/activity contract.
   `CreateDebugPause` and the raw activity writers do not provide one. These
   tests were preserved because debug remains a feature.
2. **W9 sealed selection, lead decision 1.** `runspec.Options` still has no
   `Only`, `StartAt` or `StopAt` fields in this tree. The retained selection test
   still fails in its old fixture. Its native port must consume the final seal,
   rather than introduce an unsealed flag channel.
3. **W1 policy outcomes.** Owner admission fail/queue-timeout and approval SkipIf
   success still lack their required typed transitions. Plan refusal also needs
   terminal handling for pending work. Native approval logs, annotations and
   output have not been assembled. `skipLayer` refuses approval SkipIf explicitly.
4. **W1 cancellation/capacity join.** The previous report's `cancel_others` finding
   remains open. No W1 implementation was changed in this session.
5. **W2 source generation zero.** C2's `narrowOwnerExpansion` requires a positive
   source generation. A materialized source with generation zero still cannot
   obtain the source delegation used by real submission. No foreign contract
   was changed to hide that case.
6. **W3 production cutover.** The actual local/trigger/pod orchestrator entrypoints
   still use the legacy host. Native debug/memo policies, retained local
   semaphores around remote work, authorized copied-output reconstruction and
   complete record integration remain. Socket tests stop before their behavior
   can be judged in this sandbox.
7. **W1 snapshot cancellation classification.** A repeated real SQLite generator
   resume run with race instrumentation returns `interrupted (9)` while a worker
   completion cancels the snapshot follower. The normal full and filtered samples
   passed, but `go test -race -p 4 ./internal/nodehost -run
   '^TestAResumedRunGeneratesASourcesUnrecordedNodes$' -count=10` reproduced the
   failure. A temporary overlay isolated the store boundary with 32 accepted
   jobs and 200 concurrent-read/cancel interleavings. It failed after 4.22 seconds:
   `context=context canceled error=*sqlite.Error interrupted (9)` from
   `Tenant.ReadSnapshot`, without the owner waiter. The native waiter must preserve
   independent subscription errors. Normalize a database interruption caused by
   its own canceled read at the
   persistence boundary, rather than add host retries or discard backend errors.
   No W1 file or foreign contract was changed.
8. **W2/W9 remote adapters.** `NewEngineClient`, `SourceAttempt` transport and
   `RecordStep` now exist, superseding the older report's stub observations.
   `Controller.ReadSnapshot` still returns a scheduling projection, while the
   shared host reads accepted run and execution rows. The controller interface
   also lacks the native capacity/adoption and owner-delegation surfaces. Their
   final authorized adapters remain integration work. No raw legacy writer was
   substituted for them.

The prior assignment audit stays in force for A1-01 through A1-07, A1-10 through
A1-19, A1-21 through A1-25, A1-36, A1-38/39, A1-44 through A1-48, A1-50/51,
A1-53/54 and the attempt/Executing handoffs. This session advances concurrency
resolution, native admission, source expansion authority and retained test ports.
It does not claim completion of native placement or any acceptance cell.

## Verification

All Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, at most
four package workers, and caches/temp paths under `/tmp/sw-astra-W3/`.

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for nodehost, lease, source, nodeproto and settings.
- Full nodeproto, settings, lease and source suites pass.
- The final full nodehost run has 403 passing roots and 53 failing roots. Forty-eight
  fail at forbidden socket creation. Four debug roots and one selection root
  fail in the preserved old fixtures. The package is **not green**.
- Excluding those 53 named roots, the rest of nodehost passes in 22.959 seconds.
  This is scoped evidence, not a package-suite pass.
- Native real-store admission and host reuse pass with race detection. The
  returned-authority property seeds pass. The generator race run reproduces the
  SQLite interruption described above. Generator race validation is not green.
- Commentcheck against main and whitespace checks pass. No new test sleep was added.

## Shared edits and mechanisms

Outside W3 code, the shared edits are
`internal/settings/{contract.go,plan.go,concurrency.go,concurrency_test.go}`,
`internal/nodeproto/plan_validation_test.go`, the C3 export inventory, the T1 port inventory and this report.
No foreign production caller was migrated or deleted. No amendment or handoff
file was edited.

The added mechanism is `OwnerDelegator` plus `OwnerHost.Delegator`. Directly
submitting the owner actor fails C1's source-binding check. Resolving the parent
and delegating through the existing transactional issuer supplies the required
source and lifetime constraints. Removing that field/helper and restoring owner
submission would undo the change and restore the reproduced failure. Admission
uses a private per-run binding callback because the current owner is available
at `Run`, after construction. No user setting, environment variable or service
was added.

No Git writes, Sparkwing binaries, pipelines, candidate installs or releases ran.
Scratch is removed after all checks join. Git user/core configuration was inspected
and no test identity or worktree override was introduced.

<!-- design:end -->

STATUS: CONTINUE
