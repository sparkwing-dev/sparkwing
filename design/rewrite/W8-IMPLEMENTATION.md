# W8 implementation handoff

W8 remains incomplete. The continuation at `da5917c98` is the latest implemented
work. All 26 W8 amendment rows and both W8 handoffs retain their recorded status.
This continuation corrects the Cloud documentation and records the remaining
boundaries. It adds no production code, test ports or acceptance cells and
deletes no old code.

| Owner | Remaining boundary | Evidence in this checkout |
| --- | --- | --- |
| W3 | Declare host construction, delegated node routes, generated-code replay loading and run/plan/eval execution identity inputs. | `RunHost` and `NodeHost` are interfaces without final constructors. `RunInput` has no granted runner field. Plan/eval inputs and their shared process constructor are undeclared. |
| W3/W9 | Implement the declared source, keeper, remote owner and HTTP/data clients, then bind the installed runner and Cloud entrypoints. | `source.Acquire`, `source.ForRequest`, `source.ForExecution`, `lease.Keeper.Run`, `RemoteOwnerStore`, `NewEngineClient` and `NewDataClient` still panic. W8 already consumes these interfaces through component fakes. |
| W1/W2/W9 | Supply Cloud plan/eval resources and launcher sync, the elected local controller services/activity, and CLI composition. | `EngineController.SyncLauncher` still panics. Plan/eval claims clear `Execution`. `LocalRuntimeConfig.Open` has no production shared-controller implementation. The shared bootstrap parser requires a job for plan units. |
| W3/W7 | Declare workspace-off binary-cache hashing. | `sdkinstall` builds with `GOWORK=off`, while `bincache.PipelineCacheKey` follows the ambient workspace. The existing request remains open. |
| W8/T1/lead | Complete Windows SID transport, deployment probes/node cleanup and native placement/real-repository acceptance. | Windows peer credential lookup returns no identity. `KubernetesBoundary` requires deployment-owned Probe/Clear implementations. Native catalog cells remain skipped. |

Missing shared implementations remain behind their declared interfaces and fake
component proofs. Missing shared declarations prevent the remaining W8
composition because the swarm brief forbids changing another area's contract.
The existing node-award constructor, private Cloud award transport and real
controller/SQLite components remain implemented. Installed execution, actual
hello and native deployment proofs remain open.

Verification used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4` and
private cache/temp directories under `/tmp/sw-astra-W8`. Whole-repository
`go build -p 4 ./...` passed. Vet passed for runners, engine, cluster, wingd and
both acceptance packages. Full runners/engine/component-acceptance tests passed.
Cluster passed with its socket-listener test excluded. Focused local-runtime,
lifecycle, managed-unit and client tests passed. Commentcheck passed across
1,015 files. Sleepcheck and `git diff --check` passed. No new runtime behavior
requires a documentation drift-test change. Shared production files and APIs
changed: none. No new mechanism or product question was introduced. No
Sparkwing binary, pipeline or Git write ran. Shared Git config has no local
author or `core.worktree` override. Worker scratch was deleted after checks.

## Completed before this continuation

The first session implemented exact engine pin resolution, signed release and
version-index verification, verified machine caching and staged repository
upgrades. It added the unified runner loop, managed version policy, the single
plan/eval/work Job builder, bootstrap Secrets, suspended Job creation,
reservation reconciliation and teardown ownership. Local lifecycle primitives
cover readiness, concurrent startup, stay promotion, drain, four activity holds
and peer-identity refusal.

The first session removed 8,144 lines, including 5,444 lines from the old k8s and
warm-pool dispatchers. Its test port was `TestResolveResources_StaysQuietUnderTheCeiling`
to `launcher.TestSizingBelowCeiling`. That work is committed on this branch.

## Second session

- Cut the old cluster pool, trigger, source, heartbeat, credential/cache and
  agent execution paths. `start` and `warm` now parse the final registry and run
  `ManagedRunner` through `RunnerFactory`. `run-node` and `fetch-source` use
  `BootstrapHost`. Missing composition returns an error before execution.
  There are no aliases for `runner`, `agent` or the duplicate `wingd` verb.
- `runners.Hosting` dispatches trigger awards to `nodehost.RunHost` and job
  awards to `nodehost.NodeHost`. It confirms the live award, starts the shared
  keeper before preparation, propagates authority loss, joins renewal before
  source cleanup and preserves both execution and cleanup failures. Host
  preparation and keeper execution are tested through fakes of the W3 boundary.
- The loop honors configured polling, unit kinds and a claim-count drain bound.
  Here awards must match the selected run. Long polling does not delay idle
  exit, claim deadlines or signed policy refresh. Error observers cannot hide
  completed host failures. Completed failures remain visible while required
  software updates proceed after every host has joined.
- `NewNativeInstallation` stages verified runner bytes beside the installed
  executable, preserves its path, symlink and permissions, and checks the staged
  digest. Unix uses atomic replacement. Windows retains the running image and
  restores it if publication fails. Refresh failure can be retried without
  replacing identical bytes again. Existing registration hooks own drain,
  unit refresh and restart. The verified runner version reaches the refresh
  hook separately from the installed CLI's identity.
- The runner chart calls `warm` and `--warm-module`, preserves an explicit
  trigger-claim opt-out and omits the removed source-cache/proxy arguments and
  environment. The image calls the runner directly. The `.netrc` seeding script
  and its Dockerfile caller are deleted. The bundled chart archive was rebuilt.
- `limits.Renew` and `CheckRenewal` implement their existing declarations.
  Renewal validation avoids duration multiplication overflow.

The continuation deletes 33 whole files containing 6,743 lines. Additional
removed lines replace old helpers, tests and chart configuration. It adds no
protocol adapter or legacy execution fallback.

## Evidence from the second session

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false` and private caches,
Go temporary files and test roots under `/tmp/sw-astra-W8/`. Package parallelism
was at most four for builds and two for tests.

- Whole repository `go build -p 4 ./...` passed. The CLI worker also passed a
  whole repository build after its final cluster changes.
- Full `internal/runners`, `internal/limits` and `tests/acceptance` package tests
  passed. `internal/acceptance` compiles and has no runtime tests.
