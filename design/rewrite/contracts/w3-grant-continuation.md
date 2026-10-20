# W3 process grant continuation

<!-- design:begin -->

W3 remains incomplete. This continuation resolves the working-file merge
conflict and makes process revocation the host's responsibility before startup.

## Merge

The sole conflicted file was `internal/nodehost/run_admission_test.go`.
Both imports are needed. The resolution keeps W3's lease cancellation assertions
and the incoming `teststore` tenant-scoped concurrency reads. No contract shape
changed during merge resolution. The working file contains no conflict markers.
The Git index remains unmerged because the swarm brief forbids Git writes.
The lead must stage the resolution and finish the merge.

## Implemented

The invariant is that an attempt or owner evaluation returns only after its
delegated bearer has been revoked, including rejection before process startup.

`ExecutorDependencies.Bind` and `EvaluationDependencies.Bind` now return
`ProcessGrant` with a binding and an idempotent revoker. This replaces the bare
binding result. Both hosts retain revocation before validating the returned
identity or checking the factory error. They revoke before joining the process
and running cleanup. A revocation failure becomes a failed result or a policy
error. Partial startup still joins and cleans the returned process.

The host owns this responsibility because binding validation can reject a newly
issued credential before `Start` receives it. Making only the process adapter
revoke on failed spawn leaves that earlier path uncovered. `ProtocolSession`
retains its own Close callback for direct session users. Both callbacks must be
idempotent when they refer to the same delegation.

The new type and Bind signature change are local W3 contract changes. No other
area's interface changes. No setting, limit, environment variable, daemon or
compatibility API is added. Removing the change requires restoring the bare Bind
signatures and moving all pre-start revocation into a single startup operation.

## Acceptance and ports

`credentials-delegation-subset/hermetic-process-authority` now runs real memory
delegations through the common executor. Its five cases cover successful work,
three retry processes, a changed job binding, factory failure after delegation
and failed startup. Every issued token is masked and absent from authentication
after execution returns. C2 validation, parent persistence, SDK and claim store
remain fakes in this component.

`TestClaimedNodeHostAcquiresSourceAndJoinsNativeSDK` now uses the real memory
registry through direct Git acquisition and a real pipe child. Successful work
and lease loss both revoke before cleanup and leave no process bearer live.
The installer, C2 validation, parent persistence and protocol peer remain fakes.

Unit cases also cover nil and partially started processes, rejected hello,
revocation failure, and cleanup ordering in executor and owner paths.
`FuzzExecutorBindingRevocation` and `FuzzOwnerBindingRevocation` generate changed
run/job/bearer values and factory failures. Invalid grants start no process.
Every returned grant with a revoker loses authority before the host returns.

No additional parked-test port is claimed complete. No legacy implementation or
test is deleted. The legacy deletion count for this continuation is **0 lines**.
The bare Bind return surface is removed and every caller in this checkout is
migrated without an alias.

## Assignment audit

| Assignment | Current W3 disposition |
| --- | --- |
| A1-01/02/05/21-25 and service-router handoff | Existing process services, mask registry and memory authority remain. The host now owns pre-start revocation. Production C2 router/authentication and remote parent snapshots remain open. |
| A1-03/04/06/07/10-18 | Existing shared decoding, limits, cancellation, concurrency, source and reconciliation remain. Remote failure/reuse/step/snapshot transport still needs the fields below. |
| A1-19/38/39 and identity handoff | The native claim-host component now uses real delegations with the existing sealed-engine and granted-runner path. Runner, worker, pod and CLI assembly still need the complete host cut. |
| A1-36/44-48 and handoffs 7/9 | Existing storage, captured environment, alias, secret classification, guards and startup/cleanup components remain. Owner policy composition and native callers remain open. |
| A1-50/51 and retained-test handoff | Bind callers are migrated. The old Session, Server, scheduler, layers and remaining runtime fixtures still need deletion with their native callers. |
| A1-53/54 and handoffs 4/5/6 | Existing strict expansion, eval failure, settlement and approval-condition components remain. Native placement and approval-policy integration remain open. |
| Attempt-identity handoff from W1 | Confirmed unresolved. C1 NodeTransition and C3 JobTransitionRequest still omit the source attempt ordinal. W3 cannot add another area's contract field. |

## Contract changes needed in other areas

- W1/W9 must carry the source attempt ordinal through job transitions and replay
  identity. W3 must then populate it for start, retry, report and setup refusal.
