# W3 execution continuation

<!-- design:begin -->

W3 remains incomplete. This session resolves the one conflicted file's working
contents and continues the existing implementation. Earlier completed work is
recorded in the [settlement continuation](w3-settlement-continuation.md).

## Merge

`internal/orchestrator/pod_node_host_test.go` keeps W3's version. The incoming
version restores launch helpers and tests for the deleted launcher entry point.
It contains 558 lines, compared with the resolved file's 46 lines. This preserves
the prior removal of 512 legacy test lines relative to the incoming version.
New legacy production deletion is zero. No conflict marker remains in the file.
The index remains unmerged because the brief forbids Git writes. The lead must
stage the resolution and finish the merge.

## Implemented

The concurrency invariant is that distinct validated scope, host identity and
group tuples have distinct keys, and labels recover the complete host identity.
`ConcurrencyKey` uses a byte-length prefix for scoped identities.
`ParseConcurrencyKey` is the single decoder used by scope labels and child
admission. It preserves separators, Unicode and arbitrary bytes. Global group
keys retain their existing unambiguous form. The restored regression tests
failed before this change and now pass. Round-trip and collision fuzz seeds pass.

The execution identity invariant is that the selected engine must equal the
accepted release and the runner must be supplied explicitly from its award.
`NewNodeInput` derives the claim fence, ordinal, hash and settings from an
authenticated persisted row. It refuses incomplete runner identity, changed
engine identity, terminal rows and inconsistent accepted settings. Both the
claimed host and common executor use this construction check before startup.
Its constructor-to-hello test supplies forged ambient values and observes the
explicit selected version and runner. Engine-identity fuzz seeds refuse changes
to the sealed version, manifest or SDK pin. The helper does not authenticate a
row or perform engine verification. Those remain its callers' responsibilities.

The eval invariant is that a closure failure never becomes a successful value.
One `evalValue` decoder checks the canonical error before returning JSON or
checking its declared kind. The common evaluator and all legacy predicate,
generator, memo and hook consumers use it. A real Go SDK process reproduced both
remaining failures before the fix. A throwing generator became an empty
expansion, and a failed memo key became NoMemo and ran the body. Both now fail.
The same test retains the throwing predicate case. Error-value fuzz seeds cover
every host closure kind. Raw strict expansion validation remains the separate
native scheduler proof recorded earlier.

Five new acceptance cases run `ClaimedNodeHost`, `NewNodeExecutor` and
`FollowReconciliation` against real SQLite transitions. They cover success,
application retries, a refused before hook, refused output and a refused result
transaction. The last uses a SQLite trigger, so terminal state and its event
must both stay absent after rollback. Committed results retain one generation,
advance ordinals and clear claims. Adopting each terminal result twice adds no
award or event. Processes and source/engine/delegation adapters are fakes.

## Amendment and handoff audit

| Assignment | Current disposition |
| --- | --- |
| A1-01/02/05/21-25 and handoff 2 | The eleven service handlers and mask gate remain implemented. W2 PolicyRouter, authentication and the shared HTTP writer are still panic stubs. The native router and delegated process authority remain open. |
| A1-03/04/06/07 | Existing canonical error, bounded record, cancellation and optional concurrency consumers remain. |
| A1-10/11/12 | Existing exact-source acquisition and explicit Git environments remain. W8 InstallSDK remains an integration point. |
| A1-13-18 | Existing owner/reconciliation/cancellation/ordinal rules remain. Real SQLite start, retry, terminal, rollback and repeated adoption now have host-level evidence. Restricted remote snapshots and result/reuse transport remain open. |
| A1-19 and handoff 3 | NewNodeInput checks the shared claimed/executor inputs and has hello evidence. Worker, runner award and pod assembly still need the selected-engine and granted-runner adapters. The old Session constructors remain. |
| A1-36 | Verified byte/record layers remain. Native storage authority and owner memo composition remain open. |
| A1-38/39 | Shared claimed hosting and the lease keeper remain. Native runner/bootstrap wiring and the old runtime cut remain open. |
| A1-44-48 and handoffs 7/9 | Captured environments, argument classification, guards, aliases and startup/cleanup projections remain. Native caller and CLI selection wiring remain open. |
| A1-50/51 | The incoming launcher tests stay deleted. Legacy scheduler, Session, Server, layers and runtime fixtures still need retirement. |
| A1-53/54 | Component acceptance passes. Full native placements and approval read/policy integration remain open. |
| Handoff 1 | Five final-store event cases are ported. The old dispatcher-event test's remaining diagnostic and owner-policy assertions still need their final port. |
| Handoff 4 | Failure-first decoding now covers every host eval consumer. Real Go process and fuzz seed regressions pass. |
| Handoffs 5/6 | Earlier raw-validation, settlement and native Go process evidence remains. No new topology or settlement implementation change was needed. |
| Concurrency codec handoff | Fixed with one tuple codec and decoder. Separator, identity-label and generated-input tests pass. |

