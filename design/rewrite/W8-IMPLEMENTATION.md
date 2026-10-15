# W8 implementation handoff

W8 remains incomplete. The runner, engine, launcher and local process runtime
are exercised with component fixtures. Installed process composition, the shared
controller adapter and native/Cloud acceptance remain open. The
acceptance catalog's native cells are still skipped. Component tests do not
count as native, Windows, Actions or Kubernetes evidence.

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

The fourth session below completes status/stay transport and dynamic dashboard
startup inside the runtime. Foreign-owner startup and dashboard commands still
need CLI composition.

1. **W3/W9 runner composition.** Bind `RunnerFactory` to the authenticated
   controller, `Hosting` preparation/renewal callbacks, source acquisition,
   accepted engine identity, admission and the real keeper. `nodehost.RunHost`,
   `NodeHost`, `source.Acquire`, `lease.Keeper.Run` and the W9 engine client remain
   interfaces/stubs in this checkout. `MainWithVersion` has no runtime factory.
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
5. **W2/W9 launcher transport.** Implement `CloudControl.Claim/Sync` with immutable
   award, deadline and bootstrap bindings. Wire `RunLaunchCLI` to that transport
   and the verified Cloud boundary. Its default factory is absent.
6. **W1/W2/W3 local services.** Bind `LocalRuntimeConfig.Open` to the final shared
   controller, its single store, queue consumer and orphan recovery. Final
   `EngineController`, capability authorization, claim/read/reaper and singleton
   lease methods remain stubs. W1 needs a local activity projection and a typed
   incompatible-store error. Preserve local secret sealing and stray-session
   cleanup in the final adapters; the deleted server no longer supplies them.
7. **W8/W9 local transport and services.** Bind `client.QueryLocalController`,
   `client.SetLocalStay` and admission drain into the foreign-owner lifecycle.
   Dynamic dashboard startup also needs a foreign-owner command. Implement
   native managed units once W9 specifies
   Home and foreground arguments for `dashboard start --stay` and `__tick`.
   Native runner replacement needs actual service registration hooks. Windows
   needs a native SID-authenticated transport; missing identity still refuses.
8. **Cloud deployment.** Supply real network probes and reservation-scoped node
   cleanup, then prove native sidecars, aggregate scratch accounting, Docker
   execution, network isolation and teardown. These remain deployment proofs.
9. **W7/W9 follow-through.** Delete remaining cache service chart/templates/docs,
   preserve historical release matrices, and wire upgrade/policy notices to the
   actual CLI and dashboard. This continuation removes only runner callers of
   the old cache transports.

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

Fourth-session changes remain uncommitted for the lead. No Sparkwing executable,
pipeline or git-writing command ran. Worker scratch is deleted after the final
checks and review complete.
