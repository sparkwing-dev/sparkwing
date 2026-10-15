# W3 router and native assembly continuation

<!-- design:begin -->

W3 remains incomplete. The registered host service router uses protocol v2, and
the common owner and claimed executor have a native assembly. Production placement
callers and the remaining legacy runtime still need their hard cut.

## Implemented

`Server` registers the eleven shared `ProcessServices` handlers. Hello supplies
negotiation. The version-header/426 middleware and `/node/v1/nodes` registration
are removed. Output paths use `jobs`, selectors use `job`, and errors use the
canonical envelope and reasons. Mask, annotation and summary writes acknowledge
their shared implementation. Legacy record adapters reuse `LogSink` persistence
and timestamps. Child requests carry the SDK's output-job selector, and failed
child responses carry the canonical inner error object.

Invariant: serialized host errors expose no registered secret in diagnostic
strings, detail keys or detail values. `maskHostError` normalizes JSON details
before recursively masking them. Safe reasons survive. Cause identity remains
available to `errors.Is`. Registered resolver-refusal codes also survive when a
delegation ends between authentication and service resolution. Unresolved source
diagnostics stay private. The registry-wide refusal test and generated error
payloads cover these boundaries.

`NewNativeHost` builds one executor for `OwnerHost` and `ClaimedNodeHost`, plus the
shared planner and owner evaluator. It installs delegated process services on the
supplied policy router. Source engine resolution, validation, masking, captured
environment loading and native startup come from shared inputs. Nested configs
supply policy and storage adapters. C2 authentication and the listener transport
remain explicit placement dependencies.

Invariant: each native process owns its service binding and cleanup ledger.
`NativeProcesses.Delegate` creates fresh cleanup registrations in the existing
authority registry. Startup resolves that exact bearer/run/job binding and uses
its masker, attributed logs and cleanup ledger. Revocation precedes process join
and cleanup. Cleanup log delivery failures remain errors. Authorized child waits
pause that attempt's idle budget and resume it on return.

Owner layer and expansion callbacks now receive `EvaluationInput`, including the
acquired entry, granted runner and persisted job identity. The default native
expansion callback uses `OwnerEvaluator`. Generated reconstruction can derive
ancestor replay from accepted expansion membership and source generations. It
checks the complete lineage before reading authorized recorded ancestor outputs.
Both executor and owner reconstruction use that path. Existing explicit replay
still undergoes the common member, topology and spec-hash checks.

The prior eval-failure decoder, raw-byte expansion validator, environment
projection, tuple codec, alias resolution and settled-step delivery remain in
use. This continuation does not replace those implementations.

## Ports and deletion

The route and cleanup tests retain declaration, dependency, reference, child,
revocation, masking and cleanup assertions using v2 paths and errors. The three
reported daemon lease tests use the resolved W3 admission seams, preserving host
lease ownership, resources, priority/class precedence and semaphore-only claims.
They no longer depend on the removed legacy store schema or W6's resolver stub.
The existing resolver-stub test remains visible.

The production `routes*.go` and `cleanups.go` diff removes **530 lines** and adds
**385**, a net reduction of **145 lines**. Those removals cover the old version
gate, HTTP handlers, error policy and cleanup registration handlers. The old
`Config`, `Execute`, `Session`, scheduler, executor layers and orchestrator
hosting entry points remain. No new acceptance cell is added or marked complete.

## Open work and foreign contracts

**DESIGN: legacy runtime mask namespace.** Invariant: registering a dynamic mask
cannot make an accepted job, step or group identifier secret. Legacy `Attempts`
has only a masker, and `Run.Doc` excludes generated jobs. Per-job registries cannot
protect sibling or generated identifiers. The native assembly has the shared
registry. Complete the native caller cut and delete the old scheduler rather
than adding another partial namespace patch.

The full nodehost suite still fails at
`TestAdmissionDaemonGroupBoundsParallelNodes` because its old scheduler/store
fixture queries removed `sparkwing_meta`. The failure is bounded. Legacy store
admission and runtime fixtures need migration or retirement with their callers.
`settings.ResolveConcurrency` also remains a foreign panic stub.

Required foreign completions remain:

- W1/W9: source-attempt fencing on job transitions and replay identity.
- W9/W2: typed fenced step reports, persisted failure and terminal reuse facts,
  and execution snapshots carrying seals, plan, owner, job settings and fences.
  `Controller.ReadSnapshot` still returns only a scheduling projection.
- W2/W8: concrete policy authentication/router, fresh remote parent snapshots,
  listener ownership and runner/launcher/local placement callers.
