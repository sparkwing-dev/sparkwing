# Shared Cloud capacity infrastructure proposal

Decision 75. These are proposed repository changes, not applied infrastructure.
The launcher and admission changes must ship with the pool cutover. Keep the
existing hardened `sparkwing-jobs` EC2NodeClass, including its user namespaces,
seccomp profile, metadata restrictions and node role.

The existing production and staging launcher ceilings are 2 vCPU and 8 GiB.
The manifests below provide that requested class on the same 4-vCPU/16-GiB
hardware and preserve each deployment's 8-vCPU/32-GiB fleet limit. Other classes
need their own shared pool and operator-approved fleet budget before use.
Upsizing requires an idle warm node in the next class. These deployments have
no class-4 pool, so enabling the switch alone does not provision larger nodes.
Warm Deployments start with zero replicas, matching the Off launch default.

## kikd-infra

Delete `k8s/karpenter/nodepool-sparkwing-team-qa-team.yaml` and
`k8s/karpenter/nodepool-sparkwing-staging-stg-qa.yaml`. Add the following files.
Pool names keep the existing `sparkwing-jobs-*` and `sparkwing-staging-*` budget
breaker patterns. No team label or taint appears in a shared template.

`k8s/karpenter/nodepool-sparkwing-jobs-2.yaml`:

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: sparkwing-jobs-2
spec:
  template:
    metadata:
      labels:
        node-restriction.kubernetes.io/sparkwing-cloud-class: "2"
        sparkwing.dev/launcher-namespace: sparkwing-cloud-jobs
        sparkwing.dev/warm-namespace: sparkwing-cloud-warm
    spec:
      taints:
        - key: sparkwing.dev/dedicated
          value: "true"
          effect: NoSchedule
      requirements:
        - key: kubernetes.io/os
          operator: In
          values: [linux]
        - key: kubernetes.io/arch
          operator: In
          values: [amd64]
        - key: topology.kubernetes.io/zone
          operator: In
          values: [us-west-2a, us-west-2b, us-west-2c]
        - key: karpenter.k8s.aws/instance-family
          operator: In
          values: [m6a, m6i, m7i]
        - key: karpenter.k8s.aws/instance-cpu
          operator: In
          values: ["4"]
        - key: karpenter.k8s.aws/instance-hypervisor
          operator: In
          values: [nitro]
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
      nodeClassRef:
        name: sparkwing-jobs
        group: karpenter.k8s.aws
        kind: EC2NodeClass
      expireAfter: 168h
      terminationGracePeriod: 6h15m
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 4m
  limits:
    cpu: 8
    memory: 32Gi
```

`k8s/karpenter/nodepool-sparkwing-staging-jobs-2.yaml`:

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: sparkwing-staging-jobs-2
spec:
  template:
    metadata:
      labels:
        node-restriction.kubernetes.io/sparkwing-cloud-class: "2"
        sparkwing.dev/launcher-namespace: sparkwing-staging-cloud-jobs
        sparkwing.dev/warm-namespace: sparkwing-staging-cloud-warm
    spec:
      taints:
        - key: sparkwing.dev/dedicated
          value: "true"
          effect: NoSchedule
      requirements:
        - key: kubernetes.io/os
          operator: In
          values: [linux]
        - key: kubernetes.io/arch
          operator: In
          values: [amd64]
        - key: topology.kubernetes.io/zone
          operator: In
          values: [us-west-2a, us-west-2b, us-west-2c]
        - key: karpenter.k8s.aws/instance-family
          operator: In
          values: [m6a, m6i, m7i]
        - key: karpenter.k8s.aws/instance-cpu
          operator: In
          values: ["4"]
        - key: karpenter.k8s.aws/instance-hypervisor
          operator: In
          values: [nitro]
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
      nodeClassRef:
        name: sparkwing-jobs
        group: karpenter.k8s.aws
        kind: EC2NodeClass
      expireAfter: 168h
      terminationGracePeriod: 6h15m
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 4m
  limits:
    cpu: 8
    memory: 32Gi
```

