# W8 native command audit and remaining integration

Continues `d39deb46d` and `W8-MERGE-AND-CLIENT-CUT.md`. The initial working tree
was clean and the whole-repository build passed. There is no remaining merge
conflict.

## Implemented and deleted

Invariant: an incomplete or ambiguous native command cannot authorize service
inspection or mutation. Systemd inspection previously discarded a malformed
`ExecStart` when a later command parsed successfully. The shared parser now
returns failure at the first malformed nonempty command. This is a three-line
change to the existing parser, with no new mechanism or API.

`FuzzSystemdArgumentsRejectIncompleteCommands` failed before the fix. It
generates literal argv and checks malformed and duplicate commands in both
orders, while preserving valid leading reset directives. It passes 1,000
generated executions. The service regression now
drives Inspect, Refresh and Uninstall and checks that refusal preserves the
file and never reaches the service manager. Existing local-controller and timer
callers use that same parser.

Removed 28 lines of multiline GoDoc from the new runner, installer and flag
packages. Exported API comments are one line. Native installation hook details
remain in the source documentation. No production mechanism, whole legacy file
or API was deleted in this session. No archived T1 test was newly ported.

## Production lines

The metric is production Go versus main, excluding tests, testdata, acceptance
and contract declarations. W8-owned paths add 16 and delete 41 lines this
session: **net -25**. The change consists of the three-line parser guard and
28 fewer comment lines. Production Go outside W8 changes by **0**.

W8-owned paths total **+2,641 against main**, **4,941 above the -2,300 budget**.
The overage needs lead review. Each new package retains these responsibilities:

| Package | Production lines | Remaining size justification |
| --- | ---: | --- |
| `internal/engine` | 2,016 | Decisions 101-103 add exact Go/npm pins, signed index and fallback, revocations, historical matrices, verified executable caching and staged upgrades. Those requirements were absent from the estimate. Release verification and trust roots use the shared owners. |
| `internal/engine/sdkinstall` | 169 | The selected engine builds Go and installs TypeScript through native tools and the shared binary cache. Folding it into engine creates a cycle through bincache, runspec and store. |
| `internal/runners` | 1,357 | One mixed claim loop owns committed-award adoption, independent stop causes, policy drain and verified replacement. Shared native hosts own acquisition, execution and renewal. Removing these owners would drop named lifecycle or authority contracts. |
| `internal/runners/runnerflags` | 62 | Entrypoints and native-registration inspection share the registry parser, including repeated configuration flags and literal argument values. |
| `internal/runners/service` | 486 | Linux/macOS installation, inspection, refresh and removal preserve stopped state and refuse foreign loaded registrations. This replaces the removed 645-line agentservice package, whose deletion is outside W8 accounting. |
| `internal/runners/service/unitfile` | 344 | Runners, controllers and timers share argv encoding, parsing, private atomic publication and loaded-source checks. Folding it into a caller duplicates those rules. |
| `internal/wingd/localcontroller` | 306 | The elected store supplies queue, reconciliation and maintenance and closes after workers join. Folding it into wingd creates a cycle through runners and nodehost. |

Existing-package deltas remain cluster -1,967, launcher +1,059, wingd +1,483,
wingd/client +248 and wingd/journal -21. Retired k8s, warmpool, supervise and
production testprog paths total -2,901. The admission daemon remains under
the architecture's unchanged-admission contract.

## Amendment and lead-decision audit

**A1-38 remains closed for W8.** Immutable award fences, generations and Job
instances remain correlated. Lead decisions 07:40 items 5, 6 and 10 remain
satisfied on W8's side. The four dead tables from 09:40 item 4 remain absent.
The shared retry-origin rule from 12:35 remains implemented across Hosting,
launcher Claim and bootstrap Exchange, with tests through all three entrypoints.

All 26 W8-assigned amendment rows retain the following disposition:

- A1-08/09/18/19/26/27/36/39/48/51: shared host and real SQLite components
  pass. Final remote NativeHostStore/principal, source/log/storage/process
  binding and actual SDK hello remain open. `MainWithVersion` still supplies
  an empty Runtime. There is no remote NativeHostStore adapter in this tree.
  W8 tests the declared host interfaces with fakes and the embedded store.
- A1-10/11/12/43/45/46/50/52/55: selected-engine installation, sealed source,
  environment and retry behavior remain implemented. Native placement,
  Windows release/execution evidence and final CLI/source composition remain
  open. The installer contract requests below remain unresolved.
- A1-20/49: W8 consumes private credential files and repository fences.
  Enrollment rollback, Actions issuance and startup provisioning remain
  W1/W2/W6/W9 composition.
