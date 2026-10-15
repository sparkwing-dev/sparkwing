# W8 here-host cut and shared award inputs

Continuation from `f651e294e`. The CLI here caller now uses the existing retained
credential, named-claim and shared-host handoff. Its separate checkout builder,
heartbeat loop, interrupt handler and log-follow worker are deleted. The caller
passes its selected output format to the shared attachment. Initial intake still
uses W9's `createRemoteTrigger`. Migrating that intake and its receipt remains W9
work. No old hosting route or command alias was added.

`runners.RunInputForAward` now supplies remote, embedded and CLI here inputs.
It copies the request body, accepted plan and granted runner before binding.
The here path previously supplied the entire sealed-request envelope where
`OwnerHost` expects the request body. It also kept the binder's runner identity.
The focused test failed on those inputs before this change and passes now.
Here keeps only the binding's owner authority and storage services.

Retries retain their original request seal. Their input and accepted execution
name the current awarded run. Persisted snapshots and authenticated claim fences
still have to match that run. The common constructor retains strict plan decoding
and rejects unknown fields, malformed JSON and additional JSON documents.

Remote and embedded hosting share award capture, deadline bounds, partial-service
cleanup and native dispatch in `hostAward`. Public remote service binding receives
the original award. Persistence checks use a separate captured award, so callbacks
cannot replace the sealed inputs or deadlines through shared pointers.

The invariant is that host binding cannot replace captured request, plan or granted
runner inputs, and a retry cannot replace its current execution fence with its
source run ID. The new generated property checks both unit kinds, arbitrary source
seal IDs and mutations after capture. Existing persisted-award properties remain.

## Deletions and evidence

Production Go adds 67 lines and deletes 284 against starting HEAD.
**Net production delta this session: -217 lines.** The CLI caller migration is
-203. W8-owned production packages are -14. Tests and Markdown are excluded.
`cmd/sparkwing/worker.go` removes 106 production lines. Its old here claim,
checkout preparation, separate keeper and completion calls have no remaining
production caller.

The earlier cluster worker, warm-pool dispatcher, run-host Kubernetes dispatcher
and old W8 pod builders remain deleted. Those earlier deletions are not counted
again. W3's old trigger host, orphan RunNodeOnce and run_node_options.go remain
outside W8 ownership.

The deleted worker tests have these replacements:

| Deleted test | Surviving obligation and evidence |
| --- | --- |
| TestWorkerRenewsTheClaimWhileItBuilds | TestNativeStoreOwnerW8/remote_renewal blocks acquisition until the shared native keeper renews again. It uses real C2/C1/store behavior and a source fixture. A native dependency build is still unproved. |
| TestWorkerShutdownMidBuildLeavesTheTriggerToItsLease | TestNativeStoreOwnerW8/remote_cancel cancels during acquisition, joins services and verifies the current owner fence remains unfinished. Shared OwnerHost cancellation and authority-loss tests pass too. |

The native-store component now also mutates the original accepted spec and plan
during binding. Execution retains its captured seal. The here-resume component
supplies a forged binding runner and mutates the award's labels. Execution retains
the server runner and receives the request body expected by the shared host.

New component subcases pass. **No native placement acceptance cell is newly
closed.** Native Cloud, actual SDK-process, network and platform evidence remain
open. Fakes do not establish that evidence.

## Decisions, amendments and integration

- 07:40 item 5 remains implemented as LocalController.HTTP() LocalHTTP. W2's
  production authorized API/dashboard service binding remains open.
- 07:40 item 6 remains implemented. Launcher grammar accepts --docker-image and
  refuses --deadline. Launcher and cluster tests pass.
- 07:40 item 10 remains implemented on W8's wire side. Observations carry unit
  kind/ID, holder, live claim generation, namespace/name/Job UID and node UID.
  Eval source generation stays separate. **A1-38 is not closed.**
  LauncherSyncReader still has no production implementation. W1/W2 must supply
  transactional authority, award and Job-instance correlation.
