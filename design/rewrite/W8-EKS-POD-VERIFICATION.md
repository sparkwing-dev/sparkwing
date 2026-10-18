# W8 EKS launcher continuation - 2026-10-17

Superseded on 2026-10-18: job namespace Pods are enforced at admission and the launcher keeps only a synchronous award backstop. See `internal/runners/launcher/CLOUD-JOBS.md`.

Implemented the 07:30 lead decision in launcher and charts. This report covers
the uncommitted work after 8a7a9ed68. The lead owns review and commits.

## Implementation and tests

Invariant: a created Pod may differ from the requested spec only by Kubernetes
defaults and the named EKS injections. Any other mutation deletes its Job and
fails the exact live award.

- The Job saves its original template before API defaulting. The launcher checks
  Job CREATE and activation responses, then reads Pods after activation and on
  reconciliation, including restart. Matching a changed Job template and Pod
  cannot replace the original requested spec. Pod lookup also checks owner UID
  and GenerateName, so removing the custom Job label cannot evade verification.
- Exact spec comparison replaces the previous derivative comparison. EKS
  allowances cover IRSA and container credentials env, their projected token
  volumes and read-only mounts, region/STS env, Pod ENI requests/limits and its
  exact annotation key, and ALB target-health readiness gates. The same comparator
  handles boundary canaries. Changed injection shapes fail.
- Rejection logs the Job/unit/fence, saves a rejection annotation, UID-fences Job
  deletion, and reports failure through launcher sync. The cleanup finalizer
  retains pending acknowledgement and container teardown across restart.
- Store rejection rechecks current launcher ancestry and original award identity.
  Plan, eval and work use shared transitions, billing and settlement. Exchanged
  awards fail too. Forged fences refuse. Replays cannot fail replacement awards.
- Chart infrastructure Pods have component app labels without changing immutable
  Deployment selectors. Customer Pods and measurement probes use sparkwing-job.
  The Cloud example explicitly sets fsGroup and proxy CIDRs. The NodePool example
  sets the team label in template metadata and its matching NoSchedule taint.
  The existing separate launcher token/file projection and ExternalSecret mapping
  remain the bootstrap path. No new flag or environment variable was added.

Tests were written first for Pod rejection and persisted award failure. New
acceptance TestLauncherPodRejectionCommitsFailedAwardW8 exercises the real
controller/SQLite store through the launcher with a fake Kubernetes API. Added
container, changed image, removed security field and extra hostPath each delete
the Job and persist a failed award. Launcher tests also cover each allowed EKS
injection, removed labels, malformed allowed injections, activation readback and
restart comparison. Store tests cover all three kinds, exchange, replay and
replacement safety. Property/fuzz tests cover unexpected Pod mutations and forged
award rejection. No historical port was reopened or native cell unskipped.

## Deletion and size

Deleted admission_exemption.go (109 lines), admission_guard.go (30), their
173-line test, and the 40-line Helm policy template: 352 lines. Removed all
admission API RBAC and LimitRange create/probe authority, startup checks and fake
policy setup. Removed the redundant host-container comparator and reuse one
strict spec comparison for Jobs, Pods and probes.

Production Go delta for this session, including shared adapters: +496/-191, net +305.
W8-owned production Go this session: +348/-183, net +165.
W8 production Go against main: +12457/-7142, net +5315. Counts exclude tests, testdata,
contracts directories and *_contract.go declarations. The -2,300 area budget is
still unmet. This session adds a bounded verifier and a persistent rejection path
because the approved admission-policy cut requires readback and real award
failure. It shares comparators and lifecycle transitions. The inherited area's
budget overage still needs lead review or further cuts.

## Contract changes made

- c1-data.md: RejectLauncherAwards, launcher actor and launch_rejected transitions.
- c3-controller-wire.md: optional LauncherObservation.Rejected on the existing
  sync route. Reservation-only observations cannot report rejection.
- c4-surface-config.md: original-template verification, EKS allowances, rejection
  lifecycle, chart labels, taints, ownership and credential projections.

