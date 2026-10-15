# W3 amendment handoff

<!-- design:begin -->

This is the W3 continuation snapshot after resolving the integration merge and
implementing amendment 1 against contract fakes. W3 is incomplete. Native placement
assembly and the legacy run-host cut remain. This document does not waive them.

[The captured-environment continuation](w3-snapshot-continuation.md) adds private
snapshot storage and verification before source and native SDK startup. It also
records the current verification and two reproduced W6 blockers.

## Merge disposition

All five conflicted paths have resolved contents. The lead must stage them and
finish the merge because the swarm brief forbids worker git writes.

- `internal/nodehost/scheduler.go` retains the integration masking changes and
  W3's removal of terminal-agent-loss resets and retry-auto event behavior.
- `internal/orchestrator/claim_exec.go` stays deleted.
- `internal/orchestrator/pod_node_host_test.go` retains the log-manifest test.
  The removed split-stream tests belong at the session's masking boundary now.
- `internal/source/acquire_contract.go` uses `Options.Base` and the selected
  engine installer contract. Raw Git/install environment fields are removed.
- `tests/acceptance/skeleton_test.go` retains both W3 and W6 checks.

The index still lists unmerged paths until the lead stages those resolutions.
No conflict marker remains in repository source.

## Implemented behavior

Source projection reads only sealed `Source.Location`. Retry execution retains
its URL/local checkout, pipeline directory and SHA. Repo display, legacy location
options and a resolved entrypoint cannot redirect acquisition. Git and Install
both call the shared environment builder with the captured Base. The default
installer calls the selected engine's `InstallSDK`, including checkout root,
relative pipeline directory, SDK kind and the filtered install environment.
Installer output must name the acquired pipeline directory. Partial environment
output and failed installation cannot produce an entry.

Eval cancellation targets the private wire request ID, including evals with no
job. The host waits for the original request to finish before cleanup. The SDK
process remains available after acknowledged cancellation. The old eval-kill
fallback is removed.

Owner reuse probes use `MaterializeOnly` before compute admission. A hit returns
without admission. A miss stays available to the selected executor. Neither a
probe nor an invalid response can deliver an execution award. Holder cancellation
uses the run owner's scoped transition and leaves holder result rights to C1.

Remote reconciliation projects the complete `ExecutionJob`, checks a fresh award
against its persisted claim, and delivers its bearer privately. Expansion replay
uses `ExpandedJob.Current`; a terminal or running job never becomes a fresh pending
row. Empty accepted expansions remain explicit resolutions. Recovery reads carry
the persisted source generation and require an actual recorded failure.

Start allocates the first/next ordinal from persisted zero or the last consumed
ordinal. The returned `Applied.Attempt` reaches SDK startup and body requests.
Application retries retain generation. Each committed start/retry refreshes byte
and record authority through `ExecutorDependencies.BindStorage`. Finalizers receive
the last attempt's binding. Failed refresh suppresses stale storage finalizers
while the current claim can still report failure. Owner policy, expansion and
self-claim adapters receive `RunInput.Storage`. Output publication can use
`NodeInput.Storage` and checks it against its upload authority.

`MaskRegistry` serializes identifier acceptance with acknowledged dynamic masks.
Every host sink uses its shared masker. Collisions include short, multiline and
encoded values and report only field paths. Failed acceptance adds no phantom
identifiers. Owner initial acceptance, resume and expansion use this gate when
configured. The registry is per run and must be shared with its process services,
planner, evaluator, log sinks and returned-error masker.

`RegisterProcessServices` registers the three actual masks/annotations/summary
endpoints through the common policy-router contract. Its resolver receives
`capability.Authorized`. Handlers validate optional step membership, retain
`end_line`, mask text before `limits.Record`, preserve markdown, propagate safe
write errors and acknowledge only success. Request decoding rejects missing,
null, duplicate, malformed and trailing fields under the existing JSON envelope
limit. Native routing/authentication remains separate evidence.

SDK planning preserves intake secret classifications even when describe marks
an argument public. Known intake values are registered before describe starts.
Owner acceptance unions intake classifications into its resolved proposal. C1/W6
still recheck the seal in their transaction.

## Amendment audit

