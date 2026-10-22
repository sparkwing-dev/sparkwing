# W3 settlement and expansion continuation

<!-- design:begin -->

W3 remains incomplete. This continuation resolves the working contents of all
20 merge conflicts, consumes W4's settlement and expansion-context amendments,
and exercises the shared executor with real Go SDK processes.

## Merge disposition

The working files contain no conflict markers. The index still has 20 unmerged
paths because the worker brief reserves Git writes for the lead. Stage the
resolutions and finish the merge after review.

The v2 JobSpec, PlanDoc, closure and pointer declarations win. W3's removal of
automatic retry traversal, worker layers and old launcher execution wins.
Manual retry still reuses only successful matching results. Terminal rows stay
terminal until the store commits a requeue.

Preserved deletions are internal/orchestrator/claim_exec.go (225 lines),
internal/orchestrator/run_node_claim_test.go (226 lines) and
internal/nodehost/onfailure_test.go (89 lines). These 540 lines were deleted by
earlier W3 work and reappeared as modify/delete conflicts. Additional legacy
production deletion in this continuation is zero. The unused legacy HostNode
entry point and its pod flags were also kept out of the resolution.

The other conflicted files are dispatch_test.go, events_test.go, logs.go,
memo.go, only.go, only_test.go, podnode.go, podnode_test.go, rehydrate.go,
rehydrate_test.go, retry.go, retry_test.go, scheduler.go and scheduler_test.go
under internal/nodehost, plus hosted_run.go, pod_node_host.go and
pod_node_host_test.go under internal/orchestrator. Both lease and protocol-error
imports survive where needed. The memo coordination key retains its existing
prefix because the incoming m.key method has no implementation.

## Implemented behavior

The settlement invariant is that every step body and predicate receives an
explicit snapshot of terminal steps in its bound job and current attempt.
Snapshots include skipped and failed predecessors, omit running work and reset
on retry. Both the common executor and the legacy step driver use the same
snapshot projection. Job hooks, generators and memo predicates carry no step
state. Eval requests use the closure selector. Failure context is attached only
to run_step, as Request.Validate requires.

Cancellation settles pending steps in dependency order. A Finally step behind a
cancelled descendant waits for that descendant's running ancestors. The native
finally-chain case failed before this change because the SDK refused incomplete
prerequisite state. It now runs cleanup and joins the process.

Expansion and reconstruction pass Accepted.Plan.Concurrency as
ValidationContext.ExistingConcurrency. Initial planning refuses that
expansion-only context. Generated job bytes retain unknown and duplicate
fields until the real shared validator sees them.

The owner applies terminal graph decisions before committing a predicted final
verdict. The shared decider can return both together. Rejecting that response
previously prevented a failed expansion from cancelling its barrier consumer.

The admission fixture returns setup errors to its caller instead of calling
Fatal from a worker goroutine. Its parallel cases select setup completion and
context deadlines while waiting for admission. The event fixture cancels and
joins its approval resolver on every exit. The missing legacy schema now fails
those cases promptly instead of leaving the admission case hanging.

## Acceptance and retained ports

TestNodeExecutorDrivesNativeSDKSteps builds the retained stepsfixture and runs
ten cases through NewNodeExecutor and StartProtocolSession with the real Go SDK,
real protocol validation and private pipes. Cases cover local result reads,
parallel work, fail-fast, transitive Finally cleanup, collect-all, continued and
optional failures, skipped predecessors, throwing predicates and panicking
bodies. Store, engine verification and route delegation remain fakes.

TestNodeExecutorSendsCurrentAttemptSettlement covers body and predicate requests
for success, skip, failure and retry. It checks the execution ordinal, explicit
empty state, complete transitive state and job hooks without step state.

lifecycle-generator-barrier/hermetic-library now has twelve cases. It uses
OwnerHost.Run, RunSnapshot.Scheduling, store.Decide, OwnerEvaluator and the real
nodeproto.ValidatePlan. It covers success, memoized sources, empty groups, SDK
failure, refused commit, unknown fields, duplicate fields, null collections,
invalid concurrency, a cycle through a barrier consumer and 256/257 fan-out.
Persistence and SDK evaluation are fakes. Successful generated rows are supplied
as terminal remote results by the fake store. This does not prove generated
executor placement or transactional persistence.

The existing generator replay and approval-policy acceptance helpers now follow
the amended eval selector. Lifecycle acceptance passes. Native target matrix
cells remain skipped. These additions port part of TestHostDrivesSteps to the
common executor. Step windows, dry-run variants and persisted step rows still
need their remaining ports. No complete parked inventory row is newly claimed.

## Amendment and handoff audit

