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

- `sparkwing-controller` (orchestrator) -- ships in the
  `sparkwing-full` (full self-host) chart.
- `sparkwing-web` (SPA host) -- same.

If you want the whole stack in one cluster, install
`sparkwing-full`. Use this chart when the controller already
exists somewhere else and you just need a runner pool.

> **Release blocker:** The checked-in default repositories and
> `appVersion` do not currently identify a compatible public runner/cache/logs
> image set. Until a corrected release is published, build or mirror all three
> images from one compatible Sparkwing revision and explicitly set each
> enabled component's `image.repository` and `image.tag`.

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
- Explicit repositories and tags for a mutually compatible runner, cache, and
  logs image set. The current default GHCR/appVersion combination is not a
  runnable release.
- A reachable Sparkwing controller URL.
- A pre-created Secret holding the agent bearer token (see Auth
  below). Optional for single-tenant test installs where the
  controller has auth disabled.
- A default StorageClass (or an explicit `storageClassName`) for
  the cache + logs PVCs. Both are RWO.

## Install

Create `compatible-images.yaml` before installing:

```yaml
runner:
  image: {repository: registry.example/sparkwing-runner, tag: <compatible-tag>}
cache:
  image: {repository: registry.example/sparkwing-cache, tag: <compatible-tag>}
logs:
  image: {repository: registry.example/sparkwing-logs, tag: <compatible-tag>}
```

```bash
# 1. Create the namespace.
kubectl create namespace sparkwing

# 2. Create the agent bearer-token Secret.
kubectl -n sparkwing create secret generic sparkwing-token \
    --from-literal=token=<your-token>

# 3. Install the chart.
helm install runners ./charts/sparkwing-runner-bundle \
    --namespace sparkwing \
    -f compatible-images.yaml \
    --set controller.url=https://app.sparkwing.dev \
    --set controller.tokenSecret.name=sparkwing-token \
    --set runner.labels='{cluster,arch=amd64}'
```

For a fully unauthenticated test cluster, opt the cache and the logs
service out of their token requirement explicitly:

```bash
helm install runners ./charts/sparkwing-runner-bundle \
    --namespace sparkwing --create-namespace \
    -f compatible-images.yaml \
    --set controller.url=http://sparkwing-controller.sparkwing.svc.cluster.local \
    --set cache.allowUnauthenticated=true \
    --set logs.allowUnauthenticated=true
```

## Values cheat sheet

Full schema in [`values.yaml`](./values.yaml). Most-edited keys:

| Key | Purpose | Default |
| --- | --- | --- |
| `controller.url` | Where the runner claims from. **Required.** | `""` |
| `controller.tokenSecret.name` | Existing Secret holding the bearer token. | `""` |
| `controller.tokenSecret.key` | Key inside the Secret. | `token` |
| `runner.replicas` | Pool size. | `2` |
| `runner.labels` | `--label` flags for `Requires` matching. | `[cluster]` |
| `runner.maxConcurrent` | Per-pod node concurrency. | `2` |
| `runner.alsoClaimTriggers` | Pool also claims webhook triggers. | `true` |
| `runner.extraEnv` | Extra runner environment, including an external `SPARKWING_GITCACHE_URL`. | `[]` |
| `runner.image.tag` | Override sparkwing-runner tag. | (chart appVersion) |
| `cache.enabled` | Toggle the in-cluster git cache. | `true` |
| `cache.allowUnauthenticated` | Serve the cache's blob and sync endpoints without a token. | `false` |
| `cache.dependencyProxy.enabled` | Point the runner's go / npm / pip at the cache's pull-through proxy. | `true` |
| `cache.repos` | `GITCACHE_REPOS` -- comma-separated `alias=url`. | `""` |
| `cache.sshKeySecret.name` | Required SSH-key Secret when configured. | `""` |
| `cache.storage.size` | Cache PVC size. | `20Gi` |
| `cache.storage.storageClassName` | Override default StorageClass. | `""` |
| `logs.enabled` | Toggle the log-store sidecar. | `true` |
| `logs.allowUnauthenticated` | Serve every run's logs without a token. | `false` |
| `logs.storage.size` | Logs PVC size. | `10Gi` |
| `runner.automountServiceAccountToken` | Mount the runner pod's API token. Needed only by the k8s trigger runner. | `false` |
| `serviceAccount.annotations` | Add IRSA / Workload Identity annotations. | `{}` |
| `serviceAccount.shareAcrossComponents` | Accept one shared account when `serviceAccount.create=false`. | `false` |
| `imagePullSecrets` | Private-registry pull secrets for all 3 images. | `[]` |

