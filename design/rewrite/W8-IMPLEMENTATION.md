# W8 implementation handoff

W8 remains incomplete. The implementation behind the runner, engine and launcher
contracts is exercised with component fixtures. Installed process composition,
local controller composition and native/Cloud acceptance remain open. The
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

## This continuation

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

## Evidence from this continuation

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

## Remaining integration and contract changes needed

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
6. **W8 local runtime/services.** Compose the real local controller, single store
   owner, dashboard, queue, consumer and orphan reconciliation behind
   `LocalRuntime`. Implement native managed units once W9 specifies Home and
   foreground arguments for `dashboard start --stay` and `__tick`. Native runner
   replacement still needs actual service registration hooks.
7. **Cloud deployment.** Supply real network probes and reservation-scoped node
   cleanup, then prove native sidecars, aggregate scratch accounting, Docker
   execution, network isolation and teardown. These remain deployment proofs.
8. **W7/W9 follow-through.** Delete remaining cache service chart/templates/docs,
   preserve historical release matrices, and wire upgrade/policy notices to the
   actual CLI and dashboard. This continuation removes only runner callers of
   the old cache transports.

## Shared files and mechanisms

The cluster and runners files are W8-owned. Minimal shared prerequisites/callers
changed in this continuation are:

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

No git metadata was written. An initial installed `sparkwing info --for-agent`
orientation call ran before the brief's no-binary instruction was loaded. No
candidate Sparkwing executable or pipeline ran. The metrics test's socket
restriction is the remaining sandbox friction. Worker scratch is removed after
all checks finish.
