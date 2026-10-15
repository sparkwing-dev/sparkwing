# W8 install cache and native unit parser

Continues `e4e3ca112` and `W8-SUPERVISOR-RESIDUE-CUT.md`. The initial tree was
clean, and the initial whole-repository build passed.

## Changes

Invariant: a workspace disabled for compilation cannot affect the binary cache's
input graph. The SDK installer now passes its captured compile environment to
`bincache.ExplainCacheKey`. The previous call read the ambient environment.
The new property failed on its first generated workspace edit before the fix.
It now checks 32 generated edits and a malformed disabled workspace. The existing
native-tool fixture is shared between installer tests.

Invariant: native service inspection preserves literal arguments and refuses
ambiguous or incomplete commands. Plist inspection now uses Go's XML decoder
with a small document shape. Removed the manual depth/pending-key parser and
`plistStrings`: 64 lines replaced by 29, net -35. Existing service tests and
`FuzzPlistArguments` retain escaping, empty arguments, duplicate commands,
incomplete XML and extra-document coverage.

Removed `Client.APIAdvertised`, `Client.DaemonStoreSchema` and
`Lease.WatchOwnership`, net -21 lines. They had no production callers. Schema
inspection stays with `Probe`. External wingd tests use `WatchControl`, while
client recovery tests observe a real control frame through the private watcher.
Removed its unused reattachment callback. Recovery, cancellation and
reattachment behavior remain in the production watcher.

No package, setting, limit or foreign production caller was added or migrated.
No whole legacy source file was newly deleted. Earlier W8 legacy executor,
pool, supervisor and heartbeat cuts remain deleted.

## Verification

All Go checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, W8 scratch and at most `-p 4`.

- Whole-repository `go build -p 4 ./...` passes.
- Full runners, launcher, runner services, unitfile, engine and sdkinstall suites
  pass. These include the three retry-entrypoint regressions.
- Focused wingd, client and localcontroller lifecycle/composition, joined
  shutdown and recovery tests pass.
- `tests/acceptance -run 'W8|LauncherCloud'` passes. Existing engine, claims,
  native-store component, bootstrap, launcher and lifecycle evidence survives.
  No native catalog cell is newly activated or closed.
- `FuzzPlistArguments` passes 1,000 generated executions.
- Focused race checks pass for the workspace property, plist seeds and client
  recovery/cancellation.
- Vet passes for sdkinstall, runner services, wingd, CLI and acceptance.
- Commentcheck against main passes across 2,083 Go files. `git diff --check`
  passes.

The migrated native `TestProbeAdvertisesTheDaemonStoreSchema` cannot start its
Unix listener: `setsockopt: operation not permitted`. Native schema/attach tests,
Windows/macOS and deployed Kubernetes acceptance remain unverified here.
No archived T1 test was newly ported. Existing dispositions remain in
`contracts/t1-port-inventory.md`.

## Production lines

The metric uses `git diff --no-renames --numstat`, excluding tests, testdata,
acceptance and contract declarations. W8 adds **32** and removes **88** production
Go lines this session: **net -56**. Production changes outside W8 are **0**.
W8-owned paths total **+2,762 against main**, **5,062 above the -2,300 budget**.

| New W8 package | Production lines against main | Remaining size justification |
| --- | ---: | --- |
| `internal/engine` | +2,016 | Decisions 101-103 add exact pins, verified release/cache, signed policy/fallback, historical matrices, revocations and staged upgrades. Shared trust and release verification avoid another implementation. These additions were absent from the frozen estimate. |
| `internal/engine/sdkinstall` | +171 | The selected verified executable builds both SDKs using native tools and the shared binary cache. Folding it into engine creates a cycle through bincache, runspec and store. This session adds no production lines here. |
| `internal/runners` | +1,381 | One claim loop, committed-award adoption, policy draining, verified updates and authenticated native inputs. Source, renewal and process execution remain with shared hosts. |
| `internal/runners/runnerflags` | +64 | Entrypoints and installed-service inspection share the existing grammar. |
| `internal/runners/service` | +524 | Native registration ownership, argv preservation, refresh and stopped-state handling replace agentservice, whose deletion is outside W8 accounting. |
| `internal/runners/service/unitfile` | +341 | Native argv encoding, file publication and registration checks serve runners, controllers and timers. XML parsing is 35 lines smaller. |
| `internal/wingd/localcontroller` | +306 | One elected store supplies queue, recovery and maintenance with joined shutdown. Folding it into wingd creates an import cycle. |

