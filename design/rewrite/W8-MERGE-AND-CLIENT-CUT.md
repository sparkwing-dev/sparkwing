# W8 merge recovery and unused client cut

Continues `a4ddd4092` and `W8-INSTALL-CACHE-AND-UNITFILE-CUT.md`.

The conflict in `contracts/t1-port-inventory.md` is resolved in the working
file. Both W8's retained port dispositions and the integration branch's W2
additions survive. There were no implementation or signature conflicts.
The initial and final whole-repository builds pass. The index still reports
the file as unmerged because the swarm brief prohibits worker Git writes.
The lead must stage this resolution and finish the integration merge.

## Changes and ports

Removed the unused `Client.ResetStats` API and its private retry/read path,
deleting the 43-line `internal/wingd/client/stats.go`. Removed four unused
client accessors: `Draining`, `APIReady`, `APIError` and `APISocket`.
`Probe` retains daemon status and API diagnostics. No production caller required
migration. No forwarding API was added.

`TestStatsReset_ClearsTheEventWindow` now uses the existing runtime fixture
and in-memory admission transport. It obtains a real grant, resets the event
window through the actual wire handler, checks that its holder and resource
claim survive, and reads the persisted empty window. The client-only
unsupported-response test retires with the removed client API. The daemon's
admission wire remains covered. This is a retained test port, not native socket
evidence or a newly closed archived T1 identity.

Removed 38 lines of field commentary and spacing from the runner service
declarations. The field names, types and native service behavior are unchanged.
No new mechanism, setting, restriction or contract shape was added.

## Verification

