# sparkwing-full

This chart runs the Sparkwing Cloud controller, dashboard and Cloud launcher.
One controller replica is supported with PostgreSQL or SQLite. The launch offering
is Local and Cloud, with self-hosted runners registering to Cloud. Running your
own controller is outside the launch offering.

## Cloud on EKS

[examples/cloud.yaml](examples/cloud.yaml) is a secret-free input for ArgoCD on
`kikd-prod`. It renders the controller, Service, nginx Ingress for
`api.sparkwing.dev` and `console.sparkwing.dev`, launcher Deployment, launcher and
customer ServiceAccounts, RBAC, admission guard and Job egress policies. It uses
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
same bucket for multi-team storage accounting. This is a bucket URL such as
`s3://sparkwing-cloud-data`. The example uses `controller.storage.type=emptyDir`
for the process home at `/data`, because RDS and S3 hold durable state.
`controller.storage.type=pvc` remains available for filesystem/SQLite installs.

The controller ServiceAccount's IRSA role needs S3 read, write, delete and list
access to the bucket. Set `AWS_REGION` through `controller.extraEnv`. Configure
CloudFront's origin access to that bucket, add its public key to a trusted key
group, set `controller.cloudfront.domain` and `keyPairID`, and mount the matching
`cloudfront-key`. Helm creates neither AWS resources nor TLS certificates.

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
which has no API token or cloud annotations. Each Job and its boundary probes
carry `karpenter.sh/do-not-disrupt: "true"`. Jobs tolerate
`sparkwing.dev/cpu-band:NoSchedule`, including the existing `small` band, and keep
the fixed aggregate 20 GiB scratch budget including DinD.

The existing Karpenter NodePool `sparkwing-jobs-small` needs all of the following:

- Each team's nodes have `sparkwing.dev/team-node` equal to `launcher.TeamNodeValue(team)`:
  `sha256-` plus the first 40 lowercase hex characters of SHA-256 of the team ID.
  A presence-only label or a shared `small` value cannot establish team ownership.
- Set that exact value in the team's NodePool template metadata labels and retain
  `sparkwing.dev/cpu-band=small:NoSchedule` in its template taints. Provision one
  isolated node per concurrent Job. The launcher reserves existing Ready nodes
  and does not create NodePools or request nodes from Karpenter. An empty pool
  that scales only in response to pending Pods must be provisioned externally.
- Nodes need the awarded CPU/memory plus system overhead and at least 20 GiB of
  allocatable ephemeral storage. The example's ceilings are 2 CPU and 8 GiB.
- The cluster must support native sidecars and `PodLevelResources`, and serve
  `admissionregistration.k8s.io/v1` validating policies/bindings plus the mutating
  admission policy binding API used by the launcher's exemption checks.
- Use a namespace permitting privileged customer Jobs and boundary probes. Keep
  it free of LimitRanges. Mutating webhooks touching Job/Pod CREATE or UPDATE,
  and mutating policy bindings, must exclude it with the canonical namespace
  name selector. See [runner-bundle](../sparkwing-runner-bundle/README.md).
- Enforce default-deny egress from both Pod and escaped node contexts. Allow DNS,
  the API origin, S3/CloudFront, GitHub source/releases and required dependency
  registries. Block metadata, Kubernetes API, internal networks and DaemonSet
  ports. The example allows public HTTPS and cluster DNS at the Pod layer.
  Supply node/firewall enforcement and adapt destinations for your network.

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

Projected credential symlinks are accepted only when their resolved targets
stay inside `/etc/sparkwing/credentials`. Targets must be regular files owned
by the process user or root and have no group or other write bit.
