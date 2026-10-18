# sparkwing-full

This chart runs the Sparkwing Cloud controller, dashboard and Cloud launcher.
One controller replica is supported with PostgreSQL or SQLite. The launch offering
is Local and Cloud, with self-hosted runners registering to Cloud. Running your
own controller is outside the launch offering.

## Cloud on EKS

[examples/cloud.yaml](examples/cloud.yaml) is a secret-free input for ArgoCD on
`kikd-prod`. It renders the controller, Service, nginx Ingress for
`api.sparkwing.dev` and `console.sparkwing.dev`, launcher Deployment, launcher and
customer ServiceAccounts, RBAC and Job egress policies. It uses
the existing `sparkwing-database` Secret's `dsn` key for RDS PostgreSQL.

Replace the image coordinates, example digests, AWS role ARN, bucket/domain,
public OAuth/App IDs, CloudFront public key ID and operator account ID before
syncing. Every enabled image requires **both repository and tag**. `appVersion`
is metadata. The launcher also requires SHA-256 digests for its Job runner and
DinD images. The example's repeated `a`/`b` digests are render fixtures, not
published artifacts. Build the controller, launcher and Job runner from the same
revision. The launcher uses the runner image and invokes `sparkwing-runner launch`.

```sh
helm template sparkwing ./charts/sparkwing-full --namespace sparkwing \
  -f ./charts/sparkwing-full/examples/cloud.yaml
helm upgrade --install sparkwing ./charts/sparkwing-full --namespace sparkwing \
  -f ./charts/sparkwing-full/examples/cloud.yaml
```

The source chart includes its runner-bundle dependency. After editing that bundle,
run `helm dependency update --skip-refresh ./charts/sparkwing-full` and commit the
new tarball and lockfile. No Helm repository is required. ArgoCD can use this
source chart path and the values file directly. Provision ExternalSecrets before
the Deployments consume them, using sync waves or separate applications.

## Credentials and bootstrap

Secrets are mounted as files under `/etc/sparkwing/credentials`. They never become
controller or launcher arguments or environment values. Create them in the release
namespace. [examples/external-secrets.yaml](examples/external-secrets.yaml) maps
SSM parameters through an existing `aws-ssm` ClusterSecretStore. Change its store
name and parameter paths to match the cluster. Its API version must be served by
the installed External Secrets CRDs.

| Value | Existing Secret | Secret key | Mounted file |
| --- | --- | --- | --- |
| `controller.databaseSecret` | `sparkwing-database` | `dsn` | `pg-url` |
| `controller.bootstrapAdminToken` | `sparkwing-bootstrap-admin` | `token` | `bootstrap-admin-token` |
| `controller.secretsKey` | `sparkwing-secrets-key` | `key` | `secrets-key` |
| `controller.credentialsSecret` | `sparkwing-credentials` | file-named keys below | each key keeps its name |
| `sparkwing-runner-bundle.launcher.tokenSecret` | `sparkwing-launcher-token` | `token` | `agent-token` |

Generate the bootstrap seed and encryption key outside Helm, with private files:

```sh
(umask 077 && printf 'swk_%s' "$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')" > /tmp/sparkwing-bootstrap-admin)
(umask 077 && openssl rand -base64 32 > /tmp/sparkwing-secrets-key)
kubectl -n sparkwing create secret generic sparkwing-bootstrap-admin \
  --from-file=token=/tmp/sparkwing-bootstrap-admin
kubectl -n sparkwing create secret generic sparkwing-secrets-key \
  --from-file=key=/tmp/sparkwing-secrets-key
```

`--require-auth` seeds the gated bootstrap administrator before listening. Use the
seed to provision the first real deployment administrator through operator
credential management. That transaction revokes the seed and its descendants.
Create the launcher service credential under the real administrator, with
`claims.launch`, and store its one-time output in `sparkwing-launcher-token`:

```sh
sparkwing operator tokens create --type service --principal launcher \
  --scope claims.launch --profile prod
```

Keep token output out of values files and Git. The launcher waits for its Secret
until it exists. A two-stage first install can set
`sparkwing-runner-bundle.launcher.enabled=false`, provision the service credential,
then enable the launcher. Restarts use the persisted administrator in RDS.

`credentialsSecret` accepts these file-named keys:

