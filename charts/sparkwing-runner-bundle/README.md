# sparkwing-runner-bundle

Helm chart that deploys the Sparkwing **runner-side bundle** --
`sparkwing-runner` + `sparkwing-cache` -- into a customer-owned
namespace.

This is the chart referenced by architectural decision 0001
("data plane lives with the runner"). When you run self-hosted
runners against a remote controller (Sparkwing Cloud, or a
self-hosted controller in another cluster), the runner and the git
cache live next to your compute. Artifacts never cross the VPC
boundary. The controller sees control-plane traffic plus node logs:
runners upload each node's log as segments through URLs the
controller issues, and the dashboard reads them back through the
controller.

The chart is **not** a full self-host install. It deliberately
omits:

- `sparkwing-controller` (orchestrator, which also serves the
  dashboard) -- ships in the `sparkwing-full` (full self-host) chart.
  Its console and API hosts both route to the controller Service; a
  reverse proxy in front can still split them.

If you want the whole stack in one cluster, install
`sparkwing-full`. Use this chart when the controller already
exists somewhere else and you just need a runner pool.

> **Release blocker:** The checked-in default repositories and
> `appVersion` do not currently identify a compatible public runner/cache
> image set. Until a corrected release is published, build or mirror both
> images from one compatible Sparkwing revision and explicitly set each
> enabled component's `image.repository` and `image.tag`.

## Render the chart

`helm template` stops on this chart's own `validate.yaml` unless the cache's
grant key Secret is named, because `cache.enabled` defaults to true. The
minimal read-only render is:

```bash
helm template runners ./charts/sparkwing-runner-bundle \
  --set controller.tokenSecret.name=sparkwing-token \
  --set cache.grantKeySecret.name=sparkwing-cache-grant-key
```

`charts/render_test.go` injects those same values, so what it exercises is
what this renders.

## Topology

```
   +---------------------------+        controller plane
   |   sparkwing-controller    |  <---  (cloud / remote cluster)
   +---------------------------+
              ^   |
              |   v   /api/v1/* (claim, status, log segments)
   +-------------------------------------------+
   |   YOUR CLUSTER (this chart)               |
   |                                           |
   |   sparkwing-runner   ---->  sparkwing-cache (git, go-mod,    |
   |                                            artifacts)        |
   +-------------------------------------------+
```

## Requirements

- Kubernetes 1.27+ (the chart targets standard apps/v1, batch/v1,
  rbac.authorization.k8s.io/v1).
- Explicit repositories and tags for a mutually compatible runner and cache
  image set. The current default GHCR/appVersion combination is not a
  runnable release.
- A reachable Sparkwing controller URL.
- A pre-created Secret holding the agent bearer token (see Auth
  below). Optional for single-tenant test installs where the
  controller has auth disabled.
- A default StorageClass (or an explicit `storageClassName`) for
  the cache PVC, which is RWO.

## Install

Create `compatible-images.yaml` before installing:

```yaml
runner:
  image: {repository: registry.example/sparkwing-runner, tag: <compatible-tag>}
cache:
  image: {repository: registry.example/sparkwing-cache, tag: <compatible-tag>}
```

```bash
# 1. Create the namespace.
kubectl create namespace sparkwing

# 2. Create the runner's bearer token and the cache grant key as two
#    Secrets. Each is a different secret.
kubectl -n sparkwing create secret generic sparkwing-token \
    --from-literal=token=<your-token>
kubectl -n sparkwing create secret generic sparkwing-cache-grant-key \
    --from-literal=key="$(openssl rand -hex 32)"

# 3. Install the chart.
helm install runners ./charts/sparkwing-runner-bundle \
    --namespace sparkwing \
    -f compatible-images.yaml \
    --set controller.url=https://api.sparkwing.dev \
    --set controller.tokenSecret.name=sparkwing-token \
    --set cache.grantKeySecret.name=sparkwing-cache-grant-key \
    --set runner.labels='{cluster,arch=amd64}'
```

The controller the runners claim from needs the same grant key, as the
`cache-grant-key` file in its own `--credentials-dir`: it signs runs' grants
with it, and the operator grant it presents on its own calls to the cache.

For a fully unauthenticated test cluster, opt the cache out of its
token requirement explicitly:

```bash
helm install runners ./charts/sparkwing-runner-bundle \
    --namespace sparkwing --create-namespace \
    -f compatible-images.yaml \
    --set controller.url=http://sparkwing-controller.sparkwing.svc.cluster.local \
    --set cache.allowUnauthenticated=true
```

