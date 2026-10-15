# W8 refresh reset continuation

Continues `a48557d78` and `W8-NATIVE-COMMAND-AUDIT.md`. The initial worktree
was clean. The whole repository built before changes and builds after them.

Refresh now preserves leading empty systemd `ExecStart=` resets. Previously,
the writer replaced each reset with another executable command, leaving a
registration that inspection refused and a simple service could not run.
The fix changes one condition in the shared writer. No API or mechanism is added.

Invariant: updating a valid registration preserves its argv and single-command
behavior while replacing its executable. The runner refresh property failed
before the fix and passes 1,000 generated executions afterwards. It exercises
real registration files, literal arguments, repeated config flags, empty resets,
refresh and subsequent inspection. The controller/schedule lifecycle regression
also refreshes files containing leading resets and verifies both registrations.
Native service managers remain fake boundaries in these tests.

Removed the runner loop's duplicate `unitKey` type. Active awards now use the
shared `capability.LeaseRef`, preserving kind, ID, holder and generation.
Existing duplicate-award and interruption tests pass, including under race.
No legacy file or archived T1 test is newly deleted or ported.

Production Go in W8-owned paths adds 4 and deletes 11 lines this session:
**net -7**. Production Go outside W8 changes by **0**. Against main, W8 adds
9,526 and deletes 6,892 lines: **net +2,634**, or **4,934 over the -2,300 budget**.
This uses `git diff --no-renames --numstat main -- '*.go' ':!*_test.go'`, filtered
to W8 paths and excluding testdata, acceptance and contract declarations.
Disabling rename detection avoids misclassifying the old production test program
that moved under testdata. The overage still needs lead review.

| New package | Production lines | Retained responsibility and size justification |
| --- | ---: | --- |
| `internal/engine` | 2,016 | Decisions 101-103 add exact Go/npm pin selection, signed index/fallback, restrictions, historical matrices, verified executable caching and staged upgrades. The architecture estimate omitted these. Shared release verification and trust roots remain with their existing owners. |
| `internal/engine/sdkinstall` | 169 | The selected executable builds Go and installs TypeScript with native tools. Moving this into engine creates a cycle through bincache, store and runspec. |
| `internal/runners` | 1,350 | One mixed claim loop owns committed-award adoption, independent stop causes, policy drain and replacement. Shared hosts own execution and renewal. The duplicate fence type is removed. |
| `internal/runners/runnerflags` | 62 | Commands and installed-registration inspection need the same registry parser, including repeated flags and literal values. |
| `internal/runners/service` | 486 | Linux/macOS install, inspection, refresh and removal retain native state and loaded-source checks. This replaces the deleted 645-line agentservice package, whose deletion is outside W8 accounting. |
| `internal/runners/service/unitfile` | 344 | Runners, controllers and timers share argv encoding, parsing, private publication and loaded-source checks. Folding these into callers duplicates them. |
| `internal/wingd/localcontroller` | 306 | One elected store supplies the queue, maintenance and recovery before readiness. Folding this into wingd creates a cycle through runners and nodehost. |

Other W8 package deltas against main are cluster -1,967, launcher +1,059,
wingd +1,483, wingd/client +248 and wingd/journal -21. Retired k8s, warmpool,
supervise and production testprog paths total -2,901. Admission remains under
the architecture's unchanged-admission contract.

**A1-38 remains closed for W8.** The launcher carries immutable award fences,
claim generations and Job-instance correlation. Lead decisions 07:40 items 5,
6 and 10 remain satisfied on W8's side. The four dead contract tables from
09:40 item 4 remain deleted. Retry entrypoint regressions pass for Hosting,
launcher Claim and bootstrap Exchange, preserving origin IDs independently of
the execution ID under the shared validator from lead decision 12:35.

The 26 W8-assigned amendment rows retain these dispositions:

