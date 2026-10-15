# W8 owned-store admission and shared launcher grammar

Local-controller admission now reads and cancels through the elected process's
`DefaultTeam` tenant. `State` no longer supplies a second run-store adapter.
Readiness requires a reconciled, open composition and the shared readiness
check. Disconnect finalizers survive worker cancellation and log failures.
Explicit cancellation returns errors for each failed run. Both paths use
`Tenant.TransitionRun` with the current reaper fence. Live holders drain before
maintenance finishes their run. Repeated requests preserve the first reason,
and completed runs retain their result.

Invariant: admission can mutate only runs in the local tenant, under the elected
controller's live reaper fence. The generated property varies target identity,
lease expiry and successor holder/generation. The drain test covers repeated
cancellation, a live owner, owner expiry, terminal reads, missing runs and close.
The existing store/host acceptance component now checks real run state before
and after execution through the composed controller.

The lead's launcher-flag decision is applied. `RunLaunchCLI` consumes the shared
runner parser, which declares the required digest-pinned `--docker-image` and
refuses `--deadline`. The duplicate flag declarations are deleted. Namespace
and poll behavior retain their existing defaults. The regression first failed
with `unknown flag: --docker-image`, then passed through both the shared parser
and the runtime. Poll and memory tests provide the complete required grammar
before checking their intended refusal.

Production Go adds 98 physical lines and deletes 30, including the shared
registry edit and comment corrections. No whole legacy file is newly deleted.
The warm-pool dispatcher, run-host Kubernetes dispatcher, cluster worker and
old pod builders were removed in earlier sessions and remain absent.

The added adapter implements the existing admission contract with the owned
tenant and fence because keeping the whole RunStore in the foreign binder
leaves local cancellation unimplemented. Undoing it restores State's RunStore
embedding and removes admission.go. The parser change removes a duplicate
mechanism. It adds no environment variable, timeout or compatibility path.

## Amendment and handoff disposition

All 26 W8 amendment entries and the W8 handoffs were checked against this tree.
Completed work from earlier sessions remains in place.

| Entries | Disposition |
| --- | --- |
| A1-08,09,20,26,49,55 | Ordered/named claims, embedded authorization, credential files and here handling remain implemented. Production enrollment, local claimant provisioning and Actions wiring still need their final bindings. |
| A1-10,11,12,43,45,46,48,50,51,52 | Selected-engine installation, source/environment contracts, captured inputs and historical release matrices remain implemented. Native SDK/package and CLI placement composition remain integration work. |
| A1-18,27 | Runner/bootstrap components retain SourceAttempt and returned Applied.Attempt. W3 holder transitions still omit SourceAttempt. |
| A1-19,36,39 | Real-store runner and native-host construction remain implemented. Production source/process/policy/log/storage binding and remote NativeHostStore transport remain open. |
| A1-38 | Award correlation and fenced cleanup remain implemented. Transactional LauncherSyncReader and plan/eval resources remain open. A1-38 is not closed end to end. |
| A1-40,41 | Owned-store admission now joins election, queue, maintenance, detached startup, readiness, four holds, drain and managed units. Shared service and foreground CLI bindings remain open. |
| A1-42,53 | Deployed isolation, network probes, aggregate scratch, teardown, native platforms and real-repository QA remain open. Native catalog cells remain blocked. |
| Pending-claim designs | Approved outcome and cancellation implementations remain unchanged. Runner tests pass. |
| Lead decisions 3,5,6,10 | Shared HTTP handlers and award correlation remain. Decision 6 is completed by the shared registry/parser cut. Seeded provisioning and transactional sync remain with their assigned owners. |

## Contract changes needed and real integration points

- W8's `localcontroller.State` is narrowed to readiness, secrets, reconciliation
  and activity observation. Its two fixture callers are migrated. The public
  `wingd.RunStore` method signatures are unchanged.
- The only foreign production edit is the exact shared flag change approved
  by lead decision 6 in handoffs.md. The plan bootstrap grammar still requires
  `--job`, so W9's previously requested owner-unit correction remains open.
- W1/W2 still need to bind readiness, owned secret reads, reconciled/live
  activity and kernel-peer policy handlers. W8 now supplies admission's real
  reads and cancellation. W9's `resolveLocalServices` still returns unavailable.
- `NewEngineClient` and `source.Acquire` have implementations in this tree.
  They are not stub blockers. Production runner/pod entrypoint service bindings
  and the real native transport/process evidence remain open.
- `EngineController.SyncLauncher` has an adapter, but no production
  `LauncherSyncReader.ReadLauncherSync` implementation exists. W1/W2 must supply
  its transactional fence and resolved plan/eval resources.
- W3 must bind native host services, supply remote NativeHostStore transport
  and carry SourceAttempt on holder transitions. Actual hello remains part of
  the cross-area identity proof.
- W1/W2/W6 still need the approved seeded bootstrap path. ProvisionSpec accepts
  no seeded presentation in this tree. W8 adds no alternate issuer.
- W8 still needs deployed Probe/Clear implementations and native Cloud evidence.
  The runner and launcher executable entrypoints retain their unbound factories.
  No new architecture decision or unresolved product question is introduced.

## Verification

Go commands use this worktree with GOWORK=off, GOFLAGS=-buildvcs=false,
GOMAXPROCS=4, at most four package workers and GOCACHE/GOTMPDIR/TMPDIR under
`/tmp/sw-astra-W8`. The lead retains pipeline execution and independent review.

| Check | Result |
| --- | --- |
| `go build -p 4 ./...` | Passed for the final Go tree. |
| `go vet -p 4` across wingd/..., runners/..., engine/..., cluster and clisurface | Passed. |
| Runners/..., engine/... and internal/acceptance tests | Passed. |
| Localcontroller tests, with and without `-race` | Passed. |
| Admission property, `-fuzztime=1000x -parallel=2` | Passed 1,000 executions. |
| `tests/acceptance -run 'W8\|LauncherCloud\|LauncherController\|LauncherPolling'` | Passed after the final parser change. Includes TestNativeStoreOwnerW8/local_controller and launcher polling components. |
| Cluster and clisurface tests | Passed, excluding the native-socket TestStartMetricsListener_ServesMetrics. |
| Focused wingd/client and root wingd lifecycle, HTTP, dashboard, control, drain and managed-unit tests | Passed. |
| `go run ./internal/commentcheck -base main .` | Clean across 2,120 Go files. |
| `git diff --check` | Passed. |

The initial admission property incorrectly expected the foreign row's sequence
to remain zero after startup recovery. It now compares the before/after sequence
around cancellation. No production change was needed for that failure. No new
native acceptance cell or archived T1 source-function port is claimed.

Shared files touched: internal/clisurface/registry.go,
tests/acceptance/native_store_w8_test.go and
tests/acceptance/launcher_controller_w8_test.go. Other edits stay in W8's area.
The report and source docs use the natural-writing skill.

Friction: sockets and pipeline gates remain lead-owned under the swarm brief.
Overwing reports its installed binary ten commits behind source. Git config has
no local identity or core.worktree override. No Git write, product CLI invocation,
deployment or release ran. All check processes joined. Scratch cleanup verified
no executing scratch binary and removed `/tmp/sw-astra-W8`.

STATUS: CONTINUE