All Go checks use `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, temporary directories beneath `/tmp/sw-astra-W8`
and at most `-p 4`.

- Whole-repository `go build -p 4 ./...` passes after the final Go edit.
- Full `internal/runners/...`, `internal/engine/...` and
  `internal/wingd/localcontroller` suites pass. The three retry-entrypoint
  regressions and their retained property seeds pass.
- Cluster tests pass with only `TestStartMetricsListener_ServesMetrics` excluded
  because it requires native socket binding.
- `tests/acceptance -run 'W8|LauncherCloud'` passes, including the store-backed
  runner/owner, bootstrap exchange, launcher correlation, service, lifecycle
  and engine components. No native catalog cell is newly activated.
- Focused wingd local runtime/HTTP/dashboard/control/lifecycle and managed-unit
  tests pass. Focused client recovery, cancellation and local lifecycle tests
  pass. The migrated stats-reset test and client local lifecycle/recovery selection
  pass with `-race`.
- Vet passes for cluster, runners, engine, wingd and W8 acceptance, including
  the migrated test. Commentcheck against main passes across 2,152 files.
  `git diff --check` passes.
- W8 packages cross-compile for Windows amd64 and macOS arm64. This establishes
  compilation only. Native service, peer identity and SDK execution evidence
  remain open.

A broader client selector included
`TestAcquire_CancelStillInterruptsTheWaitAfterAReconnect`, which could not bind
its Unix listener: `setsockopt: operation not permitted`. The later selection
covers the socket-free tests without that native fixture. No test was weakened
to turn the sandbox refusal into a pass.

## Production lines and package justifications

The metric uses `git diff --no-renames --numstat`, excluding tests, testdata,
acceptance and contract declarations. W8-owned production Go adds **16** and
removes **112** lines this session: **net -96**. Of that reduction, 58 lines
remove unused client code and 38 remove service commentary and spacing.
Production Go outside W8 changes by **0**. No chart file changed.

W8-owned paths total **+2,666 against main**, **4,966 above the -2,300 budget**.
The overage remains for lead review. The new packages have these sizes:

| Package | Production Go lines | Why the remaining owner is needed |
| --- | ---: | --- |
| `internal/engine` | 2,016 | Decisions 101-103 add exact Go/npm pins, signed index/fallback, revocations, historical release matrices, verified executable caching and verified upgrades. These requirements were absent from the frozen estimate. Release verification and trust roots are shared with the existing release owners. |
| `internal/engine/sdkinstall` | 171 | The selected engine compiles Go and installs TypeScript through native tools and the shared binary cache. Moving it into engine creates an import cycle through bincache, runspec and store. |
| `internal/runners` | 1,381 | One mixed claim loop owns committed-award adoption, independent stop causes, policy drain and verified replacement. Native hosts own execution and renewal. Removing those owners would drop named lifecycle or authority contracts. |
| `internal/runners/runnerflags` | 64 | Entrypoints and installed-registration inspection consume one registry-backed parser. Registration inspection must preserve literal arguments and repeated configuration flags. |
| `internal/runners/service` | 486 | Linux/macOS install, inspection, refresh and removal preserve stopped state and refuse foreign registration sources. It replaces the deleted 645-line agentservice package, whose deletion is outside W8 accounting. |
| `internal/runners/service/unitfile` | 341 | Runners, controllers and schedules share native argv parsing, escaping, private atomic publication and loaded-source checks. Folding the package would duplicate those rules. |
| `internal/wingd/localcontroller` | 306 | One elected store supplies the authenticated queue, reconciliation and maintenance, and closes after workers join. Folding it into wingd creates an import cycle through runners and nodehost. |

Existing-package deltas are cluster -1,967, launcher +1,059, wingd +1,483,
wingd/client +248 and wingd/journal -21. Deleted k8s, warmpool, supervise and
production testprog paths total -2,901. The admission daemon remains because
the frozen architecture preserves admission and its wire.

## Amendments and integration still open

**A1-38 remains closed for W8.** Immutable award fences, generations and Job
instances remain correlated. Lead decisions 07:40 items 5, 6 and 10 remain
satisfied on W8's side. The four dead tables from 09:40 item 4 remain absent.
The 12:35 shared retry-origin rule remains implemented across hosting, launcher
claim and Cloud exchange, with all three entrypoint regressions passing.

The 26 W8-assigned amendment rows retain the following disposition. Earlier
reports that call `source.Acquire`, `NewEngineClient` or W2's local HTTP adapter
stubs are superseded: those bodies exist in this tree.

- A1-08/09/18/19/26/27/36/39/48/51: shared hosts and real SQLite components
  pass. Production remote native-store/principal binding, source/log/storage
  services, per-attempt storage refresh and actual SDK hello still need final
  composition. `MainWithVersion` still supplies an empty runtime.
- A1-10/11/12/43/45/46/50/52/55: selected-engine install, sealed source,
  environment and retry behavior remain implemented. Native placement and
  Windows execution evidence, final command composition and installer handoffs
  below remain open. Uploaded working trees still need an explicit source
  contract distinction from committed bundle seeds.
- A1-20/49: W8 consumes private credentials and repository fences. Enrollment,
  Actions issuance and bootstrap provisioning remain W1/W2/W6/W9 composition.
- A1-38: the W8 wire side is closed. `LauncherSyncReader` still has no
  production store implementation in this checkout.
- A1-40/41: W8 election, store/queue/maintenance ownership, lifecycle and
  managed units pass. W2 supplies `Server.HTTP`. W9's `openLocalController`
  still returns unavailable, and the concrete local activity/recovery service
  binding remains open.
- A1-42/53: real Kubernetes Probe/Clear, enforced egress, DaemonSet inventory,
  escape probes, aggregate 20 GiB scratch and joined DinD teardown require
  deployment and native-platform QA. Callback fakes cannot close those cells.

Contract changes needed remain the two installer handoffs from the previous
report. W7's cache key must hash local replacement targets from the effective
`.resolved.mod` before W8 can honor it without stale binaries. W3 must declare
the SDK command cache-entry identity and its execution lease ownership before
W8 changes the install-result contract. Missing checksums must be materialized
in the build overlay before hashing without changing the accepted source seal.
No foreign contract was changed around those gaps.

## Shared files and friction

Shared file: `contracts/t1-port-inventory.md`, for conflict resolution and the
retained stats-reset port disposition. Source documentation:
`internal/wingd/LOCAL-TRANSPORT.md`. No W3 path, foreign production caller,
lead-owned amendment or handoff file changed. No public docs or changelog impact:
the removed internal client methods had no production callers or public commands.

The lead owns staging, merge completion, independent review and native checks.
Worker Git writes, Sparkwing binaries/pipelines, deployments and releases were
not used. Overwing discovery reported a stale installed binary. Git config has
no local identity or core.worktree override. Scratch is removed at session end,
and the shared build cache is retained.

STATUS: CONTINUE
