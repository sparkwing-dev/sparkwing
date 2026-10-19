# Self-hosted runners and Cloud operations

The launch offering is Local and Cloud. Self-hosted runners register to Cloud.
Operating your own shared controller is outside the launch offering. The
controller deployment material below also describes Cloud operator setup.

## Direct local execution

Run a pipeline on the machine where you invoke Sparkwing:

```bash
sparkwing run build
sparkwing dashboard start
sparkwing dashboard open
```

The local controller starts on demand and owns its home and state store. The
CLI hosts the run. Profiles select controller connections. Publication and
remote-cache reads are separate opt-ins. See [Local execution](local-execution.md)
for the execution and lifecycle integration boundaries.

## Cloud controller and dashboard

The [`sparkwing-full` Helm chart](../charts/sparkwing-full/README.md) is the
repository's Kubernetes deployment surface. It requires an explicitly compatible
image set. The final deployment uses one controller, authorized object storage
and runners or a launcher. Cloud Jobs require exclusive team nodes, Docker and
enforced network isolation. Chart rendering alone does not prove those properties.

The opt-in `k8s-e2e` pipeline requires an explicit cluster and image set. It does
not create or delete a cluster. Native deployment parity remains a launch check.

### First shared deployment

This is Cloud operator setup. Controller bootstrap credentials grant deployment
operations. Team membership and runner enrollment require a team credential.
Create a team and its first administrator through the audited operator command:

```bash
sparkwing operator teams create --team support-test --display-name Support --profile deployment -o json
```

