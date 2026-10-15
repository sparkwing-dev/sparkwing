# W8 implementation handoff

W8 is incomplete. The repository builds and the implemented components pass their
focused checks. Full placement acceptance is still pending. No skipped catalog
scenario has been represented as a passing native or Cloud run.

## Implemented

- `internal/engine` resolves exact Go and npm SDK pins, verifies signed releases
  and executable metadata, caches verified assets, enforces the signed version
  index, preserves verified fallback and stages pin upgrades with concurrent-edit
  checks. Tests cover all six engine platform artifacts without executing foreign
  binaries. npm lockfiles match the existing npm-only SDK installer.
- `internal/runners.Loop` claims trigger and node units through one interface,
  fences concurrent awards by generation, drains held work and joins hosts on
  cancellation. `ManagedRunner` checks signed policy before claims and during
  saturation, then drains before a verified software replacement. Native
  installation remains a supplied interface.
- The launcher has one builder for plan, eval and work. Every Job uses root,
  a privileged native DinD sidecar, a readiness probe, a shared scratch volume
  and a bootstrap Secret. It creates a suspended Job before the Secret, resumes
  only after successful setup, reconciles after restart and retains cleanup
  ownership until all containers stop.
- `KubernetesBoundary` persists exclusive reservations with node resource-version
  checks and a taint. It requires team dedication, a real network probe and
  reservation-scoped cleanup. Orphan recovery waits for the original parent
  expiry and verifies that the Job and its pods are absent.
- The local lifecycle coordinates readiness, concurrent starts, stay promotion,
  drain, doctor diagnostics and failed-start cleanup. The daemon accepts all four
  activity holds. Health probes and idle API connections do not hold it alive.
  Missing peer identity refuses access. Admission files use private permissions.

## Deletions and test ports

The text diff removes 8,144 lines, including replacements. Complete deletion of
`internal/runners/k8s` and `internal/runners/warmpool` accounts for 5,444 lines.
The old Job builder, dispatcher factories, trigger-runner flags, chart settings,
launcher tests and local connection/schema compatibility paths were removed or
replaced. The chart dependency archive was refreshed.

`TestResolveResources_StaysQuietUnderTheCeiling` is ported as
`internal/runners/launcher/launcher_test.go:TestSizingBelowCeiling`. The inventory
now names that actual path. Local tests replace connection holds with activity
holds and reverse acceptance of unavailable peer identity and old state schemas.
Runner review regressions cover reclaimed generations, cleanup errors during
policy denial, policy refresh at capacity and overflowing CLI memory quantities.

## Evidence

All commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, private Go caches and
scratch. Test parallelism was at most two per command.

- Whole repository `go build -p 4 ./...` passed.
- `go vet -p 2` passed for cluster, runners, launcher, wingd, engine, releaseasset,
  limits and acceptance.
- Full package tests passed for engine, releaseasset, runners, launcher, limits
  and acceptance. The acceptance package still contains skipped catalog cells.
- Focused cluster CLI and wingd lifecycle/identity tests passed. Focused runner
  and wingd race tests passed. Chart tests passed.
- `TestEngineW8Components`, `TestLocalControllerW8Components`,
  `TestLauncherCloudPodContract`, `TestLauncherCloudBoundaryRefusal` and
  `TestLauncherCloudPlanUsesRunBinding` pass with explicit component fixtures.
- A compile-only check passed for the remaining orchestrator, cluster, wingd
  client and CLI tests. Full socket/process integration could not run in this
  sandbox. A wingd socket test failed with `setsockopt: operation not permitted`.

## Remaining work and contract changes needed

1. The old cluster pool/trigger/source/heartbeat paths remain. Wire the unified
   loop to concrete capability-bound `RunHost`/`NodeHost` implementations, then
   remove those paths and their old credential/cache transports. The new loop
   is tested against the claim and host interfaces. It is not the installed
   runner CLI's execution path yet.
2. W2/W9 must provide the Cloud award/sync transport, including bootstrap and
   immutable engine/deadline/source bindings. `RunLaunchCLI` accepts an injected
   runtime factory. The binary entrypoint reports unavailable until that factory
   can provide the new controller and verified boundary. It cannot adapt the
   old env-bearer claim response safely.
3. W3 must wire source acquisition, accepted engine identity and bootstrap
   exchange into plan/eval/work entrypoints. W6's `procenv.Build` is required
   by the native upgrade resolver and remains a stub here. Old orchestrator
   proxy-inheritance tests need alignment with the shared environment policy.
4. The actual controller/store/dashboard/local-consumer composition must
   implement `LocalRuntime`, feed activity counts and reconcile local orphans.
   W8 added `AwaitReady`, `Status`, `SetStay` and `Drain` to its own runtime port
   because `Run` alone could not express those lifecycle operations.
5. W9 must define the Home argument and foreground semantics for installed
   `dashboard start --stay` and `__tick` units. `ManagedUnitManager` rendering,
   native runner replacement, schedule refresh and restart remain open. No
   undeclared command or environment transport was invented.
6. Cloud deployment must supply actual network probes and node cleanup. Native
   sidecars and pod-level resources must be enabled. EmptyDir accounting and
   kubelet eviction provide the shared 20 GiB accounting boundary, not a
   synchronous write quota. Real Docker, scratch exhaustion, network isolation
   and teardown acceptance remain unverified.
7. W9 must connect repository upgrade verification, CLI/run notices and dashboard
   notices. W7/release publishing must preserve the historical asset matrix
   when the current cache binary leaves the release set.

The full local lifecycle, dashboard install/update, Cloud DinD, engine-placement
and runner-update acceptance cells remain open. Native Windows/macOS execution
was not exercised. Contract changes outside W8 were not made.

## Shared files

- `internal/limits/deadline.go` and `job_deadline_test.go`: implement the existing
  deadline contract required by the builder.
- `internal/releaseasset/version_contract.go`: implement the existing retained
  release matrix and closed-set verifier required by engine resolution.
- `internal/orchestrator/pod_node_host.go`: obtain network environment names from
  the shared registry after deleting the dispatcher package.
- Deleted `internal/orchestrator/k8s_placement.go`, `k8s_placement_test.go`,
  `k8s_runner_factory.go`, `k8s_runner_factory_test.go`,
  `trigger_runner_factory.go` and `warm_runner_factory.go`.
- `cmd/sparkwing/update_release_workflow_test.go`: use the launcher's binary name.
- Three `tests/acceptance/*_w8_test.go` component suites and the engine/port-inventory
  contract documentation.

No worktree git metadata was written and no Sparkwing binary or pipeline was run. Parallel
source edits briefly invalidated two compiler file snapshots. Stable reruns passed.
Generated signing roots were used only inside tests and restored afterward.
The worker scratch directory is removed after checks.
