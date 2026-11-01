# Shared warm capacity infrastructure proposal

Apply through kikd-infra and kikd-gitops after review. This file performs no deployment.
The Sparkwing chart creates placeholders and scalers; infrastructure installs KEDA.
All operator classes start Off. Helm enabling these resources reserves zero nodes
until an operator selects Manual or Auto.

## kikd-infra: KEDA

Add `k8s/apps/keda.yaml` and include it in `k8s/apps/kustomization.yaml`.
The existing platform uses ArgoCD. Install KEDA before rendering enabled warm
capacity resources into kikd-gitops.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: keda
  namespace: argocd
spec:
  project: kikd
  source:
    repoURL: https://kedacore.github.io/charts
    chart: keda
    targetRevision: 2.18.1
  destination:
    server: https://kubernetes.default.svc
    namespace: keda
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
```

Add this Kustomize patch for `k8s/apps/project.yaml`. It appends the Helm source
and resource kinds needed by KEDA and the chart's placeholder PriorityClass.

```yaml
- op: add
  path: /spec/sourceRepos/-
  value: https://kedacore.github.io/charts
- op: add
  path: /spec/clusterResourceWhitelist/-
  value: {group: apiregistration.k8s.io, kind: APIService}
- op: add
  path: /spec/clusterResourceWhitelist/-
  value: {group: admissionregistration.k8s.io, kind: ValidatingWebhookConfiguration}
- op: add
  path: /spec/clusterResourceWhitelist/-
  value: {group: admissionregistration.k8s.io, kind: MutatingWebhookConfiguration}
- op: add
  path: /spec/clusterResourceWhitelist/-
  value: {group: scheduling.k8s.io, kind: PriorityClass}
```

The PostgreSQL [scaler contract](https://keda.sh/docs/2.18/scalers/postgresql/)
uses `TriggerAuthentication.secretTargetRef` parameter `connection`.
KEDA needs PostgreSQL TCP 5432 and Kubernetes API access. Its namespace starts
without an egress-isolating NetworkPolicy. Existing database security groups
allow the EKS node security group on 5432, so no new cloud ingress rule is needed.

## kikd-infra: read-only database identity

Run as the database owner against each deployment database. Substitute the
password through the infrastructure secret mechanism; never commit it.
The ordinary view runs with its owner's table permissions, so this role needs
only SELECT on the view, not access to customer rows or settings tables.

```sql
CREATE ROLE sparkwing_warm_reader LOGIN PASSWORD 'REPLACE_FROM_SECRET_STORE';
ALTER ROLE sparkwing_warm_reader SET default_transaction_read_only = on;
GRANT CONNECT ON DATABASE sparkwing TO sparkwing_warm_reader;
GRANT USAGE ON SCHEMA public TO sparkwing_warm_reader;
GRANT SELECT ON public.warm_pool_desired TO sparkwing_warm_reader;
```

Provision this Secret in the Helm release namespace, with the reader DSN from the
infrastructure secret store. Its password must match the role above. KEDA reads
it; placeholder containers receive no database credentials.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: sparkwing-warm-db
  namespace: sparkwing
type: Opaque
stringData:
  dsn: postgresql://sparkwing_warm_reader:REPLACE_FROM_SECRET_STORE@REPLACE_DB_HOST:5432/sparkwing?sslmode=require
```

## kikd-gitops: Sparkwing values

```yaml
warmCapacity:
  enabled: true
  namespace: sparkwing-cloud-jobs
  databaseSecret:
    name: sparkwing-warm-db
    key: dsn
```

The chart creates placeholders and KEDA resources in its release namespace,
separate from the job namespace and its launcher-only admission policy.
`warmCapacity.namespace` scopes node affinity to the Cloud job namespace.
The chart supplies all six catalog classes. Each placeholder requests the class
CPU, catalog memory and 20 GiB ephemeral storage; anti-affinity permits one
placeholder per node. Select physical instances with enough **allocatable**
resources after kubelet/system/DaemonSet overhead. For example, a physical
2-vCPU node with less than 2 allocatable CPU cannot serve sparkwing-2cpu; select
a larger instance or change the infrastructure reservations. Keep NodePool
instance requirements narrow enough that each class reserves its intended node.

## Shared-pool contract for launcher-lane reconciliation

Every class's NodePool must set deployment label
`sparkwing.dev/cloud-namespace=<Cloud job namespace>` and label
`sparkwing.dev/hardware-class=sparkwing-<cores>cpu` and taint
`sparkwing.dev/dedicated=true:NoSchedule`. Its template has no protected team
label. Placeholder affinity excludes any node with protected label
`node-restriction.kubernetes.io/sparkwing-team-node`; first-use binding makes a
node permanently ineligible for future warm placeholders. The launcher removes
the placeholder before binding the fresh node and preserves used-team/run
fences. Ordinary jobs have higher priority than -1000.

