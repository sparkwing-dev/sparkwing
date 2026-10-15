# W8 budget cuts and retained cost

Continues `444cab1ac` and `W8-LOCAL-INTEGRATION-BLOCKERS-AND-CUTS.md`.
Lead decision 17:15 assigns the remaining local integration to W9. This session
leaves that glue with W9 and changes only W8 runner and engine implementation.

The claim loop now has one event wait for pending claims, host completion,
polling, policy refresh and shutdown. The separate saturated-slot, empty-poll
and pending-claim wait loops are removed, along with their duplicate timers and
stop-result fields. Pending claims survive successful policy refresh. Shutdown
cancels and joins the pending request, hosts a late committed award, then joins
all held hosts. Policy denial and drain preserve held host contexts. Caller
cancellation and independent host failures remain in the returned error.

Runner installation now exposes one serialized `Apply` operation. The four
public stage methods and their three-boolean sequencing state are removed.
The native operation owns drain, replacement, managed-unit refresh and restart.
An error stops the sequence. Verification still precedes drain. The real-file
installation tests retain failure ordering, repeat-after-refresh-failure,
symlink and executable-mode preservation, plus Windows recoverable-image tests.
The old tests for calling individual stages out of order are deleted because
those calls are no longer representable. The duplicate fake-only sequencing
test is deleted. Managed-runner tests check one installation call after host
cleanup. Native installation tests retain failure coverage. This is a W8-local contract reduction.
No foreign production caller invokes the removed methods.

Store hosting uses the award already captured by `hostAward`. Its second JSON
copy, intermediate binding field and separate input-wrapper method are removed.
Authentication and persisted-input validation still finish before the service
callback. Existing generated award and repository-fence tests retain that proof.

SDK install requests use the existing strict JSON decoder directly. The custom
field walk and repeated decode are removed. Invariant: only unique `directory`,
`pipeline_dir` and `sdk` fields enter an install request. The generated property
varies field names, values and duplicate keys. Unknown or duplicate input leaves
the destination untouched, including its environment and mask registry.

This session adds 120 and deletes 262 production Go lines, net **-142**. No
whole production file is deleted. The earlier dispatcher, warm-pool, duplicate
source/heartbeat/trigger-host, supervisor and cache-chart cuts remain deleted.
No new T1 inventory test is ported or native acceptance cell activated.

| W8-owned path | Added versus main | Removed versus main | Net production Go lines |
| --- | ---: | ---: | ---: |
| internal/cluster | 582 | 2,564 | -1,982 |
| internal/runners | 3,498 | 2,235 | +1,263 |
| internal/wingd | 3,819 | 2,021 | +1,798 |
| internal/engine | 2,218 | 0 | +2,218 |
| charts | 0 | 0 | 0 |
| Total | 10,117 | 6,820 | **+3,297** |

Counts use the lead's `main` comparison and production exclusions: tests,
testdata, acceptance and contract declarations are excluded. Charts have no Go
files and therefore contribute zero to this budget. YAML/template deletions
remain in the diff. The checked starting HEAD measured +3,439 by this rule.
The preceding report's +3,431 differs by eight lines from that HEAD.

The -2,300 target is still exceeded by **5,597** lines. This is a justification
for the lead to review, not a budget waiver or a claim of end-to-end completion.
Each package has the following retained cost.

- **Cluster (-1,982):** the split pool/trigger/source and heartbeat code is gone.
  The remaining CLI parses the shared registry and binds runner, launcher,
  bootstrap, module warming and metrics services. Source and execution stay in
  the shared hosts. Removing these adapters would remove required entrypoints.
- **Runners (+1,263):** old k8s and warm-pool packages remove 1,871 lines. The
  replacement launcher adds 1,059 net lines because immutable award/Job fences,
  single-use Secret publication, restart-safe synchronization, exclusive node
  reservation, enforcement evidence and joined privileged-sidecar cleanup go
  beyond the architecture's roughly 250 retained Job-helper lines. These are
  A1-38/A1-42 and decisions 89/97-100 requirements. Root runner code is 1,183
  lines after the cuts, covering the shared loop, native/remote award binding,
  repository consent, verified updates and credential-file startup. Service and
  native-unit helpers add 830 lines here, while the deleted 645-line
  `internal/agentservice` is charged outside W8 by the area script. The helpers
  are shared with controller/schedule registration. Removing a second service
  implementation is already done; dropping the remaining one loses upgrades or
  native service support. Runner flag parsing adds 62 shared lines.
- **Wingd (+1,798):** decision 91 preserves admission unchanged. New runtime,
  lifecycle, local HTTP and control adapters total 1,096 lines; managed units
  require 332; Windows named-pipe transport and peer identity require 454. The
  local-controller owner and native host binder add 1,003 lines. These supply
  single-store election, reconciliation before readiness, four activity holds,
  authenticated local transport, HTTP/worker joins and captured-home services.
  Supervisor and duplicate client paths are already removed. Replacing these
  with direct CLI store opens, omitting Windows peer checks or returning before
  cleanup joins violates the contract. Local integration is now W9's area.