Replace `k8s/karpenter/kustomization.yaml` with:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ec2nodeclass.yaml
  - nodepool.yaml
  - nodepool-sparkwing-jobs-2.yaml
  - nodepool-sparkwing-staging-jobs-2.yaml
  - nodepool-staging-infra.yaml
```

## kikd-gitops

Add `sparkwing/warm-capacity.yaml` and
`sparkwing-staging/warm-capacity.yaml` below. Add each filename to its local
`kustomization.yaml` resources. The chart creates dedicated namespaces
`sparkwing-cloud-warm` and `sparkwing-staging-cloud-warm`. Warm Pod get/delete
and ReplicaSet get are bound only there. Delete old warm-capacity Roles and
RoleBindings from `sparkwing` and `sparkwing-staging`.

Each pool's warm namespace must equal its launcher's `launcher.warmNamespace`.
The launcher checks the Pod's label, priority class, negative priority and lack
of volumes. The Pod's ReplicaSet controller UID must match the actual
ReplicaSet. Its apps/v1 Deployment controller must be named
`sparkwing-cloud-warm-<vCPU>`. No release or customer Pod can qualify.
The no-used-label affinity prevents a replacement placeholder from keeping an
already-used node alive. Anti-affinity keeps one placeholder per warm node.

The shared PriorityClass is owned once, by the production directory. Add
`sparkwing/warm-priority.yaml` to production's resources:

```yaml
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: sparkwing-cloud-warm
value: -1000
globalDefault: false
preemptionPolicy: Never
description: Unused Cloud node placeholders yield to awarded jobs.
```

`sparkwing/warm-capacity.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sparkwing-cloud-warm-2
  namespace: sparkwing-cloud-warm
  labels:
    sparkwing.dev/warm-placeholder: "true"
spec:
  replicas: 0
  strategy:
    type: Recreate
  selector:
    matchLabels:
      sparkwing.dev/warm-placeholder: "true"
      sparkwing.dev/cloud-class: "2"
  template:
    metadata:
      labels:
        sparkwing.dev/warm-placeholder: "true"
        sparkwing.dev/cloud-class: "2"
    spec:
      automountServiceAccountToken: false
      enableServiceLinks: false
      priorityClassName: sparkwing-cloud-warm
      terminationGracePeriodSeconds: 0
      nodeSelector:
        kubernetes.io/os: linux
        node-restriction.kubernetes.io/sparkwing-cloud-class: "2"
        sparkwing.dev/launcher-namespace: sparkwing-cloud-jobs
        sparkwing.dev/warm-namespace: sparkwing-cloud-warm
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  - key: node-restriction.kubernetes.io/sparkwing-team-node
                    operator: DoesNotExist
                  - key: node-restriction.kubernetes.io/sparkwing-used-team
                    operator: DoesNotExist
                  - key: node-restriction.kubernetes.io/sparkwing-used-run
                    operator: DoesNotExist
        podAntiAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            - labelSelector:
                matchLabels:
                  sparkwing.dev/warm-placeholder: "true"
              topologyKey: kubernetes.io/hostname
      tolerations:
        - key: sparkwing.dev/dedicated
          operator: Equal
          value: "true"
          effect: NoSchedule
      securityContext:
        runAsNonRoot: true
        runAsUser: 65534
        runAsGroup: 65534
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: placeholder
          image: registry.k8s.io/pause:3.10
          resources:
            requests:
              cpu: "2"
              memory: 8Gi
              ephemeral-storage: 20Gi
            limits:
              cpu: "2"
              memory: 8Gi
              ephemeral-storage: 20Gi
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: [ALL]
```

`sparkwing-staging/warm-capacity.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sparkwing-cloud-warm-2
  namespace: sparkwing-staging-cloud-warm
  labels:
    sparkwing.dev/warm-placeholder: "true"