- Cluster tests pass with only `TestStartMetricsListener_ServesMetrics` excluded.
  The attempted full run failed at that test's TCP listen with `socket: operation
  not permitted`. This is an unexecuted socket proof, not a passing test.
- Vet passed for cluster, runners, limits and acceptance tests.
- Focused runner loop/hosting race tests passed. The complete chart suite passed,
  followed by final-tree checks for direct-source configuration, trigger opt-out
  and the refreshed vendored chart. The runner image release-contract test passed.
- Windows/amd64 runners tests cross-compile. Native Windows replacement and
  service restart remain untested.
- `TestStartAndWarmExecuteTriggerAndJobOnOneManagedLoop` tests CLI orchestration
  with verified policy and fake claim/host implementations. Hosting tests cover
  both host roles, source-preparation failure, cleanup and lost authority.
- `TestEngineW8IndexFailureComponents` passes invalid-signature, signed malformed
  content and unavailable-origin cases, each with persistent verified fallback
  and first-use refusal. Existing W8 component acceptance tests still pass.
- Two independent review findings were reproduced and fixed: host failures
  preventing later required updates, and long polling delaying policy/lifecycle
  deadlines. Their controlled-clock regression tests pass. Focused re-review
  found both resolved.

No additional catalog cell was unskipped. No new parked test from the T1 port
inventory was claimed as ported. Removed source and heartbeat tests exercised
superseded APIs. Their final behavior belongs to W3 source/keeper acceptance.

## Third session: elected local runtime and old-controller cut

`NewLocalRuntime` now implements the process behind `LocalRuntime`. It acquires
the admission election before calling the shared-controller factory. Store
opening, startup orphan reconciliation and the initial activity snapshot finish
before listeners start. Readiness waits for admission, API and the requested
dashboard serving loops. The runtime keeps ownership through service shutdown,
API and finalizer joins, and store close. Failures retain both execution and
cleanup errors, including a flush error joined with cancellation.

The daemon uses the same admission drain for the runtime and its wire. A failed
API bind fails startup. Sampling, idle, context and diagnostic workers join on
shutdown. All four controller activity holds prevent idle exit. Repeated status
probes do not reset the idle boundary. A failed start can be retried without
reusing the previous start's readiness. Cancellation joins reconciliation before
the caller returns.

Jev still reads its optional `TYPESAFE_API_KEY`, now through the elected
controller's owned secrets store. Missing/unreadable keys log the existing
fallback. `LocalController.TypeSafeAPIKey` makes this obligation explicit for
the adapter, without a second store opener.

The old local controller composition is deleted: its duplicate router and auth,
lazy/reopening writer, secret migration, duplicate wingd CLI and direct tests.
The admission legacy-config migration registration is also gone. This session
deletes **17 whole files containing 3,370 lines**, plus old caller fragments.
The foreground CLI helper now takes an explicit shared-controller factory and
calls `NewLocalRuntime`. The default factory is still absent, so it refuses
startup before opening the home, store or sockets. There is no old-server
fallback.

Existing admission cancellation and hosted-backend selection tests retain their
behavioral assertions with direct run-store and HTTP fixtures. Removed native
local-secret sealing, old auth-route, stray-session recovery and migration tests
depended on the deleted server. Their surviving final behavior remains an
integration obligation. No additional T1 inventory test is claimed as ported.

### Third-session evidence

Commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, and `GOCACHE`, `GOTMPDIR`
and `TMPDIR` beneath `/tmp/sw-astra-W8/`. Go package parallelism was at most four.

- Whole repository `go build -p 4 ./...` passed after the hard cut.
- Vet passed for wingd and its subpackages, engine, runners and launcher,
  cluster, orchestrator, CLI and acceptance tests.
- `go test -race -p 4 ./internal/wingd -run '^TestLocal'` passed. These include
  real file elections and in-memory listeners, with fake shared-controller
  services. They establish component behavior, not native socket or store proofs.
- Full engine, runners, launcher and component acceptance packages passed.
  Cluster passed with `TestStartMetricsListener_ServesMetrics` excluded because
  the preceding session established that its socket cannot bind in this sandbox.
- Affected CLI/orchestrator tests compile. Focused CLI missing-factory and
  elected-startup tests, status/secret-flag checks and the C1 default-team caller
  guard passed.
- Wingd tests cross-compile for Windows/amd64. This is not native Windows evidence.
- Commentcheck, sleepcheck and `git diff --check` passed for the changed tree.
- Independent review reproduced a swallowed API flush error joined with
  cancellation. The regression is in
  `TestLocalRuntimeJoinsAPIAndPreservesShutdownFailure`; the fix and reviewer's
  separate overlay reproduction pass.
- Review also found the removed Jev key lookup. Its elected-store replacement
  has a regression for configured and missing keys, without logging the value.

Existing W8 component acceptance remains green. **No native catalog cell was
unskipped**: idle process exit/closed sockets, installed managed units, Windows
identity, real orphan recovery and Cloud isolation still need their final
implementations and platform fixtures.

### Third-session contract additions and assumptions

W8 adds `LocalController`, `LocalRuntimeConfig` and `NewLocalRuntime`. The factory
is invoked under election and returns the shared controller plus its existing
`RunStore` adapter, reconciliation, queue/maintenance and serving methods. This
boundary is needed because the final W1/W2/W3 implementations are absent. Reusing
the removed router would preserve a second auth model and store owner.

An empty `DashboardAddress` starts only `api.sock`. The CLI supplies the dashboard
address when requested. The service adapter reports all four activity counts,
including dashboard connections. `AwaitReady` waits for an active start rather
than reusing a completed start. A lost election returns `ErrNotElected`; it cannot
wait for a foreign process through the in-process runtime. Cross-process owner
discovery/status/stay/drain remains a separate unbound client.

These are W8-local additions. No other area's contract was changed. The missing
cross-area contracts listed below remain explicit integration work.

## Remaining integration and contract changes needed

The fourth and fifth sessions below complete status/stay transport and dynamic
dashboard startup. The seventh supplies an owner-aware lifecycle adapter.
Detached startup and dashboard commands still need CLI composition.

1. **W3/W9 runner composition.** Bind `RunnerFactory` to the authenticated
   controller, `Hosting` preparation/renewal callbacks, source acquisition,
   accepted engine identity, admission and the real keeper. `nodehost.RunHost`,
   `NodeHost`, `source.Acquire`, `lease.Keeper.Run` and the W9 engine client remain
   interfaces/stubs in this checkout. `MainWithVersion` has no runtime factory.
   W3 must also declare host construction, delegated node-route creation and
   generator replay loading. `ClaimedUnit` alone cannot supply the `RouteBinding`
   and replay required by `NodeInput`. Moving those responsibilities behind
   another preparation callback would leave the same missing composition.
2. **W6 config/environment.** Supply the final `userconfig.ReadConfig` and
   validation implementation. Native repository upgrade commands still need
   `procenv.Build`. Factories must consume metrics, warm-module, resource,
   deadline, admission and owner-credential preferences passed in RunnerOptions.
3. **W9 claim grammar.** The registry/config declares `trigger-source` and
   `holder-prefix`, but `ClaimRequest` has neither source selection nor a holder
   preference. Nonempty values now fail explicitly. Decide the final transport
   or remove those declarations. Do not reintroduce a legacy claim client.
4. **W3/W9 Cloud bootstrap.** The W8 builder emits `--unit`, `--attempt`,
   `--source-job`, `--source-generation` and `--engine`. W9's bootstrap registry
   omits them and requires `--job` even for plan units. Reconcile that contract
   before wiring plan/eval/work entrypoints. The source init and execution
   containers also need a defined handoff for their single-use bootstrap.
5. **W2/W9 launcher transport.** `NewControllerControl` implements the award/sync
   projection. Wire `RunLaunchCLI` to a shared client with Launcher selection and
   the verified Cloud boundary. Its default factory and real HTTP client are absent.
6. **W1/W2/W3 local services.** Bind `LocalRuntimeConfig.Open` to the final shared
   controller, its single store, queue consumer and orphan recovery. Final
   `EngineController`, capability authorization, claim/read/reaper and singleton
   lease methods remain stubs. W1 needs a local activity projection and a typed
   incompatible-store error. Preserve local secret sealing and stray-session
   cleanup in the final adapters; the deleted server no longer supplies them.
7. **W8/W9 local transport and services.** Bind `client.NewLocalLifecycle` into
   CLI composition. It supplies foreign-owner query, stay, dashboard startup
   and drain. The admission spawner uses the declared `wingd supervise`
   entrypoint; its `wingd run` child still needs the shared-controller factory.
   Implement native managed units once W9 specifies
   Home and foreground arguments for `dashboard start --stay` and `__tick`.
   The registry declares `--state-db` for `wingd run` and `wingd supervise`,
   while their runtime parsers and the admission spawner use `--home`.
   W9 must reconcile those paths before W8 renders installed commands.
   Native runner replacement needs actual service registration hooks. Windows
   needs a native SID-authenticated transport; missing identity still refuses.
   `APIConn.PeerIdentity` now supplies the controller-facing UID/SID projection.
   The native Windows transport must still supply a kernel-authenticated SID.
8. **Cloud deployment.** Supply real network probes and reservation-scoped node
   cleanup, then prove native sidecars, aggregate scratch accounting, Docker
   execution, network isolation and teardown. These remain deployment proofs.
   The deployment must define required and denied probe destinations, the
   policy-revision source, a probe executable and node-state cleanup. The chart
   has no canary runner or node-reset mechanism. Deleting Kubernetes Pods cannot
   establish that privileged DinD cleared the node. Probe/cleanup callbacks
   remain required and have no success fallback.
9. **W7/W9 follow-through.** Delete remaining cache service chart/templates/docs,
   preserve historical release matrices, and wire upgrade/policy notices to the
   actual CLI and dashboard. This continuation removes only runner callers of
   the old cache transports.
10. **W3/W9 upgrade verification.** `UpgradeVerifier` takes a checkout and
    verified engine, but the exact-engine build/typecheck and describe/plan
    invocation is undeclared. W3/W9 must define its entrypoint, pipeline
    selection and inputs. `pipeline describe` and `pipeline plan` require a
    pipeline name. The old `PlanDocument` uses the superseded protocol and is
    not a substitute. Once declared, W8 can exercise the verifier with fixture
    executables without waiting for a real host implementation.

## Shared files and mechanisms

Third-session edits outside `internal/wingd`:

- `cmd/sparkwing/wingd.go` and new `wingd_runtime_test.go` migrate the foreground
  caller and test its factory boundary.
- `cmd/sparkwing/daemon_api_report_test.go`, `daemon_test.go` and
  `secret_local_test.go` remove obsolete direct callers/tests.
- `internal/orchestrator/hosted_backends.go`, `hosted_backends_test.go`,
  `local_secrets.go` and `wingd_e2e_test.go` retain independent behavior after the
  deleted owner disappears.
- `pkg/store/default_team_callers_test.go` removes the deleted methods from its
  allowlist.
- Deleted `internal/orchestrator/wingd_daemon.go`, `wingd_api.go`,
  `wingd_runstore.go`, `wingd_runstore_identity_unix.go`,
  `wingd_runstore_identity_windows.go`, `wingd_secrets_legacy.go`, `wingd_verb.go`,
  `local_secrets_test.go`, `wingd_api_contention_test.go`,
  `wingd_api_harness_test.go`, `wingd_api_routes_test.go`, `wingd_api_test.go`,
  `wingd_runstore_stray_unix_test.go`, `wingd_runstore_test.go`,
  `wingd_typesafe_key_test.go` and `wingd_verb_test.go`.

The third session also deletes `internal/wingd/legacy_migrate.go` and updates
this handoff and `internal/wingd/LOCAL-CONTROLLER.md`.

The cluster and runners files are W8-owned. Shared prerequisites/callers from
the second session were:

- `internal/limits/deadline.go` and `internal/limits/renewal_test.go`.
- `build/Dockerfile.runner` and deleted `build/runner-entrypoint.sh`.
- `cmd/sparkwing/update_release_workflow_test.go`.
- `tests/acceptance/engine_w8_test.go`.
- `charts/render_test.go`.
- `charts/sparkwing-runner-bundle/templates/runner-deployment.yaml`.
- `charts/sparkwing-runner-bundle/templates/_helpers.tpl`.
- `charts/sparkwing-runner-bundle/values.yaml` and `README.md`.
- `charts/sparkwing-full/charts/sparkwing-runner-bundle-0.1.11.tgz`.

W8-local mechanisms added are the host-preparation/keeper injection boundary,
CLI runtime factories and native installation callbacks. They connect the
existing contracts because the owning implementations are unavailable. They do
not define another protocol or persist credentials. Real integration must wire
these ports before any installed-runtime acceptance claim.

No git metadata was written. The earlier session recorded an initial installed
`sparkwing info --for-agent` call before reading the no-binary rule. This third
session ran no Sparkwing executable or pipeline. Native socket evidence remains
unavailable in the sandbox. Worker scratch is removed after all checks finish.
The lead committed the third-session changes before this continuation.

## Fourth session: owner status, stay and dashboard startup

The admission socket now carries `local_control` and `local_control_status`.
`client.QueryLocalController` reads the elected runtime's readiness, PID,
version, stay policy and four activity counts without resetting idle time.
`client.SetLocalStay` verifies the policy acknowledgement. Both reuse the
authenticated socket and refuse unsupported or malformed replies. Neither
spawns a process, replaces a version or opens a store. Probe connections cannot
mutate policy, and an admission-only daemon cannot impersonate a local runtime.
Client cancellation interrupts the handshake. An unresponsive owner is distinct
from an absent one.

`LocalRuntime.EnsureDashboard` starts the UI on an existing controller.
`StartDashboard` uses it after acquiring a ready owner. Requests for the same
address reuse its listener and wait for the serving loop. A failed bind keeps
the existing controller available. Dashboard shutdown joins before store close.

Independent review reproduced two errors and verified their fixes:

- Stay could be acknowledged between the idle decision and shutdown. The idle
  check now marks stopping and draining under the stay-policy lock. Both local
  and wire callers refuse promotion after that decision.
- A dashboard startup failure reached its caller as internal cancellation.
  The caller now waits for joined cleanup and receives the actual service error.

### Fourth-session evidence

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, and private cache,
temporary build and test directories under `/tmp/sw-astra-W8/`. Package
parallelism was at most four.

- Whole-repository `go build -p 4 ./...` passed after the final fixes.
- Vet passed for wingd and subpackages, wingwire, engine, runners and launcher,
  cluster and acceptance tests.
- `go test -race -p 4 ./internal/wingd ./internal/wingd/client -run '^TestLocal'`
  passed. The tests use file elections, fake shared-controller services,
  in-memory listeners and `net.Pipe`.
- Full engine, runners, launcher, wingwire and component acceptance tests
  passed. Cluster passed with only `TestStartMetricsListener_ServesMetrics`
  excluded because the sandbox cannot bind sockets.
- The wire registry check initially found missing round-trip fixtures for the
  two messages. The fixtures and generated shape snapshot now pass.
- Both independent review reproductions failed before the fixes and pass with
  them. The focused re-review found no remaining defect in the corrections.
- Wingd tests cross-compile for Windows/amd64. Native Windows execution and
  peer identity remain unverified.
- Commentcheck, sleepcheck and `git diff --check` passed on the final tree.

No native catalog cell was unskipped, and no additional T1 port was claimed.
The native process, service-manager and Cloud integration obligations remain
listed above. This session deletes **0 whole obsolete files / 0 legacy-code
lines**. It replaces existing lifecycle fragments but makes no further old-code
hard cut.

### Fourth-session contracts and shared files

W8 adds `EnsureDashboard` to its runtime contract and the two local-control
messages to the existing admission wire. `Daemon.SetStay` now returns an error
after shutdown is committed. Runtime callers handle it. These are W8-owned
contracts. No other area's contract was changed.

The dashboard owns one listener. A different requested address requires a stop
and restart because a live replacement would need to drain browser requests.
The simpler silent-ignore behavior was incorrect. The addition uses the same
runtime and worker joins instead of a separate dashboard process. The local
control wire reuses existing peer authentication instead of adding HTTP routes
or another auth policy. No setting, environment variable or service is added.

Shared edits are limited to:

- `pkg/wingwire/local.go`, `wingwire.go`, `wingwire_test.go` and
  `testdata/shapes.json`.
- `tests/acceptance/local_controller_w8_test.go`, whose runtime fixture supplies
  the new interface method.
- This handoff. W8 source documentation is `internal/wingd/LOCAL-CONTROLLER.md`.

Real integration still needs W1/W2/W3 controller services and activity
publication, W9 process/managed-unit arguments and owner lifecycle composition,
runner and bootstrap transports, and Cloud enforcement/cleanup probes. No new
external contract question was introduced. Socket restrictions prevent native
execution evidence. The review fixture also had to initialize Go signal
handling before entering a virtual-clock test bubble.

The lead committed the fourth-session changes before this continuation. No
Sparkwing executable, pipeline or git-writing command ran in that session.

## Fifth session: dashboard transport and Cloud resource budgets

`client.StartLocalDashboard` starts the UI on an elected owner through the
authenticated admission socket. The `local_dashboard` request waits for
`local_dashboard_ack` after the serving loop starts. Repeating an address reuses
its listener. A different address or failed bind reaches the caller as an error.
The same runtime supplies startup and shutdown. No store opener or process
fallback is added.

The request owns one connection. A joined reader cancels readiness when the
peer disconnects. Daemon shutdown closes that connection before joining its
handler, so a failed dashboard service cannot make the handler and runtime wait
on each other. Probe connections and admission-only daemons refuse startup.
The handshake retains the existing health-probe budget. Listener readiness uses
the caller's context. Cancellation closes the connection and preserves the
context error even during the deadline change after handshake.

Cloud Jobs now refuse CPU or memory above operator ceilings before reserving
a node. The old `min` calls silently reduced awarded resources. CPU conversion
uses Kubernetes decimal quantities, rounds up to a millicore, then checks the
result against the original ceiling. Exact 2.007 and 4.001 core requests retain
their value. A rounded allocation above the ceiling is refused, including a
ceiling that Kubernetes would round upward at nano precision.

`TestLauncherKubernetesBoundaryW8Components` composes the actual launcher,
Job builder, reservation boundary and reconstruction code against a fake
controller and Kubernetes API. Plan, eval and work cover launch, adoption by a
replacement launcher, exclusive placement, bootstrap deletion on cancellation,
retained reservation while Docker runs, cleanup after pod removal and node
reuse. The acceptance refusal test also covers CPU, memory and precision.
These are component proofs. No native catalog cell was unskipped.

### Fifth-session contracts and shared files

The two dashboard messages and `client.StartLocalDashboard` extend the W8-owned
local transport. The separate request uses a one-request connection because its
readiness wait needs peer cancellation. Reusing the synchronous status operation
would leave that wait unable to observe disconnect. A shared dial helper retains
the existing absent, unreachable and cancelled-owner classifications.

No new setting, environment variable, service, timeout or fallback was added.
No other area's contract changed. The open contracts and integrations listed
above still apply, with foreign-owner dashboard transport now supplied. In
particular, W9 still needs to bind the client operations into CLI lifecycle and
settle managed-unit Home/foreground arguments and Cloud bootstrap grammar.
W1/W2/W3 still need to supply the shared controller/services and authenticated
host/claim adapters. Deployment probes and node cleanup, native Windows identity
and installed-service execution remain unverified.

Shared edits outside W8 product packages are:

- `pkg/wingwire/local.go`, `wingwire.go`, `wingwire_test.go` and
  `testdata/shapes.json`.
- `tests/acceptance/launcher_w8_test.go` and
  `tests/acceptance/launcher_boundary_w8_test.go`.
- This handoff. Source documentation is
  `internal/wingd/LOCAL-CONTROLLER.md` and
  `internal/runners/launcher/CLOUD-JOBS.md`.

No API was removed, so no callers outside W8 needed migration. This session
deletes **0 obsolete files and 0 legacy-code lines**. It removes the resource
clamp and extracts the dial/test-peer helpers. No additional T1 inventory test
is claimed as ported.

Independent review found CPU rounding across a ceiling, float multiplication
adding a millicore, loss of cancellation error identity at a deadline change,
and nano rounding of the ceiling. The regression tests ran red before each
correction. The final re-review found no remaining defect in those corrections.

### Fifth-session evidence

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, and private
`GOCACHE`, `GOTMPDIR` and `TMPDIR` paths under `/tmp/sw-astra-W8/`. Package
parallelism was at most four, and race checks used two.

- Whole-repository `go build -p 4 ./...` passed on the final implementation.
- Vet passed for wingd and its subpackages, engine, runners and launcher,
  cluster, wingwire and both acceptance packages.
- Full engine, runners, launcher, wingwire and component acceptance tests
  passed. Final launcher corrections also passed the complete focused race
  selection. `internal/acceptance` compiles and has no runtime tests.
- `go test -race -p 2 ./internal/wingd ./internal/wingd/client
  ./internal/runners/launcher -run
  '^TestLocal|^TestBuildJob|^TestBoundary|^TestSync|^TestLauncher|^TestSizing'
  -count=1 -timeout 120s` passed after the final corrections.
- Wingd and its client cross-compiled for Windows/amd64 with `go test -c`.
  Native Windows execution and SID authentication remain unverified.
- The generated wire shape, round-trip registry checks, commentcheck,
  sleepcheck and `git diff --check` passed.

Native socket and Kubernetes proofs were not rerun because the sandbox cannot
supply those boundaries. No Sparkwing executable, pipeline or git-writing
command ran. No local Git identity or worktree configuration override was
introduced. Worker scratch was removed after all checks and review finished.
Changes remain uncommitted for the lead.

## Sixth session: pending claims and admitted Cloud Jobs

The runner now observes host completion, idle exit, claim expiry and signed
policy refresh while its controller request is pending. An interrupted request
is cancelled and joined before another request or software replacement starts.
An error-free committed award returned during that join remains owned by the
runner and drains under the parent context. Held hosts keep their contexts and
renewals when software policy stops new claims.

The mechanism is one pending claim goroutine in the existing loop. A request
deadline alone would not observe host completion or start the idle interval
when the last host finishes. There is no new setting, timeout policy, transport
interface or service. Removing `claimWhileHosting` restores the previous
blocking call and loses these lifecycle guarantees.

`Launcher.LaunchOne` now validates the admitted suspended Job before publishing
its bootstrap. Placement, authored containers and arguments, environment,
private volumes, shared resources, failure policy and teardown ownership must
survive admission. Ordinary Kubernetes defaults remain accepted. An injected
`PodFailurePolicy.Ignore` cannot turn an OOM failure into another pod execution.

Teardown attempts bootstrap deletion even when Job deletion fails. It first
confirms the original Job UID on that failure path. A replacement or unknown
Job retains its bootstrap and reservation. Cleanup failures remain errors.

### Sixth-session evidence

- Whole-repository `go build -p 4 ./...` passed on the final implementation.
- Vet passed for cluster, runners and launcher, engine and both acceptance
  packages.
- Full engine, runners, launcher and component acceptance tests passed.
  Cluster tests passed with only the previously established socket-bound
  `TestStartMetricsListener_ServesMetrics` excluded. `internal/acceptance`
  compiles and has no runtime tests.
- Full `go test -race -p 2 ./internal/runners/... -count=1 -timeout 120s`
  passed for both runners and launcher.
- New controlled-clock tests cover blocked claims at idle, claim-expiry and
  policy boundaries, host completion during a blocked claim and held-host
  draining after policy denial. The committed-award cancellation regression
  verifies ownership through the public loop.
- `TestLauncherRefusesAdmittedNodeChangeW8Components` passes for plan, eval and
  work. The launcher suite also covers altered admission fields, accepted API
  defaults, OOM policy injection, failed Job deletion and replacement ownership.
- The initial admission and blocked-transport regressions ran red before their
  fixes. Independent review reproduced the OOM rule, replacement-bootstrap and
  committed-award races. Its final snapshot and overlay tests pass with no
  remaining concrete finding.
- Commentcheck, sleepcheck, the docs mirror drift check and `git diff --check`
  passed.

Go commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, and private cache,
temporary and test directories under `/tmp/sw-astra-W8/`. Test package
parallelism is at most four. These remain component proofs. No native catalog
cell was unskipped and no additional T1 inventory port is claimed.

This session removes no obsolete files or legacy subsystems. It replaces ten
production lines from the prior blocking call, budget-only admission check
and cleanup error handling. No public API was removed, so no external callers
needed migration. Shared edits are
`tests/acceptance/launcher_boundary_w8_test.go` and this handoff. Product changes
stay under `internal/runners`; source documentation is `launcher/CLOUD-JOBS.md`
and the loop's API comments.

There are no new cross-area contract requests or open product questions. The
existing integration list still applies: authenticated runner/host and launcher
transports, source/bootstrap handoff and CLI grammar, the elected shared local
controller and managed-unit arguments, native Windows identity, and deployed
Cloud probes and cleanup. API default fixtures do not prove actual cluster
admission, network enforcement, DinD behavior or native process lifecycle.
No Sparkwing executable, pipeline or git-writing command ran.
No local Git identity or worktree configuration override was introduced.
Worker scratch was deleted after checks and review finished. Changes remain
uncommitted for the lead.

## Seventh session: compose the foreign-owner lifecycle

`client.NewLocalLifecycle` binds one home and composes the existing elected
runtime with authenticated owner query, stay, dashboard startup and drain.
Ready foreign owners are reused without process replacement. Standalone and
another home are refused before mutation. Missing owners delegate startup to
the in-process runtime. Unreachable, stalled and unsupported owners remain
errors instead of authorizing another store opener.

If local startup loses the election, cleanup joins before querying and reusing
the ready winner. A winner still starting returns the election error for the
caller to retry. Joined cleanup errors remain errors. An owner that stops
during stay promotion cannot satisfy startup. Local starting and draining
processes retain operations until their own cleanup finishes.

The existing drain now preserves cancellation and unreachable-owner failures.
It requires the current admission protocol and a valid `DrainAck` before
waiting for the socket and election lock to close. It never spawns a successor.

### Seventh-session contracts and assumptions

The new constructor is W8-owned composition of existing lifecycle and wire
contracts. Its private process-owner interface lets tests fake the OS process
boundary because native sockets are unavailable. Putting the composition in
`wingd` would create an import cycle with its client. Removing this adapter
restores the in-process-only lifecycle and leaves foreign-owner routing to
every CLI caller. No setting, environment variable, timeout or wire message was
added, and no other area's contract changed.

This remains in-process startup plus foreign-owner reuse. W9 must bind the
constructor and shared-controller factory, then define Home and foreground
arguments for detached startup and installed units. The admission spawner uses
the declared `wingd supervise` entrypoint. Its `wingd run` child still lacks the
shared-controller factory, so installed controller startup remains unbound.
W1/W2/W3 services and authenticated host transports,
Cloud bootstrap grammar and source handoff, native Windows SID identity,
managed services and deployed Cloud probes/cleanup remain open.

No public API was removed. There are no external caller migrations or shared
product-file edits. Product changes stay in `internal/wingd` and its client.
Source documentation is `internal/wingd/LOCAL-CONTROLLER.md`; this handoff is
the only shared edit. This session deletes **0 obsolete files and 0 legacy
subsystem lines**. It replaces existing lifecycle and drain fragments. No
additional T1 inventory port or native catalog activation is claimed.

### Seventh-session evidence

- Whole-repository `go build -p 4 ./...` passed on the final tree.
- Vet passed for wingd and its subpackages, engine, runners and launcher,
  cluster and both acceptance packages.
- `go test -race -p 2 ./internal/wingd/client ./internal/wingd -run '^TestLocal'
  -count=1 -timeout 120s` passed. The new lifecycle tests cover owner reuse,
  stay/dashboard/drain routing, standalone and wrong-home refusal, concurrent
  reuse, election loss, cancellation and failure preservation.
- Full engine, runners, launcher and component acceptance tests passed.
  Cluster passed with only `TestStartMetricsListener_ServesMetrics` excluded
  because its native socket remains unavailable in this sandbox.
  `internal/acceptance` compiles and has no runtime tests.
- Election reuse, owner exit during promotion and joined election-cleanup
  regressions ran red before their fixes. Drain uses `net.Pipe` for malformed
  acknowledgement and cancelled-handshake coverage.
- Commentcheck and sleepcheck passed. No native socket, installed service,
  Windows execution or Kubernetes evidence is claimed.
- The client tests cross-compiled for Windows/amd64. The docs mirror drift
  check and `git diff --check` passed.
- Independent review of a frozen snapshot found no concrete correctness or
  simplification finding. It was static review; it added no runtime evidence.

All Go commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, and private
`GOCACHE`, `GOTMPDIR` and `TMPDIR` under `/tmp/sw-astra-W8/`. Test package
parallelism is at most four. The lead must commit the changes.
No git metadata was written, and no local identity or worktree config override
was found. An initial installed `sparkwing info --for-agent` discovery ran
before the swarm brief's no-binary rule was loaded. No candidate, pipeline or
stateful Sparkwing command followed. Worker scratch was removed after all
checks and independent review finished.

## Eighth session: native runner registrations

The native runner service moves from `internal/agentservice` to
`internal/runners/service`. Linux and macOS registrations now execute the
final foreground `start --config` command. The package retains login startup,
restart-on-failure, managed-file ownership and config-path protection. Native
service commands receive the caller's context. W9's caller imports and manual
start hints are migrated, with no old-package forwarding shim.

`Inspect` reads executable/argv from the actual file. `Refresh` rewrites an
existing registration and restarts an active service from an external updater.
It preserves missing and stopped services, refuses foreign or other-config
registrations, and never opens the machine config or credentials. Linux uses
`try-restart`. macOS reloads only `state = running`; inactive registrations take
updated file contents on their next load. Errors preserve the file and reach
the caller. Native macOS self-update still needs an external owner: a launchd
reload stops the process performing it before loading its replacement.

The duplicate shell installer, both native templates and its shell test are
removed: **four obsolete files, 485 lines**. The old package's three files are
relocated, not counted as deleted subsystem lines. The removed shell-marker
adoption branch/test has no compatibility replacement. The Go service owns
registration rendering. W9/W6/W2 still need final runner enrollment and private
credential files; the surviving CLI enrollment code writes the old config.

Independent review reproduced two defects: restarting a loaded but inactive
launchd job, and config inspection disagreeing with actual flag consumption.
The stopped-state check is fixed. The existing runner parser now lives in
`internal/runners/runnerflags`, shared by entrypoints and registration
inspection. This also covers values consumed by another flag, repeated config
flags and end-of-options. Review reproductions pass against a frozen final
snapshot, and final re-review found no remaining concrete issue.

### Eighth-session evidence

- Whole-repository `go build -p 4 ./...` passes after the API move and parser
  extraction.
- Vet passes for runners and subpackages, cluster, engine, wingd and
  subpackages, the CLI and both acceptance packages.
- Full runners, launcher, service, engine and acceptance tests pass.
  `internal/acceptance` compiles and contains no runtime tests. Cluster passes
  with only the previously established socket-bound metrics test excluded.
- Full service and acceptance tests pass under `-race -p 4` on the final code.
- `TestRunnerServiceW8Components/linux` and `/darwin` pass. These read real
  registration files, run their argv through the actual runner parser with fake
  config/runtime boundaries, refresh without changing config bytes, and remove
  the registration through fake service managers. These are component cells.
- Windows/amd64 service tests cross-compile. This is not native Windows proof.
- CLI tests compile. Full native CLI, service-manager and login execution are
  untested because their sockets/platforms are outside this sandbox.
- Commentcheck, sleepcheck, docs-mirror drift and `git diff --check` pass.

No native catalog cell was activated. There is no additional T1 parked-test
port. The existing native installer tests move with their implementation; the
shell-marker adoption case disappears with the removed installer.

### Eighth-session remaining contracts and shared files

No other area's declared contract changed. The existing integration requests
above still apply. W9 must settle Home arguments and foreground semantics for
controller/timer units, bind the shared-controller factory, and replace legacy
runner enrollment. W2/W9 also need a final Actions exchange result carrying the
server's complete push binding; the legacy response omits ref/SHA/workflow
identity. W3 source/host and keeper composition, launcher claim/sync/bootstrap
transport, Windows SID transport, native update ownership, and deployed Cloud
probes/cleanup remain open. No native runtime completion is claimed.

The local API additions are service inspection/refresh and a shared extraction
of the existing parser. A verb-only replacement leaves upgrades and real argv
inspection incomplete. A separate hand-written config scanner reproduced
multiple disagreements with execution. Sharing the parser removes that second
policy without changing grammar. No environment variable, timeout, credential
format or compatibility mechanism was added.

Shared edits outside the W8 directories:

- `cmd/sparkwing/runners.go` and `runners_test.go`: package/context API callers
  and final manual-start argv. No enrollment schema or authorization rewrite.
- Deleted `internal/agentservice/{agentservice.go,agentservice_test.go,leak_test.go}`
  after relocation.
- Deleted `install/service-install.sh`,
  `install/linux/sparkwing-runner.service.template`,
  `install/macos/com.sparkwing.runner.plist.template`, and
  `bin/service-install-test.sh`.
- `tests/acceptance/runner_service_w8_test.go`, `docs/self-hosting.md` and its
  generated mirror, plus this handoff.

The local-controller source docs and handoff now correctly say that
`wingd supervise` remains declared and dispatched. Its `wingd run` child lacks
the shared-controller factory. The earlier report's claim that the command
was removed was incorrect.

Friction remains native sockets and unavailable platform fixtures. Standards
matching rejects a snapshot without Git metadata; review matched standards on
the real tree and reviewed an immutable source snapshot. No Sparkwing binary
or pipeline ran. No Git metadata was written. All Go caches, temporary files,
review snapshots and cross-compile outputs stay under `/tmp/sw-astra-W8/`,
which is removed after checks and review finish. Changes are uncommitted for
the lead.

## Ninth session: Cloud publication and reconciliation

`Sync` preserves a suspended Job while its controller award remains live.
Another launcher can reconcile between suspended Job creation and bootstrap
publication without deleting the Job or releasing its dedicated node.
Suspension now uses the same controller, cancellation and deadline checks as
active execution. Controller errors or mismatched decisions preserve ownership
and reach the caller. A crashed publisher's resources retire after authority
ends or the Job deadline expires. Reconciliation does not resume publication.

`LaunchOne` confirms the exact award through `CloudControl.Sync` after Secret
creation and before activation. Cancellation, unavailable authority or a
mismatched response starts teardown before making the Job runnable. The shared
comparison checks all binding fields exactly, with timestamp instants compared
through `time.Time.Equal`. JSON transport can discard monotonic clock data or
change time-zone representation without changing the award. A nanosecond change
to any deadline or parent expiry remains a mismatch.

### Ninth-session evidence

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for runners and subpackages, cluster, engine, wingd and subpackages,
  and both acceptance packages.
- Full engine, runners and subpackages, and acceptance tests pass.
- Full launcher and acceptance race tests pass.
- `TestLauncherPublicationSurvivesConcurrentReconciliationW8Components` and
  `TestLauncherCancellationDuringPublicationW8Components` pass for plan, eval
  and work. These are fake-Kubernetes component cells, not native catalog cells.
- The new unit tests cover live, cancelled, unreachable and expired suspended
  Jobs with bootstrap retention/cleanup, mismatched retirement decisions,
  preactivation refusal, and timestamp transport identity. The publication and
  timestamp regressions ran red before their fixes.
- Independent review approved the corrected immutable source snapshot. Its
  crash-recovery overlay also verified retention during controller unavailability
  followed by cleanup at expiry. Review found an acceptance assertion that
  confused finalizer removal with activation. The corrected assertion checks
  `spec.suspend`, and the reviewer reran both full packages successfully.
- Commentcheck, sleepcheck, docs-mirror drift and `git diff --check` pass.

Checks use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, and `GOCACHE`, `GOTMPDIR` and
`TMPDIR` beneath `/tmp/sw-astra-W8/`. Go test package parallelism is at most four.
No additional native acceptance cell or T1 parked-test port is claimed.

### Ninth-session contracts, remaining work and shared files

No exported API or other area's contract changed. No callers need migration.
The existing `CloudControl.Sync` interface supplies both reconciliation and
preactivation checks. No new setting, duration, credential, persisted phase or
runtime interface was introduced. This session deletes **0 obsolete files and
0 legacy subsystem lines**. It removes the unconditional suspended-Job teardown
and shares the existing binding check.

The Cloud entrypoint contract gaps remain concrete. The Job builder emits
`--unit`, `--attempt`, `--source-job`, `--source-generation` and `--engine`, while
W9's registry omits them and requires `--job` for plan units. W3 also needs a
plan/eval executor boundary that performs the awarded unit without taking over
controller scheduling. The source-init/bootstrap handoff and W2/W9 exchange
transport must be defined before composing those entrypoints. This session
adds no substitute protocol or generic callback wrapper around those gaps.

The runner/controller factories, native managed controller/timer arguments,
Actions exchange, native update ownership, Windows SID transport, and deployed
Cloud probes/cleanup remain open as listed above. W8 remains incomplete. No new
human decision is requested.

Shared edits are only `tests/acceptance/launcher_publication_w8_test.go` and this
handoff. Product edits stay in `internal/runners/launcher`, including its source
documentation. Native Kubernetes, platform services and sockets remain outside
the evidence. Standards discovery succeeded despite its installed-binary
staleness notice. An initial installed `sparkwing info --for-agent` call preceded
loading the swarm brief's no-binary rule. No candidate or stateful Sparkwing
command followed. No Git metadata was written. Worker scratch is deleted after
the final checks and reviewer finish. The lead retains committing and landing.

## Tenth session: remaining contract audit

This continuation adds no runtime code. The remaining integration list above
now names the missing host-construction inputs, daemon Home/foreground grammar,
Windows peer-identity projection, deployment probe/cleanup mechanism and
exact-engine upgrade-verification invocation. A separate read-only audit reached
the same boundaries. Existing component tests already fake the declared ports.
Additional callback wrappers would leave their executable contracts undefined.

Whole-repository `go build -p 4 ./...` passes. Vet passes for cluster, runners,
engine, wingd and their subpackages, plus both acceptance packages. Full runner,
engine and component acceptance tests pass. Local lifecycle tests pass in wingd
and its client. All commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false` and
private caches and temporary directories under `/tmp/sw-astra-W8/`.