- W9/W2 must provide typed fenced step reports, persisted `JobFailure` on
  `JobResult`, terminal result/state sequence on `ReuseSource`, and a complete
  restricted execution snapshot. The current scheduling view omits required
  request/spec, owner, generated state and result-record facts.
- W6/W9 must seal StartAt/StopAt/Only. C1 must provide owner policy-error,
  debug-activity and approval SkipIf success edges plus approval timing and
  resolution identity. These gaps still prevent complete owner policy assembly.
- W2 must implement its authentication and production policy router and provide
  fresh remote parent snapshots. Process services already use those declared
  interfaces. W8 must supply its selected-engine installation and hosting paths.
- W7/C1 must complete the memo publication/source and remote import shapes
  listed in the previous audit. Native storage clients must retain live unit
  authority through retries and generation changes.

The W4 default protocol parser and shared validator are implemented in this
checkout. Earlier notes that list these as panic stubs no longer describe this
tree. W6's `procenv.Git` still lacks a Build branch. The C2 router and
authentication and W8's engine resolver/installer still contain panic stubs.

## Scope

The only shared Go callers migrated are `internal/acceptance/host_executor.go`,
`host_output.go` and `host_steps.go`. `host_process_authority.go` gains the new
execution cases. No production caller outside W3 changes. Shared documentation
changes are this note, `c3-protocol-sdk.md`, `handoffs.md` and
`t1-port-inventory.md`.

`RunHosted`, `HostTriggerRun` and `pod_node_host` still use the legacy native
assembly. Native HTTP/api.sock, remote execution, real storage, Darwin, Windows
and real-repository QA remain open. The component checks do not satisfy those
native cells. The lead retains independent review, pipelines, staging and merge
completion. No Sparkwing binary, installer, Git write or release runs here.

## Verification

Go commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, `-p 4`
and cache/temp paths under `/tmp/sw-astra-W3/`. Scratch is deleted at shutdown.

| Check | Result |
| --- | --- |
| Whole-repository `go build -p 4 ./...` | Passed after the implementation change. |
| `go vet -p 4` for nodehost, lease, source, orchestrator and both acceptance packages | Passed. |
| Current host suite, including new grant tests and both fuzz seeds | Passed. |
| Full source, lease and internal acceptance suites | Passed. |
| Credentials, lifecycle, recovery, storage and placement acceptance selection | Passed. Native target skips remain. |
| Race checks for NodeExecutor, OwnerPolicy, ProcessAuthority, the native claim-host test and both new fuzz seed corpora | Passed. |
| Credential component under race, selected with `^TestCredentials$/^credentials-delegation-subset$/^hermetic-process-authority$` | Passed. |
| `FuzzExecutorBindingRevocation`, `-run '^$' -fuzztime=2000x -parallel 2` | Passed, 2,000 executions. |
| `FuzzOwnerBindingRevocation`, same options | Passed, 2,017 executions. |
| `go run ./internal/commentcheck -base main .` | Passed, 1,159 files. |
| `go run ./internal/sleepcheck -base HEAD .` | Passed. |
| `git diff --check` and working-file conflict-marker scan | Passed. |
| Full `go test -p 4 ./tests/acceptance` | Failed because the TypeScript native protocol fixture cannot find `sdk/typescript/node_modules/typescript/bin/tsc`. |

The current host selection is:

```text
-run '^(TestNodeExecutor|TestClaimedNodeHost|TestOwnerHost|TestOwnerEvaluator|TestOwnerEvaluation|TestOwnerPolicy|TestSDK|TestProtocolSession|TestPlanInitial|TestFollowReconciliation|TestRemote|TestStateFollower|TestProcess|TestMaskRegistry|TestLogSink|TestSessionMasksASecretSplitAtTheLineLimit|TestAnAbandonedSinkQueueShowsNoPieceOfAHeldSecret|TestNodeErrorBoundingNeverSplitsASecret|Fuzz)'
```

The full acceptance failure is retained. No TypeScript code or dependency install
changes here. Broad legacy nodehost/orchestrator suites and native HTTP/API,
other platforms and real storage remain outside this verification.
Every tracked test command has ended, and the native process test asserts joined
children. No failing fuzz corpus was written. The Git configuration retains the
default identity and has no `core.worktree` override. Scratch is removed before
the final report.

<!-- design:end -->