## Values cheat sheet

Full schema in [`values.yaml`](./values.yaml). Most-edited keys:

| Key | Purpose | Default |
| --- | --- | --- |
| `controller.url` | Where the runner claims from. **Required.** | `""` |
| `controller.tokenSecret.name` | Existing Secret holding the bearer token. | `""` |
| `controller.tokenSecret.key` | Key inside the Secret. | `token` |
| `cache.grantKeySecret.name` | Existing Secret holding the key cache grants are signed and verified with. Never the runner's token. Required when the cache is enabled, unless `cache.allowUnauthenticated`. | `""` |
| `runner.replicas` | Pool size. | `2` |
| `runner.labels` | `--label` flags for `Requires` matching. | `[cluster]` |
| `runner.maxConcurrent` | Per-pod node concurrency. | `2` |
| `runner.alsoClaimTriggers` | Pool also claims webhook triggers. | `true` |
| `runner.triggerRunner.kind` | Node execution for claimed triggers: `inprocess`, `k8s`, or agent-first `warm`. A metered token's pool needs `k8s` or `warm`; the controller refuses its `inprocess` trigger claims. | `inprocess` |
| `runner.triggerRunner.labels` | Static capabilities every trigger-spawned Kubernetes Job advertises. | `[]` |
| `runner.extraEnv` | Extra runner environment. | `[]` |
| `runner.gitcacheUrl` | External gitcache passed as `--gitcache` when `cache.enabled=false`. | `""` |
| `runner.image.tag` | Override sparkwing-runner tag. | (chart appVersion) |
| `runner.goCache.warmModules` | Modules downloaded into `GOMODCACHE` at startup. Empty warms the SDK at the runner image's version. | `[]` |
| `runner.goCache.persistence.enabled` | Mount a PVC over `GOCACHE` and `GOMODCACHE` so the caches outlive the pod. | `false` |
| `runner.goCache.persistence.size` | Go cache PVC size. | `20Gi` |
| `runner.goCache.persistence.accessModes` | Access modes on that claim. Needs `ReadWriteMany` above one replica. | `[ReadWriteMany]` |
| `cache.enabled` | Toggle the in-cluster git cache. | `true` |
| `cache.allowUnauthenticated` | Serve the cache's blob, git and admin endpoints without a grant. | `false` |
| `cache.dependencyProxy.enabled` | Point the runner's go / npm / pip at the cache's pull-through proxy; `false` also stops the cache serving it. Must be `false` when `cache.service.type` is not `ClusterIP`, because the proxy takes no credential. | `true` |
| `cache.publicUrl` | Base URL the proxy rewrites registry bodies against. Empty on a non-ClusterIP Service means each response is rewritten from its own request `Host`. | in-cluster Service URL on a ClusterIP Service |
| `cache.repos` | `--auto-register-repos` -- comma-separated `alias=url`. | `""` |
| `cache.sshKeySecret.name` | Required SSH-key Secret when configured. | `""` |
| `cache.storage.size` | Cache PVC size. | `20Gi` |
| `cache.storage.storageClassName` | Override default StorageClass. | `""` |
| `runner.automountServiceAccountToken` | Mount the runner pod's API token. Required by the `k8s` and `warm` trigger runners. | `false` |
| `serviceAccount.annotations` | Add IRSA / Workload Identity annotations. | `{}` |
| `serviceAccount.shareAcrossComponents` | Accept one shared account when `serviceAccount.create=false`. | `false` |
| `imagePullSecrets` | Private-registry pull secrets for both images. | `[]` |
| `runner.jobCeiling.cpu` | Hard CPU ceiling the runner clamps every pipeline pin and measured charge to before it creates a Job pod. Empty means no ceiling. | `""` |
| `runner.jobCeiling.memory` | Hard memory ceiling for the same. | `""` |
| `runner.jobDeadline` | Wall-clock bound on one Job pod, as a Go duration of at least a minute, passed as `--deadline`. Empty keeps 6h. | `""` |
| `runner.teamNodes` | Keep each band node to the team whose Job booted it, passed as `--team-nodes`. | `false` |
| `limitRange.enabled` | Bound what one container in the namespace may request, including the Job pods the runner creates. | `false` |
| `limitRange.max` | Per-container ceiling when the LimitRange is on. | `cpu: "16"`, `memory: 64Gi` |
| `limitRange.min` | Per-container floor, which rejects a pod asking for a sliver no quota counts. | `cpu: 10m`, `memory: 16Mi` |
| `resourceQuota.enabled` | Bound the namespace totals. Requires `limitRange.enabled=true`. | `false` |
| `resourceQuota.hard` | Namespace totals, passed through as `spec.hard`. | 32 / 64 CPU, 64Gi / 128Gi memory |

