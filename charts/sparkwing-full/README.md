# sparkwing-full

Helm chart that deploys the **complete OSS Sparkwing stack** into a
single Kubernetes cluster:

- `sparkwing-controller` -- the orchestrator (state DB, /api/v1/*,
  webhooks) and the dashboard with its sign-in pages
- `sparkwing-runner-bundle` (sub-chart) -- runners

This is the chart referenced in architectural decision 0001 for the
**Enterprise self-host** topology. It models the complete stack as one
Helm release. Single-tenant, single-instance: HA features (multi-replica
controller, leader election, replication, zero-downtime upgrades) are
paid tier and live in separate charts.

> **Release blocker:** The checked-in default repositories and
> `appVersion` do not currently identify a compatible public image set. A bare
> install renders the intended topology, but it is not a supported runnable
> release. Until a corrected release is published, build or mirror one
> mutually compatible controller and runner image set and
> explicitly set every enabled component's `image.repository` and `image.tag`.

If you only need a runner pool against a remote controller (Cloud or
external self-host), use the standalone
[`sparkwing-runner-bundle`](../sparkwing-runner-bundle/) chart
instead -- this chart pulls it in as a dependency.

## Render the chart

`helm template` requires a controller bootstrap admin token or
`controller.allowOpenBootstrap=true` (see [Auth](#auth)). The runner's values
live under the sub-chart key. A read-only render is:

```bash
helm template sparkwing ./charts/sparkwing-full \
  --set controller.allowOpenBootstrap=true \
  --set sparkwing-runner-bundle.controller.tokenSecret.name=sparkwing-token
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
        ^
        | claim, node log segments
        |
   +---------+
   |  runner |
   +---------+
            (sparkwing-runner-bundle sub-chart)
```

## Requirements

- Kubernetes 1.27+
- Explicit repositories and tags for a mutually compatible image set. The
  current default GHCR/appVersion combination is not a runnable release.
- A default `StorageClass` (or set `controller.storage.pvc.storageClassName`
  / equivalents on the sub-chart). The controller PVC is RWO.
- `helm` v3.13+ (chart uses standard idioms; nothing exotic).

### Pre-install Secrets

Operators bring their own Secrets so they can rotate without
`helm upgrade`. Create these in the install namespace before
running install:

```bash

# The once-only operator bootstrap credential. Keep this value for
# operator credential management. The umask restricts the file to its owner.
(umask 077 && printf 'swk_%s' "$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')" > "$HOME/sparkwing-bootstrap-admin")
kubectl -n sparkwing create secret generic sparkwing-bootstrap-admin \
    --from-file=token="$HOME/sparkwing-bootstrap-admin"

# At-rest encryption key for the controller's secrets store. Skip it and a
# SQLite controller creates its own key on the first secret write, a
# PostgreSQL controller refuses secret writes, and a multi-team controller
# refuses to start; it never stores a secret as plaintext.
openssl rand -base64 32 > /tmp/sparkwing-key
kubectl -n sparkwing create secret generic sparkwing-secrets-key \
    --from-file=key=/tmp/sparkwing-key

# Machine root credential for the runner bundle's claim loop. Provision
# the machine identity through operator credential management after the
# first helm install, then create its Secret -- see Auth below.
#   kubectl -n sparkwing create secret generic sparkwing-token \
#       --from-literal=token=swk_...
# Provision the machine with nodes.claim and triggers.claim grants.

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
```

```bash
# Vendor the sub-chart into ./charts/ (one-time per chart change).
helm dep up ./charts/sparkwing-full

# Install with the provisioned bootstrap credential and runner Secret.
# Runner pods wait until their machine credential Secret exists.
helm install sparkwing ./charts/sparkwing-full \
    --namespace sparkwing --create-namespace \
    -f compatible-images.yaml \
    --set controller.bootstrapAdminToken.name=sparkwing-bootstrap-admin \
    --set sparkwing-runner-bundle.controller.tokenSecret.name=sparkwing-token
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
and no `controller.bootstrapAdminToken.name`. These guards require TLS and
startup credential provisioning for a published controller. Set
`ingress.allowInsecure=true` to waive those chart guards; it
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
| `controller.credentialsSecret.name` | Secret whose keys are [credential file names](../../docs/self-hosting.md#controller-credentials) (`license`, `oidc-key`, `github-app-key`, and the rest), projected whole into the controller's `--credentials-dir`. Helm cannot read its keys, so with it set the chart skips its bootstrap-token and current-key checks and the controller refuses at startup instead. | `""` |
| `controller.databaseSecret.name` | Secret containing a PostgreSQL DSN, mounted as the `pg-url` credential. Empty keeps SQLite. | `""` |
| `controller.databaseSecret.key` | Key holding the PostgreSQL DSN. | `dsn` |
| `controller.dashboardURL` | Query-free HTTP(S) dashboard base URL for GitHub App check run links; invalid values omit the link. | `""` |
| `controller.secretsKey.name` | Secret holding the 32-byte encryption key, mounted as the `secrets-key` credential. | `""` |
| `controller.secretsPreviousKey.name` | Secret holding the key values were sealed under before `secretsKey`; read-only fallback for the window before `sparkwing secrets rotate` runs. | `""` |
| `controller.bootstrapAdminToken.name` | Secret holding the operator bootstrap credential, accepted once by the provisioning gate before the listener binds. | `""` |
| `controller.requireAuth` | Require a provisioned credential at startup. A fresh install needs `bootstrapAdminToken` or `bootstrap-admin-token` in `credentialsSecret`. | `true` |
| `controller.allowOpenBootstrap` | Render without `bootstrapAdminToken` and omit `--require-auth`. Protected routes still require credentials; provision the administrator out of band before use. Bool only. | `false` |
| `controller.argon2MemoryBudgetMB` | Memory ceiling in MiB for concurrent argon2id hashing; each hash holds 64 MiB. | `256` |
| `controller.externalURL` | Base URL the controller answers on from outside the cluster, passed as `--external-url`. | `""` |
| `controller.oauthRedirectURIs` | Comma-separated sign-in callback URLs, passed as `--oauth-redirect-uris`. | `""` |
| `controller.operatorAccounts` | Comma-separated account ids allowed into the operator console, passed as `--operator-accounts`. | `""` |
| `controller.github.clientID` | GitHub OAuth client id, passed as `--github-client-id`; the secret is the `github-client-secret` credential. | `""` |
| `controller.github.appID` / `appSlug` | The GitHub App, passed as `--github-app-id` and `--github-app-slug`; its key and webhook secret are the `github-app-key` and `github-app-webhook-secret` credentials. | `""` |
| `controller.google.clientID` | Google OAuth client id, passed as `--google-client-id`; the secret is the `google-client-secret` credential. | `""` |
| `controller.email.sender` / `configurationSet` | SES invitation email, passed as `--email-sender` and `--email-configuration-set`. | `""` |
| `controller.cloudfront.domain` / `keyPairID` | CloudFront download signing, passed as `--cloudfront-domain` and `--cloudfront-key-pair-id`; the key is the `cloudfront-key` credential. | `""` |
| `controller.extraArgs` | Further controller flags, verbatim, for settings without a value above (`--object-store-budget`, `--cache-blob-store`, `--billing-url`, ...). Never a secret. | `[]` |

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
| `sparkwing-runner-bundle.runner.replicas` | Pool size. | `1` |
| `sparkwing-runner-bundle.runner.labels` | `Requires` labels. | `[cluster]` |
| `sparkwing-runner-bundle.runner.automountServiceAccountToken` | Mount the runner pod's API token. Ordinary runners need no Kubernetes API access. | `false` |
| `sparkwing-runner-bundle.volumePermissions.enabled` | Run a CHOWN-only init before the runner. | `true` |
| `sparkwing-runner-bundle.runner.goCache.persistence.enabled` | Mount a PVC over the runner's `GOCACHE` and `GOMODCACHE`. | `false` |
| `sparkwing-runner-bundle.runner.goCache.warmModules` | Modules downloaded into `GOMODCACHE` at runner startup. | `[]` |

The automatic controller URL follows the chart's default resource names. If
you set top-level `nameOverride` or `fullnameOverride`, also set
`sparkwing-runner-bundle.controller.url` to the resulting controller Service;
the chart stops at render time with this instruction when the URL is missing.
Nested runner-bundle naming values control the runner resource names.

A Cloud launcher configured through
`sparkwing-runner-bundle.launcher.serviceAccountName` requires a dedicated
namespace with no LimitRanges. Namespace selectors must explicitly exclude
that namespace from mutating Job/Pod CREATE/UPDATE webhooks and all mutating
admission policy bindings. The bundle installs a validating guard that preserves
those exclusions. The launcher verifies the guard with read access and a dry-run
LimitRange CREATE that must be denied by that exact guard. The probe persists
nothing. Cluster administrators must preserve the guard while Jobs are live. The cluster must
serve GA validating admission policy and binding APIs, and the mutating
admission policy binding API. See the
[runner-bundle setup](../sparkwing-runner-bundle/README.md).

## Auth

Protected API routes require current capability credentials. Provision the
operator bootstrap credential with `controller.bootstrapAdminToken.name`
or `bootstrap-admin-token` in `controller.credentialsSecret`. The controller
validates the `swk_` presentation and commits the operator root through its
once-only provisioning gate before the listener binds. Keep
`controller.requireAuth=true` for a fresh deployment.

`controller.allowOpenBootstrap=true` permits rendering without a bootstrap
Secret and omits `--require-auth`. It does not authorize anonymous API access
or create an administrator. Use it only with an administrator provisioned
out of band. It must be a bool; a quoted string fails the render.

Use operator credential management to register and provision the runner's
machine identity with `nodes.claim` and `triggers.claim` grants. Put that
machine root credential in a Secret and reference it from
`sparkwing-runner-bundle.controller.tokenSecret.name`. Kubernetes holds the
runner pod until its configured Secret exists. Work executes with the
awarded unit's claim authority; the machine root is used for claiming.

The dashboard uses the signed-in browser's own authority for log reads and
needs no token Secret. Session cookies are `Secure` and `__Host-` prefixed,
so configure an HTTPS ingress before signing in. Without TLS,
`ingress.allowInsecure=true` passes `--insecure-cookies` to permit plain HTTP
sessions.

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

That second line deletes the controller's state DB and any runner Go cache
PVCs. Keep the PVCs to retain those data.

## Ingress

Disabled by default -- many self-host operators front the controller
with their own ingress controller / Gateway / cloud LB. Set
`ingress.enabled=true` to let this chart manage one. Every host in
`ingress.hosts` routes to the `<release>-controller` Service on port 80,
which serves the API, webhooks (including the GitHub App's
`POST /webhooks/github-app`), and dashboard on one port. A console host
and an API host can both point at that Service; a reverse proxy in front
can still split them. The App's settings come from `controller.github`
and the `github-app-key` and `github-app-webhook-secret` credentials in
`controller.credentialsSecret`; see the GitHub App guide.

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
- (sub-chart) `ghcr.io/sparkwing-dev/sparkwing-runner:<...>`

These fallbacks describe the chart's intended registry layout; they are not a
compatible public release contract for the current chart version. Pin both
repository and tag for every enabled image. Keep both images on the same
compatible Sparkwing revision until a corrected chart release publishes and
verifies a public default set.

## Upgrade

```bash
helm dep up ./charts/sparkwing-full
helm upgrade sparkwing ./charts/sparkwing-full \
    --namespace sparkwing -f my-values.yaml
```

Keep `controller.requireAuth=true` and supply
`controller.bootstrapAdminToken.name` or a credentials Secret carrying
`bootstrap-admin-token`. The operator bootstrap gate consumes that seed only
once. For an already provisioned controller, `controller.allowOpenBootstrap=true`
can omit the startup credential requirement; protected routes retain their
authorization checks.

The controller uses `strategy: Recreate` (RWO PVC -- can't
multi-attach), so expect a brief downtime per upgrade, dashboard
included. Runner pods rolling-update one at a time.

An upgrade from a chart that deployed `sparkwing-web` removes its
Deployment and Service; the Ingress then routes to the controller
Service. A values file that still sets `web` fails to render: drop it.
See the migration guide section "The controller serves the dashboard".

An upgrade from a chart that deployed `sparkwing-logs` removes its
Deployment, Service and ServiceAccount; runners upload node logs to the
controller instead. The logs PVC carries `helm.sh/resource-policy: keep`
by default, so it survives the upgrade; nothing reads it, so delete it. A values file
that still sets `controller.logs` or `sparkwing-runner-bundle.logs` fails
to render. See the migration guide section "The logs service is removed".

State-DB compatibility: the controller creates its schema in an empty
database and refuses one at any other schema version, naming the file to
delete. Upgrading from a release before the store baseline means deleting
`state.db` from `/data`; to downgrade, restore a backup of `/data` taken
before the upgrade.

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
Provision the operator bootstrap credential before startup, then sign in
with that operator credential in the `/login` form's operator-token field.

**`helm template` fails with "missing in charts/ directory: sparkwing-runner-bundle"**
The vendored sub-chart tarball was deleted. Restore it with
`git checkout charts/sparkwing-full/charts`, or rebuild it with
`helm dep up ./charts/sparkwing-full`.

## Source

- Chart: `charts/sparkwing-full/` in
  [`sparkwing-dev/sparkwing`](https://github.com/sparkwing-dev/sparkwing).
- Decision: 0001 -- open-core tier strategy.
- Sibling chart: `charts/sparkwing-runner-bundle/`.