| Key | Purpose |
| --- | --- |
| `license` | Enables the multi-team Cloud deployment |
| `github-client-secret` | GitHub OAuth/App client secret |
| `google-client-secret` | Google OAuth client secret |
| `github-app-key` | GitHub App private key in PEM format |
| `github-app-webhook-secret` | GitHub App delivery HMAC secret |
| `github-app-webhook-secret.previous` | Previous delivery HMAC secret during rotation |
| `cloudfront-key` | CloudFront signing private key in PEM format |
| `oidc-key` | OIDC signing private key |
| `oidc-key.published` | Previous OIDC public key during rotation |
| `billing-token` | External billing service credential, when configured |
| `stripe-api-key`, `stripe-webhook-secret` | Stripe credentials with `controller.extraArgs: [--payment-mode=test]` or `live` |
| `pg-url`, `bootstrap-admin-token`, `secrets-key`, `secrets-key.previous` | Optional bundled alternatives to individual Secret references |

Use each fixed file name once. Leave bundled database/bootstrap/encryption keys
out when separate references supply them. A PostgreSQL Cloud controller needs the
current encryption key at startup. Back it up with the database. For key rotation,
mount the old key through `controller.secretsPreviousKey`, set the new
`controller.secretsKey`, re-encrypt with `sparkwing secrets rotate`, then remove
the previous key. Do not replace an existing key without retaining it for reads.
Helm cannot inspect a Secret's keys. The controller validates their contents.

## S3, CloudFront and sign-in

`controller.dataStore.s3URL` configures the controller-owned `/data` object
backend through `--cache-blob-store`. Set `controller.bucketCeiling.store` to the
same bucket and prefix for multi-team storage accounting. The example uses
`s3://sparkwing-cloud-data/controller`. Every blob, artifact, source bundle,
upload and log key stays under `controller/`; record metadata lives in RDS.
The example uses `controller.storage.type=emptyDir` for the process home at
`/data`, because RDS and S3 hold durable state.
`controller.storage.type=pvc` remains available for filesystem/SQLite installs.

The controller ServiceAccount's IRSA role needs S3 read, write, delete and list
access under `controller/`. Scope object permissions to
`arn:aws:s3:::sparkwing-cloud-data/controller/*` and `s3:ListBucket` to
`controller/*` through its `s3:prefix` condition. Allow
`s3:GetLifecycleConfiguration` on the bucket and configure lifecycle expiry
rules for `controller/logs/r30/`, `controller/logs/r90/` and every other retention
class in use. Set `AWS_REGION` through `controller.extraEnv`. Configure
CloudFront's origin access to that prefix with an empty origin path, because
signed download paths already include `controller/`. Add its public key to a
trusted key group, set `controller.cloudfront.domain` and `keyPairID`, and mount
the matching `cloudfront-key`. Helm creates neither AWS resources nor TLS certificates.

Set `externalURL` to the API origin and `dashboardURL` to the console origin.
List allowed console callback URLs in `oauthRedirectURIs`. Set GitHub/Google
client IDs under `controller.github`/`google` and their secrets in
`credentialsSecret`. OAuth providers require the multi-team license. Set the
GitHub App webhook URL to **https://api.sparkwing.dev/webhooks/github-app**.
Use the App's numeric ID and slug in `controller.github.appID` and `appSlug`.
`operatorAccounts` is a comma-separated list of account IDs allowed to use the
operator console. The nginx Ingress sends both hosts to the controller Service.
The example references an existing `sparkwing-tls` certificate Secret.

## Cloud node and network prerequisites

The launcher runs on infrastructure nodes with its own Kubernetes identity. Job
code runs as root with a privileged DinD sidecar under `sparkwing-customer-job`,
which has no API token or cloud annotations. Do not attach an IRSA role or create
a Pod Identity association for this account. Customer Pods with AWS credential
injections are rejected. Each Job and its boundary probes
carry `karpenter.sh/do-not-disrupt: "true"`. Jobs tolerate
`sparkwing.dev/cpu-band:NoSchedule`, including the existing `small` band, and keep
the fixed aggregate 20 GiB scratch budget including DinD.

The existing Karpenter NodePool `sparkwing-jobs-small` needs all of the following:

- Each team's nodes have `node-restriction.kubernetes.io/sparkwing-team-node` equal to the `node_label_value` from
  `sparkwing operator teams show --team <team> -o json`:
  `sha256-` plus the first 40 lowercase hex characters of SHA-256 of the team slug
  (the `--team` value, for example `qa-team`).
  A presence-only label or a shared `small` value cannot establish team ownership.