## Auth

The runner reads its bearer token from `controller.tokenSecret` and uses it
for controller claims and writes to the logs service. Configuring that Secret
also enables logs-service auth: the logs service forwards each caller's
incoming Authorization header to the resolved controller's
`/api/v1/auth/whoami` endpoint and enforces the returned scopes. It does not
receive a second service bearer. The same Secret becomes the runner's
`SPARKWING_CACHE_TOKEN` and the cache's `SPARKWING_API_TOKEN`, so both sides of
the binary and dependency cache share one bearer. Once a Secret name is
configured, its key and the Secret itself are required.

A cache-enabled install without that Secret fails at render time. Set
`cache.allowUnauthenticated=true` to serve the cache's blob and sync endpoints
to anything that can reach the Service, which is appropriate only on a
bootstrap install, before the Secret exists.

A logs-enabled install without it fails the same way, because the logs service
has no controller to resolve callers against. With the Secret configured the
chart also passes `--require-auth`, so the pod crashes rather than serving
open. Set `logs.allowUnauthenticated=true` to let anything that can reach the
Service read, forge, and delete every run's logs, which is again a bootstrap
setting to turn back off with the token upgrade.

Trigger claiming always needs a gitcache because it clones and compiles the
repository before creating a run. If `cache.enabled=false` while
`runner.alsoClaimTriggers=true`, add a non-empty `SPARKWING_GITCACHE_URL` to
`runner.extraEnv`; the chart rejects the incomplete combination at render
time. A node-only pool can instead set `runner.alsoClaimTriggers=false`.

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

For an ephemeral test install:

```bash
--set cache.storage.enabled=false \
--set logs.storage.enabled=false
```

Both volumes fall back to `emptyDir` when storage is disabled.

The runner's Sparkwing home is also an `emptyDir`. By default, a short init
container assigns its mount root to the configured non-root UID and GID before
the runner starts. The init runs as UID 0 with a read-only root filesystem, no
privilege escalation, and only the `CHOWN` capability; the runner remains
non-root with all capabilities dropped. The enabled path requires the runner
image to contain `/bin/chown`.

Set `volumePermissions.enabled=false` when the mount is already owned by
`podSecurityContext.runAsUser:podSecurityContext.fsGroup`. The opt-out is also
required by Restricted Pod Security, which rejects the default UID 0 init
container.

## RBAC

The chart creates a namespace-scoped Role + RoleBinding with no rules.
The runner, cache, and logs servers talk to the controller over HTTP and
never call the Kubernetes API, so none of them mounts a ServiceAccount
token: every pod sets `automountServiceAccountToken: false`, and the
cache and logs pods run under their own ServiceAccounts rather than the
runner's.

The one runner configuration that does call the API is the opt-in
Kubernetes trigger runner (`SPARKWING_TRIGGER_RUNNER=k8s`), which creates
runner Jobs and cannot load its in-cluster config without a token. Give it
one back, along with the Role that lets it create Jobs:

```bash
--set runner.automountServiceAccountToken=true
```

Pipelines that need to reach the cluster API (e.g. `kubectl apply`,
sealed-secrets, helm-installs) should bring their own ServiceAccount and
RBAC outside this chart.

If you don't want the chart-managed Role at all:

```bash
--set rbac.create=false \
--set serviceAccount.create=false \
--set serviceAccount.name=my-existing-sa \
--set serviceAccount.shareAcrossComponents=true
```

`serviceAccount.create=false` runs all three pods under that one account.
The render fails without `shareAcrossComponents=true` so the sharing is a
recorded choice; the chart never invents `-cache` and `-logs` names you
have not created.

## Image registry

Fallback image references rendered when a component tag is empty:

- `ghcr.io/sparkwing-dev/sparkwing-runner:<chart appVersion>`
- `ghcr.io/sparkwing-dev/sparkwing-cache:<chart appVersion>`
- `ghcr.io/sparkwing-dev/sparkwing-logs:<chart appVersion>`

These fallbacks describe the intended registry layout, not a compatible public
release contract for this chart version. Pin repository and tag for every
enabled component to images built from one Sparkwing revision.

> The runner Deployment's command is
> `/usr/local/bin/runner-entrypoint.sh /usr/local/bin/sparkwing-runner`,
> so the runner image must be built from `build/Dockerfile.runner`
> (git + Go toolchain + the netrc-seeding entrypoint). Point
> `runner.image.repository` at an image built from that Dockerfile.


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

**Runner pods CrashLoopBackOff with "--controller is required"**
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