Replace per-team NodePools with these class pools. The existing
`sparkwing-jobs` EC2NodeClass
retains AL2023, user namespace and cgroup settings.

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: sparkwing-jobs-sparkwing-2cpu
spec:
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 10m
  template:
    metadata:
      labels:
        sparkwing.dev/cloud-namespace: sparkwing-cloud-jobs
        sparkwing.dev/hardware-class: sparkwing-2cpu
    spec:
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: sparkwing-jobs
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
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
        - key: node.kubernetes.io/instance-type
          operator: In
          values: [m7i.xlarge]
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: sparkwing-jobs-sparkwing-4cpu
spec:
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 10m
  template:
    metadata:
      labels:
        sparkwing.dev/cloud-namespace: sparkwing-cloud-jobs
        sparkwing.dev/hardware-class: sparkwing-4cpu
    spec:
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: sparkwing-jobs
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
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
        - key: node.kubernetes.io/instance-type
          operator: In
          values: [m7i.2xlarge]
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: sparkwing-jobs-sparkwing-8cpu
spec:
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 10m
  template:
    metadata:
      labels:
        sparkwing.dev/cloud-namespace: sparkwing-cloud-jobs
        sparkwing.dev/hardware-class: sparkwing-8cpu
    spec:
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: sparkwing-jobs
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
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
        - key: node.kubernetes.io/instance-type
          operator: In
          values: [m7i.4xlarge]
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: sparkwing-jobs-sparkwing-16cpu
spec:
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 10m
  template:
    metadata:
      labels:
        sparkwing.dev/cloud-namespace: sparkwing-cloud-jobs
        sparkwing.dev/hardware-class: sparkwing-16cpu
    spec:
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: sparkwing-jobs
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
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
        - key: node.kubernetes.io/instance-type
          operator: In
          values: [m7i.8xlarge]
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: sparkwing-jobs-sparkwing-32cpu
spec:
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 10m
  template:
    metadata:
      labels:
        sparkwing.dev/cloud-namespace: sparkwing-cloud-jobs
        sparkwing.dev/hardware-class: sparkwing-32cpu
    spec:
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: sparkwing-jobs
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
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
        - key: node.kubernetes.io/instance-type
          operator: In
          values: [m7i.12xlarge]
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: sparkwing-jobs-sparkwing-64cpu
spec:
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 10m
  template:
    metadata:
      labels:
        sparkwing.dev/cloud-namespace: sparkwing-cloud-jobs
        sparkwing.dev/hardware-class: sparkwing-64cpu
    spec:
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: sparkwing-jobs
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
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
        - key: node.kubernetes.io/instance-type
          operator: In
          values: [m7i.24xlarge]
```

Set NodePool cost limits in infrastructure before enabling Manual or Auto.
The chart's `maxReplicas` is an infrastructure safety ceiling (100 per class by
default); keep it at least the largest allowed operator maximum. KEDA reads
`warm_pool_desired` every 15 seconds. Cooldown to zero takes 30 seconds; Off
converges to zero rather than deleting nodes synchronously. Helm omits replicas
on upgrade so it does not overwrite KEDA's count. A KEDA or database outage
retains the last replica count; operators can set the Deployment to zero during
recovery. Reapply the SELECT grant after a database/schema reset.

## Placeholder removal permission

The existing deployment values select launcher service account `sparkwing-launcher`.
Add this Role and binding in each release namespace.
The launcher lists and removes only Pods bearing `sparkwing.dev/warm-placeholder`.
Kubernetes RBAC cannot restrict deletion by label, so this authority stays with the
trusted deployment launcher. Customer jobs retain their separate service account.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: sparkwing-warm-placeholders
  namespace: sparkwing
rules:
  - apiGroups: [""]
    resources: [pods]
    verbs: [get, list, delete]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: sparkwing-warm-placeholders
  namespace: sparkwing
subjects:
  - kind: ServiceAccount
    name: sparkwing-launcher
    namespace: sparkwing
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: sparkwing-warm-placeholders
```

## Merge contract

The minimal migration-free telemetry definition in this lane adds `nodes` columns
`requested_class`, `granted_class`, `fresh_node_wait` and `start_latency_ns` at
baseline 28. Reconcile these names with the shared-pool lane before merging.
That lane records them and removes placeholders before binding a node. Placeholders
live in the release namespace. Its launcher therefore needs the namespace and Pod
read/delete authority described above, in addition to the shared pool labels and
generic taint. Warm observations travel through `LauncherSyncRequest.warm_pools`
and expire after 120 seconds. Only deployment service launcher authority can write
them. No schema migration, provider call or cluster write is part of this change.

## Staging pools and replica ownership