- **Engine (+2,218):** decisions 101-103 added this package after the frozen
  budget. It covers native Go/npm pins, exact signed closed release matrices,
  executable identity verification, machine cache revalidation, signed policy
  and persisted rollback protection, notices, verified offline fallback,
  selected-engine SDK installation and staged verified upgrades. The upgrade
  must preserve unrelated manifest fields and refuse concurrent pin edits.
  Removing the range parser would refuse valid locked npm ranges. Reusing the
  ambient CLI would violate exact engine selection. The package uses existing
  release trust roots, strict JSON, process environments and native binary cache
  owners rather than adding replacements.
- **Charts (zero production Go):** the cache manifests and old dispatcher RBAC
  are already gone. Remaining deployment, separate launcher/customer service
  accounts, bootstrap Secret permissions, resources and managed volumes support
  the required runner/controller topology. No chart changes are needed for the
  internal reductions in this session.

All W8 rows in amendments-1 and handoffs retain their completed component
implementations. A1-38 remains closed on W8's side: the store-backed launcher
proof uses real controller claim, exchange, start, sync and completion with fake
Kubernetes and isolation boundaries. A1-39/40/41 retain the unified runner,
elected controller and captured-home unit implementations. A1-10/43 retain
exact-engine installation, signed policy/fallback and historical asset support.
A1-08/09/11/12/18/19/20/26/27/36/45/46/48/49/50/51/52/53/55 continue through
the existing shared interfaces. The removed dead contract tables stay removed.
The 12:35 retry-origin rule remains covered through hosting, claim and exchange.
No newly assigned independent W8 implementation remains in the lead entries.

Contract changes needed in other areas: none introduced here. W9's local
integration needs the persisted peer authority, private host-state transport and
local schedule registration/pin/tick operations named in the preceding report.
W1 retains sealed trusted schedule intake. W3/W7/W2 retain final authorized
storage, process binding and log projection integrations. W9 also owns production
runner/launcher/bootstrap service composition and notice/upgrade presentation.
T1/lead retain native Linux/Darwin/Windows sockets and process evidence,
Kubernetes default-deny enforcement, exclusive team-node probes, aggregate
20 GiB accounting, DinD teardown and real-repository placement QA. Fake-backed
component proofs do not close those native integration points.

Verification used `GOWORK=off GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W8`,
`TMPDIR=/tmp/sw-astra-W8`, `GOMAXPROCS=4` and at most `-p 4`.

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for `./internal/runners/... ./internal/engine/...
  ./internal/cluster ./internal/wingd/localcontroller`.
- All runner, launcher, service, engine and SDK-installer tests pass. The
  cluster suite passes with only `TestRunnerMetricsServesAndJoins` excluded
  because its native TCP bind is unavailable in this sandbox.
- The local-controller short suite passes. No local integration probe was
  changed or retried because W9 now owns that work.
- `go test -p 4 ./tests/acceptance -run 'W8|LauncherCloud' -count=1
  -timeout=60s` passes. This retains engine/policy/native-npm, mixed and named
  claims, authority/deadline, stored owner/node, launcher synchronization,
  bootstrap, service/unit and Cloud pod/boundary component acceptance.
- The final complete `internal/runners` suite passes under `-race`. The focused
  runner-service race checks pass too.
- Three existing loop properties each pass 1,000 generated executions:
  `FuzzLoopPendingClaimStopOutcomes`, `FuzzLoopPendingClaimNoReplay` and
  `FuzzLoopPendingPolicyInterruption`. The new
  `FuzzSDKInstallRequestFields` passes 1,007 executions.
- `commentcheck -base main .` passes across 2,261 files. `git diff --check`
  passes. No local Git identity or core.worktree override exists.

The first combined package command accidentally included the full short
acceptance suite. It failed at its 90-second overall timeout while running
`TestProtocol/protocol-host-layers-risk-cleanup/typescript_sdk_boundary`.
That broader suite is not certified. Only the subsequent named W8 selection
is reported as passing. No foreign protocol or TypeScript code was changed.
The optional deadcode command could not run because x/tools' telemetry module
has no go.sum entry. No dependency or checksum was added to enable that audit.

No new native acceptance cell is claimed. The platform and deployment evidence
listed above remains open for the owning integrator or lead.

Source docs: `internal/runners/HOSTING.md`,
`internal/runners/service/RUNNER-SERVICE.md` and W8's engine/version contract.
The implementation reductions change no CLI grammar, wire fields, configuration
schema or generated public reference, so no generated-doc drift fixture changes.

Shared production files touched: none. No `cmd/sparkwing`,
`internal/orchestrator`, lead-owned amendment or handoff file changes.
No Git writes, candidate installation, Sparkwing execution beyond orientation,
pipeline, deployment or release ran. Mechanisms added: none. No new option,
environment variable, service, store, credential issuer or compatibility path.

W8 component implementation and the independent budget-cut pass are complete.
The budget justification is submitted above. Shared composition and native
acceptance remain with their named owners. Worker scratch is deleted after
checks join; the shared build cache is retained.

STATUS: COMPLETE