No native catalog cell was activated, no T1 test was ported, and no source
caller changed. This session deletes **0 obsolete files / 0 legacy-code lines**.
Only this handoff changes. Product behavior, contracts and release artifacts
are unchanged. W8 remains incomplete until the listed contracts and native
integrations are supplied. An initial installed discovery command preceded
loading the no-binary rule. No stateful Sparkwing command or Git write ran.

## Continuation: shared machine policy fallback

`Resolver.Index` reloads the verified machine cache before returning fallback.
Another process's newer policy advances its in-memory copy through the existing
rollback and retained-record checks. Cache-read failures join the refresh error.
The five-minute refresh interval and unlimited verified fallback age stay the same.

`TestEngineW8FallbackHonorsSharedMachineRevocation` reproduces an older resolver
allowing a revoked release after a second resolver persisted the revocation.
Both unavailable-origin and older-signed-policy cases failed before the fix and
pass afterward. The test uses public APIs, real cache files and a fake HTTP
transport. A permitted release still runs. The engine contract documents this
fallback rule. No types, interfaces, trust roots or configuration were added.

Whole-repository `go build -p 4 ./...` passes. Vet passes for cluster, engine,
runners and wingd, including their subpackages and both acceptance packages.
Full tests pass for engine, runners and their subpackages, and component
acceptance. Commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false` and private
caches and temporary directories under `/tmp/sw-astra-W8/`.

This continuation deletes **0 obsolete files / 0 legacy-code lines** and ports
no additional T1 tests. No native catalog cell was activated. Shared edits are
`tests/acceptance/engine_w8_test.go` and this handoff. The W8-owned engine contract
has a prose clarification. No external caller required migration. The remaining
integration and contract list above still applies, including W3 host construction,
W9 bootstrap/managed-unit grammar, native Windows identity and deployed Cloud
probes/cleanup. No other area's contract changed. Independent review and native
QA remain with the lead. No Sparkwing binary, pipeline or Git write ran.

Commentcheck and sleepcheck pass against HEAD, and `git diff --check` is clean.
The shared Git config has no local `user.name` or `core.worktree` entry.
Worker scratch is deleted after verification.

## Continuation: executable cache recovery

`Resolver.Cached` treats a directory at the executable path, or missing owner
execute permission on Unix, as a cache miss. Auto mode uses the existing verified
publication path to restore the accepted engine. Local mode returns `unavailable`
without a download. Windows keeps its native permission semantics. No public API,
setting, environment variable or recovery mechanism was added.

`TestExactEngineCacheCorruptionAndPolicyEnforcement` now checks byte corruption,
directory replacement and Unix execute-permission loss. Each case checks local
refusal without fetching, automatic recovery and the unchanged accepted identity.
The permission regression failed before the fix with a cache hit for a file that
could not execute, then passed. The W8 engine contract states the recovery rule.

Verification uses `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOCACHE`, `GOTMPDIR`
and `TMPDIR` under `/tmp/sw-astra-W8/`, with Go package parallelism at most four.

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for engine, runners, wingd and their subpackages, cluster and both
  acceptance packages.
- Full engine, runners and component acceptance tests pass. Native catalog
  cells remain skipped. No native or deployed integration evidence is claimed.
- Engine tests cross-compile for Windows/amd64. This does not test Windows ACLs.
- Commentcheck, sleepcheck and `git diff --check` pass.
- Independent read-only review found no correctness or simplification issue.

This continuation deletes **0 obsolete files / 0 legacy-code lines** and ports
**0 additional T1 tests**. No external caller migration was needed. Only the
engine implementation, its existing test, its W8-owned contract and this shared
handoff changed. No other area's contract changed.

The remaining integration list above still applies. Source acquisition and the
lease keeper still contain unimplemented final-contract methods. Host creation
and delegated node-route/replay inputs remain undeclared. W9's pod bootstrap
registry still omits the builder's unit, attempt, source-generation and engine
arguments and requires a job for plan units. Its daemon registry names
`--state-db` while the runtime uses `--home`. Shared local-controller services,
Actions exchange, managed units, Windows identity and deployed Cloud probes and
reservation cleanup remain open. No new human decision is requested.

No Sparkwing binary, pipeline or Git write ran. The shared Git config has no
local `user.name` or `core.worktree` override. Standards discovery succeeded with
its installed-binary staleness notice. Worker scratch is deleted after the final
checks. The lead retains committing and landing.

## Continuation: bootstrap admission and cleanup

`LaunchOne` checks the admitted bootstrap Secret before activation. Altered
bytes, extra data, mutability, type, Job ownership or deletion state refuse
execution without exposing credentials or reading Secrets. Added server
metadata remains allowed. The source documentation describes this behavior.

A refused renamed Secret uses the existing teardown and `Sync`. One private
Job annotation records its name, UID and owning Job UID. Deletion uses the
Secret UID as a precondition. A failed deletion retains the Job finalizer and
reservation, so a restarted launcher retries the same target. A replacement
Secret is preserved. The launcher checks the admitted annotation update and
refuses a cleanup record injected during initial Job admission. Invalid or
wrong-Job cleanup records cannot authorize deletion.

The annotation is a W8-local mechanism. Immediate deletion alone lost its
target after a transient failure, which independent review reproduced. Reusing
the existing Job and reconciler preserves ownership without another service,
callback, setting, credential or public contract. No other area's contract
changed.

Secret creation and recording a renamed identity are separate Kubernetes
writes. If recording fails, the launcher attempts immediate UID-fenced cleanup,
retains the suspended Job and reservation, and reports the target name and UID.
If cleanup also fails, an operator must remove that Secret. A crash between
creation and recording has the same residual when admission both renames the
Secret and strips its Job owner. No atomic cross-object guarantee is claimed.

### Verification

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for cluster, engine, runners and wingd, including their subpackages
  and both acceptance packages.
- Full engine, runner and component acceptance tests pass. Final launcher and
  acceptance tests also pass under `-race -p 2`.
- `TestLauncherRefusesChangedBootstrapW8Components` passes for plan, eval and
  work. `TestLauncherRetriesRenamedBootstrapCleanupW8Components` covers retry
  after restart and UID-conflicting replacements. Both defects ran red before
  their fixes. `TestLauncherReportsBootstrapRecordFailureW8Components` covers
  failed and silently dropped writes, with immediate cleanup success/failure.
- Component fixtures enforce UID preconditions and retain deleting Jobs through
  finalization. They establish no native Kubernetes or Cloud evidence.
- Independent review of the final immutable snapshot closed its findings and
  found no concrete regression. Its adapted mutation, restart and malformed
  reference probes and focused component tests pass.
- Commentcheck, sleepcheck, docs-mirror drift and `git diff --check` pass.

Checks use `GOWORK=off`, `GOFLAGS=-buildvcs=false` and private caches and temporary
directories under `/tmp/sw-astra-W8/`. Test package parallelism is at most four.
No native catalog cell was activated. No additional T1 parked test was ported.
This continuation deletes **0 obsolete files / 0 legacy subsystem lines**.

### Remaining integration and shared files

W8 remains incomplete. The existing integration list still applies: W3 host
construction, delegated node routes, generator replay and keeper/source
composition; W9 bootstrap and daemon Home/foreground grammar; W1/W2/W3 shared
controller services; Actions exchange; native update ownership and managed
units; Windows SID transport; and deployed Cloud probes and node cleanup.
The contract interfaces still have component fakes. No new human decision is
requested, and no other area's contract is changed.

Shared edits are `tests/acceptance/launcher_publication_w8_test.go` and this
handoff. Product and source-documentation edits stay in
`internal/runners/launcher`. No external caller migration was needed. The lead
retains committing and landing.

The initial installed discovery command preceded loading the swarm brief's
no-binary rule. No stateful Sparkwing command, candidate, pipeline or Git write
followed. The shared Git config has no local identity or worktree override.
Native fixtures remain unavailable. Standards discovery succeeds with its
installed-binary staleness notice. Worker scratch is removed after final review.


## Continuation: amendment merge and selected-engine installation

The ten merge conflicts have resolved working-file contents. The four legacy
modify/delete paths stay deleted, retaining the earlier removal of 928 lines
from the incoming versions. No additional legacy subsystem was deleted. No Git
write ran, so the lead must stage all resolutions and finish the merge.

[The amendment audit](W8-AMENDMENTS-1.md) covers all 26 amendment rows assigned
to W8. It records actions, open contracts and native integration points.

Resolved.InstallSDK now rechecks the checkout pin and cached signed executable,
uses the selected engine's hidden stdin/result transport, applies procenv.Install
and masks proxy diagnostics. The hidden CLI route skips update/version-state
startup hooks. The native adapter checks its build version, uses the existing
local Go binary cache and runs TypeScript installation from the npm lockfile.
The TypeScript result contains Node and main without a preload. The adapter lives
in a child package because importing the cache into the engine identity package
creates an import cycle through store and runspec.

Runner hosting and Cloud work now accept ordinal zero before Start. They preserve
that ordinal rather than inventing the first execution attempt. Negative values
still fail. TestInitialAttemptW8Components covers both boundaries. The merged
runner enrollment test now uses runners/service and its context-aware Exec. The
cluster runner uses userconfig.ParseBudget after W6's package move.

### Verification and limits

- Whole-repository go build passes with -p 4.
- Vet passes for engine, runners, wingd and their subpackages, cluster, both
  acceptance packages and cmd/sparkwing.
- Full engine, runner and component acceptance suites pass. Selected local
  lifecycle tests, cluster tests excluding its socket listener, and CLI enrollment
  tests pass. The new install tests also pass under the race detector.
- The install adapter cross-compiles for Windows/amd64. Native Windows behavior
  remains unverified. Tests substitute Go/npm dependency tools and signed engine
  executables. No Sparkwing binary, pipeline or real dependency installation ran.
- Engine commentcheck and git diff --check pass. The full comment scan reports a
  merged controllerwire/contracts.go comment outside this continuation. The full
  sleep scan reports two merged limits/deadline_test.go wall-clock cases. A scoped
  sleep scan cannot run because its external-boundary registry expects controller
  test paths under the repository root. These are reported, not counted as passes.
- The independent snapshot review confirmed two open findings: standard Windows
  npm.cmd invocation and ambient-workspace hashing despite GOWORK=off. Their
  required shared contracts and reproduction are in the amendment audit. The
  request-field ambiguity finding was fixed and its tests pass. Review is not clean.

No native acceptance catalog cell was activated and no additional T1 test was
ported. Newly passing component cells are initial_attempt/runner and
initial_attempt/launcher. Full placement, source/host, local-store/controller,
Actions, managed-unit, Windows SID/bridge and Cloud deployment proofs remain open.

Shared edits beyond W8 packages are cmd/sparkwing/main.go and sdk_install.go for
hidden dispatch, runner_enrollment_test.go for the service migration, cmd/sparkwing/runners.go, runners_test.go and secret_local_test.go for merge
resolution, internal/limits/deadline.go, tests/acceptance/initial_attempt_w8_test.go
and these design documents. Existing orchestrator deletions remain. No other area's
contract was changed. The two requests above need the owning areas' contracts.

Checks use GOWORK=off, GOFLAGS=-buildvcs=false and private caches/temp directories
under /tmp/sw-astra-W8, with test package parallelism at most four. The shared Git
config has no user identity or core.worktree override. The lead retains staging,
committing, gates and landing. Worker scratch is removed at the end.


## Continuation: native managed units

`NewManagedUnitManager` now implements the existing home-bound managed-unit
port. Linux controller services run foreground `wingd run --stay --home` from
login with restart on failure. macOS uses a LaunchAgent with the same argv.
Neither uses socket activation. Windows refuses native installation and names
`--stay`. Read-only inspection supplies actual argv, captured Home and release
metadata to status and doctor without opening a store.

The native schedule renderer now invokes `__tick --home <absolute-home>`.
It no longer restores `crons tick`, SPARKWING_HOME plumbing or legacy timer
retirement. Refresh updates existing controller and schedule units while keeping
their captured settings. It does not install absent units. Installed controllers
restart. Installed macOS timers bootstrap again after an interrupted update.
Schedule pause remains in the store. Linux timers keep their enabled state.

Native units, timers and runner services share argument encoding, file
inspection, private atomic publication and registration-source checks in
`internal/runners/service/unitfile`. This extraction removes duplicate helpers.
Source checks happen before mutations by native unit name or launchd label.
They refuse another loaded file, foreign files, different homes and symlinks.
Systemd commands use its literal ':' prefix because executable paths and argv
must preserve dollar signs. Environment assignments keep their own semantics.
Release metadata is encoded because a valid prerelease may contain '--', which
cannot appear inside an XML comment.

Doctor now checks the complete policy argv and absolute captured Home. The
source lifecycle document and runner-service document describe these boundaries.
The all-26-row amendment audit remains in W8-AMENDMENTS-1.md. A1-41 now records
this implementation and the remaining CLI/native proofs.

### Deletion and test ports

This continuation deletes **99 legacy timer-retirement and home-detection lines**,
including their obsolete test. It removes no whole file. Replaced argument and
file helpers are extracted into one active owner. No additional parked T1 test
is claimed as ported. The earlier sizing port remains the W8 inventory port.

### Verification

Every Go command used GOWORK=off and GOFLAGS=-buildvcs=false, with GOCACHE,
GOTMPDIR and TMPDIR below /tmp/sw-astra-W8. Test package parallelism was four
or less.

- Whole-repository `go build -p 4 ./...` passed.
- Vet passed for crontimer, runners and subpackages, wingd and subpackages,
  engine and subpackages, cluster, crons, CLI, localws and acceptance tests.
- Full engine, sdkinstall, runners, launcher, runner-service, shared unitfile,
  crontimer and component acceptance package tests passed.
- Wingd `^TestManaged|^TestLocal` and client `^TestLocalLifecycle` tests passed.
- Cluster passed with only TestStartMetricsListener_ServesMetrics excluded.
  The socket prohibition was already established in this sandbox.
- Full crontimer, runner-service, unitfile and acceptance race suites passed.
  Wingd `^TestManaged|^TestLocal` race tests passed.
- Wingd tests cross-compiled for windows/amd64. This is not Windows execution.
- TestManagedUnitsW8Components exercises native files and fake service managers
  on Linux/macOS. The full acceptance package passed. **No native catalog cell
  was activated.** Login, crash, drain, live OS registration and Cloud isolation
  still require their platform drivers.
- Independent review found literal-dollar handling, registration ownership,
  retry-after-bootstrap-failure and XML-sensitive version issues. Regressions
  cover them. Final bounded review reported no remaining actionable finding.
  The reviewer also used systemd-analyze verify against a real fixture path with
  dollar signs and spaces. No service or Sparkwing binary was executed.
- `git diff --check` passed. All ten merge-conflict paths retain resolved file
  contents, including four deletions. The index remains unmerged for the lead.

### Checks that did not pass

The broad internal/crons suite still has fixtures using scalar on.schedule,
where W6 now requires a sequence, and pushed rows missing the final repo_key.
Some fixture Git commits initially attempted inherited GPG signing. A rerun of
TestHealthAcceptsAHostTickingFromItsOwnScheduler with fixture signing disabled
still failed at YAML decoding before reaching the changed timer health call.
No scheduler or store implementation was changed to hide those failures.

Repository commentcheck reports only the merged HereBearer comment at
internal/controllerwire/contracts.go:47. Sleepcheck reports only the merged
wall-clock checks at internal/limits/deadline_test.go:58 and :66. None is in this
continuation's unit-management changes. These checks remain failed for the lead
and their owning workers. A broad wingd test filter also selected two older
TestRefreshRunning socket tests, which could not run in this sandbox. The
narrowed local/managed suites pass.

### Contracts and remaining integration

The frozen ManagedUnitManager signatures are unchanged. The new W8-local
constructor accepts the existing crontimer.Host installation inputs. No other
area's contract changed. The shared helper package replaces duplication because
runner services, timers and controllers need the same native file semantics.
Undoing this continuation removes the constructor and restores the previous
unbound unit-management boundary. No environment variable, customer setting,
trust root, timeout or compatibility command was added.

W9 still needs to connect dashboard/schedule installation, update and doctor to
this manager and to the foreground runtime. This manager covers all unit kinds
for one captured home. An installer that updates several homes must invoke the
corresponding home-bound managers. Local controller/store/consumer composition,
runner factories, failed-enrollment cleanup, Windows schedules and native/Cloud
proofs remain open. The declared source.Acquire, lease.Keeper.Run and
client.NewEngineClient still panic. W3 host-construction, delegated-route and
replay contracts remain required. The two earlier cross-area install findings
(shared Windows npm invocation and workspace-off binary-cache hashing) remain
unchanged. No new human product decision is requested.

### Shared files touched in this continuation

The native timer implementation is adjacent W8 lifecycle work in
internal/crontimer. Removed context-free timer calls were migrated in
cmd/sparkwing/crons.go, cmd/sparkwing/crons_install.go,
cmd/sparkwing/crons_test.go, internal/crons/health.go and
internal/crons/crons_test.go. These are the only production callers requiring
migration. pkg/localws/crons.go keeps compiling with the updated DefaultExec
function and needed no edit. tests/acceptance/runner_service_w8_test.go now
includes registration preflight evidence. The new managed-unit component test,
W8 amendment audit and this handoff are also included.

The lead retains staging, the merge commit and landing. No repository Git write
or Sparkwing binary invocation ran. Worker scratch is deleted after the final
verification and reviewer completion.

## Continuation: runner file credentials and enrollment failure cleanup

Amendment A1-49 now has component implementations for file loading and failed
enrollment cleanup. Start defaults to userconfig.ReadConfig, loads agent-token
once and supplies a Bearer callback to runner composition. Machine files use
the shared credentials.ReadPrivate/fssecure policy. Warm services keep the
existing Kubernetes projected-file reader. Actions uses its exchanged credential
in memory. The callback honors cancellation and contains no printable bearer.

Enrollment captures absolute configuration and credential paths, including a
relative SPARKWING_CONFIG override. It writes repos and credentials_dir and
leaves claim-holder identity to controller registration. Failure attempts to
revoke only the new token. Successful forced enrollment leaves the previous
token and its files untouched.

After configuration or service failure, cleanup removes the new credential
file/directory and restores prior runner settings through userconfig.Update.
Unrelated settings and later enrollments survive. A decoded snapshot retains
YAML aliases, explicit empty collections and absent fields. A new service file
is removed even when the manager becomes unreachable. Removal is bounded to
the same regular-file inode. Replacement files and prior registrations survive.
Manager/revocation failures remain visible. Native loaded-process cleanup after
a manager outage remains a platform acceptance obligation.

No whole files were deleted. The duplicated inline credential reader removed
13 lines. The old configuration writer and keep-live-on-failure path were
replaced. Superseded enrollment documentation was removed from self-hosting.
No additional T1 inventory test was ported.

### Evidence

Commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and private
GOCACHE, GOTMPDIR and TMPDIR under /tmp/sw-astra-W8. Package parallelism was
at most four.

- Final whole-repository go build -p 4 ./... passed.
- Vet passed for CLI, cluster, runners and subpackages, engine and sdkinstall,
  wingd and subpackages, credentials, agentconfig and acceptance tests.
- Full runners, launcher, runner service, unitfile, engine, sdkinstall,
  credentials, agentconfig and tests/acceptance suites passed.
- Cluster passed with TestStartMetricsListener_ServesMetrics excluded because
  its native TCP listener is unavailable in this sandbox. Local/managed wingd
  component tests passed.
- Focused race tests passed for file credentials, startup, installed argv,
  enrollment failure and rollback. Final CLI/acceptance race tests include the
  alias, empty-list, offline-manager and changed-working-directory regressions.
- Cluster tests cross-compile for Windows/amd64. This is not native Windows
  execution evidence.
- TestRunnerCredentialW8Components passes. TestRunnerServiceW8Components now
  reads actual private configuration/credentials through installed argv rather
  than faking configuration. No native catalog cell was unskipped.
- Independent immutable-snapshot review reproduced alias loss, empty-list loss,
  offline-manager file residue and relative credential paths. Their regression
  tests ran red, then green. Final independent overlays passed all original
  probes and the enrollment filter, with no remaining finding.
- Documentation mirror and git diff --check passed.

Source checks retain the three earlier findings: controllerwire/contracts.go:47
and limits/deadline_test.go:58/:66. None is in this continuation. Full HTTP
enrollment/revocation tests require sockets and were not run. Cleanup tests use
real private files and fake service-manager/revocation boundaries.

### Shared files and remaining integration

Shared production edits are cmd/sparkwing/runners.go, the new
cmd/sparkwing/runner_config.go, internal/credentials/credentials.go and the
minimal internal/agentconfig/agentconfig.go reader migration. Shared tests are
cmd/sparkwing/runners_test.go and runner_enrollment_test.go. Source docs are
docs/self-hosting.md and CHANGELOG.md, with generated copies at
pkg/docs/mirror/self-hosting.md and pkg/docs/changelog.md. W8 cluster startup and
tests, runner credential files, the runner-service source doc, two acceptance
components and the W8 audit/handoff complete the change.

No other area's frozen contract changed. RunnerOptions.Bearer and FileBearer
are local composition helpers. ReadPrivate extends the existing credential
owner instead of duplicating its policy. Rollback uses the existing settings
lock and validator. Undo removes this continuation and restores the previous
unbound credential/failure path. No setting, environment variable, timeout,
trust root or compatibility alias was added.

Real integration still needs final W1/W2 capability enrollment/revocation,
W9 NewEngineClient transport and W3 source/keeper/host construction with bound
storage. W3 host constructors, delegated node routes, generated-code replay
and local shared-controller contracts remain required. Launcher wire adaptation,
runner/Cloud factories and bootstrap host composition remain open. Native
Linux/macOS/Windows lifecycle, Cloud node/network teardown and full real-repo QA
remain unprovided. The two SDK-install contract findings above remain open.
No new product question is raised.

All ten merge-conflict contents remain resolved, including the four deletions.
The index remains unmerged because the brief prohibits Git writes. No Sparkwing
binary, Git metadata write or external service ran. The lead must stage the
resolved files and finish the merge. Git configuration retains the default
author with no local core.worktree override. Worker scratch is removed after
verification and reviewer completion.

## Continuation: Cloud controller adapter

`NewControllerControl` now implements `CloudControl` against the shared wire
client's Claim and SyncLauncher methods. It captures observed capacity/labels
and requests the configured Cloud unit kinds in one call. The controller supplies
team, holder, generation, resources and the one-time bootstrap. Claim errors
return without retry. Plan source uses source.ForRequest. Eval/work source uses
source.ForExecution with the accepted spec and matching engine.

Jobs now persist the controller UnitID and send it with the reconstructed Job
name and node UID during sync. No process-local award registry is required.
Sync requires a complete, consistent decision for every observation and refuses
returned credentials. An unavailable controller preserves teardown ownership.
A removal decision retires the persisted Job and bootstrap through the existing
container/reservation cleanup path.

Eval's terminal source generation remains separate from its own live lease.
Binding.Generation and SourceGeneration identify the source. Expansion carries
the eval unit's ID, holder and generation. Both fences survive dispatch and sync.
Job names include unit ID and source identity so distinct awards cannot alias
one Kubernetes object under the same run/job/live generation.

The W8-local contract additions are CloudJobBinding.UnitID and
CloudJobObservation. CloudControl.Sync takes observations. CloudControl.Claim
and Launcher no longer accept a caller-selected holder. Four lines containing
the old holder field/check were deleted, and the former signatures were replaced.
No whole file or additional legacy subsystem was deleted. Previous legacy
removals and all ten conflict resolutions remain intact. No additional T1
inventory test was ported.

The final client still needs EngineOptions.Launcher=true. Each Cloud award must
supply resolved CPU/memory through ClaimedUnit.Execution. These are server facts,
never launcher defaults or admission inference. Source projections and the HTTP
client remain other areas' panic stubs. Their interfaces are tested with fakes,
not replaced with private implementations. LauncherFactory, bootstrap exchange,
shared host/controller composition, Cloud probes/node cleanup and native platform
proofs remain open. The Windows npm and workspace-off cache-key requests from the
previous continuation remain unchanged. No other area's contract changed and
no new human product decision is requested.

Shared files changed in this continuation are
tests/acceptance/launcher_boundary_w8_test.go,
tests/acceptance/launcher_publication_w8_test.go,
tests/acceptance/launcher_w8_test.go and the new
tests/acceptance/launcher_controller_w8_test.go. These migrate the removed
signatures and add the restart/transport component proof. All production changes
are inside internal/runners/launcher. CLOUD-JOBS.md and the W8 audit/handoff
record the local contract changes. There is no new public command or setting.

### Evidence for the Cloud adapter

Commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and private
GOCACHE, GOTMPDIR and TMPDIR under /tmp/sw-astra-W8, with -p 4 throughout.

- Final whole-repository go build passed.
- Vet passed for cluster, runners and subpackages, engine and sdkinstall,
  wingd and subpackages, tests/acceptance and internal/acceptance.
- Full runners/engine/component acceptance suites passed. Cluster passed with
  its existing native TCP listener test excluded. Local/managed wingd tests passed.
- Focused controller/launcher and launcher acceptance tests passed under -race.
- TestLauncherControllerW8Components passes persisted restart, live-award adoption,
  transport failure and authorized teardown. No native catalog cell was unskipped.
- Claim tests cover plan/eval/work, zero attempt, captured observations, incomplete
  awards, cancellation and uncertain one-time delivery. Sync tests cover complete
  decisions, refused credential material, both eval fences and changed holders.
- Independent immutable-snapshot review reproduced three eval-lease failures.
  The corrected fixture and independent overlays failed before the fix and passed
  afterward. The Job-name collision regression also failed before its fix.
  Final review of the adapter and naming delta found no remaining defect.
- Scoped launcher commentcheck and git diff --check passed. No new global source
  check was claimed. The earlier merged controllerwire/limits findings remain.

The lead retains staging and the merge commit. This continuation verified all
six resolved file contents and four retained deletions without writing Git
metadata. Local Git config has no user identity or core.worktree override.
No candidate runtime, external service or deployment was exercised. Initial
bootstrap discovery queried installed sparkwing info only. Scratch is removed
after all checks and the independent review finish.

## Continuation: local API peer identity

`APIConn.PeerIdentity` replaces `PeerUID`. The shared controller can consume
the existing `LocalPeerIdentity` UID/SID projection. The connection copies its
input identity and returns a separate UID copy on every read. Neither the
transport input nor a controller consumer can mutate the retained identity.
The API listener checks `LocalSubject` before yielding a connection. A missing
kernel identity closes the connection and reports the refusal.

The security invariant is that only transport-verified identities matching the
controller account reach the API. The property test generates platform,
UID/SID, presence and equality combinations, then attempts to mutate accepted
identity snapshots. A separate in-memory listener test checks actual refusal,
connection closure and diagnostics without opening a socket.

The declared local-controller platforms remain Linux, macOS and Windows.
`LocalSubject` refuses other OS names. The low-level FreeBSD UID reader does
not add FreeBSD to that supported-platform contract. Native Windows still has
no client-SID reader and refuses access. The SID fixture checks the projection
and policy, not Windows authentication or transport.

This is a W8-local contract amendment. It closes the controller-facing UID/SID
projection request. No other area's Go caller uses the removed method. The
existing `internal/wingd/apisock_test.go` recorder is migrated. The three
changed existing Go files replace **15 old lines**. No whole file or additional
legacy subsystem is deleted. No further T1 inventory test is claimed as ported.

### Evidence

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W8, with package parallelism at
most four. The new tests first failed because the typed connection API was
absent, then passed after implementation.

- Whole-repository `go build -p 4 ./...` passed.
- Vet passed for wingd and subpackages, cluster, runners and subpackages,
  engine and sdkinstall, tests/acceptance and internal/acceptance.
- Full runners, launcher, runner-service, unitfile, engine, sdkinstall and
  tests/acceptance suites passed. internal/acceptance has no runtime tests.
- Cluster passed with TestStartMetricsListener_ServesMetrics excluded because
  the earlier continuation established that its native TCP listener cannot bind
  in this sandbox. No socket proof is claimed.
- Focused local-controller, peer-policy and new identity tests passed under
  `-race`. The new property fuzz test passed 41,578 generated cases in ten seconds.
- Wingd tests cross-compiled for windows/amd64. This is no native Windows proof.
- Scoped commentcheck, sleepcheck and `git diff --check` passed.
- Independent review used immutable copies and found no concrete correctness
  or authorization defect. Its Simplify review accepted the small identity
  validation/copy helpers. Overwing's staleness notice did not prevent standards
  discovery or loading.

`TestLocalControllerW8Components` and the existing W8 component acceptance
suite remain green. No native catalog cell was unskipped.

### Remaining integration, shared files and assumptions

The real-integration points are unchanged except for the completed peer projection:
W9's engine HTTP client; W3's source, keeper, host constructors, delegated node
routes and replay; W1/W2's local controller/store/consumer and capability
composition; runner/launcher/Cloud bootstrap factories and source handoff;
native managed-service lifecycle; Windows client-SID transport, schedules and
SDK bridge; and deployed Cloud probes/node cleanup. This checkout still has
panic stubs in NewEngineClient, source.Acquire/ForRequest/ForExecution,
lease.Keeper.Run, final controller operations and final store claims. Their
declared interfaces remain covered with fakes at W8's existing boundaries.

Other-area contract requests remain shared native npm invocation, workspace-off
binary-cache hashing, host construction/delegation/replay, shared local service
construction/activity and final bootstrap grammar/handoff. No new request or
human product question is added by this continuation.

Shared edits are design/rewrite/contracts/c4-surface-config.md and the W8
audit/handoff. All production/test edits stay in internal/wingd. The source
LOCAL-CONTROLLER.md records the accessor and native limitation. No public CLI,
environment variable, setting, dependency, service or compatibility alias is
added. Undoing the local contract amendment restores the UID accessor and its
sole test caller. No Git metadata write or Sparkwing binary invocation ran.
Git configuration has no local identity or core.worktree override. Scratch is
removed after verification and review.

## Continuation: runner execution authority

The hosting invariant is that only directly usable trigger, here and work awards
with matching unit purpose and claim fences reach preparation, and execution
cannot outlive its binding, owner or parent credential deadline. Hosting checks
delivery and purpose before renewing or preparing source. It selects the earliest
of those three declared deadlines before confirmation, keeper startup and host
preparation. Go context propagation retains an earlier caller deadline. An absent
deadline stays unbounded. Lease expiry remains renewable, and per-attempt deadlines
remain the host's responsibility. Here reclaim ignores the initial claim window.

The acceptance regressions first demonstrated five invalid purpose/delivery
awards reaching preparation, expired owner/parent authority reaching renewal,
and the wrong execution deadline when an owner or parent deadline was earlier.
They pass after the small in-place changes. FuzzRunnerAuthorityW8 generates
unit/purpose/delivery combinations, generations, attempt ordinals, and absent,
zero, expired or future authority bounds through Hosting.Host. The controlled
clock tests exercise execution cancellation and keeper-before-source cleanup.
Shared host and keeper implementations remain fakes of their declared ports.

The production change replaces three old lines. Migrating the existing host
fixture replaces one more. This continuation removes **4 old lines / 0 whole
files**, with no additional legacy subsystem remaining behind a shim. No parked
T1 inventory test is newly claimed as ported. The initial-attempt acceptance
fixture now includes its declared direct-work purpose and delivery.

### Verification and review

Commands use GOWORK=off, GOFLAGS=-buildvcs=false and GOMAXPROCS=4, with GOCACHE,
GOTMPDIR and TMPDIR under /tmp/sw-astra-W8 and package parallelism at most four.

- Whole-repository `go build -p 4 ./...` passed.
- Vet passed for cluster, runners and subpackages, wingd and subpackages, engine
  and sdkinstall, tests/acceptance and internal/acceptance.
- Full runners, launcher, runner-service, unitfile, engine, sdkinstall and
  tests/acceptance suites passed. internal/acceptance has no runtime tests.
- Focused hosting/authority/deadline/initial-attempt tests passed under `-race`.
  The property fuzz test passed 10,866 generated cases in ten seconds.
- Cluster passed with the previously established socket-only metrics test
  excluded. Focused local runtime, managed-unit and local-lifecycle tests passed
  in wingd and its client. No socket or native platform proof is claimed.
- Commentcheck and sleepcheck scoped to this continuation passed. The required
  `commentcheck -base main .` still fails only on the existing HereBearer comment
  in internal/controllerwire/contracts.go:47. Sleepcheck against main still fails
  only on the existing wall-clock checks in internal/limits/deadline_test.go:58
  and :66. Neither owning area was edited to suppress those findings.
- Independent immutable-snapshot review found no blocking correctness issue and
  accepted the small validation/deadline changes. It inspected the new untracked
  test separately from the tracked diff. It did not execute tests.

TestRunnerAuthorityW8Components and TestRunnerDeadlineW8Components provide new
component acceptance evidence. No native catalog cell is unskipped. No Sparkwing
binary, external service or Git write ran. The source RUNNER-SERVICE.md records
the authority bounds. No generated reference consumes this internal document.

### Contracts, shared files and remaining integration

No contract or product-policy change is needed for this continuation. These
checks consume existing C2/C3 award fields. No interface, setting, environment
variable, timeout value, trust root or compatibility path is added.

The only shared Go edit is tests/acceptance/initial_attempt_w8_test.go. The new
tests/acceptance/runner_authority_w8_test.go and the W8 amendment audit/handoff
are also shared review artifacts. Production and existing package-test edits
remain in internal/runners. No other-area production caller required migration.

Real integration still needs W9's engine HTTP client, W3's source/keeper/host
construction, delegated node routes and generated-code replay, W1/W2's local
controller/store/consumer composition, and the runner/launcher/bootstrap factories.
Native service lifecycle, Windows SID transport/schedules/SDK bridge and deployed
Cloud probe/node-cleanup proofs remain open. Existing shared npm invocation,
workspace-off cache hashing, local activity/store errors and bootstrap grammar/
source-handoff requests remain unchanged. No new human product question is added.

Overwing's installed binary remains ten commits behind source but returned the
applicable standards. Git config has no local author or core.worktree override.
Scratch is removed after all checks finish. The lead retains staging and committing,
including the new acceptance test that is absent from plain tracked diffs.

## Continuation: shared native npm invocation

The install adapter, native lockfile upgrade and dependency-cache query now use
internal/engine/npmcmd.Command. Windows npm command shims run Node with the
adjacent installed npm-cli.js. Native executables and Unix keep direct argv.
No shell interprets arguments. Each caller supplies its environment and uses
the existing gotoolchain.Run process-group runtime for cancellation.

The removed private depcache.npmCacheCommand was 17 lines. Its four tests moved
to npmcmd and now cover install arguments, missing npm/node and case-insensitive
executable extensions. The Windows SDK-install fixture supplies npm.cmd plus
node.exe instead of npm.exe. The shim exits unsuccessfully if run, so a native
Windows execution will prove that the adapter takes the Node path.

The upgrade tool now supplies the shared masker to procenv.Install. The previous
implementation refused a credential-bearing proxy before launching a native tool.
The old-source overlay reproduces that refusal. The new implementation runs the
tool and masks both captured streams before returning a failure. The property
test generates proxy credentials and checks that raw values, full URLs and base64
encodings never appear in returned diagnostics. A separate property test checks
that generated npm arguments remain separate, caller-independent argv values.

TestEngineNativeNPMW8Components installs a local SDK tarball with the real Node/npm
tools in offline mode, then executes the returned Node entry. It runs on Linux
here. It establishes native npm invocation at the install boundary, not Windows
execution, protocol bridging, host composition or a native acceptance-catalog cell.

### Contracts, shared edits and remaining integration

This closes the shared npm invocation request in the amendment audit at the
component boundary. npmcmd.Command is a W8-local addition. The small child package
is necessary because importing engine from depcache creates a cycle through
procenv, secrets and the SDK. Restoring the private helper would leave the install
and upgrade callers without a shared implementation. Undo removes this helper
and restores those three callers. No new environment variable, setting, timeout,
trust root or compatibility command is added.

The only other-area production edit is internal/depcache/depcache.go, which removes
the private helper and migrates its caller. internal/depcache/depcache_npm_test.go
migrates its remaining callers and moves the helper tests. The new acceptance
file, W8 handoff/audit and c3-engine-version.md are shared test/design artifacts.
Production changes otherwise stay in internal/engine. Existing Go files lose
98 old lines in total. No whole file is deleted and no additional T1 inventory
test is ported.

The binary-cache owner still needs an explicit workspace-off key operation.
W8 has not changed that other area's contract or added a second hasher. Remaining
real integration is W9's engine HTTP client and notices/verifier wiring; W3 source,
keeper, host construction, delegated routes and replay; W1/W2 local controller,
store, consumer and capability composition; runner/launcher/bootstrap factories
and source handoff; Windows SID transport, schedules and SDK bridge; native managed
service lifecycle; and deployed Cloud probes/node cleanup. These declared ports
remain exercised with W8's existing fakes. No new product question is raised.

### Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W8/cache, GOTMPDIR=/tmp/sw-astra-W8/gotmp and
TMPDIR=/tmp/sw-astra-W8/tmp. Package parallelism is at most four.

- Whole-repository `go build -p 4 ./...` passed.
- `go vet -p 4 ./internal/engine/... ./internal/depcache ./tests/acceptance`
  passed.
- Full `go test -p 4` passed for internal/engine and its subpackages,
  internal/runners and its subpackages, and tests/acceptance. internal/acceptance
  has no runtime tests. The new offline npm component ran, rather than skipped.
- Full internal/depcache testing could not finish: the existing
  TestRemoteDepCacheMissSaveHitCycle panicked because httptest cannot bind a TCP
  socket in this sandbox. Focused TestResolveNpmCache and TestDefaultNpmCache
  tests passed. No socket fixture was changed or suppressed.
- Focused `-race` tests passed for npmcmd, native upgrade proxy/cancellation,
  SDK installation, depcache npm resolution and the new acceptance component.
  Filters are TestNativeDependency, FuzzNativeDependency, TestNPMCommand,
  FuzzNPMCommandArguments, TestRunSDKInstall, TestResolveNpmCache,
  TestDefaultNpmCache and TestEngineNativeNPMW8Components.
- FuzzNPMCommandArguments passed 164,764 generated cases in five seconds.
  FuzzNativeDependencyDiagnostics passed 149 generated cases with native fixture
  processes in its five-second fuzz window. Both used two workers.
- Tests cross-compiled for windows/amd64 in engine, npmcmd, sdkinstall, depcache
  and tests/acceptance. No native Windows execution is claimed.
- Required `commentcheck -base main .` still fails on the existing W9 comment in
  internal/controllerwire/contracts.go:47. The scoped `-base HEAD` check passes
  across nine Go files. Scoped sleepcheck and `git diff --check` also pass.

Source documentation is c3-engine-version.md. No generated documentation consumes
that design source. Own diff inspection covered the import-cycle boundary,
caller cancellation ownership, masking and moved tests. This continuation has
no independent review; the lead retains that review and committing. The new
untracked files must be included in that review and staging.

No Sparkwing executable, service, deployment or Git metadata write ran. Native npm
ran offline. Local Git configuration has no author or core.worktree override. The installed
Overwing reports ten commits of staleness but returned all applicable cards.
Worker scratch is deleted after verification. No native catalog cell is unskipped.

## Continuation: integration merge and award-bound controllers

All five new modify/delete conflicts retain their W8 deletions. Incoming edits
only update obsolete routes, reconciliation and matcher calls. The three
orchestrator wingd files and two warmpool files are absent, preserving deletion
of 2,046 incoming lines. The lead must stage these paths because the brief
forbids worker Git writes. No conflict markers remain in working files.

Hosting replaces its generic Renew callback with the shared controller factory.
It binds EngineOptions.Bearer to one award, uses Controller.Renew for confirmation
and the keeper, and passes the same controller as the third argument to
PrepareRun/PrepareNode. The root client stays in the claim loop. A cancelled
bearer lookup returns no credential. Watch updates reach the existing observer.
Keeper renewal does not start execution because the shared host owns that
transition and W1 refuses executing renewal of an unstarted job.

The security invariant is that each unit's renewal and preparation use its own
award-bound client. TestRunnerControllerW8Components covers trigger/job preparation,
confirmation, watches and keeper-before-source cleanup. The concurrent mixed-claim
test keeps both clients live before checking their separate bearers. The fuzz
property generates bearer/unit identities and cancelled credential lookups.
Controller connection, missing-client and confirmation failures stop before source
preparation. Existing initial-attempt, authority and deadline components use the
new API. No native catalog cell or additional T1 inventory port is claimed.

This is a W8-local signature change. It replaces one injectable callback with
the shared client factory because the old callback left credential selection
outside the common hosting boundary. It adds no wrapper, package, configuration
key, process environment variable, timeout, trust root or compatibility path.
Undo restores Hosting and its callers together. Source documentation is
internal/runners/service/RUNNER-SERVICE.md. No generated reference consumes it.

Shared Go files are tests/acceptance/initial_attempt_w8_test.go,
tests/acceptance/runner_authority_w8_test.go and the new
tests/acceptance/runner_controller_w8_test.go. Production edits stay in
internal/runners/hosting.go. Other-area production APIs needed no caller migration.
The existing Go files replace 45 old lines. No additional legacy subsystem is
removed. The five merge deletions preserve earlier completed cuts.

The integrated W1 claim/store/controller operations now have runtime bodies.
Earlier handoff statements calling those operations stubs are superseded.
Remaining real integration is W9's engine HTTP client, W3 source/keeper/host
construction/delegation/replay, local shared-controller/queue/activity composition,
runner/launcher/bootstrap factories and source handoff, managed-unit CLI wiring,
native Windows/services and deployed Cloud probes/node cleanup. Existing
workspace-off hashing and bootstrap grammar requests remain. No other-area
contract or human product decision is added.

### Verification for the controller binding

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and private
GOCACHE/GOTMPDIR/TMPDIR below /tmp/sw-astra-W8. Package parallelism is at most four.

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for cluster, runners/engine/wingd and their subpackages, plus
  tests/acceptance and internal/acceptance.
- Full runner and engine package suites and tests/acceptance pass.
  internal/acceptance has no runtime tests. Cluster passes with the previously
  blocked TestStartMetricsListener_ServesMetrics excluded. Focused
  `^(TestLocal|TestManaged)` wingd tests pass. Socket/native proofs are unexecuted.
- Focused hosting and runner acceptance pass with `-race`, including the final
  concurrent test refinement. FuzzRunnerControllerBindingW8 passes 7,165 cases
  in five seconds with two workers.
- Runner and acceptance tests cross-compile for windows/amd64. This supplies
  compilation evidence only. Native Windows execution remains open.
- Scoped commentcheck passes across both changed runner files and all three
  changed acceptance files. Sleepcheck against HEAD and `git diff --check` pass.
- Required `commentcheck -base main .` fails on existing merged comments in
  internal/controllerwire/contracts.go:47, pkg/store/state_materialize.go:15 and
  pkg/store/state_result_records.go:9. Those W1/W9 files are untouched here.
- Independent immutable-snapshot review found no actionable correctness/security
  issue and accepted the small factory seam. Its test-rendezvous concern is
  resolved with synctest.Wait and a nonblocking count assertion. Recheck accepted
  that change and the observer slice-ownership assertion. Review ran no tests.

No Sparkwing binary or Git metadata write ran. Local Git configuration has no
identity or core.worktree override. Overwing reports ten commits of staleness
but returned its standards. The lead retains staging and the merge commit,
including the new untracked runner_controller_w8_test.go. Scratch is removed
after all checks finish.

## Continuation: bootstrap exchange boundary

The sole incoming conflict keeps the deletion of
internal/orchestrator/wingd_api_routes_test.go. Its 336 incoming lines test the
retired local API. The lead must stage the deletion and finish the merge because
the swarm brief forbids worker Git writes.

BootstrapHost now binds the shared controller client to one bootstrap, exchanges
once and validates the returned direct award before dispatching to runners.UnitHost.
It preserves the original binding, eval source/live fences, deadlines, accepted
engine and execution resources. It forwards the complete granted runner and
accepted inputs. Cancellation and transport/validation failures stop before
dispatch. The unit host still owns its keeper and source/execution lifecycle.

TestCloudBootstrapW8Components exercises all three Cloud kinds through BuildJob
and the new exchange boundary with fakes. Launcher unit tests cover changed
bindings, authority, engine/resources and connection/exchange/cancellation failures.
FuzzBootstrapBinding generates identity, generation and bearer changes. No native
catalog cell or additional T1 inventory port is claimed.

The new W8-local API consumes existing contracts. Its smaller alternative, the
unimplemented CLI callback, leaves exchange and fence validation to each caller.
The shared private binding validator gains an explicit delivery argument instead
of a second validator. Undo removes the new bootstrap implementation/tests and
restores that private signature. No configuration, timeout, trust root or
compatibility mechanism is added.

Shared edits are tests/acceptance/bootstrap_w8_test.go and the W8 audit/handoff.
Production/package-test edits stay in internal/runners/launcher. No other-area
production caller requires migration. Source documentation is CLOUD-JOBS.md.
No generated reference consumes this internal document.

The bootstrap CLI grammar and original award/source handoff remain contract
requests with W9/W3. Concrete shared HTTP/host/source/keeper adapters and local
controller services, workspace-off cache hashing, native Windows/services and
deployed Cloud proofs remain integration obligations. The new component is not
a working container entrypoint or evidence that native acceptance passes.

Read-only inspection verifies another integration point:
pkg/controller/engine_claim.go clears Execution for plan/eval in claimedUnit,
including exchange. W1/W2 must populate those resolved Cloud resources under the
existing wire shape. ControllerControl.Claim already requires them before creating
a CloudJobAward. This continuation keeps that requirement and edits no other-area
runtime or contract.

### Verification for bootstrap exchange

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and private
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W8. Package parallelism is at most four.

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for runners/engine/wingd and their subpackages, cluster,
  tests/acceptance and internal/acceptance.
- Full runners, engine and tests/acceptance suites pass. The new component's
  plan/eval/work cases run without skips. Its first eval fixture failed because
  the older shared builder fixture gave the source and eval different job IDs.
  The new test now uses the amended wire's same source job and separate generations.
- Focused bootstrap and component acceptance tests pass with `-race`, including
  the review's self-consistent replacement-engine and nonzero-deadline cases.
- FuzzBootstrapBinding passes 6,249 generated cases in five seconds with two workers.
- Launcher tests cross-compile for windows/amd64. Native execution remains untested.
- Required `commentcheck -base main .` reports the same existing W1/W9 comments
  in controllerwire/contracts.go:47, store/state_materialize.go:15 and
  store/state_result_records.go:9. Those files are untouched.
- `commentcheck -base HEAD .` passes across 126 changed files, including the new
  untracked Go files. HEAD-based sleepcheck and `git diff --check` pass.
- Independent immutable-snapshot review finds no blocking correctness/security
  defect and accepts the small UnitHost boundary. Its two coverage suggestions
  are included in the passing race run. Review ran no tests.

The lead retains staging and the merge commit, including bootstrap.go,
bootstrap_test.go and tests/acceptance/bootstrap_w8_test.go. No candidate, service,
pipeline or Git write ran. Local Git configuration has no author/core.worktree
override. Initial discovery ran the installed Sparkwing info command only.
Worker scratch is deleted after the checks. No new product decision is requested.

## Continuation: execution identity handoff

The prior work is committed at ec12ba220. This continuation starts clean, with
no merge conflicts. All 26 amendment rows naming W8 and the new W4 handoff
were rechecked. A1-19/39 gain a node-input constructor. A1-38 gains a component
that connects bootstrap exchange to that same Hosting path for Cloud work.

The invariant is that a job reaches NodeHost with its awarded runner and claim
fence, and an acquired engine whose portable identity matches both the award
and accepted spec. Hosting captures these facts before controller, observation
and preparation callbacks. The constructor supplies Engine, GrantedRunner,
NodeID, Generation and Attempt. It copies runner labels. A prepared identity
cannot override the award, and an absent or mismatched source engine cannot
reach Execute. Ordinal zero stays zero until the host's Start transition.

The acceptance regression ran red before implementation. It demonstrated
execution with forged prepared identity and with missing or mismatched source
identity. The passing tests now cover those cases, forged ambient runner
variables, exact pin/manifest changes, preserved route/source preparation,
captured authority despite later pointer mutation, and keeper/source cleanup.
FuzzRunnerExecutionIdentityW8 generates replacement identity inputs against
that invariant. TestCloudWorkUsesRunnerExecutionIdentityW8 passes an exchanged
work award through real BootstrapHost and Hosting with fake shared interfaces.

W4 finding 3 remains partial. The W8 node-award slice is done. C3 still lacks
a granted runner in RunInput, plan/eval execution inputs and a shared process
constructor consuming the selected engine and granted runner together. W3
must declare that boundary before W8 can compose every unit kind. The retained
node-host runtime still reads runner identity from ambient env. No actual hello
or selected-engine process launch is proved by these component tests.

The existing other-area requests remain: source acquisition/host factories,
delegated routes and replay, workspace-off binary-cache hashing, local shared
services/activity, Cloud bootstrap grammar and original award/source transport,
and resolved plan/eval resources. NewEngineClient and Keeper.Run still panic.
Native Windows/services, Kubernetes enforcement/cleanup and real-repository QA
remain open. No native catalog cell is unskipped, and no additional T1 inventory
port or old production deletion is claimed (0 lines deleted).

The private nodeExecution value holds the captured authority and owns this
constructor. This adds no public API, environment variable or configuration.
Keeping arbitrary prepared identity would leave the grant outside the common
W8 execution boundary. Undo removes execution.go and its Hosting calls/tests.
The existing source and NodeHost contracts retain release verification and
per-start policy checks. Their fakes do not establish those integrations.

Shared edits are the three existing W8 acceptance files, runner_identity_w8_test.go,
contracts/handoffs.md and the W8 audit/handoff. Production edits stay in
internal/runners. No other-area production caller was migrated. CLOUD-JOBS.md
records the new work path. No generated reference consumes that internal doc.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS at most four,
and private GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W8. Tests use -p 4.

- Whole-repository build passes.
- Vet passes for runners, cluster, engine, wingd and their subpackages, plus
  tests/acceptance and internal/acceptance.
- Full runners, engine and tests/acceptance suites pass. Cluster tests pass
  with TestStartMetricsListener_ServesMetrics excluded because sockets are
  blocked in this worker sandbox.
- Focused runner/identity/Cloud-work tests pass with the race detector.
- The new property passes 8,547 generated cases in five seconds with two workers.
- internal/runners tests cross-compile for windows/amd64. Native execution is open.
- commentcheck -base HEAD passes. Required commentcheck -base main still flags
  the three unchanged comments in controllerwire/contracts.go:47,
  store/state_materialize.go:15 and store/state_result_records.go:9.
- Formatting and git diff --check pass. Local Git configuration has no author
  or core.worktree override. No Sparkwing executable, pipeline or Git write ran.

The lead retains independent review, staging and committing. No new product
decision or other-area contract shape was introduced.

The verification commands, under the environment above and this worktree, are:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/runners/... ./internal/cluster ./internal/engine/... ./internal/wingd/... ./tests/acceptance ./internal/acceptance
go test -p 4 ./internal/runners/... ./internal/engine/... ./tests/acceptance
go test -p 4 ./internal/cluster -skip '^TestStartMetricsListener_ServesMetrics$'
go test -p 4 -race ./internal/runners ./tests/acceptance -run '^(TestHosting.*|TestRunner.*W8.*|TestCloudWorkUsesRunnerExecutionIdentityW8|FuzzRunnerExecutionIdentityW8)$' -count=1
go test -p 4 ./tests/acceptance -run '^$' -fuzz '^FuzzRunnerExecutionIdentityW8$' -fuzztime=5s -parallel=2
go run ./internal/commentcheck -base main .
go run ./internal/commentcheck -base HEAD .
```