- A1-38: `LauncherSyncReader` still has no production store implementation.
  Lead decision 14:40 assigns that dependency composition to W2, including
  its minimal store changes. W8's launcher wire is implemented.
- A1-40/41: elected store, queue, maintenance, lifecycle and managed-unit
  components pass. W2 supplies Server.HTTP. W9's `openLocalController` still
  returns unavailable. The concrete local activity/recovery service remains
  unbound, including a store-owned activity projection and typed incompatible
  store error.
- A1-42/53: actual Kubernetes Probe/Clear, enforced default-deny egress,
  DaemonSet inventory, escape probes, aggregate 20 GiB scratch eviction and
  joined DinD teardown need deployment and native-platform evidence. Callback
  fakes cannot close those acceptance cells.

## Contract changes needed and stop boundary

Two installer handoffs remain unchanged:

1. W7 must hash replacement targets from the effective `.resolved.mod`.
   `bincache.keyParts` still takes local replacement targets from `go.mod` only.
   W8 can then use the effective overlay for SDK selection and compilation,
   materialize build sums before hashing, and preserve the original source seal.
2. W3 must declare the SDK command's cache-entry identity and execution lease
   consumption. `__sdk-install` returns a command path after releasing its
   cache lease, while `source.Entry` has no reacquisition ownership. W8 owns
   the install-result change once that foreign contract is declared.

The source contract must also distinguish uploaded working trees from committed
bundle seeds. No foreign contract or W3 implementation was changed around these
gaps. Remaining implementation requires these shared composition contracts or
native deployment evidence. No new architecture choice is proposed.

## Verification and friction

All Go checks use `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, scratch beneath `/tmp/sw-astra-W8` and at most `-p 4`.

- Whole-repository build passed before edits and after the parser fix.
- Full runners, engine and localcontroller suites passed. W8 acceptance
  components passed, including runner/service, bootstrap, launcher, native-store,
  lifecycle and engine components. The three retry-entrypoint regressions pass.
- Cluster tests pass with the native metrics-listener test excluded.
- Focused wingd local runtime, HTTP, dashboard, control, lifecycle, managed-unit
  and stats-reset tests pass.
- Service and unitfile race tests pass. The first combined acceptance race
  invocation failed in automatic vet because files under the shared build cache
  disappeared. Separate vet had passed. The acceptance race selection was
  rerun with automatic vet disabled and passed the managed-unit/runner-service
  selection. The final service and unitfile race suites pass with vet enabled.
- Vet passes for runners, engine, cluster, wingd and acceptance. Commentcheck
  against main passes across 2,152 files. `git diff --check` passes.
- An extra broad `tests/acceptance -run 'Test|FuzzSystemd|FuzzControllerRetry'`
  selection failed after 186.561 seconds. Its audit output exceeded the terminal
  buffer, which retained the tail but lost the failing test names. This is an
  unresolved broad-suite failure, not a full acceptance pass. The focused
  `-run 'W8|LauncherCloud'` selection passes on the final source. The lead needs
  to isolate the broad failure with output captured to a file.

The focused commands, after applying the Go environment above, are:

```sh
go -C /home/bombuntu/.xwing/envs/1086/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1086/sparkwing test -p 4 ./internal/runners/... ./internal/engine/... ./internal/wingd/localcontroller
go -C /home/bombuntu/.xwing/envs/1086/sparkwing test -p 4 ./tests/acceptance -run 'W8|LauncherCloud'
go -C /home/bombuntu/.xwing/envs/1086/sparkwing test -p 4 -race ./internal/runners/service/...
go -C /home/bombuntu/.xwing/envs/1086/sparkwing test -p 4 ./internal/runners/service/unitfile -run '^$' -fuzz '^FuzzSystemdArgumentsRejectIncompleteCommands$' -fuzztime=1000x -parallel=2
go -C /home/bombuntu/.xwing/envs/1086/sparkwing run ./internal/commentcheck -base main .
```

No native acceptance cell is newly closed. Native sockets and platforms still
need the lead's environment. Overwing discovery reports a stale installed
binary. Local Git config has no identity or core.worktree override.

Only W8 production/test files, its service source documentation and this report
changed. Shared production callers, W3 paths, lead-owned amendments and handoffs
were untouched. No candidate, pipeline, deployment, release or Git write ran.
There is no public docs or changelog impact because command grammar and valid
registration behavior are unchanged. The internal service documentation records
the malformed-command refusal.
The lead owns independent review and committing. Scratch is removed at session
end and the shared build cache is retained.

STATUS: CONTINUE
