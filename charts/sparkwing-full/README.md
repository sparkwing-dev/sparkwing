# sparkwing-full

Helm chart that deploys the **complete OSS Sparkwing stack** into a
single Kubernetes cluster:

- `sparkwing-controller` -- the orchestrator (state DB, /api/v1/*,
  webhooks)
- `sparkwing-web` -- the dashboard SPA host
- `sparkwing-runner-bundle` (sub-chart) -- runner + cache + logs

This is the chart referenced in architectural decision 0001 for the
**Enterprise self-host** topology. It models the complete stack as one
Helm release. Single-tenant, single-instance: HA features (multi-replica
controller, leader election, replication, zero-downtime upgrades) are
paid tier and live in separate charts.

> **Release blocker:** The checked-in default repositories and
> `appVersion` do not currently identify a compatible public image set. A bare
> install renders the intended topology, but it is not a supported runnable
> release. Until a corrected release is published, build or mirror one
> mutually compatible controller, web, runner, cache, and logs image set and
> explicitly set every enabled component's `image.repository` and `image.tag`.

If you only need a runner pool against a remote controller (Cloud or
external self-host), use the standalone
[`sparkwing-runner-bundle`](../sparkwing-runner-bundle/) chart
instead -- this chart pulls it in as a dependency.

## Topology

```
   +------------------------------------+
   |  Browsers / CLI / webhooks         |
   +------------------------------------+
                 |
                 v
   +-------------------------------+
   |   sparkwing-web (dashboard)   |     [optional Ingress]
   +-------------------------------+
                 |
                 v  /api/v1/*
   +-------------------------------+      +---------------+
   |   sparkwing-controller        | <--- |  Webhooks     |
   |   (state DB on PVC)           |      |  (GitHub etc) |
   +-------------------------------+      +---------------+
        ^               ^
        | claim         | log writes / reads
        |               |
   +---------+      +---------+      +---------+
   |  runner |----> | gitcache|      |  logs   |
   +---------+      +---------+      +---------+
            (sparkwing-runner-bundle sub-chart)
```

## Requirements

- Kubernetes 1.27+
- Explicit repositories and tags for a mutually compatible image set. The
  current default GHCR/appVersion combination is not a runnable release.
- A default `StorageClass` (or set `controller.storage.pvc.storageClassName`
  / equivalents on the sub-chart). The controller, cache, and logs
  PVCs are all RWO.
- `helm` v3.13+ (chart uses standard idioms; nothing exotic).

### Pre-install Secrets (optional but recommended)

Operators bring their own Secrets so they can rotate without
`helm upgrade`. Create these in the install namespace before
running install:

```bash
# Webhook signing secret (HMAC for /webhooks/github/{pipeline}).
# Skip if you won't expose webhooks publicly.
kubectl -n sparkwing create secret generic sparkwing-webhook \
    --from-literal=webhook-secret=<your-shared-secret>

# GitHub token for PR commit statuses. Use a fine-grained token with
# Commit statuses: Read and write for every repository this controller serves.
kubectl -n sparkwing create secret generic sparkwing-github-status \
    --from-literal=token=<your-github-token>

# At-rest encryption key for the controller's secrets store.
# Skip and the controller logs a WARNING + stores plaintext.
openssl rand -base64 32 > /tmp/sparkwing-key
kubectl -n sparkwing create secret generic sparkwing-secrets-key \
    --from-file=key=/tmp/sparkwing-key

# Bearer token for sparkwing-web (controller proxy) and the runner
# bundle (claim loop). The controller only accepts tokens IT minted,
# so create this Secret AFTER the first `helm install` -- see Auth below.
#   kubectl -n sparkwing create secret generic sparkwing-token \
#       --from-literal=token=swr_...
# Tokens carry scopes: a runner token needs `nodes.claim`,
# `triggers.claim`, `runs.state`, `secrets.read`, and `logs.write`.
# The web pod's needs `runs.read` + `logs.read`, plus
# `runs.write` where operators cancel, retry, or release runs from the
# dashboard and `approvals.write` where they resolve approval gates.
# Deleting a run from the dashboard needs `admin` on the web token AND
# on the signed-in account; leave it off to keep deletion on the CLI.
# Mint the two separately so neither carries the other's reach.
```

## Install from source

Create `compatible-images.yaml` with images built from the same Sparkwing
revision or copied together into your registry:

```yaml
controller:
  image: {repository: registry.example/sparkwing-controller, tag: <compatible-tag>}
web:
  image: {repository: registry.example/sparkwing-web, tag: <compatible-tag>}
sparkwing-runner-bundle:
  runner:
    image: {repository: registry.example/sparkwing-runner, tag: <compatible-tag>}
  cache:
    image: {repository: registry.example/sparkwing-cache, tag: <compatible-tag>}
  logs:
    image: {repository: registry.example/sparkwing-logs, tag: <compatible-tag>}
```

```bash
# Vendor the sub-chart into ./charts/ (one-time per chart change).
helm dep up ./charts/sparkwing-full

# Install the complete stack with an explicitly compatible image set. This
# source-test configuration has no auth, webhook verification, or encryption-at-rest,
# so the cache and the logs service must opt out of their token requirement
# explicitly.
helm install sparkwing ./charts/sparkwing-full \
    --namespace sparkwing --create-namespace \
    -f compatible-images.yaml \
    --set sparkwing-runner-bundle.cache.allowUnauthenticated=true \
    --set sparkwing-runner-bundle.logs.allowUnauthenticated=true
```

### Verify the source stack in Kubernetes

The repository pipeline installs the chart in an explicit Kubernetes context
and exercises the authenticated controller-to-runner path with caller-supplied
images:

```bash
sparkwing run k8s-e2e
```

Use a dedicated namespace and immutable image tag:

```bash
export SPARKWING_K8S_E2E_KUBE_CONTEXT=sparkwing-e2e
export SPARKWING_K8S_E2E_NAMESPACE=sparkwing-e2e-verify
export SPARKWING_K8S_E2E_RELEASE=sparkwing-e2e
export SPARKWING_K8S_E2E_IMAGE_PREFIX=registry.example/sparkwing
export SPARKWING_K8S_E2E_TAG=commit-0123456789ab
export SPARKWING_K8S_E2E_ALLOW_CLEANUP="$SPARKWING_K8S_E2E_NAMESPACE/$SPARKWING_K8S_E2E_RELEASE"
sparkwing run k8s-e2e
```

The check refuses an absent context, implicit image coordinates, a
pre-existing namespace, or an allow-list that differs from the
configured namespace and release. It installs the Git fixture from a ConfigMap
instead of a node host mount. Cleanup rechecks a unique per-run namespace owner
token, uninstalls the allow-listed Helm release only when its durable Helm
release metadata carries the same token, and deletes only resources carrying
both ownership labels. It leaves the cluster and namespace intact. Set
`SPARKWING_K8S_E2E_KEEP_RESOURCES=1` to retain those resources for inspection.

The check exercises the caller-selected repository prefix and tag and records
those coordinates in its evidence; it does not resolve a mutable tag to a
digest or prove how those images were built. It does not make the chart's
incompatible default public image tags runnable.

For a production install, attach the Secrets you created above:

```bash
helm install sparkwing ./charts/sparkwing-full \
    --namespace sparkwing --create-namespace \
    -f compatible-images.yaml \
    --set controller.githubWebhookSecret.name=sparkwing-webhook \
    --set controller.githubStatusToken.name=sparkwing-github-status \
    --set controller.dashboardURL=https://sparkwing.example.com \
    --set controller.secretsKey.name=sparkwing-secrets-key \
    --set web.tokenSecret.name=sparkwing-token \
    --set sparkwing-runner-bundle.controller.tokenSecret.name=sparkwing-token \
    --set web.requireLogin=true \
    --set ingress.enabled=true \
    --set ingress.hosts[0].host=sparkwing.example.com \
    --set ingress.hosts[0].paths[0].path=/ \
    --set ingress.hosts[0].paths[0].pathType=Prefix \
    --set ingress.tls[0].hosts[0]=sparkwing.example.com \
    --set ingress.tls[0].secretName=sparkwing-tls
```

An Ingress with an empty `ingress.tls` or with `web.requireLogin=false`
fails to render, because publishing the dashboard is the one knob whose
purpose is reaching browsers outside the cluster. Set
`ingress.allowInsecure=true` to publish it unencrypted or open anyway;
it must be a bool, since a quoted string fails the render instead of
reading as an opt-out. Opting in without TLS also sets
`SPARKWING_WEB_INSECURE_COOKIES=1` and `--allow-insecure-cookies-remote`
on the web Deployment, so the login gate still works over plain HTTP on
a pod that binds a non-loopback address. The `ingress.tls` check is
presence-only: an entry without `secretName` leaves TLS to the ingress
controller's default certificate.

## Values cheat sheet

Full schema in [`values.yaml`](./values.yaml). Most-edited keys:

### Controller

| Key | Purpose | Default |
| --- | --- | --- |
| `controller.image.repository` | Override controller image. | `ghcr.io/sparkwing-dev/sparkwing-controller` |
| `controller.image.tag` | Override controller tag. | (chart appVersion) |
| `controller.storage.type` | `pvc` (default) or `emptyDir` for ephemeral. | `pvc` |
| `controller.storage.pvc.size` | State DB volume size. | `5Gi` |
| `controller.storage.pvc.storageClassName` | Override default StorageClass. | `""` |
| `controller.storage.pvc.keepOnUninstall` | Annotate PVC `helm.sh/resource-policy: keep`. | `true` |
| `controller.githubWebhookSecret.name` | Secret holding `webhook-secret`. | `""` |
| `controller.githubStatusToken.name` | Secret holding a GitHub token with commit-status write access. | `""` |
| `controller.dashboardURL` | Query-free HTTP(S) dashboard base URL for commit-status run links; invalid values omit the link. | `""` |
| `controller.secretsKey.name` | Secret holding 32-byte encryption key. | `""` |
| `controller.pool.enabled` | Enable warm-PVC pool (needs RBAC). | `true` |
| `controller.trustedProxyCIDRs` | Proxy source CIDRs allowed to supply `X-Forwarded-For` for login throttling. Include the web pod's source, or dashboard logins all share one budget; when the pod IP is unknown, use the cluster pod CIDR. | `[]` |
| `controller.argon2MemoryBudgetMB` | Memory ceiling in MiB for concurrent argon2id hashing; each hash holds 64 MiB. | `256` |

### Web

| Key | Purpose | Default |
| --- | --- | --- |
| `web.image.repository` | Override web image. | `ghcr.io/sparkwing-dev/sparkwing-web` |
| `web.replicas` | Replica count (web is stateless). | `1` |
| `web.controller.url` | Override controller URL. | (auto-computed in-cluster) |
| `web.logs.url` | Override logs URL. | (auto-computed from sub-chart) |
| `web.cache.url` | Cache the services panel probes. Probe-only; empty and no bundled cache leaves it off the panel. | (auto-computed from sub-chart) |
| `web.tokenSecret.name` | Secret holding the controller-bearer token. | (defaults to `sparkwing-runner-bundle.controller.tokenSecret`) |
| `web.addr` | Address the web pod binds. Empty binds `0.0.0.0:<web.port>`, which the Service needs; a loopback value is reachable only through a port-forward. | `""` |
| `web.requireLogin` | Gate the dashboard behind /login (first visit offers first-admin signup). | `false` |
| `web.trustedProxyCIDRs` | Proxy source CIDRs allowed to supply `X-Forwarded-For` for login throttling. | `[]` |

### Security and volume ownership

| Key | Purpose | Default |
| --- | --- | --- |
| `podSecurityContext.runAsUser` | Non-root UID for controller and web. | `65534` |
| `podSecurityContext.fsGroup` | Group for mounted storage. | `65534` |
| `podSecurityContext.seccompProfile.type` | Seccomp profile the Pod Security "restricted" profile requires. | `RuntimeDefault` |
| `containerSecurityContext.readOnlyRootFilesystem` | Read-only image layer; each pod writes to its mounted volumes and a `/tmp` scratch `emptyDir`. | `true` |
| `volumePermissions.enabled` | Run a CHOWN-only init container before controller and web. | `true` |

### Ingress

| Key | Purpose | Default |
| --- | --- | --- |
| `ingress.enabled` | Create the Ingress resource. | `false` |
| `ingress.className` | IngressClass. Empty = cluster default. | `""` |
| `ingress.hosts[].host` | Hostname for the dashboard. | `sparkwing.example.com` |
| `ingress.tls` | TLS section. Empty fails the render unless `ingress.allowInsecure`; presence-only, `secretName` optional. | `[]` |
| `ingress.allowInsecure` | Publish the dashboard without TLS or without a login gate. Bool only. | `false` |

### Runner-bundle sub-chart

Override under the `sparkwing-runner-bundle:` key. See the
[`sparkwing-runner-bundle` values](../sparkwing-runner-bundle/values.yaml)
for the full schema; a few commonly overridden keys:

| Key | Purpose | Default in this chart |
| --- | --- | --- |
| `sparkwing-runner-bundle.enabled` | Toggle the whole runner side. | `true` |
| `sparkwing-runner-bundle.controller.url` | Where the runner claims from. | (in-cluster controller Service) |
| `sparkwing-runner-bundle.controller.tokenSecret.name` | Bearer-token Secret, shared by the runner and the cache. | `""` |
| `sparkwing-runner-bundle.cache.allowUnauthenticated` | Serve the cache without a token (bootstrap only). | `false` |
| `sparkwing-runner-bundle.logs.allowUnauthenticated` | Serve every run's logs without a token (bootstrap only). | `false` |
| `sparkwing-runner-bundle.runner.replicas` | Pool size. | `1` |
| `sparkwing-runner-bundle.runner.labels` | `Requires` labels. | `[cluster]` |
| `sparkwing-runner-bundle.runner.triggerRunner.kind` | Node execution for claimed triggers: `inprocess`, `k8s`, or agent-first `warm`. | `inprocess` |
| `sparkwing-runner-bundle.runner.automountServiceAccountToken` | Mount the runner pod's API token for `k8s` or `warm` trigger execution. | `false` |
| `sparkwing-runner-bundle.volumePermissions.enabled` | Run a CHOWN-only init before the runner. | `true` |
| `sparkwing-runner-bundle.cache.dependencyProxy.enabled` | Point the runner's go / npm / pip at the cache's pull-through proxy. | `true` |

The automatic controller URL follows the chart's default resource names. If
you set top-level `nameOverride` or `fullnameOverride`, also set
`sparkwing-runner-bundle.controller.url` to the resulting controller Service;
the chart stops at render time with this instruction when the URL is missing.
Nested `sparkwing-runner-bundle.nameOverride` and `fullnameOverride` values are
included in the web and controller URLs for the bundled logs and cache
Services.

Set `sparkwing-runner-bundle.runner.triggerRunner.kind=warm` and
`sparkwing-runner-bundle.runner.automountServiceAccountToken=true` to offer
unlabeled nodes to outbound-only remote agents before using Kubernetes Jobs
for overflow. This mode reuses the bundled runner image, namespace, service
account, pull policy, and cache. It grants namespace-scoped Job lifecycle and
pod-read access to the runner Role. The default `inprocess` mode keeps the
existing behavior and renders an empty Role.
The fallback `run-node` process receives the runner token in its environment,
so use warm mode only for trusted pipeline code and rotate short-lived tokens.
The compiled pipeline binary interprets `warm`, so upgrade the controller,
runner, and pipeline module to the same Sparkwing release before enabling it.

## Auth

API clients authenticate with **bearer tokens the controller mints**;
each token carries scopes. Per decision 0001, SSO and advanced RBAC
are explicitly *not* paid gates -- they may land in OSS later. For now:

1. Until the tokens table has a row, the controller serves **every
   endpoint unauthenticated** and logs a warning at boot. Mint the
   first token through that open window, then restart to turn auth on:

   ```bash
   kubectl -n sparkwing port-forward deploy/sparkwing-controller 9001:80 &
   sparkwing cluster tokens create --profile <profile-pointing-at-localhost:9001> \
       --type user --principal admin --scope admin
   kubectl -n sparkwing rollout restart deploy/sparkwing-controller
   ```

   Auth only takes effect on that restart -- the tokens table is read
   once at startup.

2. Mint one token per consumer, stash each in a Secret (see Pre-install
   above), and reference them from `web.tokenSecret.name` /
   `sparkwing-runner-bundle.controller.tokenSecret.name`. Separate tokens keep
   the web pod's proxy bearer to the dashboard's scopes.

   A configured Secret name requires a non-empty key; the chart rejects
   incomplete pairs. Web, runner, and cache Secret references are required, so
   Kubernetes holds those pods until the configured Secret is present.
   `sparkwing-runner-bundle.controller.tokenSecret` is also what the cache
   reads as `SPARKWING_API_TOKEN`, the runner as `SPARKWING_CACHE_TOKEN`, and
   the logs service as the signal to resolve callers against the controller;
   a cache-enabled install without it fails at render time unless
   `sparkwing-runner-bundle.cache.allowUnauthenticated=true`, and a
   logs-enabled one unless
   `sparkwing-runner-bundle.logs.allowUnauthenticated=true`. The bootstrap
   window above needs both and the token upgrade should turn both back off.

   An install that sets only `sparkwing-runner-bundle.controller.tokenSecret`
   gives the web pod that same Secret, so the dashboard's log panes carry a
   bearer the authenticated logs service accepts. Set `web.tokenSecret.name`
   to hand the dashboard its own narrower token instead.

3. Set `web.requireLogin=true` to gate the dashboard behind `/login`.
   On a fresh cluster `/login` renders a "create first admin" form and
   the account you create there becomes the admin; afterwards, seed
   users with `sparkwing cluster users add`, whose `--scope` bounds what a
   signed-in account reaches through the dashboard proxy. Login cookies are `Secure`,
   so configure an HTTPS ingress before signing in. The chart's plain HTTP
   port-forward remains useful for probes but cannot retain a browser login.

## Storage

The controller's state DB lives on an RWO PVC. PVC is annotated
`helm.sh/resource-policy: keep` by default so `helm uninstall`
doesn't wipe run history. Disable with
`controller.storage.pvc.keepOnUninstall=false`, or
`controller.storage.type=emptyDir` for a fully ephemeral test install.

By default, controller and web each run a short ownership init container before
the non-root application starts. The init container runs as UID 0 with a
read-only root filesystem, no privilege escalation, and only the `CHOWN`
capability; it assigns the mounted Sparkwing home root to
`podSecurityContext.runAsUser:podSecurityContext.fsGroup`. The application
container remains non-root with all capabilities dropped. Set
`volumePermissions.enabled=false` only when the storage driver provisions the
mounted root with that ownership already. The enabled path requires controller
and web images containing `/bin/chown`; Sparkwing's release-shaped Alpine images
include it, but custom images must provide it themselves.

The ownership init container runs as UID 0 with `CHOWN`, so Kubernetes' baseline
policy admits it but the Restricted Pod Security Standard does not. In a
Restricted namespace, arrange the configured UID/GID through the CSI driver or
another provisioning step and set `volumePermissions.enabled=false`. This
opt-out removes only the init container; the application containers retain the
chart's non-root, drop-all-capabilities security context.

The runner sub-chart applies the same bounded ownership init to its
`/tmp/sparkwing` home. Configure its independent opt-out with
`sparkwing-runner-bundle.volumePermissions.enabled=false`.

For a clean uninstall:

```bash
helm uninstall sparkwing --namespace sparkwing
kubectl -n sparkwing delete pvc -l app.kubernetes.io/instance=sparkwing
```

(That second line wipes the controller's state DB AND the runner
bundle's cache + logs PVCs. Skip it if you want to roll forward
later with the same data.)

## Ingress

Disabled by default -- many self-host operators front the dashboard
with their own ingress controller / Gateway / cloud LB. Set
`ingress.enabled=true` to let this chart manage one. The Ingress
points at `sparkwing-web` (port 80); the SPA proxies `/api/v1/*` to
the controller, so you don't need a separate Ingress for the
controller.

The web pod proxies `/api/v1/*` only. GitHub webhooks are served by
the controller at `POST /webhooks/github/{pipeline}`; if you expose
them, add your own Ingress rule (or host) routing that path to the
`<release>-controller` Service on port 80 -- this chart does not
create one.

## Sub-chart dependency

`charts/sparkwing-full/Chart.yaml` declares
`sparkwing-runner-bundle` as a sibling-directory dependency:

```yaml
dependencies:
  - name: sparkwing-runner-bundle
    version: "0.1.6"
    repository: "file://../sparkwing-runner-bundle"
    condition: sparkwing-runner-bundle.enabled
```

This works for local development. **For a real release** the
repository should point at a published Helm chart repo (TBD --
likely `https://sparkwing-dev.github.io/charts`). That migration is
out of scope here.

After any change to the sub-chart's templates / version, re-run:

```bash
helm dep up ./charts/sparkwing-full
```

This refreshes `Chart.lock` and re-vendors the sub-chart under
`charts/`.

## Image registry

Fallback image references rendered when a component tag is empty:

- `ghcr.io/sparkwing-dev/sparkwing-controller:<chart appVersion>`
- `ghcr.io/sparkwing-dev/sparkwing-web:<chart appVersion>`
- (sub-chart) `ghcr.io/sparkwing-dev/sparkwing-runner:<...>`,
  `sparkwing-cache`, `sparkwing-logs`

These fallbacks describe the chart's intended registry layout; they are not a
compatible public release contract for the current chart version. Pin both
repository and tag for every enabled image. Keep all five images on the same
compatible Sparkwing revision until a corrected chart release publishes and
verifies a public default set.

## Upgrade

```bash
helm dep up ./charts/sparkwing-full
helm upgrade sparkwing ./charts/sparkwing-full \
    --namespace sparkwing -f my-values.yaml
```

The controller uses `strategy: Recreate` (RWO PVC -- can't
multi-attach), so expect a brief downtime per upgrade. Web is
RollingUpdate. Runner pods rolling-update one at a time.

State-DB compatibility: the controller's SQLite schema migrates
forward automatically on startup. There is no rollback story for
schema migrations -- if you need to downgrade across a schema
change, restore from a backup of `/data` taken before the upgrade.

## Uninstall

```bash
helm uninstall sparkwing --namespace sparkwing
```

PVCs survive (see Storage). Secrets you pre-created
(`sparkwing-webhook`, `sparkwing-secrets-key`, `sparkwing-token`)
also survive -- the chart references them but doesn't own them.
Delete manually if you want a fully clean slate.

## Troubleshooting

**Controller pod stuck Pending**
PVC binding failed. Check `kubectl describe pvc <release>-controller`.
Most common: no default StorageClass. Set
`controller.storage.pvc.storageClassName` explicitly.

**Web pod 502s on /api/v1/***
Web can't reach the controller. Check
`kubectl logs deploy/<release>-web` -- you'll see the upstream URL.
Confirm the controller Service resolves:

```bash
kubectl -n <ns> run --rm -it -q probe --image=curlimages/curl -- \
    curl -v http://<release>-controller.<ns>.svc.cluster.local/api/v1/health
```

**Runner not claiming work**
The bundled runner's `controller.url` defaults to the in-cluster
controller Service. If you overrode it, confirm reachability from
inside the runner pod. See
[`sparkwing-runner-bundle/README.md`](../sparkwing-runner-bundle/README.md#troubleshooting).

**Dashboard redirects to /login and I have no account**
The first visit to /login on a fresh cluster offers a "create first
admin" form. If the users table is already seeded, add accounts with
`sparkwing cluster users add`.

**`helm template` fails with "no chart found at file://../sparkwing-runner-bundle"**
You skipped `helm dep up`. Run it once before lint / template /
install.

## Source

- Chart: `charts/sparkwing-full/` in
  [`sparkwing-dev/sparkwing`](https://github.com/sparkwing-dev/sparkwing).
- Decision: 0001 -- open-core tier strategy.
- Sibling chart: `charts/sparkwing-runner-bundle/`.
