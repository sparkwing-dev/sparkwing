# W8 supervisor residue cut

Continues `146086982` and `W8-FIXTURE-CUT.md`. The starting tree was clean and
the initial whole-repository build passed.

## Changes

Removed the deleted supervisor's heartbeat reader, Unix mmap writer, Windows
writer, daemon ticker and path API. The counter had no production reader.
Removed `AppendSupervisor`, its stream reader and the CLI's three supervisor
event explanations. Journal fixtures now write daemon records. The synchronous
journal appender belongs to tests because the real writer keeps its file open.

Removed `HostSpawn`, `ResolveHostBin`, `NoHostSpawn` and their unused error.
Their remaining callers were tests. W6 already classifies
`SPARKWING_WINGD_BIN` as a removed transport. Production lifecycle startup takes
an explicit executable. Process fixtures now take an explicit binary too.
Missing-host tests use `NoTakeover` with no spawner. Startup-failure tests call
the existing detached spawner. Removed the unused `DaemonVersionOf` accessor
and its error wrapper. Error messages, sentinels and admission timing remain.

Updated admission, journal-retention and environment docs plus their embedded
mirrors. The environment registry row still documents the forbidden transport.
No new production mechanism, setting, limit, package or foreign contract was
introduced. No W3 path was edited.

## Verification and test dispositions

All Go commands used workspace/VCS stamping disabled and the shared cache.
Tests used at most `-p 4`. The final checks pass:

- Whole-repository `go build -p 4 ./...` and an explicit build of the process
  fixture under `internal/wingd/testdata/testprog`.
- Full cluster, runners, launcher, service, unitfile, engine, sdkinstall,
  journal and localcontroller tests.
- Wingd/client selections `TestLocal*`, `TestComposition*`,
  `TestShutdownJoins*`, `FuzzLocal*` and `TestSpawn_FailedStart*`.
- CLI journal/explain and wingd-runtime tests, both environment-doc drift checks,
  and the embedded-doc mirror check.
- `tests/acceptance -run 'W8|LauncherCloud'`, retaining the engine, claims,
  native-store component, bootstrap, launcher and lifecycle evidence.
  The existing retry regressions pass through hosting, claim and exchange.
- Vet over cluster, runners, engine, wingd, opsview, CLI and docs.
- Commentcheck against main and `git diff --check`.

The full client suite and a broader docs selection could not establish native
evidence because Unix/TCP socket calls return `operation not permitted`.
The narrowed checks above passed. Native process startup and socket suites
remain for the lead, including the two migrated process-fixture tests.

The heartbeat tests and environment/PATH chooser tests retire with their dead
mechanisms. Startup failure, no-host refusal, journal filtering, pagination and
corruption tests remain. No archived T1 test was newly ported and no native
acceptance cell is newly closed. Independent review found no blocker. Its stale
supervisor-timeout comment finding was corrected without changing the timeout.

## Production lines and size justification

Using `git diff --no-renames --numstat` and excluding tests, testdata, acceptance
and contract declarations, W8 removes **310** and adds **38** production Go
lines this session: **net -272**. The shared CLI removes another **6**, giving
**-278** repository production lines this session. Rename detection must be off
so the prior move into testdata counts as deleted production code.

Against main, **W8-owned paths total +2,818**, which is **5,118 above the -2,300
budget**. The new-package justifications remain for lead approval:

| Package | Net production Go lines | Required behavior and size constraint |
| --- | ---: | --- |
| `internal/engine` | +2,016 | Decisions 101-103 add exact pins, verified release/cache, signed policy/fallback, historical matrices, revocation and staged upgrades. They were absent from the frozen size estimate. Shared trust and native dependency tools avoid another implementation. |
| `internal/engine/sdkinstall` | +171 | The verified executable installs/builds both SDKs. Folding it into engine creates an import cycle through bincache, store and runspec. |
| `internal/runners` | +1,381 | One claim loop adopts committed awards, drains on policy changes and constructs authenticated native inputs. Source, lease and process execution stay with the shared hosts. |
| `internal/runners/runnerflags` | +64 | Entrypoints and service inspection share the existing grammar. |
| `internal/runners/service` | +524 | Linux/macOS install, inspect, refresh and removal preserve argv, registration ownership and stopped state. This replaces agentservice outside W8 accounting. |
| `internal/runners/service/unitfile` | +376 | Runners, controllers and timers share native argv encoding/parsing, source ownership and atomic file publication. Shell splitting cannot replace those native argument rules. |
| `internal/wingd/localcontroller` | +306 | One elected store supplies queue, recovery and maintenance, with joined shutdown. Folding it into wingd creates a dependency cycle. |

Existing-package deltas are cluster -1,967, launcher +1,059, wingd +1,483,
wingd/client +327 and wingd/journal -21. Retired k8s, warmpool, supervise and
production testprog paths total -2,901. This session adds no runtime owner.

## Amendments, contracts and remaining integration

**A1-38 is closed for W8.** Immutable award fences, claim generations and Job
identities remain correlated. Lead decisions 07:40 items 5, 6 and 10 are satisfied
on W8's side. The four tables from 09:40 item 4 remain absent. The shared
retry-origin validator and all three 12:35 entrypoint regressions remain green.

The other amendment dispositions in `W8-RETRY-AND-SIZE.md` and
`W8-FIXTURE-CUT.md` still apply. No new foreign contract change is requested.
These real-integration points remain open:

- A1-08/09/18/19/26/27/36/39/48/51: remote native store and authenticated
  principal bindings, source/log/storage/process services and actual SDK hello.
  The runner executable still calls `MainWithVersion` with an empty runtime.
- A1-10/11/12/43/45/46/50/52/55: native placement and Windows release evidence.
  W3/W6 still need a distinct source marker for uploaded working-tree snapshots
  versus committed bundle seeds before W8 can consume that distinction.
- A1-20/49: enrollment/rollback and startup/Actions issuance remain W1/W2/W6/W9
  composition work. W8 consumes the declared private credential and repo fence.
- A1-38: W1/W2's transactional `ReadLauncherSync` implementation is absent.
- A1-40/41: W9's `openLocalController` still returns unavailable. W8's elected
  store/queue/runtime composition remains covered through contract fakes.
- A1-42/53: deployed Probe/Clear, enforced egress, DaemonSet inventory, escape
  probes, aggregate 20 GiB scratch and complete DinD teardown require real
  Kubernetes and native platform QA.

## Shared files and friction

The minimal shared production edit is `cmd/sparkwing/daemon.go` (-6 lines).
Removed-API test callers migrated in
`cmd/sparkwing/daemon_journal_test.go`,
`cmd/sparkwing/daemon_events_contract_test.go` and
`internal/opsview/doctor_rejections_live_test.go`.
Shared docs are `docs/admission.md`, `docs/diagnosing-admission.md`,
`docs/environment-variables.md`, `docs/migrations/v0.67.0.md` and their four
`pkg/docs/mirror` copies. Lead-owned amendments/handoffs are unchanged.

Friction: native sockets are blocked. Overwing's stale-binary notice did not
block standards discovery. No Git writes or Sparkwing candidate/pipeline runs
were performed. Worker scratch is removed at session end. The shared build
cache is retained.

STATUS: CONTINUE
