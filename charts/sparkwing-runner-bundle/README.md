# sparkwing-runner-bundle

This chart deploys Sparkwing runners in a customer-owned namespace. It connects
to an existing controller. The controller and dashboard are included in the
`sparkwing-full` parent chart.

The chart has no cache server, git mirror or package proxy. Runners acquire source
from the repository and use the controller's signed data paths. Configure a
standard package mirror through `runner.extraEnv` if your deployment needs one.

Set both runner image repository and tag to a compatible build. Both are required.
`appVersion` is metadata and supplies no image fallback.

```sh
helm template sparkwing ./charts/sparkwing-runner-bundle \
  --set controller.url=https://controller.example.com \
  --set controller.tokenSecret.name=sparkwing-token \
  --set 'runner.repos[0]=github.com/your-org/*' \
  --set runner.image.repository=registry.example.com/sparkwing-runner \
  --set runner.image.tag=YOUR_BUILD
```

Create the controller credential Secret separately. The chart mounts it into the
runner's credentials directory. Pipeline code on that runner can read its runner
credential. Limit the credential to the runner's authorized work.

Set `runner.repos` to the repository patterns this machine may fetch, such as
`github.com/your-org/*`. The runner checks this list before hosting each award.
The default empty list admits no repositories. The full chart exposes it as
`sparkwing-runner-bundle.runner.repos`.

Go build and module caches use scratch storage by default. To preserve them across
pod replacements, enable `runner.goCache.persistence`. More than one runner replica
requires a `ReadWriteMany` volume. An existing claim can be selected through
`runner.goCache.persistence.existingClaim`.

The runner has its own ServiceAccount. Its token is not mounted by default
because the runner claims work through the controller and needs no Kubernetes
API access. The chart creates no runner Role or RoleBinding. The launcher owns
Kubernetes Jobs under its separate permissions. Set `runner.alsoClaimTriggers`
to `false` for a pool that should claim only jobs.

Set `launcher.enabled=true` to run a Cloud launcher Deployment. Supply its
`image.repository` and `image.tag`, `tokenSecret`, `jobImage` and `dockerImage`
(each with repository, tag and digest), plus `cpuCeiling` and `memoryCeiling`.
`controller.url` selects its controller. The launcher token mounts as `agent-token`
in its credentials directory and needs the deployment `claims.launch` grant.
`launcher.serviceAccountName` selects its separate Kubernetes identity. An empty
name generates one from the release. Setting a name with the launcher disabled
provisions only the job namespace, RBAC and admission policies for an externally
managed launcher, which still needs `jobImage` and `dockerImage` and must run as
`system:serviceaccount:<release namespace>:<launcher.serviceAccountName>`, the
only launcher identity the policies admit.
Set `runner.enabled=false` when all work goes through Cloud Jobs.

`launcher.networkPolicy.egress` supplies allowed Job destinations alongside the
selecting default-deny policy. The [Cloud example](../sparkwing-full/examples/cloud.yaml)
and [Cloud prerequisites](../sparkwing-full/README.md#cloud-node-and-network-prerequisites)
describe team-node labels, CPU-band taints, disruption protection, native
sidecars, PodLevelResources and node-level network enforcement.
Cloud Jobs run in `launcher.jobNamespace`, `<release namespace>-cloud-jobs` when
empty, which must differ from the release namespace. The chart owns that
namespace and creates it with privileged Pod Security unless
`launcher.createNamespace=false`. Keep every other NetworkPolicy and Pod
Security label out of it: an additive egress policy there widens Job egress.
The chart adds the customer ServiceAccount, launcher Role and egress policies, plus two
ValidatingAdmissionPolicies with Deny bindings selecting only that namespace.
They admit launcher-created Jobs, capacity requests and boundary probes and
Job-controller Pods of the launcher's exact container set, pinned images,
volumes, environment, team placement and labels, judged after every mutating
webhook. EKS region values, VPC resource controller ENI data and load-balancer
readiness gates pass. AWS credential injection, host access, extra capabilities,
service account tokens, ephemeral containers and exec, attach or port-forward
are refused. `nodes/proxy` reaches the kubelet without admission; grant it only
to cluster administrators. The policy
template is generated from `launcher.AdmissionPolicies`; regenerate it with
`GOWORK=off go test ./charts -run TestLauncherAdmissionTemplateIsGenerated -update`.

The launcher refuses to start unless those policies and bindings match its
identity, namespace and images, and stops claiming work while they do not. It then checks each Job's award fields (team
placement, node, images and resources) at activation and on every sync. A
mismatch deletes the Job, fails its exact award, cordons the node and deletes
its Karpenter NodeClaim. Nodes keep their first team assignment through
reservation cleanup. Do not annotate the customer ServiceAccount with an IRSA
role or create a Pod Identity association for it.

Upgrading from an install whose Jobs ran in another namespace: let Jobs and node
reservations recorded under the old namespace drain first, or delete those Jobs
and remove the `sparkwing.dev/node-reservation`, `sparkwing.dev/reservation-namespace`
and `sparkwing.dev/award-binding` annotations and the `sparkwing.dev/cloud-reserved`
taint from their nodes. The launcher logs every node still reserved under
another namespace and never reuses it.

Grant Pod and Job creation in the job namespace only to the launcher
ServiceAccount. Kubernetes administrators and the Job controller remain trusted.
Do not bind namespace edit/admin roles to customer accounts. Customer Job and
runner ServiceAccounts receive no such rights. Keep team nodes tainted and keep
other workloads from tolerating those taints. Cluster DaemonSets such as
`aws-node`, `ebs-csi-node` and `kube-proxy` tolerate every taint and run
privileged there; they remain trusted. No RoleBinding can revoke a grant
made by another RoleBinding, so audit existing namespace and cluster bindings.

The chart labels infrastructure Pods `app: sparkwing-controller`,
`app: sparkwing-launcher` and `app: sparkwing-runner`. Customer Pods use
`app: sparkwing-job`. Exclude these values from any baseline allow-all policies
that would union away the corresponding chart fences. Node escape probes must
pass before a Job is published.

`values.yaml` lists the supported chart values.
