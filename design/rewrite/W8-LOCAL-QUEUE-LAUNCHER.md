# W8 local queue and store launcher

## Implemented

`localcontroller.Config.Open` constructs `runners.NewStoreRunner` over its owned
store and controller before returning. The binder supplies `StoreRunnerConfig`,
state and shared HTTP handlers. The injectable `Queue` interface is deleted.
Missing native services or signed policy, and a changed controller store, fail
startup through the existing partial-binding cleanup. The shared loop joins its
pending claim and native hosts before the local controller closes its store.

`launcher.NewStoreLauncher` constructs the existing launcher over an
`EngineController` and its store. `runners.StoreController` shares the embedded
runner's C2 authorization with launcher claims and synchronization. Launcher
selection still requires the service credential's `claims.launch`. The shared
controller owns version admission, enrollment, issuance and the cleanup reader.
This consumes W2's implemented `LauncherSyncReader` interface. Its persistence
implementation remains foreign integration work.

Invariant: each embedded claim or cleanup read requires the current credential's
complete route authority before persistence receives the request.
`FuzzStoreLauncherAuthorityW8` generates credential mutations and combinations of
narrowing, revocation, route selection and provider failure for both operations.
Denied claims leave the job unclaimed. Existing named/mixed-runner properties
continue through the shared adapter.

`TestNativeStoreOwnerW8/local_controller` now drives `Config.Open`, leased
reconciliation, the real queue, C2 authentication, shared controller issuance,
native owner completion and joined shutdown. State, HTTP handlers, enrollment,
version policy and source acquisition remain fixtures. The accepted empty plan
does not start an SDK process.

`TestStoreLauncherW8` drives real SQLite/C2 issuance through bootstrap publication,
Job activation and synchronized cleanup. It reads the private award file and
checks the source pin and distinct bootstrap. Kubernetes, network evidence and
the cleanup reader remain fixtures. This does not establish native Cloud
isolation or safe persistence-side award correlation.

## Cuts, ports and shared files

The local `Queue` contract loses five lines and its `queueFunc` fake loses three
lines, including separating whitespace. Their callers use the real runner.
The former private `storeClaimant` is generalized in place rather than retained
as a second authorization implementation. There is no additional legacy
dispatcher deletion in this continuation. The warm-pool dispatcher, run-host
Kubernetes dispatcher, cluster worker and old pod builder remain absent.

No historical port or native acceptance cell is newly claimed. The existing
native-owner component gains the local-controller case. Store-launcher and
authority proofs exercise the new constructor. Source docs are updated in
`internal/runners/HOSTING.md`, `internal/runners/launcher/CLOUD-JOBS.md` and
`internal/wingd/LOCAL-CONTROLLER.md`.

The local composition contract changes from `Services.Queue` to `Services.Runner`.
Its sole existing implementation was the W8 fixture. All callers are migrated.
`LocalRuntime` and foreign contracts are unchanged. No other area's production
file is touched. W8-owned shared acceptance fixtures changed in
`runner_store_w8_test.go`, `bootstrap_store_w8_test.go` and
`native_store_w8_test.go`. New coverage is `store_launcher_w8_test.go`.

## Amendment and handoff disposition

All W8 entries in amendments-1.md and handoffs.md were checked. The lead-owned
ledgers are unchanged. Earlier completed work is retained.