| Rows requiring W3 action | W3 action and remaining integration |
| --- | --- |
| A1-01, A1-02, A1-05 | Registry and actual endpoint handlers implemented and tested. W2 must supply the real router, live resolver, delegated process authority and transactional record writers. Cross-process mask propagation remains unspecified below. |
| A1-03 | W3 continues to use the shared frame/parser error contract. No private HTTP error decoder added. W4's real parser and W9's real HTTP client remain integration points. |
| A1-04 | Both typed service writes and retained log records use `limits.Record` after masking. Raw store refusal remains W1's responsibility. |
| A1-06 | Private target cancellation and joined eval cleanup implemented with native pipe fixtures. |
| A1-07 | The host passes optional declarations unchanged and consumes sealed execution settings. Defaults remain with W6. No host default or declaration-hash rewrite was added. |
| A1-10 | Selected-engine installation call implemented. W8's installer and selected-engine host-process handoff remain open. |
| A1-11, A1-12 | Sealed location and shared environment calls implemented. W6's default `procenv.Git` path still returns unknown-kind in this tree. Tests inject its declared policy seam. |
| A1-13, A1-15 | Materialize-only probes and scoped cancellation transitions implemented. Real store/HTTP transactions remain W1/W2/W9 integration. |
| A1-14 | Failure reader now carries source generation and requires the recorded failure. Native authenticated adapter remains open. |
| A1-16, A1-17 | Reconcile/expansion projections implemented. Full remote owner snapshots still need the contract completion below. |
| A1-18 | Zero-before-start and authoritative start/retry ordinal implemented. Existing keeper/executor acceptance fixtures migrated. |
| A1-19 | Shared hello/environment paths retain their supplied runner and sealed engine. Native award/bootstrap assembly must supply them. No ambient identity fallback was added. |
| A1-21 through A1-25 | New services consume Authorized and the common router contract. Source uses live Access. W1/W2 still own authentication snapshots, route policy, source consent/audit and scoped plan secrets. No alternate authorization implementation was added. |
| A1-36 | Owner adapters receive bound clients. Executor refresh, finalizer binding and bound output publication implemented. Real artifact/memo/depcache layers and remote-cache readers remain open. |
| A1-38, A1-39 | Host library consumes the immutable accepted inputs. W8 still owns unified runner/bootstrap wiring; W3's old launcher executor remains deleted. |
| A1-44 | Intake secret classifications are retained through describe and acceptance. |
| A1-45 through A1-48 | Shared environment builder and sealed settings remain the only new host paths. W6 owns inference/proxy/guard rules. Private snapshot loading is implemented. Intake capture, common guard assembly and native placements remain open. Execution/no-progress budgets retain their existing distinct implementation. |
| A1-50 | W3 exposes shared hosts, storage and source boundaries. W9 still owns CLI/here/publish/cache adapters. Publication does not enter W3's terminal transition. |
| A1-51 | Native protocol fixtures now exercise target cancellation. Existing source, replay and executor fixtures use amended inputs. Remaining legacy runtime fixture retirement depends on the run-host cut. |
| A1-53 | Hermetic cells are active. Native placement, HTTP/api.sock, Windows/Darwin, Postgres and real-repository QA cells remain blocked for the lead. |
| A1-54 | SkipIf remains an approval condition. Executable approval hooks are not restored. The approval transition/read gaps below still prevent full native owner policy assembly. |

## Contract changes needed in other areas

1. C1/W9 must expose a restricted full execution snapshot/read, not just
   `ScheduleSnapshot`. Its jobs omit raw execution state and it omits the run's
   request/spec/owner data needed by `OwnerStore.ReadSnapshot`,
   `NodeTransitionStore.ReadNode` and resumed generated-code execution. The
   amended `ReconcileResponse.Job` fixes reconciliation, not these reads.
2. C1/W9 must expose the terminal output/manifest record references with their
   source attempt/generation. `NodeRow` and `ExecutionJob` still omit them. A
   status or digest alone cannot authorize owner expansion/replay reads.
3. C1 owner policy still lacks pending-to-failed policy errors, typed debug
   activity, approval SkipIf success, and persisted approval opening/resolution
   data. Do not implement these through legacy raw writers. The zero-generation
   source/eval-log identity gap from the previous report also remains.
