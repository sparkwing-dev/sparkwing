# W3 native grant continuation

<!-- design:begin -->

W3 remains incomplete. This continuation connects validated grants to native
startup, consumes C1's cancellation reason and removes one legacy lease loop.

## Implemented

Invariant: each native attempt or evaluation uses its validated grant's revoker,
including rejected startup. It cannot use a revoker left in shared configuration.

The executor and owner evaluator pass the validated revoker through a private
input field. Native `Start` and `StartEvaluation` select it before environment
construction or spawning. The host still revokes before process join and cleanup.
Direct session users still provide their own callback. There is no new exported
factory, configuration option, credential transport or compatibility alias.

The existing direct-Git/native claim-host test failed without its configuration
mutation, then passed through this handoff. Native owner evaluation uses the same
path. `FuzzNativeStartupGrantIsolation` varies run/job/bearer identity and factory
failure. Rejected grants reach no environment builder. No stale callback runs.

C1's `LeaseBeat.CancelReason` now passes through both host adapters into the
keeper's `CancelError`. An absent reason keeps the existing `cancel_requested`
value. Owner and claimed-job acceptance cases preserve the reason before source
acquisition. The cancellation/watch component and `FuzzKeeperCancellationReason`
cover reason preservation through the keeper. The added Beat field is a local W3
contract completion. C1 and controller wire already declare it.

## Ports and deletions

`lifecycle-approval-decisions-and-owner-rights/hermetic-native-owner-failures`
has four real Go SDK cases: throwing generator, throwing skip predicate, failed
memo key, and the same memo failure under `NoMemo`. They use the shared validator,
hash, environment builder, evaluator and native pipes. Failure exposes no topology,
skip decision or memo success. No job body runs. Revocation precedes cleanup.
Persistence, engine verification and credential issuance are explicit fakes.

The eleven native step-policy cases now use `ProtocolSessionConfig.Start` with
the sealed local environment and validated grant. They previously called the
raw pipe startup function and supplied an unrelated no-op revoker.

Deleted legacy blocks total **338 lines**:

- The old `runHeartbeat` loop/timing knobs and `HeartbeatTrigger` API: 80 lines.
- `internal/orchestrator/heartbeat_test.go`: 163 lines. The existing common
  keeper acceptance cases cover lost authority, silence and requested cancel.
- `internal/nodehost/eval_failure_unix_test.go`: 95 lines. The new native owner
  acceptance cases replace its Session/Server-based generator/predicate/memo test.

The CLI worker caller and the retained legacy host fixture use `lease.Keeper`.
Their old HTTP renewal shape still needs the W8/W9 lifecycle cut. No old API alias
is retained. The generic SDK fixture build/plan helpers are shared by both native
acceptance components.

## Assignment audit

| Assignments | Disposition |
| --- | --- |
| A1-01/02/05/21-25, service-router handoff | Existing service/mask/authority components remain. Native grant handoff and persisted cancellation reasons now reach the common host. Production C2 router/authentication and remote parent snapshots remain open. |
| A1-03/04/06/07/10-18 | Existing decoding, bounds, cancellation, concurrency, source and reconciliation remain. Native SDK owner failures now exercise the common evaluator. Attempt fences and full remote transport still need the foreign fields below. |
| A1-19/38/39, identity handoff | Common native starts use validated grants and sealed environment/engine/runner inputs. The separate host-trigger lease loop is removed. Runner, worker, pod and CLI placement assembly remains incomplete. |
| A1-36/44-48, findings 7/9 | Existing storage, captured environment, aliases, secret classification, guards and cleanup remain. Native step acceptance now uses the shared environment builder. Complete owner policy composition and native callers remain open. |
| A1-50/51, retained-test handoff | Two legacy test files and one legacy lease loop are removed. The old Session, Server, scheduler, executor layers and runtime fixtures still need their callers' hard cut. |
| A1-53/54, findings 4/5/6 | Shared eval/expansion/settlement components remain. Real SDK owner-failure coverage replaces the legacy regression test. Approval admission and full native placement remain open. |

## Contract changes needed in other areas

- W1/W9: source attempt ordinal on NodeTransition/JobTransitionRequest, including
  retry replay identity and unstarted-report semantics.
- W9/W2: typed fenced step reports, persisted JobFailure on JobResult, complete
  terminal result/state sequence on ReuseSource, and restricted execution snapshots
  with request/spec, owner, generated-state and result-record facts.
- W6/W9 and C1: sealed StartAt/StopAt/Only, owner policy-error/debug-activity and
  approval SkipIf-success edges, plus approval timing/resolution identity.
- W2/W8: production authentication/router and remote parent snapshots, verified
  engine installation and complete native host placement wiring.
- W7/C1: memo publication/source and remote import shapes from the memo audit.

No foreign contract is changed here. The real-integration points still include
HTTP/api.sock, storage signing/backends, complete local/runner/launcher placement,
Darwin, Windows and real-repository QA. Existing component checks cannot close
those native matrix cells. No new architecture question is raised.

## Shared files

The production caller migration outside W3 is `cmd/sparkwing/worker.go`.
`internal/orchestrator/worker.go` changes only the host-trigger lease loop.
Shared acceptance changes are `host_steps.go`, `host_native_owner.go`,
`host_claimed_node.go`, `host_owner.go`, `host_lease.go` and
`tests/acceptance/skeleton_test.go`. Contract docs changed are this note,
`c3-protocol-sdk.md`, `handoffs.md` and `t1-port-inventory.md`.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4 and
GOCACHE/GOTMPDIR/TMPDIR below `/tmp/sw-astra-W3/`. The directory is removed
after the final checks. The lead owns Git writes, independent review and pipelines.

The native grant reproducer failed with `bound SDK process requires a route
revocation callback` before the implementation, then passed. The two cancellation
acceptance cases failed with the generic reason before their implementation,
then passed.

| Check | Result |
| --- | --- |
| Whole-repository `go build -p 4 ./...` | Passed. |
| `go vet -p 4` on nodehost, lease, source, orchestrator, internal acceptance, acceptance tests and CLI | Passed. |
| Focused common-host selection from the grant continuation, plus TestNativeSDK and fuzz seeds | Passed. |
| Full lease and internal acceptance suites | Passed. |
| Credentials, lifecycle, recovery, storage, placement and selected W3 protocol acceptance | Passed. Native matrix skips remain. |
| Node/owner/native-start/keeper race selection, including both new fuzz seed corpora | Passed. |
| Native owner-failure acceptance component under race | Passed. |
| `FuzzNativeStartupGrantIsolation`, 2000x with two workers | Passed, 2,008 executions. |
| `FuzzKeeperCancellationReason`, 2000x with two workers | Passed, 2,000 executions. |
| `go run ./internal/commentcheck -base main .` | Clean, 1,160 files. |
| `go run ./internal/sleepcheck -base HEAD .` and `git diff --check` | Clean. |

The broad acceptance run still fails because TypeScript's native fixture cannot
find `sdk/typescript/node_modules/typescript/bin/tsc`. The full nodehost suite
with fail-fast still fails `TestAdmissionLeasesHostAroundEachNode` in the legacy
host path. Those failures remain visible. The focused common-host suite and
selected W3 acceptance components are separate evidence.

The tracked check processes have ended. The Git identity remains unchanged and
has no core.worktree override. No Sparkwing binary, Git write or release ran.

<!-- design:end -->