| Entries | Disposition |
| --- | --- |
| A1-08,09,20,26,49,55 | Shared runner selection, named here claims, private credential loading and issuance remain. Embedded launcher routing now uses real C2 authorization. Production enrollment, local claimant provisioning and Actions evidence remain open. |
| A1-10,11,12,43,45,46,48,50,51,52 | Existing engine, source, environment and snapshot owners remain. This continuation adds no alternate implementation. W9's startup, client, notices and upgrade bindings and native packaging evidence remain open. |
| A1-18,27 | Zero-based award ordinals and immutable billing inputs remain. W3/W9 still omit SourceAttempt on holder transitions. C1 refuses it. |
| A1-19,36,39 | Local startup now constructs the real store runner and native hosts. W3's shared constructors remain in use. Source/process/log/storage bindings and complete server-granted runner-label projection remain integration work. |
| A1-38 | Store launcher construction and the W2 cleanup-reader adapter are consumed. Wire observations still lack the award fence and Job UID. Plan/eval awards still lack Execution resources. Neither foreign signature was changed. |
| A1-40,41 | Local queue construction moves into the store owner. Existing readiness, four activity holds, drain, managed units and maintenance remain. Production state/peer/service binding and W9 foreground/detached startup remain open. |
| A1-42,53 | Native isolation, egress, scratch, teardown and platform QA remain unproven. No native cell is enabled. |
| Pending-claim outcomes and cancellation handoffs | Existing approved implementation remains unchanged. Local queue drain now joins that implementation rather than a fake queue. |

## Contract changes needed and real integration

- W1/W2 must bind admission's `RunStore`, the owned secret read, initial/live
  activity and kernel-peer authorization through the declared `State` and
  `LocalHTTP` interfaces. W8 owns the election, store open/close, queue and
  leased maintenance. No SQL projection or synthetic local root was added.
- W9/W3 must carry captured `SourceAttempt` and consume `Applied.Attempt` on
  holder start, retry, report and unstarted release. The wire field is still
  missing, so W8 does not substitute a guessed ordinal.
- W8/W9/W1 still need immutable claim-generation and Kubernetes Job-instance
  correlation on launcher sync. W2's controller adapter is implemented, but
  W1 cannot implement its safe reader from stable UnitID and node UID alone.
- W2/W9 must project policy-owned plan/eval resources and complete granted
  runner labels. W8 does not derive them from operator ceilings or observations.
- W2/W3/W7 must supply the native source, process, policy, log and storage
  bindings. Remote execution also needs the NativeHostStore transport.
- W9 must implement `NewEngineClient`, which still panics, and compose the CLI
  entrypoints. Its existing launcher grammar gaps remain in the prior report.
- W1/W2/W6 must resolve startup bootstrap input versus newly generated issuer
  output. W8 adds no seeded credential path.
- T1/lead retain deployed Cloud evidence and native Linux/Darwin/Windows,
  Postgres and real-repository QA.

There is no new product decision. The remaining questions are the existing
cross-area contracts above.

## Mechanisms and verification

The existing embedded claimant becomes a shared runner/launcher policy adapter.
The store-launcher constructor parallels the existing store-runner constructor.
Duplicating C2 checks or opening another HTTP transport was unnecessary because
both execute in the controller process. Removing these changes restores
caller-owned composition. There is no new issuer, keeper, schema, setting,
environment variable, service or software-policy implementation.

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, private cache/temp paths
and at most four package workers. Final checks are recorded below.

| Check | Result |
| --- | --- |
| Whole repository build | Passed. |
| Focused vet for runners, launcher, local composition and acceptance | Passed. |
| Full runners/... and engine/... suites; localcontroller | Passed. |
| Focused local runtime/lifecycle/control/dashboard and runner/launcher/bootstrap CLI tests | Passed. |
| Local composition, owner and store-launcher components and authority seeds with race | Passed. |
| Launcher-authority fuzz, 100x budget, two workers (140 executions reported) | Passed. |
| internal/acceptance | Passed. |
| Full tests/acceptance | Failed at the SourceAttempt boundary, then reached its 120-second package limit during the filesystem-depcache storage property. No full acceptance pass is claimed. |
| commentcheck -base main | Passed. |
| git diff --check | Passed. |

The SourceAttempt failure affects the existing Cloud bootstrap and executor
lifecycle/event proofs before their bodies run. It cannot establish a native
pass or an observed independent masking failure. Storage breadth is outside
this continuation's changed paths.

Friction: Overwing reports its installed binary behind source. The broad
acceptance suite exceeds the selected budget after reporting the known foreign
transition failure. No Sparkwing binary, pipeline, Git write, deployment or
release ran. The lead retains review and commits. Scratch is removed after all
check processes join.

STATUS: CONTINUE
