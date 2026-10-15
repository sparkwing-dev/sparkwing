# W8 merge and award reconciliation

## Implemented

Both conflicted files have resolved contents. `cmd/sparkwing/wingd.go` uses the
incoming shared parser and foreground runtime contract. Its duplicate
`runWingdRunWith` constructor is deleted, and its election/failure tests use
`runWingdRunCommand`. `internal/nodehost/cache_directory.go` uses the incoming
`pipelineentry.NPMCommand` while retaining grouped tool cleanup. No conflict
markers remain. The index still lists both paths as unmerged because the swarm
brief prohibits Git writes. The lead must stage the resolved files and finish
the merge. Lead-owned contracts and handoff files are unchanged.

Launcher synchronization now correlates awards by `(kind, id, holder,
generation)`. The former unit-ID-only map rejected an old Job and its replacement
before contacting the controller. Multiple generations can now reconcile in one
batch. The actual Job namespace/name/UID travels through the amended wire, and
teardown retains its UID deletion precondition.

Invariant: cleanup for an older award cannot select the replacement generation's
Job. `FuzzControllerSyncSeparatesClaimGenerations` generates plan/eval/work
generations and observation order. It failed all six seeds before the change.
`TestLauncherRetiresStaleGenerationAfterRestart` reconstructs a fresh launcher
from persisted Kubernetes fixtures, removes the stale Job, checks its UID
precondition and retains the current Job. Kubernetes and the cleanup reader are
fixtures. This does not prove persistence-side authorization or deployed cleanup.

The real-store launcher fixture now consumes `LauncherObservation.Award` and
`LauncherJobRef`, and checks its observed fence against persisted ownership.
The runner and Cloud bootstrap fixtures now supply the captured `SourceAttempt`
and consume `Applied.Attempt` for completion. W9's newly merged wire field makes
those migrations possible. No production ordinal is guessed.

## Deletions and ports

Deleted 163 lines: the 36-line duplicate foreground constructor from the incoming
merge and the 127-line `internal/engine/npmcmd` implementation/test package.
Engine installation, upgrade, dependency-cache resolution and native npm
acceptance now use `pipelineentry.NPMCommand`. Its existing platform cases cover
the deleted examples. The existing arbitrary-argument fuzz test moves to that
shared helper. The warm-pool dispatcher, run-host Kubernetes dispatcher, cluster
worker and old pod builders remain absent. Their prior deletions are not counted
again. No additional historical acceptance port or native cell is claimed.

Shared files touched beyond W8 production ownership:

- `cmd/sparkwing/wingd.go` and `cmd/sparkwing/wingd_runtime_test.go`.
- `internal/nodehost/cache_directory.go` for conflict resolution.
- `internal/depcache/depcache.go` and `internal/depcache/depcache_npm_test.go`.
- `internal/pipelineentry/npm_test.go` for the moved fuzz property.
- W8 fixtures in `tests/acceptance/store_launcher_w8_test.go`,
  `runner_store_w8_test.go`, `bootstrap_store_w8_test.go` and
  `engine_native_tools_w8_test.go`.

Source docs changed in `internal/wingd/LOCAL-CONTROLLER.md` and
`internal/runners/launcher/CLOUD-JOBS.md`.

## Amendment and handoff disposition

All W8 entries in `amendments-1.md` and `handoffs.md` were checked against the
merged tree. Earlier completed work remains in place.

