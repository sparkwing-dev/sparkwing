# W8 production fixture cut

Continues `fdb4d95e5` and `W8-SYNC-DECISIONS.md`. The starting tree was clean.
The initial and final whole-repository builds pass.

## Changes

Moved `FuncRunStore` into wingd's existing `export_test.go`. Every remaining
consumer is a wingd test. The sole consumer outside those tests was the chaos
driver, which now supplies its own four-method cancellation logger. The real
local controller still implements `RunStore` directly.

Moved all three process-driver files from `wingd/testprog` into
`wingd/testdata/testprog` and updated the process test's explicit build path.
Removed the unused `supervise` branch and function from that fixture. The
production spawn path uses `wingd run`.

No runtime contract, security rule, configuration or customer limit changed.
No new test was needed for moving the existing fake. No archived T1 test was
newly ported. The existing ports remain in `contracts/t1-port-inventory.md`.

## Verification

All Go checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, the shared
`GOCACHE=/tmp/sw-gocache`, and scratch under `/tmp/sw-astra-W8`. Tests used
`-p 4` and a two-minute timeout.

- `go build -p 4 ./...` passes. The relocated fixture also builds explicitly
  with `go build -o /tmp/sw-astra-W8/testprog ./internal/wingd/testdata/testprog`.
- Full cluster, runners, launcher, runnerflags, service, unitfile, engine and
  sdkinstall tests pass.
- Wingd's `TestLocal*` and
  `TestShutdownJoinsConnectionCleanupBeforePersistence` pass. Local controller
  and client selections `TestLocal*`, `TestComposition*` and `FuzzLocal*` pass.
- `go test -p 4 ./tests/acceptance -run 'W8|LauncherCloud' -count=1` passes.
  This retains component evidence for engine policy, claims, native-store
  hosting, bootstrap, launcher reconciliation/isolation and local lifecycle.
  No native placement or deployed boundary cell is newly closed.
- Vet passes for `./internal/wingd/... ./internal/chaos/crashdummy`.
- `go run ./internal/commentcheck -base main .` is clean across 2,076 files.
  `git diff --check` passes.
- Independent review found two overwritten test-export constants. Both were
  restored before the passing test/vet runs. The reviewer confirmed closure
  and accepted the removal without further findings.

Native process/socket, Windows/Darwin and real Kubernetes tests remain with the
lead. The swarm brief prohibits candidate execution and pipelines here.

## Production lines

W8-owned paths are **-351 production Go lines this session**, measured with the
lead's exclusions. That is 291 lines moved to testdata, 44 lines of test adapter
moved to `_test.go`, and 16 comment/spacing lines removed. These are accounting
and fixture cuts, not 351 lines of deleted runtime behavior. The relocated
fixture also loses 20 obsolete supervisor lines.

The only shared implementation edit is `internal/chaos/crashdummy/main.go`,
**+12 production lines**, to migrate the removed fake API's caller. The repository
production delta this session is therefore **-339**. No W3 file changed.

Against `main`, W8-owned paths total **+3,090 production Go lines**, still
**5,390 above the -2,300 budget**. Charts are unchanged. New-package justifications
remain for the lead's decision:

| Package | Net production lines | Required behavior and size constraint |
| --- | ---: | --- |
| `internal/engine` | +2,016 | Decisions 101-103 add exact pins, verified GitHub releases/cache, signed policy/fallback, historical matrices, revocation and staged upgrades. The frozen budget did not size these additions. It shares release trust and native dependency tools. |
| `internal/engine/sdkinstall` | +171 | The verified selected executable builds/installs both SDKs. Moving it into engine creates a cycle through bincache, runspec and store. It reuses Go binary caching and npm. |
| `internal/runners` | +1,381 | One claim loop, committed-award adoption, policy-driven draining, verified updates and authenticated native-input construction. Source, lease and process execution stay with shared hosts. |
| `internal/runners/runnerflags` | +64 | Runner entrypoints and registration inspection share one parser over the existing grammar. It owns no second vocabulary. |
| `internal/runners/service` | +524 | Linux/macOS install, inspect, refresh and removal preserve arguments, registration ownership and stopped state. It replaces agentservice outside W8's accounting. |
| `internal/runners/service/unitfile` | +376 | Runners, controllers and timers share argv encoding/parsing, source ownership and atomic file publication. Those consumers use every exported helper. |
| `internal/wingd/localcontroller` | +306 | One elected store supplies the queue, recovery and maintenance. Workers join before services/store close. Moving this into wingd creates a dependency cycle. |

Existing-package deltas are cluster -1,967, launcher +1,059, wingd +1,648 and
wingd/client +413. Retired k8s, warmpool, supervise and production testprog paths
total -2,901. This session adds no package or general-purpose mechanism.

## Remaining obligations

**A1-38 is closed for W8.** The launcher sends the immutable award fence and
correlates claim generations and Job identities. Lead decisions 07:40 items 5,
6 and 10 remain satisfied on W8's side. The four tables named by 09:40 item 4
remain absent. The shared retry-origin rule and hosting/claim/exchange regressions
from 12:35 remain implemented and pass in the package tests.

The amendment dispositions in `W8-RETRY-AND-SIZE.md` and
`W8-MERGE-RETRY-VALIDATION.md` still apply. No new foreign contract change is
requested. The unresolved real-integration points are:

- A1-08/09/18/19/26/27/36/39/48/51: production native source/log/storage/process
  bindings and actual SDK hello. `cmd/sparkwing-runner` still selects an empty
  runtime. Authenticated remote `NativeHostStore` and principal bindings belong
  to W2/W3/W9. W3 still supplies plan/eval and initialized-checkout hosting.
- A1-10/11/12/43/45/46/50/52/55: native placement and Windows release evidence.
  Selected-engine, sealed-source/environment and here-claim components remain.
- A1-20/49: enrollment/rollback, Actions and bootstrap-admin production issuance
  remain W1/W2/W6/W9 integration. W8 consumes private credentials and repository
  admission through the existing contracts.
- A1-38: `LauncherSyncReader.ReadLauncherSync` still has only its W1/W2 interface
  and test fixture, with no production reader in this checkout.
- A1-40/41: W8's elected runtime and store/queue composition pass with fixtures.
  W9's `openLocalController` still returns unavailable. Production authorized
  handlers and the executable service factory remain W2/W9 integration.
- A1-42/53: deployed Probe/Clear, enforced egress, DaemonSet inventory, escape
  probes, aggregate 20 GiB accounting and complete DinD teardown need a real
  deployment. Native platforms and repository placement QA remain open.

The source contract still has no distinct marker for an uploaded working-tree
snapshot versus a committed Git-bundle seed. W8 must consume that distinction
when its owners provide it. Component fakes cannot establish the missing native
or deployed evidence.

No git writes, Sparkwing binaries or lead-owned handoff/amendment edits ran.
Friction was the corrected test-export overwrite and Overwing's stale-binary
notice. The notice did not block standards discovery. Worker scratch is removed
at session end. The shared build cache remains.

STATUS: CONTINUE
