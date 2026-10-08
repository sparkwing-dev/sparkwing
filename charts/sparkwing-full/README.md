# sparkwing-full

Helm chart that deploys the **complete OSS Sparkwing stack** into a
single Kubernetes cluster:

- `sparkwing-controller` -- the orchestrator (state DB, /api/v1/*,
  webhooks) and the dashboard with its sign-in pages
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
> mutually compatible controller, runner, cache, and logs image set and
> explicitly set every enabled component's `image.repository` and `image.tag`.

If you only need a runner pool against a remote controller (Cloud or
external self-host), use the standalone
[`sparkwing-runner-bundle`](../sparkwing-runner-bundle/) chart
instead -- this chart pulls it in as a dependency.

## Render the chart

`helm template` stops on the sub-chart's `validate.yaml` unless the cache's
operator token Secret is named, and the values live under the sub-chart's
key. It also stops while the controller has neither a bootstrap admin token
nor `controller.allowOpenBootstrap=true` (see [Auth](#auth)). The minimal
read-only render is:

```bash
helm template sparkwing ./charts/sparkwing-full \
  --set controller.allowOpenBootstrap=true \
  --set sparkwing-runner-bundle.controller.tokenSecret.name=sparkwing-token \
  --set sparkwing-runner-bundle.cache.tokenSecret.name=sparkwing-cache-token \
  --set sparkwing-runner-bundle.cache.grantKeySecret.name=sparkwing-cache-grant-key
```

`charts/render_test.go` injects those same values, so what it exercises is
what this renders. The sub-chart is vendored in the repository, so this works in a
fresh clone with no `helm dependency update` first.

## Topology

```
   +------------------------------------+
   |  Browsers / CLI / webhooks         |
   +------------------------------------+
                 |
                 v                          [optional Ingress]
   +-------------------------------+
   |   sparkwing-controller        |
   |   API + dashboard pages       |
   |   (state DB on PVC)           |
   +-------------------------------+
        ^               |
        | claim         | dashboard log reads
        |               v
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

# The first admin token. The controller stores it as an admin credential
# before it binds, so it never serves a request unauthenticated. Keep the
# value: it is the admin bearer for `sparkwing cluster tokens create`. The
# umask makes the file 0600, so no other local account can read it.
(umask 077 && printf 'swu_%s' "$(openssl rand -hex 24)" > "$HOME/sparkwing-bootstrap-admin")
kubectl -n sparkwing create secret generic sparkwing-bootstrap-admin \
    --from-file=token="$HOME/sparkwing-bootstrap-admin"

# At-rest encryption key for the controller's secrets store.
# Skip and the controller logs a WARNING + stores plaintext.
openssl rand -base64 32 > /tmp/sparkwing-key
kubectl -n sparkwing create secret generic sparkwing-secrets-key \
    --from-file=key=/tmp/sparkwing-key

# Bearer token for the runner bundle (claim loop). The controller only
# accepts tokens IT minted, so mint it with the bootstrap admin token
# after the first `helm install` -- see Auth below.
#   kubectl -n sparkwing create secret generic sparkwing-token \
#       --from-literal=token=swr_...
# A runner token needs `nodes.claim`, `triggers.claim`, `runs.state`,
# `secrets.read`, and `logs.write`.

# The cache's operator token and the key cache grants are signed with.
# Both are random secrets of their own and neither is ever a runner token:
# pipeline code can read the runner's token, and either of these opens
# every team's cache. The chart refuses to render when two of the three
# name the same Secret key.
kubectl -n sparkwing create secret generic sparkwing-cache-token \
    --from-literal=token="$(openssl rand -hex 32)"
kubectl -n sparkwing create secret generic sparkwing-cache-grant-key \
    --from-literal=key="$(openssl rand -hex 32)"
```

## Install from source

Create `compatible-images.yaml` with images built from the same Sparkwing
revision or copied together into your registry:

```yaml
controller:
  image: {repository: registry.example/sparkwing-controller, tag: <compatible-tag>}
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
# source-test configuration has no auth or encryption-at-rest,
# so the controller, the cache and the logs service must opt out of their
# token requirement explicitly.
helm install sparkwing ./charts/sparkwing-full \
    --namespace sparkwing --create-namespace \
    -f compatible-images.yaml \
    --set controller.allowOpenBootstrap=true \
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
    --set controller.dashboardURL=https://sparkwing.example.com \
    --set controller.secretsKey.name=sparkwing-secrets-key \
    --set controller.bootstrapAdminToken.name=sparkwing-bootstrap-admin \
    --set sparkwing-runner-bundle.controller.tokenSecret.name=sparkwing-token \
    --set sparkwing-runner-bundle.cache.tokenSecret.name=sparkwing-cache-token \
    --set sparkwing-runner-bundle.cache.grantKeySecret.name=sparkwing-cache-grant-key \
    --set ingress.enabled=true \
    --set ingress.hosts[0].host=sparkwing.example.com \
    --set ingress.hosts[0].paths[0].path=/ \
    --set ingress.hosts[0].paths[0].pathType=Prefix \
    --set ingress.tls[0].hosts[0]=sparkwing.example.com \
    --set ingress.tls[0].secretName=sparkwing-tls
```

The Ingress publishes the whole controller: its API, webhooks, and
dashboard. An Ingress fails to render with an empty `ingress.tls`, with
`controller.requireAuth=false`, or with `controller.allowOpenBootstrap=true`
and no `controller.bootstrapAdminToken.name`, because each one publishes a
controller that anyone on the path can read or drive. Set
`ingress.allowInsecure=true` to publish it unencrypted or open anyway; it
must be a bool, since a quoted string fails the render instead of reading
as an opt-out. Opting in without TLS also passes `--insecure-cookies` to
the controller, so a browser keeps its session over plain HTTP. The
`ingress.tls` check is presence-only: an entry without `secretName` leaves
TLS to the ingress controller's default certificate. An `ingress.tls`
entry also passes `--hsts` to the controller, so it sends
Strict-Transport-Security and requires an https Origin on
cookie-authenticated writes.

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
| `controller.databaseSecret.name` | Secret containing a PostgreSQL DSN. Empty keeps SQLite. | `""` |
| `controller.databaseSecret.key` | Key holding the PostgreSQL DSN. | `dsn` |
| `controller.dashboardURL` | Query-free HTTP(S) dashboard base URL for GitHub App check run links; invalid values omit the link. | `""` |
| `controller.secretsKey.name` | Secret holding 32-byte encryption key, mounted as a file and named with `--secrets-key-file`. | `""` |
| `controller.secretsPreviousKey.name` | Secret holding the key values were sealed under before `secretsKey`; read-only fallback for the window before `sparkwing secrets rotate` runs. | `""` |
| `controller.bootstrapAdminToken.name` | Secret holding the first admin token, stored as an admin credential before the listener binds when the tokens table is empty. | `""` |
| `controller.requireAuth` | Refuse to start when no live token exists. Without `bootstrapAdminToken` the render refuses unless `allowOpenBootstrap` is true, because a fresh controller would have no way to mint its first token. | `true` |
| `controller.allowOpenBootstrap` | Render without `bootstrapAdminToken` and drop `--require-auth`, so an empty tokens table serves every route unauthenticated until a token exists and the controller restarts. A controller that already holds a token stays authenticated. Bool only. | `false` |
| `controller.argon2MemoryBudgetMB` | Memory ceiling in MiB for concurrent argon2id hashing; each hash holds 64 MiB. | `256` |
| `controller.logs.url` | The logs service the controller announces to clients and reads dashboard log panes from, passed as `--logs-url`. | (auto-computed from sub-chart) |
| `controller.cache.url` | The cache the controller's gitcache proxy forwards to. | (auto-computed from sub-chart) |

### Security and volume ownership

| Key | Purpose | Default |
| --- | --- | --- |
| `podSecurityContext.runAsUser` | Non-root UID for the controller. | `65534` |
| `podSecurityContext.fsGroup` | Group for mounted storage. | `65534` |
| `podSecurityContext.seccompProfile.type` | Seccomp profile the Pod Security "restricted" profile requires. | `RuntimeDefault` |
| `containerSecurityContext.readOnlyRootFilesystem` | Read-only image layer; each pod writes to its mounted volumes and a `/tmp` scratch `emptyDir`. | `true` |
| `volumePermissions.enabled` | Run a CHOWN-only init container before the controller. | `true` |

### Ingress

| Key | Purpose | Default |
| --- | --- | --- |
| `ingress.enabled` | Create the Ingress resource. | `false` |
| `ingress.className` | IngressClass. Empty = cluster default. | `""` |
| `ingress.hosts[].host` | Hostname routed to the controller Service. List a console host and an API host to publish both. | `sparkwing.example.com` |
| `ingress.tls` | TLS section. Empty fails the render unless `ingress.allowInsecure`; presence-only, `secretName` optional. Any entry passes `--hsts` to the controller. | `[]` |
| `ingress.allowInsecure` | Publish the controller without TLS or without `--require-auth`. Without TLS it also passes `--insecure-cookies`. Bool only. | `false` |

### Runner-bundle sub-chart

Override under the `sparkwing-runner-bundle:` key. See the
[`sparkwing-runner-bundle` values](../sparkwing-runner-bundle/values.yaml)
for the full schema; a few commonly overridden keys:

| Key | Purpose | Default in this chart |
| --- | --- | --- |
| `sparkwing-runner-bundle.enabled` | Toggle the whole runner side. | `true` |
| `sparkwing-runner-bundle.controller.url` | Where the runner claims from. | (in-cluster controller Service) |
| `sparkwing-runner-bundle.controller.tokenSecret.name` | The runner's bearer-token Secret. | `""` |
| `sparkwing-runner-bundle.cache.tokenSecret.name` | The cache's operator token, which the controller also holds. Never the runner's token. | `""` |
| `sparkwing-runner-bundle.cache.grantKeySecret.name` | The key the controller signs cache grants with and the cache verifies them with. | `""` |
| `sparkwing-runner-bundle.cache.allowUnauthenticated` | Serve the cache without a token (bootstrap only). | `false` |
| `sparkwing-runner-bundle.logs.allowUnauthenticated` | Serve every run's logs without a token (bootstrap only). | `false` |
| `sparkwing-runner-bundle.runner.replicas` | Pool size. | `1` |
| `sparkwing-runner-bundle.runner.labels` | `Requires` labels. | `[cluster]` |
| `sparkwing-runner-bundle.runner.triggerRunner.kind` | Node execution for claimed triggers: `inprocess`, `k8s`, or agent-first `warm`. | `inprocess` |
| `sparkwing-runner-bundle.runner.automountServiceAccountToken` | Mount the runner pod's API token for `k8s` or `warm` trigger execution. | `false` |
| `sparkwing-runner-bundle.volumePermissions.enabled` | Run a CHOWN-only init before the runner. | `true` |
| `sparkwing-runner-bundle.runner.goCache.persistence.enabled` | Mount a PVC over the runner's `GOCACHE` and `GOMODCACHE`. | `false` |
| `sparkwing-runner-bundle.runner.goCache.warmModules` | Modules downloaded into `GOMODCACHE` at runner startup. | `[]` |
| `sparkwing-runner-bundle.cache.dependencyProxy.enabled` | Point the runner's go / npm / pip at the cache's pull-through proxy. | `true` |

The automatic controller URL follows the chart's default resource names. If
you set top-level `nameOverride` or `fullnameOverride`, also set
`sparkwing-runner-bundle.controller.url` to the resulting controller Service;
the chart stops at render time with this instruction when the URL is missing.
Nested `sparkwing-runner-bundle.nameOverride` and `fullnameOverride` values are
included in the controller URLs for the bundled logs and cache Services.

Set `sparkwing-runner-bundle.runner.triggerRunner.kind=warm` and
`sparkwing-runner-bundle.runner.automountServiceAccountToken=true` to offer
unlabeled nodes to outbound-only remote agents before using Kubernetes Jobs
for overflow. This mode reuses the bundled runner image, namespace, service
account, pull policy, and cache. It grants namespace-scoped Job lifecycle and
pod-read access to the runner Role. The default `inprocess` mode keeps the
existing behavior and renders an empty Role.
The fallback Job runs `sparkwing-runner run-node`, the executable the runner
image installs, and that process receives the runner token in its environment,
so use warm mode only for trusted pipeline code and rotate short-lived tokens.
The compiled pipeline binary interprets `warm`, so upgrade the controller,
runner, and pipeline module to the same Sparkwing release before enabling it.

## Auth

API clients authenticate with **bearer tokens the controller mints**;
each token carries scopes. Per decision 0001, SSO and advanced RBAC
are explicitly *not* paid gates -- they may land in OSS later. For now:

1. Give the controller its first admin token with
   `controller.bootstrapAdminToken.name` (see Pre-install above). It stores
   that token before the listener binds and starts with `--require-auth`
   (`controller.requireAuth`, on by default), so it never serves a request
   unauthenticated. Without that Secret the render refuses, because a
   controller whose tokens table is empty serves **every endpoint
   unauthenticated**, token minting and first-admin creation included, to
   anything that reaches its Service.

   `controller.allowOpenBootstrap=true` opts into that open window instead.
   The chart then drops `--require-auth`; mint the first token through the
   window, then restart to turn auth on:

   ```bash
   kubectl -n sparkwing port-forward deploy/sparkwing-controller 9001:80 &
   sparkwing cluster tokens create --profile <profile-pointing-at-localhost:9001> \
       --type user --principal admin --scope admin
   kubectl -n sparkwing rollout restart deploy/sparkwing-controller
   ```

   Auth only takes effect on that restart -- the tokens table is read
   once at startup. `allowOpenBootstrap` must be a bool; a quoted string
   fails the render.

2. Mint the runner's token, stash it in a Secret (see Pre-install above),
   and reference it from `sparkwing-runner-bundle.controller.tokenSecret.name`.

   A configured Secret name requires a non-empty key; the chart rejects
   incomplete pairs. Runner and cache Secret references are required, so
   Kubernetes holds those pods until the configured Secret is present.
   `sparkwing-runner-bundle.controller.tokenSecret` is also the logs
   service's signal to resolve callers against the controller.
   `sparkwing-runner-bundle.cache.tokenSecret` is what the cache reads as
   `SPARKWING_API_TOKEN` and the controller as `SPARKWING_CACHE_TOKEN`, and
   `sparkwing-runner-bundle.cache.grantKeySecret` what both read as
   `SPARKWING_CACHE_GRANT_KEY`. A cache-enabled install without the cache
   token fails at render time unless
   `sparkwing-runner-bundle.cache.allowUnauthenticated=true`, and a
   logs-enabled one without the runner token unless
   `sparkwing-runner-bundle.logs.allowUnauthenticated=true`. The bootstrap
   window above needs both and the token upgrade should turn both back off.

   The dashboard's log panes read the logs service with the signed-in
   browser's own credential, so the dashboard needs no token Secret.

3. The controller serves the dashboard and gates its pages behind `/login`
   whenever it enforces tokens. Create dashboard accounts with
   `sparkwing cluster users add`, whose `--scope` bounds what a signed-in
   account reaches. During the `allowOpenBootstrap` window the controller
   serves pages without sign-in, and `/login` offers a "create first admin"
   form. Session cookies are `Secure` and `__Host-` prefixed, so configure
   an HTTPS ingress before signing in; `ingress.allowInsecure=true` without
   TLS passes `--insecure-cookies`, which drops both so a plain HTTP session
   holds.

## Storage

The controller's state DB lives on an RWO PVC. PVC is annotated
`helm.sh/resource-policy: keep` by default so `helm uninstall`
doesn't wipe run history. Disable with
`controller.storage.pvc.keepOnUninstall=false`, or
`controller.storage.type=emptyDir` for a fully ephemeral test install.

By default, the controller runs a short ownership init container before
the non-root application starts. The init container runs as UID 0 with a
read-only root filesystem, no privilege escalation, and only the `CHOWN`
capability; it assigns the mounted Sparkwing home root to
`podSecurityContext.runAsUser:podSecurityContext.fsGroup`. The application
container remains non-root with all capabilities dropped. Set
`volumePermissions.enabled=false` only when the storage driver provisions the
mounted root with that ownership already. The enabled path requires a controller
image containing `/bin/chown`; Sparkwing's release-shaped Alpine images
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

Disabled by default -- many self-host operators front the controller
with their own ingress controller / Gateway / cloud LB. Set
`ingress.enabled=true` to let this chart manage one. Every host in
`ingress.hosts` routes to the `<release>-controller` Service on port 80,
which serves the API, webhooks (including the GitHub App's
`POST /webhooks/github-app`), and dashboard on one port. A console host
and an API host can both point at that Service; a reverse proxy in front
can still split them. The App's settings come from
`SPARKWING_GITHUB_APP_*` variables set through `controller.extraEnv`; see
the GitHub App guide.

## Sub-chart dependency

`charts/sparkwing-full/Chart.yaml` declares
`sparkwing-runner-bundle` as a sibling-directory dependency:

```yaml
dependencies:
  - name: sparkwing-runner-bundle
    version: "0.1.10"
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

The vendored `sparkwing-runner-bundle-<version>.tgz` is committed. Helm
refuses to lint, template, or install a chart whose declared dependency is
missing from `charts/`, and nothing in the build packages the chart, so
dropping the tarball would make every documented command in this README fail
in a fresh clone until the reader ran `helm dependency build` first. The price
is that two branches that both re-vendor conflict on a binary file;
`TestVendoredRunnerBundleMatchesItsSource` in `charts/vendor_test.go` compares
the vendored `values.yaml` and `templates/` against the source chart, and
`bash bin/regen-all.sh` re-vendors only when that comparison fails.

## Image registry

Fallback image references rendered when a component tag is empty:

- `ghcr.io/sparkwing-dev/sparkwing-controller:<chart appVersion>`
- (sub-chart) `ghcr.io/sparkwing-dev/sparkwing-runner:<...>`,
  `sparkwing-cache`, `sparkwing-logs`

These fallbacks describe the chart's intended registry layout; they are not a
compatible public release contract for the current chart version. Pin both
repository and tag for every enabled image. Keep all four images on the same
compatible Sparkwing revision until a corrected chart release publishes and
verifies a public default set.

## Upgrade

```bash
helm dep up ./charts/sparkwing-full
helm upgrade sparkwing ./charts/sparkwing-full \
    --namespace sparkwing -f my-values.yaml
```

Chart 0.2.0 defaults `controller.requireAuth` to `true`, and an upgrade
from an earlier chart without `controller.bootstrapAdminToken.name` fails
to render. Set `controller.bootstrapAdminToken.name`, which the controller
ignores once a live token exists and which keeps `--require-auth` on, or
`controller.allowOpenBootstrap=true`, which keeps a controller that already
holds a token authenticated but reopens the window if its tokens table is
ever emptied.

The controller uses `strategy: Recreate` (RWO PVC -- can't
multi-attach), so expect a brief downtime per upgrade, dashboard
included. Runner pods rolling-update one at a time.

An upgrade from a chart that deployed `sparkwing-web` removes its
Deployment and Service; the Ingress then routes to the controller
Service. A values file that still sets `web` fails to render: move
`web.logs.url` to `controller.logs.url` and drop the rest. See the
migration guide section "The controller serves the dashboard".

State-DB compatibility: the controller's SQLite schema migrates
forward automatically on startup. There is no rollback story for
schema migrations -- if you need to downgrade across a schema
change, restore from a backup of `/data` taken before the upgrade.

## Uninstall

```bash
helm uninstall sparkwing --namespace sparkwing
```

PVCs survive (see Storage). Secrets you pre-created
(`sparkwing-secrets-key`, `sparkwing-token`)
also survive -- the chart references them but doesn't own them.
Delete manually if you want a fully clean slate.

## Troubleshooting

**Controller pod stuck Pending**
PVC binding failed. Check `kubectl describe pvc <release>-controller`.
Most common: no default StorageClass. Set
`controller.storage.pvc.storageClassName` explicitly.

**Dashboard pages answer 503**
The controller image carries no dashboard bundle, which happens with a
source build that skipped `bin/build-web.sh`. The API still works; deploy
an image built after that step. Confirm the controller Service resolves:

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
A controller that enforces tokens offers no sign-up form. Add an
account with `sparkwing cluster users add` using an admin token, such
as the bootstrap admin token.

**`helm template` fails with "missing in charts/ directory: sparkwing-runner-bundle"**
The vendored sub-chart tarball was deleted. Restore it with
`git checkout charts/sparkwing-full/charts`, or rebuild it with
`helm dep up ./charts/sparkwing-full`.

## Source

- Chart: `charts/sparkwing-full/` in
  [`sparkwing-dev/sparkwing`](https://github.com/sparkwing-dev/sparkwing).
- Decision: 0001 -- open-core tier strategy.
- Sibling chart: `charts/sparkwing-runner-bundle/`.
