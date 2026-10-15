# W8 on-demand process continuation

`wingd/client.NewLocalLifecycle` now starts an absent controller through the
existing detached `wingd supervise` launcher. The captured absolute executable,
home and version reach its `wingd run` child. The admission client's start claim,
election checks and startup budget are reused, with takeover disabled. Readiness
requires running local-controller status. Stay, dashboard attachment and drain
use the authenticated socket. Installed-unit status survives startup.

Standalone retains the supplied in-process runtime. Ordinary startup never
falls back to opening the store in the requesting CLI. Request cancellation
does not stop a detached owner. Unknown or unreachable owners fail without
starting another process. Concurrent callers reuse the ready owner, including
an owner with a different version.

Invariant: ordinary on-demand startup never opens the store in the requesting
CLI. Only standalone may run its supplied in-process runtime.
`FuzzLocalProcessLifecycleKeepsOwnership` varies owner presence, stay, dashboard,
spawn failure, cancellation and foreign homes. The new lifecycle tests also
cover reuse after request cancellation, version skew, standalone failure,
startup failure and joined stop. The process launcher and socket owner are
contract fakes in these tests. They do not prove a native detached process.

Production Go adds 139 physical lines and deletes 7. One deleted executable line
selected the former ordinary in-process lifecycle. The rest replace its GoDoc
and private return signature. No whole legacy file was deleted in this pass.
The warm-pool dispatcher, run-host Kubernetes dispatcher, cluster worker and old
pod builders were already deleted and remain absent. No historical T1 inventory
port or native catalog cell is newly claimed.

The added mechanism selects detached versus standalone lifecycle execution.
The elected in-process runtime alone cannot outlive a requesting CLI. Reverting
the public constructor call and deleting `local_process.go` removes this dispatch.
There is no new environment variable, CLI flag, timeout value or compatibility
path. Source documentation changed in `internal/wingd/LOCAL-CONTROLLER.md`.
No shared production caller, foreign contract, amendment or handoff file changed.

## Amendment and handoff disposition

All 26 W8 amendment rows and the W8 handoffs were checked against this tree.
Finished work from the previous sessions remains in place.

| Entries | Disposition |
| --- | --- |
| A1-08,09,20,26,49,55 | Ordered/named claims, embedded controller authorization, credential files and here handling remain implemented. Production enrollment, local claimant provisioning and Actions wiring need their final bindings. |
| A1-10,11,12,43,45,46,48,50,51,52 | Existing selected-engine install, source/environment contracts, captured inputs and retained historical release matrices remain. Native SDK/package execution and CLI placement composition remain integration work. |
| A1-18,27 | Store runner/bootstrap components pass with captured SourceAttempt and returned Applied.Attempt. W3 production transitions still omit SourceAttempt. |
| A1-19,36,39 | Real-store runner and NewNativeHost construction remain. Source/process/policy/log/storage services and remote NativeHostStore transport still need production bindings. |
| A1-38 | Award-generation correlation and UID-fenced cleanup remain implemented. A1-38 is not closed end to end. Transactional LauncherSyncReader and plan/eval resources remain open. |
| A1-40,41 | Detached startup now joins single-store composition, readiness, four holds, drain and managed units. Foreground service binding and W9 local-services construction remain open. |
| A1-42,53 | Native Cloud enforcement, scratch accounting, teardown, platform execution and real-repository QA remain open. Native cells stay blocked. |
| Pending-claim designs | The approved outcome and cancellation implementation remains unchanged. Full runners tests pass. |
| Lead decisions 3,5,6,10 | HTTP handler composition and award correlation remain implemented. Seeded startup provisioning, W9 grammar and transactional launcher sync remain with their assigned owners. |

## Real integration and contract needs

- W1/W2 must supply `localcontroller.State` and `wingd.LocalHTTP`: admission
  cancellation and readiness, owned secret reads, reconciled/live activity and
  kernel-peer policy handlers. W8 owns election, the sole store handle, shared
  queue and leased maintenance. W9's `resolveLocalServices` still returns
  unavailable. No working production on-demand controller is claimed.
- W9 can use the existing `client.NewLocalLifecycle(home, runtime, units)`
  signature for detached startup. Supply absolute Executable and release Version
  in LocalStartOptions, and bind the standalone runtime to that same home.
- W1/W2 must implement `LauncherSyncReader.ReadLauncherSync` transactionally.
  Its controller adapter exists, but no production reader implementation is
  present. W2 must supply resolved plan/eval CPU and memory and complete granted
  runner identity. W8 does not infer these facts from observed capacity.
- W3 must bind native source/process/policy/log/storage services, provide remote
  NativeHostStore transport and carry SourceAttempt on holder transitions.
  Its executor masking failure was recorded by the previous session. That
  broader acceptance failure was not rerun in this pass.
- W9 must compose runner and pod entrypoints, add the approved `--docker-image`
  registry entry, remove `--deadline`, and allow plan bootstrap without a job.
  Those shared grammar changes have not landed in this checkout.
- W1/W2/W6 must consume the approved seeded bootstrap provisioning decision.
  ProvisionSpec still has no seeded presentation. No alternate issuer was added.
- T1/lead retain native HTTP/api.sock, deployed isolation/egress/scratch/cleanup,
  Windows/macOS/Postgres and real-repository evidence. No new product decision
  or foreign contract amendment was made.

## Verification

Commands use `go -C` with this worktree, GOWORK=off, GOFLAGS=-buildvcs=false,
GOMAXPROCS=4, at most four package workers and GOCACHE/GOTMPDIR/TMPDIR under
`/tmp/sw-astra-W8`.

| Check | Result |
| --- | --- |
| `go build -p 4 ./...` | Passed for the whole repository. |
| Full runners/..., engine/..., localcontroller and internal/acceptance tests | Passed. |
| `tests/acceptance -run 'W8\|LauncherCloud\|LauncherController\|LauncherPolling'` | Passed, including real-store runner, launcher and bootstrap components. |
| Client lifecycle tests and ownership fuzz seeds with `-race` | Passed. |
| Ownership fuzz, `-fuzztime=1000x -parallel=2` | Passed 1,000 executions. |
| Focused root wingd local-runtime/HTTP/dashboard/control/drain/managed-unit tests | Passed. |
| Cluster tests excluding `TestStartMetricsListener_ServesMetrics` | Passed. The excluded test requires native sockets. |
| Vet across wingd/..., runners/..., engine/... and cluster | Passed. |
| `go run ./internal/commentcheck -base main .` | Clean across 2,117 Go files. |
| `git diff --check` | Passed. |

Friction: native sockets and pipelines remain lead-owned under the swarm brief.
Overwing loaded the standards while reporting its installed binary ten commits
behind source. Git config has no local identity or core.worktree override. No
Git write, Sparkwing binary, deployment or release ran. Shared files touched:
none. The lead retains independent review and commits.

All check processes joined. The shell filter rejected force-style removal, so
scoped Python cleanup removed `/tmp/sw-astra-W8` after checking for scratch
executables. The directory is absent.

STATUS: CONTINUE