The last identity-only race run includes the added forged ambient variables.
Windows compilation uses GOOS=windows GOARCH=amd64 with
`go test -p 4 -c -o /tmp/sw-astra-W8/runners.test.exe ./internal/runners`.
Sleepcheck passes. Worker scratch was removed and its absence verified.

## Continuation: private Cloud award transport

The continuation starts clean at e230ae5cf. All 26 amendment rows naming W8 and
the W4 execution-identity handoff were rechecked. A1-38 gains the missing
W8-owned payload transport. The other rows keep their recorded integration
limits. The W4 run/plan/eval constructor and actual hello remain open.

The invariant is that private award decoding preserves every serialized
execution input and the exact bootstrap bytes, or returns an empty award with
no credential bytes in its error. The immutable Job Secret now contains
`award.json` beside the unchanged `bootstrap` file. CloudJobAward declares
the metadata fields `engine`, `source`, `binding`, `cpu_cores` and
`memory_bytes`. Bootstrap stays excluded from JSON. The complete source
projection remains in the Secret rather than Job metadata, argv or env.

ReadBootstrapAward reads the projected files through the existing credential
reader. BootstrapHost.RunFile feeds the restored award into the existing
one-time exchange and binding checks. Fetch-source can read the same award
without consuming exchange. Neither helper opens a store, grants source
authority, acquires source or starts SDK processes. Those remain the shared
source and host contracts. The existing Secret admission check compares the
whole data map, so changed metadata cannot activate the suspended Job.

