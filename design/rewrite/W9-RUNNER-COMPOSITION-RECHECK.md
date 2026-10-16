# W9 runner composition recheck

Base `dc88126f7`, environment `1087`. The runner executable composition is
implemented. `MainWithVersion` supplies production runner, launcher, source-init
and bootstrap services. The four dependencies from the preceding report are closed.
Lead-owned handoffs and amendment files were read and left unchanged.

## Implemented

| Dependency | Production implementation |
| --- | --- |
| Canonical owner placement | Private snapshots carry decisions bound to owner authority/generation and node generation/declaration hash. `OwnerCanExecute` consumes them. Snapshot and transactional reconciliation use the shared matcher, resources, repository scope and ordered preferences. A declined self-claim dispatches work instead of aborting the owner. A live owner retains its captured claimant observation while its only runner slot is occupied. |
| Owner/eval reporting | Annotations and summaries use live owner or source/eval authority independently of work attempts. Auxiliary process services retain that source relation. Steps remain attempt-fenced. Declared references retain pipeline/job and tenant checks. Remote owner expansion derives a narrower source capability at the controller. Frozen award grants are unchanged. |
| Production Cloud boundary | `productionLauncher` constructs client-go, controller transport and measured boundary callbacks. Live listener/witness/workload canaries test enforcement, controller access and pod/node escape targets. Policy and reservation snapshots are fenced. Cleanup selects reservation-owned object UIDs and waits for Pods to disappear. The chart and image carry the required permissions and tools. |
| Plan/eval hosts | `OwnerHost.Plan` accepts the plan and leaves scheduling to the controller. `OwnerHost.Evaluate` submits the awarded source expansion under an independent eval lease. Private state/authentication/storage transports support both. Logs publish before their leases retire. Unaccepted plan failures atomically finish the plan and run through the existing result grant. |

Cloud execution reuses the init container checkout. The verifier checks origin,
commit, tracked changes, untracked/ignored files and pipeline containment before
installation. It does not fetch source again with an exchanged eval credential.
Unaccepted remote TypeScript pipelines select their SDK after checkout. Local
paths retain early validation, and accepted sources retain their sealed engine.

Memoized source generation zero survives real claim, exchange, renewal, expansion
authorization and log publication. The independent eval lease remains positive.
The final schema admits zero source generations without migrations or backfill.

Launcher preparation uses the existing sync route with optional `preparing` and
`prepared` fences. A joined shared lease keeper covers blocking boundary probes
and Job publication. Subsequent sync covers source-init until exchange. Only the
original launcher's live bootstrap can renew. Exchanged claims remain with their
host; ended awards still reach cleanup. Preparation uses the shared Cloud billing
and credit-grace functions. Invalid observations cannot mutate a lease.

## Evidence

Tests ran red before implementation, including the original four probes, absent
plan/eval host methods, wrong TypeScript selection, untracked/ignored prepared
source, API-defaulted canaries, conflicting destination classifications and
missing preparation contracts. Store and HTTP tests use persisted SQLite state.
Kubernetes tests use API fakes; SDK-host tests use scripted external boundaries.

| Probe or acceptance component | Result |
| --- | --- |
| `TestProductionRunnerCommandsClaimAndHost`, start/warm trigger and node | Claims, renews and binds native services; socket-skipped at the sandbox listen refusal. |
| Production owner/node and Cloud plan/eval native-host probes | Reach the same socket boundary. The placement-policy and eval-liveness refusals are gone. |
| `TestProductionCloudExchangeReachesAwardRenewal` | Plan, eval and node exchange once and reach renewal with the exchanged award. |
| `TestInstalledRunnerRuntimeComposition` | All five commands reach their production HTTP operation, including launch's Kubernetes Job listing. |
| `TestReviewOwnerEvaluationReportsBeforeWorkAward` | Annotation and summary return 204. |
| `TestRunnerPlanAndEvalUseTheirAwardedUnitAuthority` | Plan, eval and generation-zero eval reach authenticated hosting; a missing eval lease refuses before renewal. |
| `TestLaunchOneKeepsLeaseDuringBoundaryPreparation` | Publication succeeds after more than three minutes of blocked preflight, then joins its keeper. |
| Persisted placement/source/metadata/plan-failure/preparation properties | Pass, including stale/foreign fences, exact expansion replay, source generation zero, billing progression and credit-grace expiry. |

Whole-repository `go build ./...`, focused vet, short suites for cluster, launcher,
blob, capability, client, clisurface, controllerwire, source, nodehost,
localcontroller, doccheck, apispec and apiref pass. Focused runner lifecycle,
acceptance components and controller/store regressions pass. API/OpenAPI/reference
snapshots and the embedded API reference are regenerated. Commentcheck and
whitespace checks pass. No archived test was newly ported. Native acceptance
cells remain blocked until the lead exercises their native drivers.