Apply these staging pools beside the production pools above. Remove
`nodepool-sparkwing-team-qa-team.yaml` and
`nodepool-sparkwing-staging-stg-qa.yaml` from the infrastructure kustomization
and remove those manifests. Add the new shared pool files instead.

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: sparkwing-staging-jobs-sparkwing-2cpu
spec:
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 10m
  template:
    metadata:
      labels:
        sparkwing.dev/cloud-namespace: sparkwing-staging-cloud-jobs
        sparkwing.dev/hardware-class: sparkwing-2cpu
    spec:
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: sparkwing-jobs
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
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
        - key: node.kubernetes.io/instance-type
          operator: In
          values: [m7i.xlarge]
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: sparkwing-staging-jobs-sparkwing-4cpu
spec:
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 10m
  template:
    metadata:
      labels:
        sparkwing.dev/cloud-namespace: sparkwing-staging-cloud-jobs
        sparkwing.dev/hardware-class: sparkwing-4cpu
    spec:
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: sparkwing-jobs
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
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
        - key: node.kubernetes.io/instance-type
          operator: In
          values: [m7i.2xlarge]
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: sparkwing-staging-jobs-sparkwing-8cpu
spec:
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 10m
  template:
    metadata:
      labels:
        sparkwing.dev/cloud-namespace: sparkwing-staging-cloud-jobs
        sparkwing.dev/hardware-class: sparkwing-8cpu
    spec:
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: sparkwing-jobs
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
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
        - key: node.kubernetes.io/instance-type
          operator: In
          values: [m7i.4xlarge]
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: sparkwing-staging-jobs-sparkwing-16cpu
spec:
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 10m
  template:
    metadata:
      labels:
        sparkwing.dev/cloud-namespace: sparkwing-staging-cloud-jobs
        sparkwing.dev/hardware-class: sparkwing-16cpu
    spec:
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: sparkwing-jobs
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
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
        - key: node.kubernetes.io/instance-type
          operator: In
          values: [m7i.8xlarge]
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: sparkwing-staging-jobs-sparkwing-32cpu
spec:
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 10m
  template:
    metadata:
      labels:
        sparkwing.dev/cloud-namespace: sparkwing-staging-cloud-jobs
        sparkwing.dev/hardware-class: sparkwing-32cpu
    spec:
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: sparkwing-jobs
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
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
        - key: node.kubernetes.io/instance-type
          operator: In
          values: [m7i.12xlarge]
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: sparkwing-staging-jobs-sparkwing-64cpu
spec:
  disruption:
    consolidationPolicy: WhenEmpty
    consolidateAfter: 10m
  template:
    metadata:
      labels:
        sparkwing.dev/cloud-namespace: sparkwing-staging-cloud-jobs
        sparkwing.dev/hardware-class: sparkwing-64cpu
    spec:
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: sparkwing-jobs
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
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
        - key: node.kubernetes.io/instance-type
          operator: In
          values: [m7i.24xlarge]
```

Add this patch to the production ArgoCD Application so static `helm template`
output with `replicas: 0` does not fight KEDA. Staging already has these two
settings. Keep both when updating that Application.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: sparkwing
  namespace: argocd
spec:
  ignoreDifferences:
    - group: apps
      kind: Deployment
      jqPathExpressions:
        - 'select(.metadata.name | contains("-warm-")) | .spec.replicas'
  syncPolicy:
    syncOptions:
      - CreateNamespace=true
      - RespectIgnoreDifferences=true
```

In `sparkwing-staging/helm-values.yaml`, use:

```yaml
warmCapacity:
  enabled: true
  namespace: sparkwing-staging-cloud-jobs
  databaseSecret:
    name: sparkwing-warm-db
    key: dsn
```

Re-render each deployment's `rendered.yaml` from its existing Helm values after
adding this block. Keep the current image pins and all other deployment values.
Install the database reader in each deployment database before enabling the
scalers. Add the following ExternalSecrets to their deployment kustomizations.
Store each DSN under the shown Secrets Manager path with only the reader's login.

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: sparkwing-warm-db
  namespace: sparkwing
  annotations:
    argocd.argoproj.io/sync-wave: "-1"
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: aws-secretsmanager
    kind: ClusterSecretStore
  target:
    name: sparkwing-warm-db
  data:
    - secretKey: dsn
      remoteRef:
        key: /kikd-prod/sparkwing/warm-reader
        property: dsn
```

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: sparkwing-warm-db
  namespace: sparkwing-staging
  annotations:
    argocd.argoproj.io/sync-wave: "-1"
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: aws-secretsmanager
    kind: ClusterSecretStore
  target:
    name: sparkwing-warm-db
  data:
    - secretKey: dsn
      remoteRef:
        key: /kikd-prod/sparkwing-staging/warm-reader
        property: dsn
```

Staging placeholder removal uses the same account name in its own namespace:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: sparkwing-warm-placeholders
  namespace: sparkwing-staging
rules:
  - apiGroups: [""]
    resources: [pods]
    verbs: [get, list, delete]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: sparkwing-warm-placeholders
  namespace: sparkwing-staging
subjects:
  - kind: ServiceAccount
    name: sparkwing-launcher
    namespace: sparkwing-staging
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: sparkwing-warm-placeholders
```