- Set that exact value in the team's NodePool template metadata labels and in a
  `node-restriction.kubernetes.io/sparkwing-team-node=<hash>:NoSchedule` taint. Retain the existing
  `sparkwing.dev/cpu-band=small:NoSchedule` taint. Start from
  [the team NodePool example](examples/team-nodepool.yaml). Provision one
  isolated node per concurrent Job. Enable Node authorizer and NodeRestriction.
  Reservation checks the node's owning NodeClaim, its node name/provider identity,
  and its NodePool UID and template label. A protected first-use label permanently
  binds the node to one team. Clearing a reservation retains that label.
  A rejected Pod cordons the node and deletes its exact NodeClaim. With no vacant
  Ready team node, the launcher holds the award and creates an unbound
  `sw-capacity-*` Pod sized to the award, with the team node selector and the job
  tolerations and no credentials, so Karpenter adds a node from the team's pool.
  The request stays on the node until the award reserves it, and is deleted when
  the award ends. Set the team NodePool's `disruption` to
  `consolidationPolicy: WhenEmpty` with `consolidateAfter: 10m`; it must exceed
  the 3-minute award lease, the longest a node can sit empty after a launcher
  restart before its unit is re-awarded. Cap cost with the NodePool `limits`.
- Nodes need the awarded CPU/memory plus system overhead and at least 20 GiB of
  allocatable ephemeral storage. The example's ceilings are 2 CPU and 8 GiB.
- The cluster must support native sidecars and `PodLevelResources`.
- Use a namespace permitting privileged customer Jobs and boundary probes. Grant
  Pod and Job creation only to the launcher identity. Pod verification allows
  the named EKS injections and rejects other mutations. See
  [runner-bundle](../sparkwing-runner-bundle/README.md).
- Enforce default-deny egress from both Pod and escaped node contexts. Allow DNS,
  the API origin, S3/CloudFront, GitHub source/releases and required dependency
  registries. Block metadata, Kubernetes API, internal networks and DaemonSet
  ports. The example allows public HTTPS and cluster DNS at the Pod layer.
  Supply node/firewall enforcement and adapt destinations for your network.

The Cloud example enables `volumePermissions` so the controller owns `/data`
and can set its private mode to `0700`. `fsGroup: 65534` grants group access but
cannot authorize that mode change on a root-owned emptyDir. The example also
enables `trustedProxyListener` for the `ingress-nginx` namespace, because a
VPC-wide pod CIDR would let any pod in the cluster claim a client address.

Infrastructure Pods carry component `app` labels. Customer Pods carry
`app: sparkwing-job`. Baseline allow-all ingress/egress policies must exclude
customer Pods and the fenced infrastructure components. On kikd, extend the
baseline exclusions to `sparkwing-job` before enabling the launcher. Chart RBAC
cannot remove privileges granted by other bindings.

The chart's policies select customer Jobs and enforcement probes. The controller
and launcher keep their infrastructure networking. Standard NetworkPolicy alone
does not constrain privileged code in the node network namespace. The launcher
runs actual probes before publishing each Job and refuses incomplete evidence.
Rendering this chart proves neither EKS feature availability nor network isolation.

## Self-hosted runner pools

Enable `sparkwing-runner-bundle.runner.enabled`, pin its image repository and tag,
set `runner.repos` to its authorized repository patterns, and reference the
registered machine credential through `controller.tokenSecret` in the bundle.
`runner.alsoClaimTriggers` controls trigger claims. `runner.maxConcurrent` limits
concurrent units. `runner.nodeSelector`, `tolerations` and `affinity` schedule pool
pods. Cloud Job team ownership comes from the launcher, not pool runner values.

## Trusted proxies

Set `controller.trustedProxyCIDRs` to a list of the ingress proxy networks,
for example `["10.0.0.0/8", "2001:db8:ffff::/48"]`. The chart passes
`--trusted-proxy-cidrs` to the controller. Its empty default ignores
`X-Forwarded-For`. Request logs and per-IP limits use the first untrusted
address when walking the forwarded chain from the trusted TCP peer toward
the client. Configure the ingress to append its observed peer or replace the
header with a verified chain.

`controller.trustedProxyListener.enabled` adds a second controller port that
reads the client address from `X-Real-IP` (`--trusted-proxy-addr`). The Ingress
routes to it, and a chart NetworkPolicy admits only pods matching
`trustedProxyListener.namespaceSelector` and optional `podSelector` to that
port. Rendering refuses an empty `namespaceSelector`. Baseline policies that
allow all ingress to the controller must exclude it, or they reopen the port.

Projected credential symlinks are accepted only when their resolved targets
stay inside `/etc/sparkwing/credentials`. Targets must be regular files owned
by the process user or root and have no group or other write bit.