The deployment profile needs `operator.tokens`. The command returns the first
team credential once. See [Deployment bootstrap](auth.md#deployment-bootstrap)
and the [controller chart](../charts/sparkwing-full/README.md) for credential files.

Team members save their credential with `profiles add --token-stdin`. The command
checks health and authenticates through `/api/v1/auth/whoami`. A deployment
credential can pass the probe while remaining unable to call team routes.

When isolating the CLI with `SPARKWING_HOME`, set `SPARKWING_CONFIG` to a private
config path before saving a profile. The runtime home does not relocate the
machine config.

### Node log retention

The log storage contract assigns a retention class before signing segment
uploads. Object-store lifecycle rules must cover each class before URLs are
issued. The shared free and funded classes are thirty and ninety days.
Filesystem storage uses its backend's retention mechanism.

Log reads reauthorize at the controller and cover tagged job-attempt and
trigger, plan or eval subjects. Log expiry is independent of newest-successful
run and output pins. Verify retention through persisted reads after completion.
Native signing, lifecycle verification and final completeness still need
integration evidence. See [log completeness](observability.md#log-completeness).

### Overlapping controllers during an upgrade

One controller replica is the supported topology. SQLite has one controller
owner. PostgreSQL coordination uses persistent claim fences and singleton leases
for maintenance and recovery. Replica-safe storage alone does not establish
cross-replica live-log fan-out or coordinated request limiting. Validate upgrades
against the deployment's native acceptance driver.

### Controller credentials

`sparkwing-controller` reads every secret from one directory, named with
`--credentials-dir`, holding one file per credential under a fixed name. A file
that is absent leaves the feature it guards off, and a file that is present
but empty or only whitespace stops startup, naming the file; there is no
environment or flag form of any of them. The chart projects its Secret values into this
directory, and `controller.credentialsSecret` names a Secret whose keys are
these file names, mounted whole.

| File | Holds | Without it |
|---|---|---|
| `pg-url` | PostgreSQL connection string for the state store | SQLite at `$SPARKWING_HOME/state.db` |
| `secrets-key` | Key stored secrets are sealed under, 32 raw bytes or their base64 | see [Security](security.md#secrets-at-rest) |
| `secrets-key.previous` | Key values were sealed under before `secrets-key`, during a rotation | one key |
| `bootstrap-admin-token` | First admin token, stored before the listener binds | no token is provisioned |
| `license` | Signed multi-team license | one team |
| `oidc-key`, `oidc-key.published` | OIDC signing key and the extra key the key set publishes | no ID tokens; see [OIDC](oidc.md) |
| `google-client-secret`, `github-client-secret` | OAuth client secrets for sign-in | no sign-in with that provider |
| `github-app-key`, `github-app-webhook-secret` | GitHub App private key and webhook secret | no GitHub App |
| `github-app-webhook-secret.previous` | Previous webhook secret accepted during a rotation | only the current secret verifies deliveries |
| `cloudfront-key` | CloudFront signing key | no signed CloudFront downloads |
| `billing-token` | Bearer for the hosted checkout service | `--billing-url` refuses to start |
| `stripe-api-key` | Stripe API credential matching `--payment-mode test` or `live` | a configured payment callback refuses to start |
| `stripe-webhook-secret` | Signature key for `/webhooks/stripe` | a configured payment callback refuses to start |

Credential targets must be regular files owned by the process user or root,
with no group or other write bit. Symlinks are accepted when their resolved
target stays inside the configured credentials directory, including Kubernetes
`..data` projections. Escaping links and writable targets stop startup. On
Windows, targets must belong to the process user, LocalSystem or local
administrators, and their ACL must grant no write access to other principals.
Use `0700` directories and `0600` files for private local storage. Kubernetes
projected volumes can use `defaultMode: 0400`.

### Client addresses behind proxies

Set `--trusted-proxy-cidrs=10.0.0.0/8,2001:db8:ffff::/48` to the networks of
proxies that connect to the controller. The full chart takes the same networks
as a list in `controller.trustedProxyCIDRs`. The empty default ignores
`X-Forwarded-For`.

The controller accepts that header only when its TCP peer belongs to a configured
network. It walks the chain from right to left across trusted proxies and uses
the first untrusted address for request logs and per-IP limits. Proxies must
append the address they observed or replace the header with their verified
chain. Trust only proxy networks because every address in those networks can
assert a forwarded client. Malformed hops in the trusted portion fall back to the TCP peer.

On Kubernetes, prefer a dedicated listener over a CIDR when ingress pods share
an address range with other pods, as they do on a VPC-wide pod network. Set
`controller.trustedProxyListener.enabled` and a
`controller.trustedProxyListener.namespaceSelector` that names the ingress
controller's namespace. The chart passes `--trusted-proxy-addr`, routes the
Ingress to that port and adds a NetworkPolicy that admits only the selected
ingress pods to it. Requests on that port key per-IP limits and request logs on
`X-Real-IP`, so the ingress must overwrite that header with the peer it observed.
Another NetworkPolicy that allows all ingress to the controller defeats the fence.
The fence holds only on a CNI that enforces NetworkPolicy, and it cannot
separate an ingress controller that runs with `hostNetwork`, because its
traffic arrives from the node address rather than a pod in the selected namespace.

### Outbound source clones

When the controller or a shared runner clones a tenant's repository, it
resolves the host once, refuses any address inside the local network and makes
git connect to the address it checked. HTTPS pins the address with
`http.curloptResolve`; ssh pins it with `HostName` and verifies the host key
under the repository's name. An operator `HTTPS_PROXY` or `ALL_PROXY` in the
controller's environment bypasses the pin, because the proxy resolves the name
itself; restrict such a proxy's destinations at the proxy. Clones the owning
user runs locally keep their ssh configuration, agent and `Host` aliases.

### Signed downloads through CloudFront

For object-store downloads through a public ingress, configure the controller
with a CloudFront distribution domain, trusted key pair ID, and private key.
The selected storage backend verifies the object before the controller signs
a download. Public ingress and in-cluster requests use that same authorization
boundary. Native backend and ingress configuration still need integration proof.
Pass `--cloudfront-domain` and `--cloudfront-key-pair-id`, then put the
signing key in the `cloudfront-key`
[credential](#controller-credentials). Keep it out of Helm values, command
arguments, and logs. The distribution must serve the private data bucket through its origin
access control configuration.

The controller returns a 60-second CloudFront URL for requests arriving
through the public ingress. Requests through the in-cluster Service receive a
regional S3 URL instead, so runner pods in the bucket's region can use the S3
gateway endpoint. Without an S3 bucket, the standalone controller opens filesystem storage in
its home directory's `data` directory and mounts signed data and log transfers.
It uses `--external-url` when set, otherwise its listener address. Wildcard
listener addresses use localhost in signed URLs. Set `--external-url` for remote
runners. Filesystem storage needs no CloudFront signing keys.
Controller embedders configure authenticated data routes through
`WithDataController`. Host storage bindings need separate wiring. See
[Data downloads](api.md#data-downloads) for route behavior.
### Storage class

When you deploy sparkwing in-cluster (Helm chart at `charts/sparkwing-full`),
the controller provisions a PersistentVolumeClaim for its state DB. A PVC
that omits `storageClassName` falls back to the cluster's default
StorageClass; on clusters without one (some bare-metal kubeadm installs,
fresh kind clusters with the local-path provisioner not installed, etc.)
the PVC sits `Pending` indefinitely with no clear error.

Start from the [Cloud values example](../charts/sparkwing-full/examples/cloud.yaml)
and prepare `my-values.yaml` with compatible image repositories/tags and existing
credential Secret references. For a PVC-backed process home, select its class:


```bash
helm install sparkwing ./charts/sparkwing-full -f my-values.yaml \
    --set controller.storage.type=pvc \
    --set controller.storage.pvc.storageClassName=gp3
```

Common values: `gp3` (EKS), `standard-rwo` (GKE), `managed-csi` (AKS),
`standard` (kind/minikube with the default local-path provisioner).

The controller logs a `WARNING` at startup when no PVC declares a class
and the cluster has no default StorageClass.

### PostgreSQL state

Sparkwing Cloud runs its controller state on PostgreSQL. Self-hosted installs
keep SQLite by default. To use PostgreSQL, create a Secret whose value is a DSN
and configure the chart to read it:

```bash
kubectl -n sparkwing create secret generic sparkwing-database \
    --from-file=dsn=/private/path/database-dsn

helm upgrade --install sparkwing ./charts/sparkwing-full -f my-values.yaml \
    --set controller.databaseSecret.name=sparkwing-database \
    --set controller.databaseSecret.key=dsn \
    --set controller.storage.type=emptyDir
```

The controller reads the Secret as its `pg-url` credential. Keep the DSN out of Helm
values and command arguments. Initialize and verify the PostgreSQL data before
starting the controller against it.

Filesystem node outputs use signed URLs on the controller. Behind a proxy, set
the controller's `--external-url` to the address runners can reach. Without that
setting, the URLs use the controller request's address. Uploads and downloads
use the signed URL's authority and carry no runner token.

Working-tree source bundles use the source-bundle authority and upload contract.
Native intake and bundle ownership remain integration requirements. The bundle
can seed acquisition, which still verifies the sealed commit.

### Backups and upgrades

[Backup, restore, and upgrade](backup-restore.md) covers both database
shapes: what a restore needs beside the database, how to prove a backup
is good before relying on it, and what rollback means at each stage of a
controller upgrade.

## Add workstation capacity

Enrollment uses a named controller connection and a runner identity:

```bash
sparkwing runners add --profile prod --runner dev-laptop
```

To offer your own machine only to your runs, enroll it as personal capacity:

```bash
sparkwing runners add --profile prod --runner my-desktop --personal
```

Personal enrollment requires an independent account session. A member-token
profile alone cannot authorize it. The controller binds personal capacity to your signed-in account and checks that
ownership when matching runs. Cloud-routed runs can prefer your registered
machines before using Cloud pools. Omitting `--personal` enrolls a team runner.
Repository filters, capacity settings and service installation work the same way
for both. JSON enrollment output includes `personal` alongside the runner name.

The CLI checks local prerequisites before minting a runner credential. It writes
that credential to a private file under `runner.credentials_dir`, then updates
the `runner` section of the complete machine file. Labels, repository patterns
and capacity observations grant no authority beyond the controller's enrollment.

`--label` and `--repos` accept repeated values. `--no-service` writes the
configuration for a machine supervised separately. `--force` permits replacing
an enrollment. A configuration or service-install failure revokes the newly
minted credential and removes only newly created files. Failed replacement
restores the previous section without overwriting concurrent changes. Output
contains metadata, never the bearer.

Retire an enrollment with:

```bash
sparkwing runners remove --profile prod --runner dev-laptop
```

Removal validates its controller and credential kind before stopping the service
and revoking authority. A failed or unconfirmed revocation is an error.

GitHub Releases also contain `sparkwing-runner` binaries for amd64 and arm64
on macOS, Linux, and Windows (Windows asset names end in `.exe`). Before
registering or restarting a downloaded runner, inspect its embedded identity
without contacting the network:

```bash
sparkwing-runner version -o json --offline
```

The record names the runner binary, release version, target platform, and any
VCS provenance embedded by the Go build. The runner does not yet download or
replace its own executable; update the service binary through the same installer
or service manager you used to install it.

Runner service definitions run `sparkwing-runner start --config PATH` in the
foreground. Linux uses a systemd user service. macOS uses a launchd agent.
The service file names an existing machine configuration and contains no
credential. The duplicate shell enrollment installer has been removed.

Windows enrollment prints manual supervision steps rather than
installing a login service. See [runner reference](cli-runners.md) and
[machine settings](machine-config.md).

### Shared runners and pool trust

A shared runner runs one job after another under one account, so a job can
leave files, caches and processes that a later job meets. Trust decides which
runs may share that state:

- A runner that holds a pool with a secret reserved to it takes only runs from
  a verified push to the repository's default branch. Pull request, feature
  branch, manual and scheduled runs wait for another runner.
- Any other runner takes any run its repository patterns and labels admit.
- `--trust=verified-default` or `runner.trust: verified-default` applies the
  restriction to a runner without reserved secrets. `--trust=any` lifts it from
  a pool with reserved secrets.

A cached pipeline binary runs only as a private copy whose bytes matched the
digest recorded when it was compiled. Jobs on a shared runner run under the
runner's account and can rewrite both the binary and its digest, so the check
catches corruption, not tampering, until jobs run under a separate account. Builds on a host the run's owner does not own set
`GOENV=off`. A run's submitted environment reaches the runner only under names
the runner's own `run.submit_env_allow` lists; see
[machine settings](machine-config.md).

Pipeline code runs as the runner's own account and can read the runner's
credential file. Keep runners that serve untrusted branches apart from pools
that hold deployment secrets.

### Operate the service

On macOS:

```bash
tail -f ~/.sparkwing/runner.log
launchctl list | grep sparkwing
launchctl unload ~/Library/LaunchAgents/com.sparkwing.runner.plist
launchctl load ~/Library/LaunchAgents/com.sparkwing.runner.plist
```

On Linux:

```bash
journalctl --user -u sparkwing-runner -f
systemctl --user status sparkwing-runner
systemctl --user stop sparkwing-runner
systemctl --user start sparkwing-runner
```

Enable lingering with `loginctl enable-linger $USER` if the Linux runner must
remain active after logout.

### Troubleshooting

**The runner returns `401 unauthorized`.** Confirm that `agent-token` under
`runner.credentials_dir` holds a live controller credential. Restart the runner
after replacing that file because it retains the credential read at startup.

**The runner does not claim work.** Confirm that the pipeline was remotely
triggered and that the runner can reach the configured controller and logs
URLs.

**A private repository cannot be cloned.** Check the sealed source location,
live claim, repository permission and source-release audit. A team
credential reaches only shared runners and Cloud work, never a personal machine. See [team git credentials](git-credentials.md).

**A Docker step fails.** Docker is a pipeline dependency, not a Sparkwing
service requirement. Install and start Docker only on machines assigned jobs
that invoke it.

## Object storage

The final storage path uses controller-authorized signed `/data` transfers and
logical records. It has no cache pod, Git mirror proxy or cache-service token.
The storage owner selects filesystem or object storage and verifies bytes before
publication. Local pipeline binaries stay in the machine's binary cache.

Native backend and deployment wiring remains an integration requirement. See
[data uploads](data-uploads.md) and [caching](caching.md).

## Bound storage growth

Run retention is disabled by default on a self-hosted controller. A multi-team
controller defaults to 90 days. An explicit operator value, including zero,
survives restarts. Node metrics have their own retention window.

Read the deployment policy with `GET /api/v1/operator/object-store` and write it with
`PUT /api/v1/operator/object-store/settings` (grant `operator.object_store`):

```bash
curl -sS -X PUT "$CONTROLLER/api/v1/operator/object-store/settings" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"run_retention_days":90,"node_metric_retention_days":14,
       "database_alarm_bytes":8589934592,"default_tier":"free"}'
```

Retention windows use whole days. Zero disables expiration for that window. The hourly
maintenance worker holds a database lease and removes eligible data in bounded
batches. Run retention removes operational children with the run and preserves
each pipeline's newest successful run and outputs. Financial rows and recorded
usage survive run deletion. Metrics expire under their independent window.

`database_alarm_bytes` sets a warning threshold. Passing it logs a warning and
sets `database.alarm` on `GET /api/v1/health`. Sizes and table names appear only
on `GET /api/v1/operator/object-store` with `operator.object_store`.

SQLite samples allocated pages minus its free pages. Removing data can lower
the sample while the file keeps its size until `VACUUM`. PostgreSQL samples
relation sizes, including indexes.

### Per-team storage quotas

Quotas belong to registered teams. Token principal labels do not select a
quota. `default_tier` supplies limits only when a team has no explicit row.
An empty default leaves those teams unlimited. A zero write limit is unbounded.

Source uploads reserve monthly capacity before a run exists. Intake applies
the committed unique-byte charge to the run and its per-run limits.

Blob upload tickets reserve pending capacity against monthly bytes and per-run
bytes and objects. Commit counts each new blob once. An authorized duplicate
adds no bytes or objects, and a new empty blob counts as one object. Aborting or
expiring a ticket releases its pending capacity. A month's boundary resets the
monthly budget while the run's byte and object totals continue across months.
Deployment default-tier changes apply to the next ticket for teams without an
explicit quota.

The free tier configures ten megabytes per run, a gigabyte per month and a
thousand objects per run. The paid tier configures a gigabyte per run, a hundred
gigabytes per month and a hundred thousand objects per run. Set an explicit
team policy with `PUT /api/v1/operator/object-store/quotas/{team}` with deployment `operator.object_store` authority:

```bash
curl -sS -X PUT "$CONTROLLER/api/v1/operator/object-store/quotas/acme" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"tier":"paid"}'
```

Monthly reports sum each team's replica counters. They are historical usage,
so deleting a run does not lower them. `retained_bytes` measures the team's
unique committed blobs and stored log segments. Multiple references to the
same blob count once. Each team accounts for its own copy.

Output, manifest and memo pointers add no quota usage. Their underlying blobs
consume bytes and objects when a verified upload first commits them.

### Storage allowances

`storage_allowance_bytes` caps the retained byte count used for billing. Zero
leaves that count uncapped. Set it independently of write limits with
`PUT /api/v1/operator/object-store/quotas/{team}/allowance` with deployment `operator.object_store` authority:

```bash
curl -sS -X PUT "$CONTROLLER/api/v1/operator/object-store/quotas/acme/allowance" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"storage_allowance_bytes":53687091200}'
```

Quota and allowance writes preserve each other's fields. Creating an allowance
for a team without a quota preserves its effective default-tier limits.
[Retained storage billing](auth.md#retained-storage) describes the rate, free
allowance and billing interval.