## Contracts and real integration points

W3 adds only the local `NewNodeInput` and `ParseConcurrencyKey` helper declarations
to the C3 export inventory. No other area's contract shape changes.

The earlier W9 gaps remain. The restricted execution snapshot is not declared,
`JobResult` omits persisted `JobFailure`, and `ReuseSource` omits its terminal
result/state sequence. C2 must implement the production policy router and live
process delegation. W8 must connect selected SDK installation and native hosting.
C1 owner policy/approval edges and W7/C1 native storage assembly remain as listed
in the earlier continuation. W4 and the lead still need to reconcile the
expansion concurrency mismatch requirement with W4's accepted membership tests.

The C1 policy gaps are a fenced pending-to-failed edge for an errored admission
condition, a pending-to-success edge for an approval bypass, and persisted
approval opening time and resolution identity. A skipped approval would suppress
its dependents, so that outcome cannot stand in for a successful bypass.

The remaining acceptance cells need real HTTP/api.sock, placement, controller,
storage and platform drivers. No native cell is unskipped on fake evidence.
The old runtime still compiles alongside the common implementation. Retiring it
remains W3 work after those callers migrate.

## Shared files and assumptions

Outside W3, changes are limited to `internal/acceptance/host_claimed_node.go`,
`host_executor.go`, `host_events.go`, `store_recovery.go`,
`tests/acceptance/skeleton_test.go` and the four contract documents changed in
this continuation. The recovery fixture gains an explicit started/unstarted
parameter. Its two existing callers still select started. The two execution
fixtures now use NewNodeInput and supply their granted runner.

The host-related orchestrator caller change is `child_admission.go`, which now
uses the shared key decoder. No other external production caller changed.

Mechanisms added are the shared tuple decoder, execution-input constructor and
failure-first eval decoder required by the handoffs. Separator replacement
cannot make a tuple codec injective. Keeping independent constructors and eval
checks would preserve the duplicated decisions those handoffs require cutting.
These helpers add no setting, service, environment variable or customer limit.
Their implementation and consumers can be reverted together.

## Verification

All Go commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4` and
`-p 4`. `GOCACHE`, `GOTMPDIR` and `TMPDIR` are under `/tmp/sw-astra-W3`.
The directory is removed before the final report.

- Whole-repository build passes.
- Scoped vet passes for nodehost, orchestrator, source, lease and both acceptance
  packages.
- Complete source, lease and both acceptance suites pass. The lifecycle cell
  `lifecycle-transition-replay-and-fencing/hermetic-store-executor-events` passes
  all five cases described above.
- Current-host tests pass, including native SDK step execution, real SDK eval
  failures, owner/evaluator, claim host, process services, cleanup, state follower,
  planning/cache, constructor and fuzz seeds.
- The restored orchestrator scope-label and collision seed tests pass.
- Race checks pass for node executor, owner/evaluator, native eval regressions,
  constructors, fuzz seeds and lifecycle acceptance. A final constructor race
  check also uses the real TOOLCHAIN and runner environment variable names.
- Five-second, two-worker fuzz runs pass for ConcurrencyKeyRoundTrip (4,241
  executions), ConcurrencyKeyPreservesIdentity (5,147),
  ExecutionInputKeepsSealedEngine (1,200) and EvalFailureNeverExposesValue (4,120).
- Commentcheck against main is clean across 1,012 files. Git diff whitespace
  checks pass.

The broad legacy nodehost/orchestrator suites are not claimed green. A broader
TestProtocol selection reached TestProtocolMismatchAnswers426WithSupportedVersion
and failed because its old server requests a sandbox-denied TCP listener.
The previous continuation records the separate old-schema failures. Native
placement pipelines, final route authentication and external platform runs stay
with their declared drivers and the lead.

The current-host selection is reproducible with:

```text
test -p 4 ./internal/nodehost -run '^(TestNodeExecutor|TestNewNodeInput|TestLegacyEvalFailures|TestParseConcurrencyKey|TestPlanGroupFixture|TestOwner|TestProtocolSession|TestNativeSDK|TestProcessServices|TestProcessCleanups|TestClaimedNodeHost|TestStateFollower|TestLayerOwnership|TestPlanInitial|TestValidatePlanCacheHit|Fuzz)' -count=1 -timeout 3m
test -race -p 4 ./internal/nodehost ./tests/acceptance -run '^(TestNodeExecutor|TestNewNodeInput|TestLegacyEvalFailures|TestOwner|TestLifecycle|Fuzz)' -count=1 -timeout 3m
```

No Sparkwing binary, pipeline, installer, Git write, deployment or release ran.
The local Git configuration has no user.name or core.worktree override.

<!-- design:end -->