### Remote capacity before Kubernetes

Set `runner.triggerRunner.kind=warm` and
`runner.automountServiceAccountToken=true` to offer each unlabeled pipeline
node to remote agents before creating a Kubernetes Job. Remote agents may run on
Windows, macOS, or Linux workstations and servers. They poll the controller
over outbound HTTP(S); they need no inbound route, and Tailscale is optional.
Labels and advertised capacity keep incompatible or saturated machines from
claiming work. An offline or saturated pool therefore sends unlabeled work to
Kubernetes after a short internal claim window. A labeled node stays queued
for a matching agent by default. Set `runner.triggerRunner.labels` only to
capabilities every spawned Job can honor; matching labeled work may then use
the fallback. These labels are separate from `runner.labels`, which describes
the outer pool process and may name a different architecture.

Warm mode reuses the runner image, pull policy, namespace, service account,
and cache configuration already present in this chart. It grants the runner
Role namespace-scoped Job create, get, and delete plus pod list. The default
`inprocess` mode retains the prior arguments and empty Role.
The fallback Job runs `sparkwing-runner run-node`, the executable the runner
image installs, and that process receives the runner token in its environment,
so use warm mode only for trusted pipeline code and rotate short-lived tokens.
The compiled pipeline binary interprets `runner.triggerRunner.kind=warm`.
Upgrade the controller, runner, and pipeline module to the same Sparkwing
release before enabling it.

## Auth

The runner reads its bearer token from `controller.tokenSecret`, which the
chart projects as the file `agent-token` under `--credentials-dir`, and uses it
to claim, renew its claims and make its own controller calls. The work a claim
runs uses the claim token the controller returns with it: a node the runner
executes, every Job a `k8s` or `warm` trigger runner creates, and the trigger's
own secret reads. A Job spec carries its node's claim token, never this
bearer. Mint it with `nodes.claim`, `triggers.claim` and `runs.state`: enough
to claim triggers and nodes, drive the runs it claimed from plan to finish,
and upload their logs. The claims' own tokens read the secrets each run's plan
declares. It needs no `admin`. A warm-pool dispatcher that marks nodes ready
for a pool is the exception and keeps an `admin` token. Once a Secret name is
configured, its key and the Secret itself are required.

The chart projects `cache.grantKeySecret` into a volume at
`/etc/sparkwing/credentials` and passes it as `--credentials-dir`: the cache
reads the key it verifies cache grants with from `cache-grant-key`. Grants are
the only bearer the cache accepts. The runner does not hold the key: for each
claimed run it asks the controller for a cache grant, which the controller
signs with the grant key, and it hands the run only that grant. The controller
signs a short-lived operator grant for its own calls to the cache. Pipeline
code can read the runner's token, so the chart refuses to render when
`controller.tokenSecret` and `cache.grantKeySecret` name the same Secret key:
a runner token that was also the grant key would sign a grant for any team.

A cache-enabled install without `cache.grantKeySecret` fails at render time.
Set `cache.allowUnauthenticated=true` to serve the cache's blob, git and admin
endpoints to anything that can reach the Service, which is appropriate only on
a bootstrap install, before the Secret exists. The chart refuses
`cache.tokenSecret`, which named the static operator token earlier releases
used; see the migration guide section "The cache's static operator token is
removed". A cache started with `--controller` outside this chart also needs a
`storage.count` service token in the file `controller-token`; the chart does
not start the cache with `--controller`.

With `cache.enabled=false` and no `runner.gitcacheUrl`, runners fetch each run's source straight from its host.
The controller releases the credential for each run: the team's GitHub App
token when an installation covers the repository, else the git credential
the team stored for the host. A run with neither fails with a message naming
both, and a runner never falls back to credentials of its own. Working-tree
runs (`sparkwing pipeline trigger --working-tree`) still need a gitcache.

The chart does NOT create the Secret -- bring your own. This means
rotating the token is `kubectl create secret ... --dry-run=client -o
yaml | kubectl apply -f -` followed by `kubectl rollout restart`,
not a `helm upgrade`.

## Storage