- W6/W9/C1: sealed step windows and complete owner approval/debug policy edges.
- W7/C1: memo publication, source and remote-import shapes already listed in the
  memo continuation.
- W4: default child arguments serialize as `args:null`. The actual registered
  router refuses that request. Normalize absent arguments to an object in the SDK
  transport. W3 keeps the strict service decoder.

Native placement matrices still need real HTTP/api.sock, storage backends,
controller credentials, runner/launcher/local assembly, Darwin, Windows and
real-repository QA. The common host uses the declared foreign interfaces and
explicit test fakes. It does not fabricate production authority or snapshots.

## Assignment audit

| Assignments | Disposition |
| --- | --- |
| Service-router findings 2/9, A1-01/02/05/21-25 | Actual service registration and v2 wire cut complete. C2 production authorization and remote parent transport remain external. Legacy mask namespace is the DESIGN above. |
| Eval/expansion/settlement findings 4/5/6, A1-03/04/06/07/10-18 | Existing common policies retained. Native process cleanup and child-wait activity connected. Persisted ancestor replay added. Attempt/remote contract gaps remain listed above. |
| Identity/alias findings 3/7, A1-19/38/39/50 | Shared native assembly and acquired/granted owner evaluation inputs added. Full worker, pod and CLI caller migration remains. |
| A1-36/44-48 | Existing storage, environment, secret-classification, settings and guard boundaries retained in native assembly. |
| Retained-test finding 1, A1-51 | Route/cleanup and three lease-policy tests ported. Old schema/runtime fixtures remain visible and need their hard cut. |
| A1-53/54 | Existing owner/generator acceptance components pass. Native matrix and complete owner policy composition remain open. |

## Shared files and contract changes

W3-owned local contract completions are `NativeHostConfig`, `NativeHostStore`,
`NativeProcesses`, `ExecutorDependencies.ReadOutput`, owner layer/expansion
`EvaluationInput`, the child output-job selector and canonical child errors.
No foreign contract is changed.

Caller migrations outside nodehost are
`internal/acceptance/host_expansion.go`, `host_services.go`, `host_owner.go` and
`host_failure.go`. Source protocol prose and its generated docs mirror are
updated together. The C3 contract, handoffs and port inventory point here.
No orchestrator, CLI, worker or pod production caller is claimed migrated.

## Verification

Checks use GOWORK=off, GOFLAGS=-buildvcs=false and at most four Go test packages.
No Git write, Sparkwing binary, release or pipeline runs in this session.

| Check | Result |
| --- | --- |
| Whole-repository `go build -p 4 ./...` | Passed. |
| Scoped vet on nodehost, lease, source, orchestrator, internal acceptance, acceptance tests and CLI | Passed. |
| Common host, native process, owner/evaluator, router and three admission-policy unit selection | Passed. |
| Existing W3 credentials, lifecycle, recovery, environment and protocol acceptance selection | Passed with TypeScript dependencies available. Native matrix skips remain. |
| Native/process/router/masking and persisted-ancestor execution race selection | Passed. |
| Canonical error masking fuzz | Passed, 1,523 executions. |
| Native service-binding isolation fuzz | Passed, 1,500 executions. |
| Actual registered-router secret-binding fuzz | Passed, 3,000 executions. |
| Persisted replay-lineage fuzz | Passed, 1,149 executions. |
| `commentcheck -base main .`, `sleepcheck -base HEAD .`, docs mirror drift and `git diff --check` | Clean. |

The focused host selector includes `NodeExecutor`, `Owner`, `Claimed`, `Native`,
`Process`, `ProductionRouter`, `HostCanonical`, `SDKPlanner`, `StateFollower`,
`NewNodeInput`, `ConcurrencyKey` and the three reported admission test names.
The acceptance selection covers the existing process-service, native owner
failure, owner seal, expansion, replay, step/event fencing, executor retry,
recovery keeper and captured/startup/checkout-environment components. It adds no
product-placement claim.

The complete nodehost suite still fails at the legacy schema fixture described
above. TCP route tests and the actual-SDK HTTP integration test compile, but this
sandbox refuses listening sockets. Real transports and placement checks stay with
the lead. Independent review's resolver-code finding is fixed and rechecked.

The native assembly introduces composition of existing contracts, not another
runtime interface or process registry. Its only new storage adapter is the
executor's authorized ancestor-output reader. Cleanup activity stays in the
existing delegated service record. No new environment variable, customer limit,
compatibility route or production credential source is added.

One helper created a sibling temporary cache outside the requested scratch root.
It stopped that command, removed its directory and reran under the allowed root.
All owned checks and child processes are joined before scratch removal.

<!-- design:end -->