spec:
  replicas: 0
  strategy:
    type: Recreate
  selector:
    matchLabels:
      sparkwing.dev/warm-placeholder: "true"
      sparkwing.dev/cloud-class: "2"
  template:
    metadata:
      labels:
        sparkwing.dev/warm-placeholder: "true"
        sparkwing.dev/cloud-class: "2"
    spec:
      automountServiceAccountToken: false
      enableServiceLinks: false
      priorityClassName: sparkwing-cloud-warm
      terminationGracePeriodSeconds: 0
      nodeSelector:
        kubernetes.io/os: linux
        node-restriction.kubernetes.io/sparkwing-cloud-class: "2"
        sparkwing.dev/launcher-namespace: sparkwing-staging-cloud-jobs
        sparkwing.dev/warm-namespace: sparkwing-staging-cloud-warm
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  - key: node-restriction.kubernetes.io/sparkwing-team-node
                    operator: DoesNotExist
                  - key: node-restriction.kubernetes.io/sparkwing-used-team
                    operator: DoesNotExist
                  - key: node-restriction.kubernetes.io/sparkwing-used-run
                    operator: DoesNotExist
        podAntiAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            - labelSelector:
                matchLabels:
                  sparkwing.dev/warm-placeholder: "true"
              topologyKey: kubernetes.io/hostname
      tolerations:
        - key: sparkwing.dev/dedicated
          operator: Equal
          value: "true"
          effect: NoSchedule
      securityContext:
        runAsNonRoot: true
        runAsUser: 65534
        runAsGroup: 65534
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: placeholder
          image: registry.k8s.io/pause:3.10
          resources:
            requests:
              cpu: "2"
              memory: 8Gi
              ephemeral-storage: 20Gi
            limits:
              cpu: "2"
              memory: 8Gi
              ephemeral-storage: 20Gi
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: [ALL]
```

Set these launcher values in `sparkwing/helm-values.yaml`:

```yaml
sparkwing-runner-bundle:
  launcher:
    warmNamespace: sparkwing-cloud-warm
    nodeClassName: sparkwing-jobs
```

And in `sparkwing-staging/helm-values.yaml`:

```yaml
sparkwing-runner-bundle:
  launcher:
    warmNamespace: sparkwing-staging-cloud-warm
    nodeClassName: sparkwing-jobs
