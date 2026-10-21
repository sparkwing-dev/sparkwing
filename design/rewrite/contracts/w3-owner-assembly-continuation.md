# W3 owner assembly continuation

<!-- design:begin -->

W3 remains incomplete. This continuation advances native assembly and a retained
failing test. The [assignment audit](w3-ingress-lease-continuation.md#remaining-contracts-and-integration)
still defines the remaining contract and placement work.

## Implementation and deletion

Executable jobs evaluate SkipIf and memo checks in the shared executor on their
executing host, under the actual award, after concurrency and machine admission.
The coordinator acquires no job groups for evaluation. Predicate errors fail the
job before its body runs. Timeout diagnostics include the predicate index. A true
predicate settles a skipped result after process revocation and joined cleanup,
then releases job capacity. A job that skips still waits for its slot and briefly
occupies a machine.

Approval SkipIf remains an admission condition before the human wait. Its
executing-host placement remains unresolved because approval-only jobs have no
execution award. The existing approval bypass promise remains in force.

The existing native-host fixture now exercises `OwnerHost.Run`, the real shared
scheduling projection and decider, self-claim, delegated process execution and
`FinishRunAndUnit`. Its second branch exercises claimed execution. Both hosts use
the same executor. Store lifecycle, C2 validation and admission are explicit
fakes. The SDK child is a synthetic process with the existing test codec.

The stalled-describe assertion now follows intake's lifecycle. It uses the native
SDK planner under an owner and requires failure to finish that owner without
accepting a plan. Its shell child and grandchild must have exited before return.
The old assertion that no run exists before describe is removed because intake
precedes planning in the frozen lifecycle.

Both sessions now call `startSDKProcess`. The duplicate native Unix wrapper and
Windows launcher are deleted, totaling **59 lines**. The shared platform owners
move to `process_unix.go` and `process_windows.go`. Including the platform merge,
production launch code shrinks by **63 lines**. The larger legacy scheduler,
Session, Config, Execute and orchestration callers remain.

Invariant: the Windows parent clears protocol-handle inheritance after success
and startup failure. The shared implementation retains the native launcher's
deferred clearing. A Windows fuzz test varies both initial handle flags and
startup refusal. Native Windows execution remains required.

## Verification

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, at most four test
packages, and cache/temp paths under `/tmp/sw-astra-W3/`.

| Check | Result |
| --- | --- |
| Whole-repository build | Passed. |
| Vet on nodehost, lease, source and orchestrator | Passed. |
| Core native host, protocol, owner, executor, environment and planning selection | Passed in 7.01 seconds. |
| Native host, protocol and stalled-describe race selection | Passed in 8.51 seconds. |
| Retained session, sink and describe selection | Passed in 3.85 seconds. |
| Existing lifecycle, protocol, recovery, environment and credentials hermetic acceptance selection | Passed in 10.59 seconds. Product placement children remain skipped. |
| Windows amd64 nodehost test compilation, including the handle property | Passed. Native execution remains open. |
| Commentcheck against main and sleepcheck against HEAD | Clean. |
| Documentation mirror test, docs-sync drift and whitespace check | Passed. |

The first assembled skip test reproduced release without evaluator invocation.
The first native skip-success run rejected the synthetic child's partial log
frame. That fixture now uses its existing test codec. The Windows property first
failed compilation because x/sys does not expose GetHandleInformation. Its
Windows-only probe now calls the system API directly. Final checks above include
those corrections. No acceptance cell is added or marked complete.

## Remaining work and foreign contracts

The native owner retains debug, approval and plan-admission policies. Executable
job memo materialization belongs to the shared executor after job admission. Production local, trigger and pod callers still use the old host.
Their migration and the remaining old runtime/test deletion stay with W3.

Foreign contract gaps remain the full remote execution snapshot, fenced step
writer and source attempt identity, owner policy-failure and approval-success
edges, typed debug operations, terminal record references, cross-process dynamic
mask propagation and selected-engine host invocation. The C2 authentication and
delegation validators and C4 concurrency resolver are still stubs in this tree.
No foreign contract shape is changed in this continuation.

The initial full nodehost run failed on removed-schema fixtures, including
`sparkwing_meta`, then on a sandbox-denied HTTP listener. It terminated in 3.33
seconds. The suite is not green. Native HTTP/api.sock, remote credentials,
storage backends, native Windows/Darwin and the placement matrix remain open.

No new acceptance cell, exported API, environment variable, customer limit or
compatibility path is added. Shared files are `docs/node-protocol.md`, its
generated mirror, the C3 contract, port inventory, handoffs and this continuation.
No caller outside `internal/nodehost` changes. There are no Git writes, installed
candidates, Sparkwing executable runs, pipeline runs or releases. Worker scratch
is removed after all checks join.

<!-- design:end -->
