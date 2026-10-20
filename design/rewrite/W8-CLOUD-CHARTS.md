# W8 Cloud Helm integration

The assigned Cloud chart vertical is complete. The secret-free
`charts/sparkwing-full/examples/cloud.yaml` renders a complete Cloud topology
without injected test defaults: two Deployments, controller Service and nginx
Ingress, three ServiceAccounts, launcher Role/ClusterRole and bindings, the
admission guard and binding, and two selecting Job egress policies. It requires
operator-owned Secrets and AWS/node/network prerequisites documented alongside it.
The example uses synthetic image digests and public ID placeholders. It is not
an assertion that these images exist or that a production deployment ran.

## Implemented

- Optional launcher Deployment in runner-bundle, enabled by the Cloud example.
  Separate service credential projected as agent-token, Kubernetes identity,
  existing scoped RBAC and admission guard. Runner pool, cache PVC and unused
  runner ServiceAccount disappear with runner.enabled=false.
- Required repository and tag together for each enabled image. Job runner and
  Docker coordinates additionally require SHA-256 digests. No appVersion image
  fallback or default empty tag. Chart versions are full 0.2.2 and bundle 0.1.13.
  The dependency tarball and lockfile are refreshed.
- Controller S3 values wired to its existing /data backend flag. CloudFront,
  RDS sparkwing-database/dsn, bootstrap seed, encryption key, file-named
  credential bundle, OAuth/App IDs, operator accounts and both public hosts
  appear in the Cloud example. SSM ExternalSecret mappings contain no secrets.
- Cloud Job CPU-band NoSchedule toleration and Karpenter do-not-disrupt
  annotations on Jobs' Pods and boundary probes. Exact hashed team-node
  ownership and root/DinD remain the common builder's contract. Admission
  preserves Job template annotations. Property coverage spans plan/eval/work,
  arbitrary team IDs and weakening of placement/disruption protection.
- Replaced stale full-chart README and NOTES image/repository advice. Removed
  deleted trigger-runner/team-node settings from deployment docs. Marked shared
  controller self-hosting outside the launch offering and fixed its Helm inputs.

## Contract changes made

`design/rewrite/contracts/c4-surface-config.md`, Cloud chart integration amendment,
records the new Helm values, required images, launcher identity, namespace,
network policies, CPU-band tolerance and disruption protection. Go interfaces
and wire contracts are unchanged. No contract change is requested from another
area. Lead-owned amendments and handoffs were read and not edited.

The new Helm switches are the requested deployment controls. No process setting,
new environment variable, alternate runtime, timeout, retry or compatibility path
was added. The existing --cache-blob-store name remains the controller's actual
S3 backend flag. Replacing it would be an unrelated CLI contract change.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and -p 4. Temporary Go paths were beneath
/tmp/sw-astra-W8. No Sparkwing candidate binary, pipeline, Git write or cluster
mutation ran.

- Repository-wide go build -p 4 ./... passed.
- Full charts suite passed after final chart/vendor changes (46.316 seconds).
  Cloud render tests inspect actual typed Deployments/Ingress for credential
  projections, /data home, ingress routing and image flags. Negative renders
  cover missing repositories/tags, Job digests and the launcher credential.
- Full internal/runners/launcher suite passed. The scheduling property passed
  6,573 fuzz executions with four workers. Admission weakening regression cases
  pass for the disruption annotation and CPU-band toleration.
- Focused tests/acceptance launcher/Cloud/store suite passed. This includes
  TestLauncherCloudPodContract, TestLauncherCloudBoundaryRefusal,
  TestLauncherCloudPlanUsesRunBinding, TestStoreLauncherW8 and matching W8
  polling, boundary, bootstrap, controller, publication and identity cases.
- go vet passed for charts, launcher and pkg/docs. Helm lint --strict passed
  with the Cloud values. Direct helm template with that file passed.
- Documentation mirror and changelog drift tests passed. sync-docs --check
  passed. Diff check and sleepcheck against HEAD passed.
- Required commentcheck against main reports an inherited violation in
  internal/store/store.go:107. This session did not touch that file. Commentcheck
  against HEAD passes across seven changed Go files.
- The complete pkg/docs suite could not finish because its WebClient httptest
  listener is forbidden by the sandbox. Focused content/changelog drift tests
  passed separately. No native Kubernetes acceptance cell is claimed.

New tests: charts/cloud_test.go and launcher/cloud_scheduling_test.go, plus two
admission mutation cases. No T1 test port was needed for this chart vertical.
The pre-existing render suite now supplies explicit fixture images and expects
the current single-controller support message. Earlier W8 implementations and
lead-owned acceptance responsibilities were preserved.

## Deletions and budget

This session removes the image fallback, old default image coordinates, stale
NOTES warning and obsolete deployment instructions. It replaces 495 lines of
the old full-chart README with 166 lines and removes the old bundle tarball.
No legacy production Go component remained to delete in this chart slice.
Production Go replaces five lines with eight: net +3.

W8's production Go delta against main is +12,080/-6,900, net +5,180, excluding
_tests, testdata and contract declaration files. This inherits the broader
area's overage against the -2,300 budget. This session contributes only three
net lines for the requested CPU-band/disruption behavior and its admission
check. The completion claim here is for the explicitly assigned Cloud-render
vertical, not a waiver of the area's inherited budget or native launch gates.

## Shared files and real integration

Production changes stay in charts and internal/runners/launcher. No production
caller in another package required migration. Shared documentation files are
CHANGELOG.md, docs/deployment.md, docs/self-hosting.md,
docs/migrations/_unreleased.md, their generated pkg/docs mirrors/changelog, and
the C4 contract amendment. This report is the W8 handoff.

Remaining real-integration work is deployment evidence, not a chart stub:

- Supply actual compatible images/digests, SSM parameters, credentials and public
  IDs, RDS connectivity, S3 IAM and CloudFront origin/signing configuration.
- Confirm ingress/TLS, OAuth and GitHub App delivery at /webhooks/github-app.
- Verify target EKS native-sidecar/PodLevelResources and admission APIs. Preserve
  webhook/policy namespace exclusions and a namespace without LimitRanges.
- Provision Ready nodes with the exact hashed team-node value and existing
  small CPU-band taint. The launcher does not create Karpenter capacity. Confirm
  root/privileged Pod allowance, actual escaped-node egress enforcement,
  DaemonSet inventory/probes and cleanup under real workloads.

No new product question or cross-area implementation request was introduced.
Friction: the first dependency refresh attempted unrelated configured Helm
repositories and hit sandbox DNS refusal. The local file dependency succeeded.
Subsequent refreshes used --skip-refresh. Full docs sockets and the inherited
main-relative commentcheck finding are the verification limits above.

Worker scratch /tmp/sw-astra-W8 is removed at completion. The shared Go cache
/tmp/sw-gocache is retained. Git has no local identity/core.worktree override.

STATUS: COMPLETE
