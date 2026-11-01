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
        sparkwing.dev/warm-namespace: sparkwing
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
        sparkwing.dev/warm-namespace: sparkwing-staging
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
`kustomization.yaml` resources. These Deployments run in the trusted release
namespaces, outside the customer Job namespace. Each pool's warm namespace
must equal its launcher's release namespace. The chart grants the launcher Pod
get/delete in that namespace. The launcher recognizes only
Pods with the warm label and priority class, negative priority, and an apps/v1 ReplicaSet controller reference in the
namespace proved by pool provenance. That namespace is trusted operator
infrastructure.
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
  namespace: sparkwing
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
        sparkwing.dev/warm-namespace: sparkwing
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
  namespace: sparkwing-staging
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
        sparkwing.dev/warm-namespace: sparkwing-staging
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
