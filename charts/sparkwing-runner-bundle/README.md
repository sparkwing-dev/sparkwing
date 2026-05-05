# sparkwing-runner-bundle

Helm chart that deploys the Sparkwing **runner-side bundle** --
`sparkwing-runner` + `sparkwing-cache` + `sparkwing-logs` -- into a
customer-owned namespace.

This is the chart referenced by architectural decision 0001
("data plane lives with the runner"). When you run self-hosted
runners against a remote controller (Sparkwing Cloud, or a
self-hosted controller in another cluster), the runner, the git
cache, and the log store all live next to your compute. Logs and
artifacts never cross the VPC boundary; the controller only sees
control-plane traffic.

The chart is **not** a full self-host install. It deliberately
omits:

- `sparkwing-controller` (orchestrator) -- ships in
  `sparkwing-stack` (full self-host) chart.
- `sparkwing-web` (SPA host) -- same.

If you want the whole stack in one cluster, install
`sparkwing-stack`. Use this chart when the controller already
exists somewhere else and you just need a runner pool.

## Topology

```
   +---------------------------+        controller plane
   |   sparkwing-controller    |  <---  (cloud / remote cluster)
   +---------------------------+
              ^   |
              |   v   /api/v1/* (claim, status, log forwarder)
   +-------------------------------------------+
   |   YOUR CLUSTER (this chart)               |
   |                                           |
   |   sparkwing-runner   ---->  sparkwing-cache (git, go-mod,    |
   |        |                                   artifacts)        |
   |        v                                                     |
   |   sparkwing-logs   <----  pipeline log writes               |
   +-------------------------------------------+
```

## Requirements

- Kubernetes 1.27+ (the chart targets standard apps/v1, batch/v1,
  rbac.authorization.k8s.io/v1).
- A reachable Sparkwing controller URL.
- A pre-created Secret holding the agent bearer token (see Auth
  below). Optional for kind / single-tenant test installs where the
  controller has auth disabled.
- A default StorageClass (or an explicit `storageClassName`) for
  the cache + logs PVCs. Both are RWO.

## Install

```bash
# 1. Create the namespace.
kubectl create namespace sparkwing

# 2. Create the agent bearer-token Secret.
kubectl -n sparkwing create secret generic sparkwing-token \
    --from-literal=token=<your-token>

# 3. Install the chart.
helm install runners ./charts/sparkwing-runner-bundle \
    --namespace sparkwing \
    --set controller.url=https://app.sparkwing.dev \
    --set controller.tokenSecret.name=sparkwing-token \
    --set runner.labels='{cluster,arch=amd64}'
```

For a fully unauthenticated test against a kind cluster:

```bash
helm install runners ./charts/sparkwing-runner-bundle \
    --namespace sparkwing --create-namespace \
    --set controller.url=http://sparkwing-controller.sparkwing.svc.cluster.local
```

## Values cheat sheet

Full schema in [`values.yaml`](./values.yaml). Most-edited keys:

| Key | Purpose | Default |
| --- | --- | --- |
| `controller.url` | Where the runner claims from. **Required.** | `""` |
| `controller.tokenSecret.name` | Existing Secret holding the bearer token. | `""` |
| `controller.tokenSecret.key` | Key inside the Secret. | `token` |
| `runner.replicas` | Pool size. | `2` |
| `runner.labels` | `--label` flags for `RunsOn` matching. | `[cluster]` |
| `runner.maxConcurrent` | Per-pod node concurrency. | `2` |
| `runner.alsoClaimTriggers` | Pool also claims webhook triggers. | `true` |
| `runner.image.tag` | Override sparkwing-runner tag. | (chart appVersion) |
| `cache.enabled` | Toggle the in-cluster git cache. | `true` |
| `cache.repos` | `GITCACHE_REPOS` -- comma-separated `alias=url`. | `""` |
| `cache.storage.size` | Cache PVC size. | `20Gi` |
| `cache.storage.storageClassName` | Override default StorageClass. | `""` |
| `logs.enabled` | Toggle the log-store sidecar. | `true` |
| `logs.storage.size` | Logs PVC size. | `10Gi` |
| `serviceAccount.annotations` | Add IRSA / Workload Identity annotations. | `{}` |
| `imagePullSecrets` | Private-registry pull secrets for all 3 images. | `[]` |

## Auth

The runner identifies itself to the controller with a bearer token
read from the `controller.tokenSecret`. The same token is mounted on
the logs service so it can call the controller's
`/api/v1/auth/whoami` endpoint to resolve incoming requests.