The full runners suite also exposed unchanged proxy parsing/masking port failures
in `TestMaskProxyCredentialsCoversEveryFormOfTheCredential` and
`FuzzRunnerProxyCredentialsRemainMasked`. Focused runner lifecycle tests pass.
An initial scheduler cancellation test failed once; its focused rerun and later
full nodehost suites passed without a scheduler change. These observations are
retained for the lead's broader gate.

## Contract changes made

- C1: authenticated placement projection and Dispatch recheck; plan/eval private
  snapshots; source metadata/reference authorization; bounded pre-seal plan
  failure; generation-zero eval/log identity; fenced preparation lease writer
  using the existing billing/grace machinery.
- C2: source-report binding and route alternatives using existing grants;
  source-bound owner expansion; plan failure through `node.result`; preparation
  renewal through the existing launcher authorization boundary.
- C3 wire: private placement transport and optional preparation fences/acks.
- C3 host/protocol: plan/eval host methods, remote expansion actor adapter,
  prepared checkout and SDK selection, zero-source propagation and log ordering.
- C4: production boundary callbacks, recovery cleanup, chart RBAC and runner tools.
  Endpoint classification refuses a destination that is both required and
  forbidden. API scalar defaults are authored explicitly.

No foreign contract change remains requested for this slice. Each amendment is
recorded in its corresponding contract file. Decisions 111/113 and preceding W9
wire/CLI cuts remain implemented; this session does not reopen them.

## Mechanisms and scope

The private placement projection replaces unconditional local execution. The
small direct-store snapshot wrapper exists because plain `Tenant.ReadSnapshot`
does not carry authenticated placement decisions. Remote expansion uses an
actor callback because the controller owns source delegation. The prepared
checkout option is necessary because eval's exchanged grants exclude source
fetching. Live Kubernetes canaries replace absent enforcement callbacks. The
preparation writer and shared keeper prevent long preflight from consuming the
initial award lease. These additions are used by production composition; no
parallel scheduler, compatibility API or new bootstrap grant was added.

This session changes production Go by **+1,375/-116, net +1,259**, including
integration edits in other areas. No complete legacy file or API was removed;
116 superseded production lines were replaced in place. W9-owned production Go
versus main is **+16,975/-23,817, net -6,842**, within the -2,900 budget. Counting
all new integration code against W9's preceding -6,856 baseline still leaves
-5,597 net lines. No API removal required caller migration. Dispatch consumers
and CloudControl test doubles moved directly to the completed contracts.

Shared production files touched (brace groups list complete filenames):

- `internal/blob/logs.go`; `internal/capability/{policy.go,routes.go}`.
- `internal/cluster/{production_runtime.go,production_launcher.go}`.
- `internal/nodehost/{awarded_units.go,owner.go,owner_expansion_authority.go,owner_schedule.go,process_authority.go,reconcile.go,remote_store.go,run_input.go}`.
- `internal/runners/{hosting.go,loop.go,store_host.go}` and `launcher/{boundary.go,controller.go,job.go,launcher.go,preparation.go,production_boundary.go}`.
- `internal/source/{acquire.go,checkout.go,source.go}`.
- `internal/wingd/localcontroller/{native.go,native_client.go,native_logs.go}`.
- `pkg/controller/{engine_intake.go,engine_launcher_sync.go,private_host.go,source_expansion.go}`.
- `pkg/store/{authority_binding.go,authority_issue.go,binding_execution.go,launcher_preparation.go,node_state.go,owner_placement.go,result_metadata.go,run_state.go,source_reporting.go,state_authority.go,state_owner_read.go,state_reconcile.go,unit_claim.go,unit_matching.go,unit_renewal.go,schema.sql}`.
- `build/Dockerfile.runner`, `bin/check-runner-image.sh`,
  `charts/sparkwing-runner-bundle/templates/launcher-rbac.yaml` and
  `charts/sparkwing-full/charts/sparkwing-runner-bundle-0.1.12.tgz`; focused tests
  and generated API snapshots accompany these changes. W9 CLI callers changed
  in `cmd/sparkwing/{local_host.go,local_run_host.go}`.

## Native integration and friction

Native execution remains the lead's work: actual SDK processes and sockets,
Docker, Kubernetes CNI/host firewall enforcement, UDP and additional provider
source/blob destinations, kubelet reclamation and other native platforms. The
boundary clears launcher-owned ephemeral state; arbitrary privileged host writes
require node reprovisioning. Fake Kubernetes evidence is not native security
proof. No deployment or release ran.

The Go shim intermittently stalled, so final checks used the installed Go 1.26.9
binary directly. Helm's unrelated repository refresh encountered sandbox DNS
refusals; local chart/vendor checks passed. Overwing reported installed-source
lag while returning standards cards. Go checks used `GOWORK=off`,
`GOFLAGS=-buildvcs=false`, `GOCACHE=/tmp/sw-gocache`, scratch under
`/tmp/sw-astra-W9`, and at most four test package workers. No Git writes or
operational Sparkwing commands ran. Scratch is removed after checks join; the
shared cache is retained.

STATUS: COMPLETE
