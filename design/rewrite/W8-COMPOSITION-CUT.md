# W8 composition cut

`NewLauncher` is now the sole launcher constructor. Its injected controller
factory requires only `LauncherController`'s claim and sync operations. The
default still opens W9's shared engine client with launcher route selection.
The store-backed acceptance component uses this constructor with the existing
authenticated `StoreController`. Removed `NewStoreLauncher`, its duplicate
`StoreRuntimeConfig` and their production file. The component retains real C2
authorization, store issuance, private bootstrap publication and fenced cleanup.
Its sync reader and Kubernetes boundary remain fakes.

The local process lifecycle now shares one `ownerRuntime` with the underlying
local lifecycle. Removed the intermediate `ownerLifecycle`, its constructor,
duplicate home validation and forwarding methods. Home binding stays in the
process lifecycle. Runtime ownership still stays local through startup and
drain, and foreign owners retain their status, stay, dashboard and drain paths.
Existing tests now exercise the process lifecycle. The election and startup
failure tests exercise its underlying local lifecycle directly.

Removed the unused `SDKPlatform` type and `SDKPlatforms` catalog. Neither had a
Go consumer, including tests. Platform support still follows the release and SDK
contracts. Removed their export rows from the engine contract inventory.

## Priority and amendment status

- Lead item 5 remains implemented through `LocalController.HTTP() LocalHTTP`.
  W2 still supplies the production API/dashboard and local state bindings.
- Lead item 6 remains implemented. The launcher accepts digest-pinned
  `--docker-image` and refuses `--deadline`. Focused cluster CLI tests pass.
- Lead item 10 remains implemented on the launcher side. Sync carries the
  operational unit, holder and claim generation, actual Job namespace/name/UID
  and node UID. Eval source and live claim generations remain separate.
  **A1-38 remains open end to end.** `EngineController.SyncLauncher` delegates to
  `LauncherSyncReader`, whose production store implementation is still absent.
- The cluster worker, warm-pool dispatcher, run-host Kubernetes dispatcher and
  old W8 pod builders remain deleted. The only W8 Cloud builder is
  `launcher.BuildJob`. This session does not count those historical cuts again.
- All other assigned amendments and handoffs retain their completed work and
  open integrations from [the preceding report](W8-SERVICE-LIFECYCLE-CUT.md).
  The approved pending-claim outcome and cancellation mechanisms are unchanged.
  The four dead catalogs assigned at 09:40 remain deleted.

## Lines and contracts

Against starting HEAD `ffc57d0c3`, production Go adds 31 lines and removes 120.
**Net production lines deleted this session: 89.** Tests and Markdown are excluded.
Across W8's ownership paths against `main`, the lead's metric is **+4,599**,
which is **6,899 over** the -2,300 budget. W8 remains incomplete.

The overage still needs lead review. The earlier justification remains: D102-103
require the signed engine resolver, cache and SDK upgrade implementation, and
D91 requires local process, HTTP and managed service lifecycles. Those additions
were not separately sized in the area estimate. This session removes duplicate
composition instead of removing those ratified features.

The narrower launcher factory and deleted SDK catalog are local W8 contract
changes. No foreign production signature changed. No new mechanism, product
decision or security posture was introduced.

## Real integration points left open

- W1/W2: implement the transactional launcher sync reader and bind it to current
  authority. W2 still clears plan/eval `Execution`; those awards need the
  policy-owned resolved resources required by the launcher.
- W2: provide local state/activity, authorized HTTP handlers, provisioning and
  claimant enrollment over `localcontroller.Binding`'s sole store handle.
- W3: provide the remote `NativeHostStore`, Cloud plan/eval lifecycle and
  initialized-checkout adoption contracts. W9/W2 must align source credentials.
- W8: finish executable composition with those bindings and deployed
  `KubernetesBoundary.Probe`/`Clear`. Native isolation, scratch accounting,
  teardown, Docker, Windows/macOS and real-repository QA remain open with T1/lead.

No new foreign contract change is requested. No historical test port or native
acceptance cell is newly closed. Existing tests were migrated with the removed
constructors. No test-only cell was added.

## Verification and handoff

Whole-repository `go build -p 4 ./...` passes. Focused vet passes for runners,
engine, the lifecycle client and acceptance. The full runners, engine and
localcontroller suites pass. The lifecycle client's existing local process,
dashboard, control and drain selection passes with and without the race detector.
The existing `W8|LauncherCloud|LauncherController|LauncherPolling` acceptance
selection passes. Focused cluster CLI tests pass. Commentcheck against main
passes across 2,122 Go files. Diff whitespace passes.

Checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, at most four
package workers, `/tmp/sw-gocache` and temp files under `/tmp/sw-astra-W8`.
The shared files touched are `tests/acceptance/launcher_boundary_w8_test.go` and
`tests/acceptance/store_launcher_w8_test.go`, for the narrowed factory and sole
launcher constructor. Other edits are within W8, including its engine export
inventory and source Cloud documentation. No off-area production caller needed
migration. Lead-owned handoffs and amendments are unchanged.

Friction: Overwing reports its installed binary behind source. The required
foreign service bindings remain absent. Bootstrap discovery invoked installed
`sparkwing info --for-agent` once before the swarm brief was loaded. No candidate
runtime, pipeline, deployment, release or Git write ran. Git configuration has
no local identity or core.worktree override. All check processes joined. Scratch
is removed and the shared cache is kept. The lead retains review and commits.

STATUS: CONTINUE