The chart does NOT create the Secret -- bring your own. This means
rotating the token is `kubectl create secret ... --dry-run=client -o
yaml | kubectl apply -f -` followed by `kubectl rollout restart`,
not a `helm upgrade`.

## Storage

Both `sparkwing-cache` and `sparkwing-logs` use `ReadWriteOnce`
PVCs. That bounds them to 1 replica each (`replicas: 1`,
`strategy: Recreate`). For an HA log store, point your runners at
an external S3-backed log service (out of scope for this chart;
see the full self-host docs).

PVCs are annotated `helm.sh/resource-policy: keep` by default so
`helm uninstall` doesn't wipe history. Disable per-component with
`<component>.storage.keepOnUninstall=false`.

For an ephemeral install (kind / CI / a throwaway test):

```bash
--set cache.storage.enabled=false \
--set logs.storage.enabled=false
```

Both volumes fall back to `emptyDir` when storage is disabled.

## RBAC

The chart creates a namespace-scoped Role + RoleBinding. The runner
SA can:

- Read pods + pod logs + events (for self-debugging output)
- Read configmaps + secrets (so pipelines can mount config)

It cannot create / update / delete cluster resources. Pipelines
that need to mutate cluster state (e.g. `kubectl apply`,
sealed-secrets, helm-installs) should bring their own RBAC outside
this chart.

If you don't want the chart-managed Role at all:

```bash
--set rbac.create=false \
--set serviceAccount.create=false \
--set serviceAccount.name=my-existing-sa
```

## Image registry

Default images:

- `ghcr.io/sparkwing-dev/sparkwing-runner:<chart appVersion>`
- `ghcr.io/sparkwing-dev/sparkwing-cache:<chart appVersion>`
- `ghcr.io/sparkwing-dev/sparkwing-logs:<chart appVersion>`

Override `<component>.image.repository` if you mirror images
internally.

> **NOTE:** Multi-arch (linux/amd64 + linux/arm64) images are
> published to GHCR on every `v*` tag push by
> `.github/workflows/release.yaml` (ISS-052). Each release pushes
> `:vX.Y.Z`; stable (non-pre-release) tags also update `:latest`.
> All images are cosign-keyless-signed via GitHub OIDC -- verify
> with:
>
> ```bash
> cosign verify ghcr.io/sparkwing-dev/sparkwing-runner:vX.Y.Z \
>     --certificate-identity-regexp "https://github.com/sparkwing-dev/sparkwing/" \
>     --certificate-oidc-issuer "https://token.actions.githubusercontent.com"
> ```
>
> Override `*.image.repository` if you mirror images internally.

## Upgrade

```bash
helm upgrade runners ./charts/sparkwing-runner-bundle \
    --namespace sparkwing \
    -f my-values.yaml
```

Runner pods rolling-update one at a time; in-flight claims on the
rolled pod time out and re-queue. Cache + logs use `Recreate`
because of their RWO PVCs -- expect ~30s of downtime per upgrade.
For zero-downtime cache, run a separate cache deployment with
`cache.enabled=false` here and point runners at it via your own
`SPARKWING_GITCACHE_URL` env override.

## Uninstall

```bash
helm uninstall runners --namespace sparkwing
```

PVCs survive (see Storage). Delete them manually if you want a
clean slate:

```bash
kubectl -n sparkwing delete pvc -l app.kubernetes.io/instance=runners
```

## Troubleshooting

**Runner pods CrashLoopBackOff with "controller URL not set"**
You forgot `--set controller.url=...`.

**Runner pods running but no claims happening**
Confirm the controller is reachable from the cluster:

```bash
kubectl -n sparkwing run --rm -it -q debug --image=alpine -- \
    wget -qO- <controller.url>/api/v1/health
```

Check the runner is registered with the right labels:

```bash
kubectl -n sparkwing logs deploy/runners-runner | grep -i label
```

**Cache pod stuck in Pending**
PVC binding failed. Check `kubectl describe pvc runners-cache`. Most
common: no default StorageClass. Set `cache.storage.storageClassName`
explicitly.

**Logs pod 500s on read**
Runner is writing logs but the controller can't read them. Confirm
the controller has network access to the logs service URL the
runners report at trigger time. The default URL is
`http://<release>-logs.<namespace>.svc.cluster.local`, which only
resolves from inside this cluster -- a remote (cloud) controller
needs an Ingress or external Service in front of the logs pod.

## Source

- Chart: `charts/sparkwing-runner-bundle/` in
  [`sparkwing-dev/sparkwing`](https://github.com/sparkwing-dev/sparkwing).
- Decision: 0001 -- open-core tier strategy.
- Ticket: ORG-004.
