# sparkwing-full

Helm chart that deploys the **complete OSS Sparkwing stack** into a
single Kubernetes cluster:

- `sparkwing-controller` -- the orchestrator (state DB, /api/v1/*,
  webhooks)
- `sparkwing-web` -- the dashboard SPA host
- `sparkwing-runner-bundle` (sub-chart) -- runner + cache + logs

This is the chart referenced in architectural decision 0001 for the
**Enterprise self-host** topology -- one `helm install` and you have
a working Sparkwing instance. Single-tenant, single-instance: HA
features (multi-replica controller, leader election, replication,
zero-downtime upgrades) are paid tier and live in separate charts.

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

# At-rest encryption key for the controller's secrets store.
# Skip and the controller logs a WARNING + stores plaintext.
openssl rand -base64 32 > /tmp/sparkwing-key
kubectl -n sparkwing create secret generic sparkwing-secrets-key \
    --from-file=key=/tmp/sparkwing-key

# Bearer token used by sparkwing-web (proxying to controller) and
# sparkwing-runner-bundle (claim-loop auth). Same token works for
# both -- the controller's auth is single-tenant.
kubectl -n sparkwing create secret generic sparkwing-token \
    --from-literal=token=<random-32-byte-token>
```

## Quick install

```bash
# Vendor the sub-chart into ./charts/ (one-time per chart change).
helm dep up ./charts/sparkwing-full

# Install. With nothing pre-created, this gives you a working
# stack on a kind cluster -- no auth, no webhook verification,
# no encryption-at-rest.
helm install sparkwing ./charts/sparkwing-full \
    --namespace sparkwing --create-namespace
```

For a production install, attach the Secrets you created above:

```bash
helm install sparkwing ./charts/sparkwing-full \
    --namespace sparkwing --create-namespace \
    --set controller.githubWebhookSecret.name=sparkwing-webhook \
    --set controller.secretsKey.name=sparkwing-secrets-key \
    --set web.tokenSecret.name=sparkwing-token \
    --set sparkwing-runner-bundle.controller.tokenSecret.name=sparkwing-token \
    --set ingress.enabled=true \
    --set ingress.hosts[0].host=sparkwing.example.com \
    --set ingress.hosts[0].paths[0].path=/ \
    --set ingress.hosts[0].paths[0].pathType=Prefix
```

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
| `controller.secretsKey.name` | Secret holding 32-byte encryption key. | `""` |
| `controller.pool.enabled` | Enable warm-PVC pool (needs RBAC). | `true` |

### Web

| Key | Purpose | Default |
| --- | --- | --- |
| `web.image.repository` | Override web image. | `ghcr.io/sparkwing-dev/sparkwing-web` |
| `web.replicas` | Replica count (web is stateless). | `1` |
| `web.controller.url` | Override controller URL. | (auto-computed in-cluster) |
| `web.logs.url` | Override logs URL. | (auto-computed from sub-chart) |
| `web.tokenSecret.name` | Secret holding the controller-bearer token. | `""` |
| `web.requireLogin` | Force /login redirect. Off until tokens table seeded. | `false` |

### Ingress

| Key | Purpose | Default |
| --- | --- | --- |
| `ingress.enabled` | Create the Ingress resource. | `false` |
| `ingress.className` | IngressClass. Empty = cluster default. | `""` |
| `ingress.hosts[].host` | Hostname for the dashboard. | `sparkwing.example.com` |
| `ingress.tls` | TLS section. | `[]` |

### Runner-bundle sub-chart

Override under the `sparkwing-runner-bundle:` key. See the
[`sparkwing-runner-bundle` values](../sparkwing-runner-bundle/values.yaml)
for the full schema; a few commonly overridden keys:

| Key | Purpose | Default in this chart |
| --- | --- | --- |
| `sparkwing-runner-bundle.enabled` | Toggle the whole runner side. | `true` |
| `sparkwing-runner-bundle.controller.url` | Where the runner claims from. | (in-cluster controller Service) |
| `sparkwing-runner-bundle.controller.tokenSecret.name` | Bearer-token Secret. | `""` |
| `sparkwing-runner-bundle.runner.replicas` | Pool size. | `1` |
| `sparkwing-runner-bundle.runner.labels` | `Requires` labels. | `[cluster]` |

## Auth

The OSS controller's auth model is **single-tenant bearer token**.
Per decision 0001, SSO and advanced RBAC are explicitly *not* paid
gates -- they may land in OSS later. For now:

1. The first admin token is bootstrapped via the controller's CLI
   (`sparkwing cluster tokens create`). Run it after the controller
   pod is healthy:

   ```bash
   kubectl -n sparkwing exec deploy/sparkwing-controller -- \
       sparkwing-controller --help
   # The controller pod ships only the daemon binary today -- to
   # bootstrap a token, port-forward and use the local sparkwing
   # CLI against the in-cluster controller, or seed the tokens
   # table directly via the state DB.
   ```

2. Stash the token in the `sparkwing-token` Secret (see Pre-install
   above) and reference it from `web.tokenSecret.name` /
   `sparkwing-runner-bundle.controller.tokenSecret.name`.

3. Once the tokens table has at least one row, set
   `web.requireLogin=true` in `helm upgrade` so the dashboard
   redirects unauthenticated browsers to `/login`. Before that,
   leaving it off avoids a redirect loop on a fresh install.

## Storage

The controller's state DB lives on an RWO PVC. PVC is annotated
`helm.sh/resource-policy: keep` by default so `helm uninstall`
doesn't wipe run history. Disable with
`controller.storage.pvc.keepOnUninstall=false`, or
`controller.storage.type=emptyDir` for a fully ephemeral install
(only useful for kind / CI smoke tests).

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

## Sub-chart dependency

`charts/sparkwing-full/Chart.yaml` declares
`sparkwing-runner-bundle` as a sibling-directory dependency:

```yaml
dependencies:
  - name: sparkwing-runner-bundle
    version: "0.1.0"
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

Default images:

- `ghcr.io/sparkwing-dev/sparkwing-controller:<chart appVersion>`
- `ghcr.io/sparkwing-dev/sparkwing-web:<chart appVersion>`
- (sub-chart) `ghcr.io/sparkwing-dev/sparkwing-runner:<...>`,
  `sparkwing-cache`, `sparkwing-logs`

> **NOTE:** Multi-arch (linux/amd64 + linux/arm64) images are
> published to GHCR on every `v*` tag push by
> `.github/workflows/release.yaml`. Each release pushes
> `:vX.Y.Z`; stable (non-pre-release) tags also update `:latest`.
> All images are cosign-keyless-signed via GitHub OIDC -- verify
> with:
>
> ```bash
> cosign verify ghcr.io/sparkwing-dev/sparkwing-controller:vX.Y.Z \
>     --certificate-identity-regexp "https://github.com/sparkwing-dev/sparkwing/" \
>     --certificate-oidc-issuer "https://token.actions.githubusercontent.com"
> ```
>
> Override `*.image.repository` if you mirror images internally.

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

**Dashboard redirects to /login but I haven't bootstrapped a token**
Set `web.requireLogin=false` (default) until you've seeded the
tokens table.

**`helm template` fails with "no chart found at file://../sparkwing-runner-bundle"**
You skipped `helm dep up`. Run it once before lint / template /
install.

## Source

- Chart: `charts/sparkwing-full/` in
  [`sparkwing-dev/sparkwing`](https://github.com/sparkwing-dev/sparkwing).
- Decision: 0001 -- open-core tier strategy.
- Sibling chart: `charts/sparkwing-runner-bundle/`.