`sparkwing-cache` uses a `ReadWriteOnce` PVC, which bounds it to
1 replica (`replicas: 1`, `strategy: Recreate`). The per-team counts
that hold free teams to their storage shares live in the controller's
database; see [Tenant limits](../../docs/limits.md). Node logs live
with the controller, in its cache bucket or output directory, not in
this chart.

PVCs are annotated `helm.sh/resource-policy: keep` by default so
`helm uninstall` doesn't wipe history. Disable per-component with
`<component>.storage.keepOnUninstall=false`.

For an ephemeral test install:

```bash
--set cache.storage.enabled=false
```

The volume falls back to `emptyDir` when storage is disabled.

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

### Go caches across restarts

The runner compiles a repository's pipeline on the first claim of each source
hash, then publishes the binary to the cache service, so later runs of the same
source skip the compile. Every source change still pays one, and a two-CPU pod
takes three to five minutes to compile from empty Go caches.
`runner.maxClaimsBeforeRestart` restarts a runner after 25 claims, and a restart
that reschedules the pod leaves the next compile with nothing.

Two settings shorten it, and they compose:

```bash
--set runner.goCache.persistence.enabled=true \
--set runner.goCache.persistence.storageClassName=efs-sc
```

`runner.goCache.persistence` mounts one PersistentVolumeClaim over both Go
cache paths. Every replica mounts that claim, so the StorageClass must serve
`ReadWriteMany` unless `runner.replicas` is 1; the chart refuses to render
otherwise. Sharing is safe and useful: the go command locks both caches and
addresses their contents by hash, so one pod's compile warms the whole pool.
Point `runner.goCache.persistence.existingClaim` at a claim you already manage
to skip the one the chart creates.

`runner.goCache.warmModules` downloads modules into `GOMODCACHE` while the
claim loop starts. An empty list warms the Sparkwing SDK at the runner image's
own version, which most of a pipeline's dependency graph hangs off. Name your
own modules to widen it, or set `["off"]` to start cold. A failed warm logs and
leaves the compile to fetch what it needs.

## RBAC

The chart creates a namespace-scoped Role + RoleBinding with no rules.
The runner and cache servers talk to the controller over HTTP and
never call the Kubernetes API, so neither mounts a ServiceAccount
token: every pod sets `automountServiceAccountToken: false`, and the
cache pod runs under its own ServiceAccount rather than the runner's.

The opt-in `k8s` and `warm` trigger runners call the API to create and inspect
runner Jobs. Give the runner pod a token when selecting either mode:

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

`serviceAccount.create=false` runs both pods under that one account.
The render fails without `shareAcrossComponents=true` so the sharing is a
recorded choice; the chart never invents a `-cache` name you have not
created.

## Image registry

Fallback image references rendered when a component tag is empty:

- `ghcr.io/sparkwing-dev/sparkwing-runner:<chart appVersion>`
- `ghcr.io/sparkwing-dev/sparkwing-cache:<chart appVersion>`

These fallbacks describe the intended registry layout, not a compatible public
release contract for this chart version. Pin repository and tag for every
enabled component to images built from one Sparkwing revision.

> The runner Deployment's command is
> `/usr/local/bin/runner-entrypoint.sh /usr/local/bin/sparkwing-runner`,
> so the runner image must be built from `build/Dockerfile.runner`
> (Debian slim, the pipeline compilation Go toolchain, Node.js 22 and npm
> for TypeScript pipelines, bash, common CLI tools, and the netrc-seeding
> entrypoint). Point
> `runner.image.repository` at an image built from that Dockerfile.


## Upgrade

```bash
helm upgrade runners ./charts/sparkwing-runner-bundle \
    --namespace sparkwing \
    -f my-values.yaml
```

Runner pods rolling-update one at a time; in-flight claims on the
rolled pod time out and re-queue. The cache uses `Recreate`
because of its RWO PVC -- expect ~30s of downtime per upgrade.
For zero-downtime cache, run a separate cache deployment with
`cache.enabled=false` here and point runners at it with
`runner.gitcacheUrl`.

An upgrade from a chart that deployed `sparkwing-logs` removes its
Deployment, Service and ServiceAccount; runners upload node logs to the
controller instead. The logs PVC carries `helm.sh/resource-policy: keep`
by default, so it survives the upgrade; nothing reads it, so delete it. A
values file that still sets `logs` fails to render. See the migration
guide section "The logs service is removed".

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

## Source

- Chart: `charts/sparkwing-runner-bundle/` in
  [`sparkwing-dev/sparkwing`](https://github.com/sparkwing-dev/sparkwing).
- Decision: 0001 -- open-core tier strategy.