4. W2/W3/W4 must define how a mask registered on a remote executor reaches the
   owner/eval validator before later expansion. A per-run registry closes the
   same-host race. The declared nodehost-local route does not itself provide
   cross-process propagation or durable replay of dynamic masks.
5. W8/W9 must provide the selected engine's run/node/eval host-process handoff.
   `InstallSDK` is declared, but the current engine contract has no equivalent
   typed host invocation. The library must execute inside that matched engine;
   resolving its identity alone is not proof of the host-process cutover.

No cross-area contract was changed locally. Local additions are the source
installer/environment injection seams, storage-aware owner/finalizer callbacks,
`BindStorage`, planning intake classifications, registry and service registrar.
Each uses the existing frozen concern and has a default or explicit adapter.
No new customer limit, credential kind, daemon or compatibility path was added.

## Remaining W3 work

`RunHosted`, `HostTriggerRun` and dispatched-pod assembly still use the old runtime.
Replace their callers with sealed intake and the real shared hosts, then delete
legacy scheduler/session/layer/routes/types/tests together. Owner policy assembly,
artifact/memo/depcache implementations, process delegation, source release,
state subscriptions, selected-engine hosting and the read gaps above must be
connected. Contract fakes prove the library behavior but do not complete those
native boundaries.

Named obsolete sections removed in this continuation total 34 implementation
lines: the old installer interface (7), local Git filtering loop (8), eval-stop
error type (5), and duplicate record-bounder section (14). Another 82 lines of
obsolete pod split-stream tests were removed. The already-deleted launcher
executor was not resurrected. This is not the full legacy hosting deletion.

Existing parked-test ports were migrated to the amended source and ordinal
contracts. No additional parked-test port is claimed complete. The session
masking tests cover the split-stream boundary that the removed pod tests had
exercised at the wrong layer.

## Shared files

Only `internal/acceptance/host_claimed_node.go`, `host_executor.go`,
`host_expansion.go`, `host_output.go`, `host_replay.go`,
`host_services.go`, `host_source.go`, `tests/acceptance/skeleton_test.go`,
`c3-protocol-sdk.md`, `t1-port-inventory.md` and this handoff are outside W3's production directories.
No production caller outside W3 needed migration. No git writes, Sparkwing binaries or pipelines ran.

## Verification boundary

The reproducible environment is GOWORK=off, GOFLAGS=-buildvcs=false,
GOMAXPROCS=4, with GOCACHE, GOTMPDIR and TMPDIR under `/tmp/sw-astra-W3/`.
Every Go test uses at most `-p 4`.

Whole-repository `go build -p 4 ./...`, scoped vet for nodehost/source/lease/
orchestrator/acceptance, the full source and lease suites, the full acceptance
package and the focused host suite pass. New hermetic cells are
`credentials-secret-identifiers/hermetic-process-services`,
`protocol-sdk-secrets-mask-annotations/hermetic-process-services`, and
`recovery-application-retry-ordinal/hermetic-executor-ordinals`. Existing active
hermetic cells pass too. Native target children remain skipped.

Broad legacy host selections still fail on sandbox TCP binding and the merged
store's missing `sparkwing_meta` table. They are unverified coverage, not passes.
Commentcheck reports one inherited detached HereBearer comment in
`internal/controllerwire/contracts.go`; it reports no W3 comment finding.
Race passes for the focused host suite, full source/lease suites and full
acceptance package. The focused host command uses:

```text
-run '^(TestNodeExecutor|TestClaimedNodeHost|TestOwnerHost|TestOwnerEvaluator|TestOwnerPolicy|TestSDK|TestProtocolSession|TestPlanInitial|TestPlanCache|TestFollowReconciliation|TestRemote|TestStateFollower|TestProcessServices|TestMaskRegistry|TestLogSink|TestLayerOwnership|TestSessionMasksASecretSplitAtTheLineLimit|TestAnAbandonedSinkQueueShowsNoPieceOfAHeldSecret|TestNodeErrorBoundingNeverSplitsASecret)'
```

`git diff --check` passes. Sleepcheck reports two inherited wall-clock assertions
in `internal/limits/deadline_test.go` at lines 58 and 66 and no W3 finding. The
lead retains pipeline/gate execution and staging. `/tmp/sw-astra-W3/` is removed
at worker shutdown.

<!-- design:end -->
