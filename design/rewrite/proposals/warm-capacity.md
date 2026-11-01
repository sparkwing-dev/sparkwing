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

## Shared pool and placeholder ownership

Apply the [shared Cloud capacity proposal](shared-cloud-capacity.md) for the
production and staging pools, hardened EC2NodeClass, dedicated warm namespaces
and launcher RBAC. Its security contract owns these resources. Keep the existing
fleet limits and launcher ceilings; additional classes need their own approved
shared pool and fleet budget. Enabling a scaler does not provision those pools.

When enabling the chart's warm controls, remove the proposal's standalone
`warm-capacity.yaml` and `warm-priority.yaml` resources from GitOps. The chart
owns the single `sparkwing-cloud-warm-<vCPU>` Deployment per class and the shared
`sparkwing-cloud-warm` PriorityClass. Set `warmCapacity.createPriorityClass: false`
in staging so production remains its sole owner.

The chart resolves the warm namespace from
`sparkwing-runner-bundle.launcher.warmNamespace`, falling back to
`<release namespace>-cloud-warm`. Its Deployments, KEDA ScaledObjects,
TriggerAuthentications and database Secret live there. `warmCapacity.namespace`
selects the Cloud Job namespace for node affinity. These are distinct namespaces.

Placeholder pods mount no volumes, including ServiceAccount tokens. The launcher
checks the placeholder label, priority class and negative priority, then verifies
the ReplicaSet controller UID and its owning `sparkwing-cloud-warm-<vCPU>`
Deployment. Pod get/delete and ReplicaSet get are bound only in the dedicated
warm namespace. Remove old release-namespace warm Roles and RoleBindings.

## kikd-infra: read-only database identity

Run as the database owner against each deployment database. Substitute the
password through the infrastructure secret mechanism; never commit it.
The ordinary view runs with its owner's table permissions, so this role needs
only SELECT on the view, without access to customer rows or settings tables.

```sql
CREATE ROLE sparkwing_warm_reader LOGIN PASSWORD 'REPLACE_FROM_SECRET_STORE';
ALTER ROLE sparkwing_warm_reader SET default_transaction_read_only = on;
GRANT CONNECT ON DATABASE sparkwing TO sparkwing_warm_reader;
GRANT USAGE ON SCHEMA public TO sparkwing_warm_reader;
GRANT SELECT ON public.warm_pool_desired TO sparkwing_warm_reader;
```

Provision this Secret in the dedicated warm namespace, with the reader DSN from
the infrastructure secret store. Its password must match the role above. KEDA
reads it; placeholder containers receive no database credentials.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: sparkwing-warm-db
  namespace: sparkwing-cloud-warm
type: Opaque
stringData:
  dsn: postgresql://sparkwing_warm_reader:REPLACE_FROM_SECRET_STORE@REPLACE_DB_HOST:5432/sparkwing?sslmode=require
```

## kikd-gitops: Sparkwing values

Production:

```yaml
sparkwing-runner-bundle:
  launcher:
    warmNamespace: sparkwing-cloud-warm
    nodeClassName: sparkwing-jobs
warmCapacity:
  enabled: true
  namespace: sparkwing-cloud-jobs
  databaseSecret:
    name: sparkwing-warm-db
    key: dsn
```

Staging:

```yaml
sparkwing-runner-bundle:
  launcher:
    warmNamespace: sparkwing-staging-cloud-warm
    nodeClassName: sparkwing-jobs
warmCapacity:
  enabled: true
  namespace: sparkwing-staging-cloud-jobs
  createPriorityClass: false
  databaseSecret:
    name: sparkwing-warm-db
    key: dsn
```

The chart supplies all six catalog classes. Each placeholder requests the class
CPU, catalog memory and 20 GiB ephemeral storage; anti-affinity permits one
placeholder per node. Select physical instances with enough allocatable resources
after kubelet, system and DaemonSet overhead. The protected numeric class label
is the requested class, which can be smaller than the physical instance size.
The launcher pins the pool's EC2NodeClass name to `sparkwing-jobs` by default.

Set NodePool cost limits in infrastructure before enabling Manual or Auto.
The operator's Manual count or Auto max is the warm replica ceiling. KEDA's
maximum permits Kubernetes' supported replica range, 0..2147483647, which
operator writes validate. KEDA reads `warm_pool_desired` every 15 seconds.
Cooldown to zero takes 30 seconds; Off converges to zero. Helm omits replicas on
upgrade so it does not overwrite KEDA's count. A KEDA or database outage retains
the last replica count; operators can set the Deployment to zero during recovery.
Reapply the SELECT grant after a database/schema reset.

Add this patch to the production ArgoCD Application so static `helm template`
output with `replicas: 0` does not fight KEDA. Keep the same settings in staging.

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
        - 'select(.metadata.name | startswith("sparkwing-cloud-warm-")) | .spec.replicas'
  syncPolicy:
    syncOptions:
      - CreateNamespace=true
      - RespectIgnoreDifferences=true
```

Re-render each deployment's `rendered.yaml` from its existing Helm values.
Keep the image pins and other deployment values. Install the database reader in
each deployment database before enabling the scalers. Add these ExternalSecrets
to their deployment kustomizations. Store each DSN under the shown Secrets
Manager path with only the reader's login.

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: sparkwing-warm-db
  namespace: sparkwing-cloud-warm
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
---
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: sparkwing-warm-db
  namespace: sparkwing-staging-cloud-warm
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

Ensure the dedicated warm namespaces exist before syncing their ExternalSecrets.

## Telemetry contract

The single capacity fact source is `cloud_job_capacity` at schema baseline 28.
It records requested/granted class, fresh-node waits and claim-to-start latency
per team and billing reservation, preserving retries. Operator statistics and
`warm_pool_desired` join `billing_awards` and select `unit_kind='node'`, so plan
and eval preparation never affect job-start percentiles. Class values are
`sparkwing-<vCPU>cpu`. Claim/start timestamps and billing use the store clock.

Warm observations travel through `LauncherSyncRequest.warm_pools` and expire
after 120 seconds. Only deployment service launcher authority can write them.
Counts use protected class labels and deployment provenance, and exclude team,
used-team, used-run and reservation bindings. The launcher binds a fresh node
before removing its proven placeholder and preserves permanent team/run fences
on a forbidden deletion. No provider call, cluster write or live database change
is part of this proposal.