| Amendments | Implemented W8 boundary and remaining integration |
| --- | --- |
| A1-08/09/18/19/26/27/36/39/48/51 | Unified claims and shared native-host constructors have fake-backed and real SQLite component evidence. Remote NativeHostStore/principal, source/log/storage/process composition and actual SDK hello remain open. `MainWithVersion` still supplies an empty Runtime. |
| A1-10/11/12/43/45/46/50/52/55 | Selected-engine install, source seals, environments and retries are implemented. Installer contracts below, native placement/Windows evidence and final CLI/source composition remain open. |
| A1-20/49 | W8 reads private credential files and enforces repository fences. Enrollment rollback, Actions issuance and startup provisioning remain W1/W2/W6/W9 composition. |
| A1-38 | W8 wire work is closed. Production `LauncherSyncReader` composition remains W2's explicit 14:40 assignment. |
| A1-40/41 | Elected store/queue/maintenance, lifecycle and managed-unit components pass. The reset regression now covers runner, controller and timer refresh. W9's `openLocalController` still returns unavailable. Concrete store activity/recovery and incompatible-store classification remain unbound. |
| A1-42/53 | Kubernetes Probe/Clear, default-deny enforcement, DaemonSet inventory, escape probes, aggregate 20 GiB eviction and joined DinD teardown still need deployment/native evidence. Callback fakes cannot close these cells. |

Contract changes needed in other areas remain unchanged:

1. W7 must hash local replacement targets from the effective `.resolved.mod`.
   `bincache.keyParts` still reads them from `go.mod`. W8 can then finish overlay
   selection/compilation and checksum materialization before hashing while
   preserving the accepted source seal.
2. W3 must declare SDK cache-entry identity and lease ownership through execution.
   `__sdk-install` releases its lease before returning its executable path, and
   `source.Entry` carries no cache reacquisition contract. W8 owns the install
   result once that interface exists.
3. The source contract must distinguish uploaded working trees from committed
   bundle seeds. This session changes neither that contract nor W3 implementation.

All focused checks pass with `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, scratch under `/tmp/sw-astra-W8` and at most `-p 4`:

- `go build -p 4 ./...` before and after changes.
- Full runners, engine, localcontroller and crontimer tests. Engine tests ran
  before the changes to runner/service code.
- `tests/acceptance -run 'W8|LauncherCloud'` on the final source.
- `internal/runners/...` and wingd managed-unit/loop/retry selection under race.
- Runner refresh fuzzing with `-fuzztime=1000x -parallel=2`.
- Scoped vet over runners, engine, wingd, cluster, crontimer and acceptance.
- Commentcheck against main is clean across 2,152 files. `git diff --check` passes.

The previously unexplained broad acceptance failure is now captured. The
expanded selection fails in three top-level names. These are foreign handoffs,
not a passing native acceptance claim:

| Owner | Failing test | Observed failure |
| --- | --- | --- |
| W2 | `TestActionsIssuanceHTTPCallerFieldsCannotSelectAuthority` | `actions_issuance_http_test.go:254` receives HTTP 500 `policy_missing` instead of 405 `method_not_allowed`. |
| W3/W1 | `TestLifecycle/lifecycle-transition-replay-and-fencing/hermetic-store-executor-events` and `hermetic-store-step-records` | `host_events.go:54` and `:181` report `holder transition requires a nonnegative source attempt`; refusal cases fail their expected boundary as well. |
| W3 | `FuzzExecutorFailureMask` | Both seeds report `rendered host error contains an SDK secret payload` at `failure_test.go:13`. Running this name alone reproduces the failure. |

No native acceptance cell is newly closed. Remaining implementation depends on
the contracts/composition above or native deployment evidence. No shared caller,
W3 file, lead-owned amendment or handoff changed. Changed files are the runner
loop, shared unit writer, runner refresh tests, wingd managed-unit tests, runner
service source documentation and this report. No external mechanism was added.

Overwing discovery reports its installed binary is stale. Local Git config has
no user identity or core.worktree override. No Git write, candidate binary,
pipeline, deployment or release ran. The lead owns independent review and commit.
No public docs or changelog impact: command grammar and native registration
policy are unchanged. Source documentation and behavioral checks cover the fix.
Scratch is removed at session end. The shared build cache is retained.

STATUS: CONTINUE