The acceptance component failed before implementation because the reader and
file entrypoint were absent. It now covers plan/eval/work from BuildJob through
real private files and the one-time exchange to a fake UnitHost. Focused tests
cover nested duplicate and unknown fields, case changes, malformed/trailing
JSON, invalid fences, missing files, cancellation and Kubernetes projection
symlinks. Absent or null source metadata also fails before returning authority.
The shared source package retains validation of the source contents.
Admission components replace award.json for all three Cloud kinds.
FuzzCloudAwardTransport checks preserved inputs and credential-free failures.
An initial source-leak assertion used an all-a SHA also present in the fixture's
image digest. The fixture now uses a distinct SHA. This was a test collision.

The local contract addition consists of the metadata field names,
ReadBootstrapAward and BootstrapHost.RunFile. It adds no flag, env variable,
volume, trust root, timeout, compatibility path or independent parser. The
existing Kubernetes JSON module is promoted to a direct dependency at the
unchanged version because its strict decoder rejects nested duplicates and
unknown fields. The smaller argv-only alternative omits the awarded source
and other inputs needed by the pod. Undo removes the metadata file and reader
with these component tests. No other-area Go contract or production caller
changes. The redundant engine serialization and undeclared unit, attempt,
engine and eval-source argument blocks are deleted (**13 production lines,
0 files**). Their values now travel in the private file. The initial-attempt
component checks ordinal zero after reading that file. New T1 inventory ports
total **0**. A new grammar component failed with unknown --attempt before this
cut, then passed both eval/work entrypoints through RunBootstrapCLI.