| Assignment | W3 disposition |
| --- | --- |
| A1-01/02/05 and handoff 2 | The eleven service registrations and mask gate remain. C2 PolicyRouter is still a panic stub. Replacing the production Server, /nodes routes, header gate and legacy errors together remains open. |
| A1-03/04/06/07 | Existing error, record, cancel and optional-declaration implementations remain. Eval requests now match the amended field restrictions. |
| A1-10/11/12 | Source acquisition and explicit environments remain. procenv.Git is implemented in this merge. W8 engine.InstallSDK is still a panic stub. |
| A1-13 through A1-18 | Existing reconciliation, cancellation, failure and ordinal boundaries remain. The restricted full remote snapshot, result failure transport and terminal-result reuse transport remain open. |
| A1-19 and handoff 3 | The shared hello receives selected engine and granted runner inputs. The native Go test exercises that path. One production constructor across worker, claimed and pod paths remains open. |
| A1-21 through A1-25 | Existing Authorized services, exact request evidence and holder authority remain. Concrete authentication and transactional release remain cross-area integration points. |
| A1-36 | Verified byte/record layers remain. Native storage assembly and owner memo composition remain open. |
| A1-38/39 | Shared claimed hosting and its lease keeper remain. Native runner/bootstrap wiring and the old runtime cut remain open. |
| A1-44 through A1-48 and handoffs 7/9 | Existing captured environments, classification, guards, alias/entrypoint separation and startup/cleanup projections remain. Native constructor, CLI selection and real service integration remain open. |
| A1-50/51 | Earlier launcher deletions are preserved and v2 consumers compile. The old scheduler, Session, Server, layers and remaining runtime fixtures still need deletion after native migration. |
| A1-53/54 | Component acceptance passes. Full native placement evidence and approval policy/read integration remain open. |
| Handoff 4 | The common eval decoder checks errors before values. Throwing step predicates and job closures are tested. Legacy generator/memo consumption still needs retirement with the old runtime. |
| Handoff 5 | Raw strict validation, accepted concurrency context, fan-out and cross-boundary-cycle component proofs pass through the owner scheduler. Cross-area concurrency interpretation remains below. |
| Handoff 6 | W4's settled and attempt fields are consumed by both step drivers. Ten real Go SDK process cases pass. TypeScript and native placement parity remain separate. |
| Handoff 1 and failing legacy tests | All nodehost tests compile. The retained event port remains incomplete. Named admission tests fail on missing sparkwing_meta. The old event test also reaches a sandbox-denied TCP listener. These failures are retained as failed evidence. |

## Contract changes needed in other areas

W4's previously missing settled and ExistingConcurrency fields are present and
consumed. No further declaration is needed for those two integrations.

The handoff's demand to reject mismatched concurrency differs from W4's merged
tests. TestConcurrencyMembershipHasIndependentCost and
TestExpansionPreservesConcurrencyDeclarations accept differing capacities and
costs across memberships. ValidatePlan also accepts a positive cost above its
capacity. W3 passes the full accepted context and tests refusal of explicit zero.
W4 and the lead must reconcile the mismatch requirement. W3 added no private
concurrency rule.

W9 still needs a restricted full execution snapshot, JobResult's persisted
JobFailure and ReuseSource's terminal result reference/state sequence. The merged
JobResult and ReuseSource declarations still lack those fields. C2 must provide
the production policy router and live process delegation. W8 must connect the
selected engine installer and native hosting. C1 owner policy/approval edges,
W7/C1 memo integration and native storage authority remain as listed in the
preceding handoffs.

The shared Options declaration has no sealed start-at/stop-at fields even though
the legacy driver retains step windows. That part needs a W6/W9 contract decision
before the common executor can consume it.

## Verification

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE, GOTMPDIR and TMPDIR are under /tmp/sw-astra-W3. Scratch is removed before
the final report.

- Whole-repository build passes.
- Scoped vet passes for nodehost, source, lease, orchestrator, internal acceptance
  and tests/acceptance.
- Complete source, lease and both acceptance package suites pass.
- The current-host selection below passes, including native SDK processes.
- Race checks pass for node executor, owner, planning and fuzz seed tests.
- Commentcheck against main and git diff --check pass.
- The old admission/event selection fails for the schema/socket reasons above.
- Sleepcheck against HEAD reports the incoming W4 time.After in
  sparkwing/sparkwingtest/host_test.go. It reports no W3 finding.

The current-host selection is:

```text
test -p 4 ./internal/nodehost -run '^(TestNodeExecutor|TestOwner|TestProtocolSession|TestNativeSDK|TestProcessServices|TestProcessCleanups|TestClaimedNodeHost|TestStateFollower|TestPlanInitial|Fuzz)' -count=1 -timeout 3m
test -race -p 4 ./internal/nodehost -run '^(TestNodeExecutor|TestOwner|TestPlanInitial|Fuzz)' -count=1 -timeout 3m
```

Shared files changed outside W3 are internal/acceptance/host_expansion.go,
host_replay.go and host_policy.go, plus this handoff, c3-protocol-sdk.md,
handoffs.md and t1-port-inventory.md. No external production caller changed.
No contract shape, customer limit, environment variable or service was added.

Bootstrap invoked the installed sparkwing info command before the brief's ban
was read. No further Sparkwing command ran. No Git write, candidate install,
pipeline, deployment or release ran. The lead retains merge completion,
independent review, pipelines and native placement acceptance.

<!-- design:end -->