- 09:40 item 4 remains done. KeepAliveReasons, LocalPlatformPolicies,
  CloudUnitPolicies and CloudPodInvariants have no Go declarations or callers.
- A1-19/39/48 advance through the shared award constructor and deletion of the
  CLI worker's second acquisition and renewal path. Native here hosting still
  needs W9/W2/W3's resolveHereRuntime binding. Remote NativeHostStore and its
  authenticated principal still need production adapters.
- A1-40/41 retain the existing local store/queue/maintenance and lifecycle
  composition. The localcontroller suite passes. W1's typed activity and
  incompatible-store signal, W2 provisioning and authorized HTTP handlers,
  and W9's resolveLocalServices remain real integration points.
- A1-08/09/18/26/27/55 retain the implemented claim/ordinal/selection boundaries.
  Enrollment, billing and here requester authority remain server integration.
- A1-10/43/52 retain implemented engine installation, signed policy, updates and
  historical release support. Windows/native release evidence remains open.
- A1-11/12/19/36/45/46/48 retain shared source, process environment, identity and
  storage inputs. Production credentials, process grants and fresh per-attempt
  storage bindings still need W2/W3/W7 assembly.
- A1-20/49 retain private credential-file startup and enrollment cleanup.
  Production provisioning remains W2 work.
- A1-42 still needs deployed network/isolation/scratch and teardown evidence.
  KubernetesBoundary.Probe and Clear need production deployment bindings.
- A1-50/51/53 remain open for native CLI placement, W3 runtime retirement and the
  native/real-repository QA matrix. Cloud plan/eval hosting and adoption of the
  initialized Cloud checkout remain W3/W8 integration points.

Only W8's input constructor becomes exported. No foreign Go contract changes are
requested. No new environment variable, limit, service or compatibility API was
introduced. The production initial-intake caller remains a W9 migration point.

## Budget

Using the lead's production-line exclusions against main, W8 is **+4,185 lines**,
which is **6,485 over** the -2,300 budget. Cluster is -1,951, engine is +2,217,
runners is +1,521 and wingd is +2,398. Required signed-engine distribution and
cross-platform local lifecycles account for much of the addition. This session
does not establish that those implementations are minimal and requests no budget
waiver. W8 remains incomplete.

## Verification and shared files

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR=/tmp/sw-astra-W8. Test package concurrency
never exceeds four.

- Whole-repository go build -p 4 ./... passes.
- Full internal/runners/..., internal/cluster and internal/wingd/localcontroller
  suites pass. Focused here, initial-intake and trigger-parser tests pass.
- All tests selected by W8 in tests/acceptance pass.
- Focused vet and race checks pass.
- FuzzRunInputForAwardCapturesRetrySeals passes 1,000 generated executions.
  FuzzHereAwardsCannotChangeRunBinding passes at least 1,000.
- Shared OwnerHost acquisition-cancellation and authority-loss tests pass.
- go run ./internal/commentcheck -base main . passes across 2,121 Go files.
- Diff whitespace passes. Git has no local user or core.worktree override.

Shared caller files changed are cmd/sparkwing/here_resume.go, here_resume_test.go,
pipeline_trigger.go and pipeline_trigger_here.go. Deleted shared files are
cmd/sparkwing/worker.go and worker_host_test.go. The acceptance fixture is
tests/acceptance/native_store_w8_test.go. The W8 integration paragraphs in
contracts/c3-protocol-sdk.md and internal/runners/HOSTING.md are updated.
Lead-owned handoffs and amendments are untouched.

Friction: Overwing reports its installed binary behind source. Matching and
reading standards still work. No Sparkwing executable, pipeline, deployment,
release, external report write or Git write ran. Scratch is removed after checks.
The shared build cache is retained. The lead owns review and commits.

STATUS: CONTINUE