Contract changes still needed in other areas:

- W3 must declare granted runner inputs for run/plan/eval and the common
  selected-engine process constructor required by W4 finding 3. NodeInput's
  awarded identity slice remains implemented.
- W9 must allow plans without a job in bootstrap grammar. Eval/work commands
  now use only declared flags. The original award and source transport is
  available through the two W8 helpers above.
- The binary-cache owner still needs the explicit workspace-off key operation
  already requested for selected-engine installation.

Remaining real integrations include W3 source projections/acquisition,
Keeper.Run, shared hosts, delegated routes and replay, W9 NewEngineClient,
and local shared controller/store/consumer services. The merged W1/W2
EngineController still clears Execution for plan/eval and leaves SyncLauncher
unimplemented. Those are implementation gaps in existing interfaces.
Kubernetes enforcement probes and node cleanup, native Windows/SID/services,
actual hello, all native placement catalog cells and real-repository QA remain
open. File and fake-service proofs do not complete them. No new product
decision or unanswered product question was introduced.

Shared edits are go.mod, tests/acceptance/bootstrap_w8_test.go,
tests/acceptance/launcher_publication_w8_test.go,
tests/acceptance/initial_attempt_w8_test.go and contracts/handoffs.md.
Production edits stay in internal/runners/launcher. CLOUD-JOBS.md records the
private file contract. Its component tests verify builder/reader agreement.
No generated reference consumes that internal document. W8-AMENDMENTS-1.md
records the amended status. No shared Go API was removed and no other-area production
caller needed migration.

All commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS at most four,
and GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W8. Go tests use -p 4.

- Whole-repository `go build -p 4 ./...` passes on the changed production tree.
- Vet passes for runners, engine, cluster, wingd and subpackages, plus both
  acceptance packages. A focused vet also covers the final added tests.
- Full runners, engine and component acceptance suites pass.
- Cluster tests pass with TestStartMetricsListener_ServesMetrics excluded
  because this sandbox blocks sockets.
- Local runtime/lifecycle/control/dashboard/drain and managed-unit tests pass
  in wingd and its client using their existing fake service boundaries.
- The final focused transport, projection, cancellation, exchange, initial
  attempt, grammar and changed metadata admission tests pass.
- The final FuzzCloudAwardTransport pass covers 4,243 generated cases with two workers.
- Required commentcheck -base main reports one unchanged W9 finding at
  internal/controllerwire/contracts.go:47. The earlier two W1 findings are
  absent after the merge. commentcheck -base HEAD passes for all eight changed
  Go files, including the new untracked files.
- Sleepcheck and git diff --check pass. Local Git configuration has no author
  or core.worktree override. No owned Go check process remains after completion.

The lead retains independent review, staging and committing. No Sparkwing
binary, pipeline, deployment, release or Git write ran. Worker scratch is
removed after verification. Reproducible commands under the environment above:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/runners/... ./internal/engine/... ./internal/cluster ./internal/wingd/... ./tests/acceptance ./internal/acceptance
go test -p 4 ./internal/runners/... ./internal/engine/... ./tests/acceptance ./internal/acceptance
go test -p 4 ./internal/cluster -skip '^TestStartMetricsListener_ServesMetrics$' -count=1
go test -p 4 ./internal/wingd ./internal/wingd/client -run 'Test(Local(Runtime|Lifecycle|Control|Dashboard|Drain)|ManagedUnits)' -count=1
go test -p 4 ./internal/runners/launcher ./tests/acceptance
go test -p 4 ./internal/runners/launcher -run '^$' -fuzz '^FuzzCloudAwardTransport$' -fuzztime=5s -parallel=2
go run ./internal/commentcheck -base main .
go run ./internal/commentcheck -base HEAD .
go run ./internal/sleepcheck -base HEAD .
```


## Continuation: integration merge and chart identity plumbing

The continuation starts at c88ef4512 with two unmerged paths. handoffs.md
retains both histories. runner_pool_cli.go remains deleted. Its incoming edit
only removes the retired ambient runner identity path, which W8's granted
NodeInput construction already replaces. Retaining the deletion keeps 887
incoming legacy lines out of the tree. No Git write ran, so the lead must
stage both resolutions and finish the merge.

All 26 amendment rows assigned to W8 and the W4 execution-identity handoff
were rechecked. The remaining source, lease, controller-client and shared
host construction contracts have no implementation delta in this merge.
W8's existing components continue to test those interfaces with fakes.

The runner chart still injected POD_NAME, contrary to frozen section 7.4.
Removed that four-line default block and regenerated the vendored bundle with
`helm dependency update /home/bombuntu/.xwing/envs/1086/sparkwing/charts/sparkwing-full --skip-refresh`.
The controller enrollment profile supplies identity. Pod metadata and advertised
labels cannot replace it. The chart's Auth documentation states that rule.
The existing source and full-chart render tests both failed on POD_NAME before
the cut. This continuation adds no mechanism, contract shape, flag, environment
name, timeout or compatibility path. No other-area production caller needed
migration. No T1 inventory test was newly ported and no native catalog cell was
activated. New production deletion is four lines in one retained file.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W8/cache, GOTMPDIR=/tmp/sw-astra-W8/gotmp and
TMPDIR=/tmp/sw-astra-W8/tmp. Commands run in this worktree with `go -C`:

- `go build -p 4 ./...` passed for the whole repository.
- `go vet -p 4 ./internal/runners/... ./internal/engine/... ./internal/cluster ./internal/wingd/... ./tests/acceptance ./internal/acceptance` passed.
- `go test -p 4 ./internal/runners/... ./internal/engine/... ./tests/acceptance ./internal/acceptance -count=1 -timeout=3m` passed.
- `go test -p 4 ./internal/cluster -skip '^TestStartMetricsListener_ServesMetrics$' -count=1 -timeout=2m` passed. The omitted metrics listener needs a socket denied by this sandbox.
- `go test -p 4 ./internal/wingd -run '^(TestLocal|TestManagedUnit|TestManagedController|FuzzLocal)' -count=1 -timeout=2m` passed against its file/fake boundaries.
- `go vet -p 4 ./charts` passed.
- `go run ./internal/commentcheck -base main .` passed across 1,009 files.

The W8 component acceptance suite passes. Native cells remain blocked pending
real HTTP/api.sock, host, platform and deployed boundary evidence. Existing
contract changes needed are the W3 run/plan/eval granted identity and shared
process constructor, W9's plan-without-job bootstrap grammar, and the cache
owner's explicit workspace-off key operation. Runtime integration still needs
W3 source/keeper/shared hosts/delegated routes/replay, W9 NewEngineClient,
W1/W2 local services and plan/eval resource awards plus SyncLauncher. Native
Windows SID/services, Kubernetes enforcement probes and reservation cleanup,
actual hello and real-repository QA remain open. No new product question arose.

Edits outside the W8 implementation directories are the chart source/template,
its existing render tests, the regenerated bundle, the chart README and these
W8 design notes. charts is W8-owned. The shared handoffs.md resolution retains
the incoming W3 notice. No cross-area Go contract or implementation was edited.
The cache chart/templates remain W7's deletion work.

Full `go test -p 4 ./charts -count=1 -timeout=3m` passed in 120.159 seconds,
including both identity-environment regressions and the vendor drift check.
`git diff --check` passed. Shared Git config has no author or core.worktree
override. All owned check sessions completed. Worker scratch was deleted.
The lead retains independent review, staging and committing. W8 remains
incomplete at the real-integration boundaries listed above.

## Continuation: controller and store lifecycle components

`tests/acceptance/runner_store_w8_test.go` joins the existing W8 loop and
Hosting implementation to the real EngineController and SQLite store. Machine
roots use Store.ProvisionCapability. A test transport resolves each raw bearer
through Store.LookupCapability and calls the controller methods in process.
Enrollment and software-policy providers, hosts and keeper runtime are fakes.
Accepted run/plan fixtures are inserted directly, so this does not prove intake,
planning, authentication middleware, HTTP or api.sock.

The trigger and job components claim through the same loop, confirm and renew
the awarded authority, complete through the controller, read persisted success,
and refuse renewal after completion. The job component checks the granted
engine/runner/fence at NodeInput, award ordinal zero, Applied.Attempt one and
persisted ordinal one. Source cleanup follows keeper join. A separate component
revokes the machine root after acquisition and proves the real renewal refuses
host preparation and keeper startup. Its first fixture called deployment-root
revocation for a tenant machine and failed. Tenant self-revocation is the correct
public boundary, and that corrected fixture passes.

All 26 W8 amendment rows and the W8 handoffs retain their existing scope.
A1-08/18/39 gain these component proofs. No native catalog cell is activated,
no T1 inventory test is newly ported, and no production implementation or old
code is changed. Deleted production lines/files in this continuation: **0/0**.
The sole shared Go file added is the acceptance test. W8-AMENDMENTS-1.md updates
those three rows. No other-area caller or contract changes.

Contract changes still needed are the recorded W3 run/plan/eval granted identity
and common selected-engine process constructor, W9 plan-without-job bootstrap
grammar, and explicit workspace-off binary-cache key operation. W3 host factories,
delegated routes and replay inputs remain undeclared. Source projection/acquisition,
Keeper.Run, remote-owner operations, W9 EngineClient/DataClient, shared local
controller services and final CLI factories still need integration. Plan/eval
resource awards and SyncLauncher remain controller gaps. Native Windows/SID,
Kubernetes probes/cleanup, actual hello, native placement cells and real-repository
QA remain open. No new product question or production mechanism was introduced.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W8/cache, GOTMPDIR=/tmp/sw-astra-W8/gotmp and
TMPDIR=/tmp/sw-astra-W8/tmp. All commands use this worktree through `go -C`.

- Whole-repository `go build -p 4 ./...` passed.
- Vet passed for runners, engine, cluster, wingd and subpackages, plus both acceptance packages. Focused vet also covers the final new test.
- Full runners and engine suites passed. The final `go test -p 4 ./tests/acceptance ./internal/acceptance -count=1 -timeout=2m` passed, including both new test functions. Acceptance took 14.592 seconds.
- Cluster passed with only its socket-based metrics listener excluded, as required by the swarm sandbox boundary.
- Focused local-runtime, lifecycle, control, dashboard, drain and managed-unit tests passed in wingd and its client.
- Required `commentcheck -base main` passed across 1,010 files. `sleepcheck -base HEAD`, gofmt and `git diff --check` passed.

The lead retains independent review and committing. No Sparkwing executable,
pipeline, deployment, release or Git write ran. Shared Git config has no author
or core.worktree override. All owned check processes finished. Worker scratch
was deleted. W8 remains incomplete at the integration boundaries above.

## Continuation: persisted Cloud work bootstrap

`tests/acceptance/bootstrap_store_w8_test.go` connects real service provisioning,
Cloud work claiming and exchange to BuildJob's private files, BootstrapHost.RunFile
and Hosting. The test reuses the existing in-process controller transport and
SQLite fixture. It proves that exchange refuses replay while the work claim is
live, preserves ancestry to the launcher root, renews with exchanged authority,
passes the accepted engine and granted runner to NodeInput, starts attempt one,
persists completion and joins the keeper before source cleanup.

The confinement invariant is that changed claim identity, engine, deadlines or
resources cannot reach Cloud execution. FuzzCloudStoreBootstrapConfinementW8
mutates those fields against a real persisted award and checks both refusal and
credential-free errors. Its thirteen seed cases include independent owner,
attempt and parent deadlines.

The fixture inserts accepted run/plan rows directly. Enrollment/software policy,
source projection/acquisition, the keeper and executor are fakes. Source is a
fixture projection because source.ForExecution remains a panic stub. HTTP,
middleware, actual SDK hello and Kubernetes execution are outside this proof.
Native catalog cells remain blocked. This adds component acceptance evidence for
A1-19/38/39 and no new T1 inventory port. All 26 W8 amendment rows and the W8
handoffs were rechecked. Their remaining production obligations are unchanged.

No production implementation, old code, API or contract changed. Deleted lines
and files in this continuation: **0/0**. Shared files touched are the new
bootstrap_store_w8_test.go and the four-line Exchange method in
runner_store_w8_test.go. W8-AMENDMENTS-1.md records the A1-38 evidence.
No other-area production caller required migration and no mechanism was added.

Contract changes still needed are W3's run/plan/eval identity inputs and shared
process constructor, W9's plan-without-job grammar, and the cache owner's
workspace-off key operation. W3 source/keeper/host/delegated-route/replay runtime,
W9 clients and final factories, shared local services, controller plan/eval
resources and SyncLauncher, Windows SID transport, Cloud enforcement/cleanup
and native/real-repository QA remain integration work. No new product question
or contract change in another area is introduced.

Verification used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W8. Commands use this worktree
through `go -C`.

- Whole-repository `go build -p 4 ./...` passed.
- `go vet -p 4 ./internal/runners/... ./internal/engine/... ./internal/cluster ./internal/wingd/... ./tests/acceptance ./internal/acceptance` passed.
- `go test -p 4 ./internal/runners/... ./internal/engine/... ./tests/acceptance ./internal/acceptance -count=1 -timeout=3m` passed. Component acceptance took 17.977 seconds.
- `go test -p 4 ./tests/acceptance -run '^$' -fuzz '^FuzzCloudStoreBootstrapConfinementW8$' -fuzztime=5s -parallel=2 -timeout=2m` passed with thirteen seeds and sixteen executions.
- Cluster passed with only TestStartMetricsListener_ServesMetrics excluded because the sandbox refuses listeners. Focused local-runtime and managed-unit tests passed in wingd and its client.
- `go run ./internal/commentcheck -base main .` passed across 1,015 files. `sleepcheck -base HEAD`, gofmt and `git diff --check` passed.

No Sparkwing binary, pipeline, Git write, deployment or release ran. Git config
has no author or core.worktree override. All owned check sessions finished and
worker scratch was deleted. The lead retains independent review and committing.