| Entries | Current disposition |
| --- | --- |
| A1-08,09,20,26,49,55 | Shared ordered/named claim routes, service authorization, credential files and here scope remain. Production enrollment, local claimant provisioning, startup bootstrap input and Actions evidence remain integration work. |
| A1-10,11,12,43,45,46,48,50,51,52 | Existing engine/source/environment/snapshot implementation remains. The npm command duplicate is cut. W9's HTTP clients are now implemented. Native packaging, SDK execution and caller composition remain unproven. |
| A1-18,27 | W8's affected fixtures now send the captured source ordinal and use the returned execution ordinal. W3's production holder transitions still omit SourceAttempt. |
| A1-19,36,39 | Real-store queue/native-host construction remains. W3 now declares GrantedRunner and NewNativeHost, already consumed by W8. Native process, policy, source, storage and remote NativeHostStore transport still need production bindings. |
| A1-38 | W8's wire observations, restart correlation and UID-fenced deletion are implemented, including overlapping claim generations. Overall A1-38 remains open: W1/W2 need the transactional LauncherSyncReader, and plan/eval awards still need resolved execution resources. |
| A1-40,41 | Single-store composition, readiness, four holds, joined drain, managed units and leased maintenance remain. The duplicate foreground constructor is removed. Production state/peer/service binding and W9 local-services construction remain open. |
| A1-42,53 | Deployment isolation, egress, scratch accounting, native teardown and platform/real-repository QA remain open. No native cell is enabled. |
| Pending-claim outcome/cancellation designs | The earlier approved implementation remains unchanged. Full runners tests pass. |
| Lead decisions 3,5,6,10 | The HTTP composition interface is retained. Award correlation is implemented. Seeded bootstrap provisioning and the W9 launcher grammar changes remain with their owners. |

## Real integration and contract needs

- W1/W2 must supply the declared local `State` and `LocalHTTP` implementations:
  admission run state, owned secret read, initial/live activity and kernel-peer
  authorization. W8 retains the election, store, shared queue and maintenance.
  `resolveLocalServices` still returns unavailable, so no working production
  on-demand controller is claimed.
- W1/W2 must implement `LauncherSyncReader` against current persisted authority
  and the amended award/Job instance observations. W8 needs no further wire
  shape change. A1-38 is not closed end to end.
- W2 must supply policy-owned CPU/memory for plan/eval awards and complete
  granted runner labels. W8 does not infer these from ceilings or observations.
- W3 must carry SourceAttempt on production holder transitions, bind the native
  source/process/policy/log/storage services and provide remote NativeHostStore
  execution transport. Its executor error-masking property also fails.
- W9 must compose the local-services and runner/pod entrypoints. The shared
  launcher registry still lacks `--docker-image`, retains `--deadline`, and
  requires a job ID for plan units. The existing RunLaunchCLI parser remains
  until those lead-directed grammar changes land.
- W1/W2/W6 must consume the lead's seeded bootstrap provisioning decision.
  ProvisionSpec still has no seeded presentation. No alternate issuer was added.
- T1/lead retain native Cloud, Windows/Darwin/Linux, Postgres and real-repository
  evidence. This continuation makes no new product decision.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, private GOCACHE/GOTMPDIR
under `/tmp/sw-astra-W8`, GOMAXPROCS=4 and at most four package workers.

| Check | Result |
| --- | --- |
| Whole-repository `go build -p 4 ./...` | Passed after all caller migrations. |
| Full runners, engine, localcontroller, pipelineentry and depcache suites | Passed. |
| `tests/acceptance -run 'W8\|LauncherCloud\|LauncherController\|LauncherPolling'` | Passed. Covers store runner/launcher/bootstrap, local controller, managed units, engine, here, credential and authority components. |
| Local runtime/lifecycle/HTTP/control/dashboard/drain and cache-directory selections | Passed. |
| Foreground CLI tests | Passed through the single shared command path. |
| Runner/launcher/bootstrap CLI selection | Passed. |
| Claim-generation fuzz, 1,000 executions with two workers | Passed. |
| `internal/acceptance` | Passed. |
| Focused vet across W8, changed shared packages, CLI, nodehost and acceptance | Passed. |
| commentcheck against main | Passed across 2,115 files. |
| `git diff --check` | Passed. |

A broader acceptance selection including every fuzz seed fails
`FuzzExecutorFailureMask` for both existing seeds with
"rendered host error contains an SDK secret payload". This is outside W8's
changed code. The full cluster suite fails its metrics listener with socket
operation not permitted. No full acceptance, native deployment or full cluster
pass is claimed. The lead retains independent review and commits.

Friction: the sandbox blocks sockets and Git metadata writes. Overwing reports
its installed binary behind source. No candidate, deployment, release or Git
write ran.

All check processes joined. `/tmp/sw-astra-W8` was removed after verification.

STATUS: CONTINUE