No changes are requested from another area to finish this assigned slice.
The lead-owned handoffs/amendments files were not edited.

## Verification

- Whole-repository go build ./... passed with GOWORK=off and buildvcs disabled.
- Full launcher, controllerwire and charts suites passed.
- Focused controller/store launcher suites and acceptance launcher/reservation
  components passed, including the new persisted Pod-rejection cell.
- Vet passed for launcher, controllerwire, controller, store, charts and acceptance.
- Main-relative commentcheck passed across 2,787 files.
- Pod fuzz: 5,677 executions. Award-fence fuzz: 193 executions. Both passed.
- Helm lint of the Cloud example, generated API spec and embedded docs drift
  checks passed. The chart suite includes Cloud rendering and vendored parity.

Checks use GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and go test -p 4. Socket-dependent
native tests belong to the lead harness. Its required
W8-launcher-native-latest.md file was absent at both the first read and the final
check, so this report cannot claim native completion.

## Shared files and generated drift

Shared implementation edits: internal/controllerwire/engine.go and launcher.go,
pkg/controller/engine_launcher_sync.go, pkg/store/node_state.go, run_state.go,
run_spec.go, state_authority.go and new launcher_rejection.go. The deleted policy
API's other callers were migrated in pkg/controller/burst_cloud_job_test.go and
tests/acceptance/launcher_kube_w8_test.go. The acceptance boundary fake now supplies
real Job-owned Pod metadata. New controller/store/acceptance tests exercise failure.

Shared docs: the three contracts above, CHANGELOG.md, generated
api/openapi.yaml, docs/api-reference.md, pkg/docs mirrors and .apidiff/pkg_store.txt.
Regeneration also refreshed pre-existing AppFacts/PullRequest schema fields,
operator-settings policy rows and store snapshot drift already present in source.
No unrelated production implementation was changed.

## Real integration and remaining work

- The lead must commit, review, then run its native launcher checks and produce
  the requested latest report. No EKS deployment, service or credential mutation
  was attempted here.
- Apply the team NodePool template with the actual TeamNodeValue, existing
  EC2NodeClass and provisioned Ready capacity. The launcher still does not create
  capacity. Audit namespace/cluster bindings: chart RBAC cannot revoke another
  binding's grants. Only launcher identity may create customer Pods/Jobs.
- kikd-infra baseline allow-all policies must exclude sparkwing-job from ingress
  and egress. The current baseline egress only excludes sparkwing-launcher, and
  ingress has no sparkwing-job exclusion. Component app labels fix infrastructure
  selection, while real probes keep customer launch closed until enforcement is
  correct. The infra repository was read, not edited.
- Verify the actual EKS PodLevelResources/native-sidecar defaults, each installed
  webhook injection and escaped-node/network enforcement. Supply real images,
  credentials, S3 IAM/CloudFront and proxy CIDRs through the existing bootstrap.
- Verification is detection after creation, as assigned. If no Pod exists at the
  immediate read, reconciliation checks it when the Job controller creates it.
  There is no pre-execution admission gate. Rejection never rewrites an already
  terminal or replaced award. These timing limits need native integration evidence.
- Earlier W8 amendment implementations were retained. Native placement/OS proof
  and the inherited production budget remain open rather than being re-declared
  complete in this chart/launcher slice.

Primary source checks for EKS shapes used the
[AWS pod-identity webhook](https://github.com/aws/amazon-eks-pod-identity-webhook),
[AWS VPC controller](https://github.com/aws/amazon-vpc-resource-controller-k8s/blob/master/docs/troubleshooting.md)
and [ALB readiness documentation](https://kubernetes-sigs.github.io/aws-load-balancer-controller/v2.0/guide/controller/pod_readiness_gate/).

Scratch /tmp/sw-astra-W8 is removed after verification. The shared cache remains.
No Git writes were performed. Git has no local identity or core.worktree override.

STATUS: CONTINUE