Existing-package deltas are cluster -1,967, launcher +1,059, wingd +1,483,
wingd/client +306 and wingd/journal -21. Retired k8s, warmpool, supervise and
production testprog paths total -2,901. The overage still needs lead approval.

## Amendments and lead decisions

**A1-38 remains closed for W8.** Immutable award fences, generations and Job
instances remain correlated. Lead decisions 07:40 items 5, 6 and 10 remain
satisfied on W8's side. The four dead tables from 09:40 item 4 remain absent.
The shared retry-origin rule and hosting/launcher-claim/exchange tests from
12:35 remain implemented and pass.

All 26 W8-assigned amendment rows retain the dispositions in
`W8-RETRY-AND-SIZE.md`, with the installer progress above. Remaining integration:

- A1-08/09/18/19/26/27/36/39/48/51: remote authenticated native-store and
  principal bindings, source/log/storage/process services and actual SDK hello.
  `cmd/sparkwing-runner` still supplies an empty runtime through `MainWithVersion`.
- A1-10/11/12/43/45/46/50/52/55: native placement and Windows release evidence,
  final CLI/source binding and the installer items below. The source contract
  still needs to distinguish uploaded working trees from committed bundle seeds.
- A1-20/49: enrollment rollback, Actions issuance and startup provisioning remain
  W1/W2/W6/W9 composition work. W8 consumes private credentials and repo fences.
- A1-38: W1/W2's `ReadLauncherSync` still has no production store implementation.
- A1-40/41: W9's `openLocalController` still returns unavailable. W8's elected
  store/queue/runtime composition passes through its declared service bindings.
- A1-42/53: deployed Probe/Clear, enforced egress, DaemonSet inventory, escape
  probes, aggregate 20 GiB scratch and complete DinD teardown require real
  Kubernetes and native-platform QA.

## Contract changes needed

The earlier request for a workspace-off key is closed on W8's side by consuming
`ExplainCacheKey` with the explicit compile environment. The existing shared
key API already supports it.

Two installer boundaries remain open:

1. **W7 effective overlay hashing.** The installer currently forces `go.mod`.
   Honoring `.resolved.mod` also requires the shared cache key to hash its local
   replacement targets. `bincache.keyParts` currently reads targets from `go.mod`
   only. Extend the shared key owner to include the effective overlay's targets.
   W8 can then use that modfile consistently for SDK selection, checksum
   materialization and compilation. Keep the original manifest/checksum seal
   unchanged, materialize build sums in the generated overlay, and hash after
   preparation. A temporary entrypoint test reproduced the ignored overlay.
   No overlay or checksum fix is claimed in this session.
2. **W3 source/cache lease handoff.** `__sdk-install` returns a bare command path
   after releasing its binary-cache lease. Agree a cache-entry identity in the
   install result that `source.Entry` reacquires before exposing the command and
   releases on Close. W8 owns the install-result side. W3 must declare its source
   consumption before this changes a foreign contract. No second lease owner or
   undeclared source field was added.

No W3 path, including internal/orchestrator, was edited. These requests extend
the installer integration items already recorded in `c3-engine-version.md`.

## Shared files and friction

Shared documentation touched: `contracts/c3-engine-version.md`. W8 source docs
updated: `internal/runners/service/RUNNER-SERVICE.md`. All code and test edits
are W8-owned. Lead-owned amendments and handoffs remain unchanged.

Friction: native Unix sockets are sandbox-blocked. Overwing discovery worked
while reporting a stale installed binary. Initial Sparkwing discovery preceded
loading the brief's no-binaries rule. No candidate, pipeline, Git write, deployment
or release ran. Local Git config has no identity or core.worktree override.
Worker scratch is removed after checks. The shared cache is retained.

STATUS: CONTINUE