```

Replace the per-team NodePool comments under `launcher` in both
`helm-values.yaml` files with the shared class/deployment contract. Preserve
launcher ceilings, credentials, images and infrastructure-node placement.
Regenerate each `rendered.yaml` from the Sparkwing chart containing the new
shared selectors, generic toleration and matching admission policies. Image
publication and the deployment revision are separate release work.

KEDA resources and nonzero warm replica ownership belong to the warm-controls
change. Its scaler must target the matching Deployment and use
`warm_pool_desired`. Do not enable warm replicas before that ownership is clear.
Operator setting `cloud_upsize_enabled` defaults to true and is read from the
controller's operator settings, not a Helm environment override.

## Cutover verification

Finish old runs before pruning team pools. Remove any hand-made per-team pools
through the separately approved operator cutover. Let Karpenter remove their
empty nodes. Never strip used-team/used-run labels or repurpose old nodes.

Verify two teams and two runs against the shared pool: each first reservation
sets exact team/run fences, a later job of that run can use its node, and a
foreign team or run cannot. A cold request must choose a never-used node and
keep requested resources. Check warm placeholder replacement after an awarded
job claims a node. Verify a requested class with no shared pool fails clearly
and refunds the unstarted credit hold. No cluster or AWS write is part of this
proposal or its repository checks.

## Warm-controls lane merge contract

This proposal owns the shared pool, binding and capacity-fact contract. Replace
the earlier `warm-capacity.md` proposal's conflicting pool, placeholder and RBAC
sections with the manifests above. Keep its KEDA installation, reader role,
operator modes and scaler ownership. Do not install a second placeholder
Deployment per class. KEDA targets `sparkwing-cloud-warm-<vCPU>` in the warm
namespace. Its ScaledObject, TriggerAuthentication and reader Secret also live
there, because KEDA targets and authentication are namespaced. Resolve the warm
namespace from `sparkwing-runner-bundle.launcher.warmNamespace`, falling back to
`<release namespace>-cloud-warm`, so there is one namespace setting. Keep
`warmCapacity.namespace` as the Cloud Job namespace used for node affinity.
The Secret stays unmounted by placeholders.

| Surface | Contract |
| --- | --- |
| Protected hardware label | `node-restriction.kubernetes.io/sparkwing-cloud-class=<vCPU>` |
| Deployment provenance | `sparkwing.dev/launcher-namespace=<Cloud Job namespace>` |
| Warm namespace | `sparkwing.dev/warm-namespace=<release namespace>-cloud-warm` |
| Placeholder Deployment | `sparkwing-cloud-warm-<vCPU>` |
| PriorityClass | `sparkwing-cloud-warm`, value -1000 |
| Placeholder label | `sparkwing.dev/warm-placeholder=true` |
| Placeholder volumes | None, including automatic ServiceAccount token volumes |
| Warm Role | Pod get/delete and apps ReplicaSet get in the warm namespace only |
| Controller setting | `cloud_upsize_enabled`, default true |
| Capacity facts | `cloud_job_capacity`, schema 28, one row per billing reservation |

Delete the warm-controls lane's duplicate telemetry columns from `nodes`:
`requested_class`, `granted_class`, `fresh_node_wait`, `start_latency_ns`.
Use the authoritative `cloud_job_capacity` fields `requested_class`,
`granted_class`, `waited_for_fresh_node`, `claim_to_start_ns`, `claimed_at` and
`started_at`. Class values are `sparkwing-<vCPU>cpu`. Keep retries distinct by
reservation, so one job's earlier attempt does not overwrite its later attempt.
Read work-job metrics by joining `billing_awards` on team/reservation and
filtering `unit_kind='node'`. Plan/eval preparation facts remain available but
must not enter job-start percentiles. Billing starts at the store-clock first
execution renewal. `claimed_at` and `started_at` use that same clock.

For example, replace the warm-controls view's source CTEs with these exact CTEs,
then retain its `demand`, `targets`, `clamped` and final SELECT:

```sql
WITH jobs AS (
    SELECT c.* FROM cloud_job_capacity c
    JOIN billing_awards b ON b.team=c.team AND b.reservation_id=c.reservation_id
    WHERE b.unit_kind='node'
), starts AS (
    SELECT requested_class AS class, claim_to_start_ns,
           ROW_NUMBER() OVER (PARTITION BY requested_class ORDER BY claim_to_start_ns) AS position,
           COUNT(*) OVER (PARTITION BY requested_class) AS total
    FROM jobs
    WHERE started_at >= DATABASE_TIMESTAMP - 3600000000000
      AND started_at <= DATABASE_TIMESTAMP AND claim_to_start_ns IS NOT NULL
), medians AS (
    SELECT class, AVG(claim_to_start_ns / 1000000000.0) AS median_seconds
    FROM starts WHERE position IN ((total + 1) / 2, (total + 2) / 2) GROUP BY class
), waits AS (
    SELECT requested_class AS class, (COUNT(*) + 1) / 2 AS desired
    FROM jobs WHERE waited_for_fresh_node=1
      AND COALESCE(started_at,claimed_at) >= DATABASE_TIMESTAMP - 1800000000000
      AND COALESCE(started_at,claimed_at) <= DATABASE_TIMESTAMP
    GROUP BY requested_class
)
```

The launcher binds the fresh node before removing a placeholder. A forbidden
Pod delete removes the reservation annotation and reservation taint, preserves
the permanent team/run fences, and leaves the placeholder untouched. Capacity
observations must use the protected class label, translate its numeric value to
`sparkwing-<vCPU>cpu`, and scope by the launcher namespace label. Warm counts
exclude every node with a team, used-team, used-run or reservation binding.
