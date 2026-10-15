# Self-hosting Sparkwing

Use the CLI and local controller on one machine, or deploy the controller and
runner stack to Kubernetes. Native controller, source, storage and chart
composition still need acceptance evidence. The sections below identify the
shared contract and the remaining installation boundaries.

## Direct local execution

Run a pipeline on the machine where you invoke Sparkwing:

```bash
sparkwing run build
sparkwing dashboard start
```

The local controller starts on demand and owns its home and state store. The
CLI hosts the run. Profiles select controller connections. Publication and
remote-cache reads are separate opt-ins. See [Local execution](local-execution.md)
for the execution and lifecycle integration boundaries.

## Shared controller and dashboard

The [`sparkwing-full` Helm chart](../charts/sparkwing-full/README.md) is the
repository's Kubernetes deployment surface. It requires an explicitly compatible
image set. The final deployment uses one controller, authorized object storage
and runners or a launcher. Cloud Jobs require exclusive team nodes, Docker and
enforced network isolation. Chart rendering alone does not prove those properties.

The opt-in `k8s-e2e` pipeline requires an explicit cluster and image set. It does
not create or delete a cluster. Native deployment parity remains a launch check.

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
| `cloudfront-key` | CloudFront signing key | no signed CloudFront downloads |
| `billing-token` | Bearer for the hosted checkout service | `--billing-url` refuses to start |
| `stripe-api-key` | Stripe API credential matching `--payment-mode test` or `live` | a configured payment callback refuses to start |
| `stripe-webhook-secret` | Signature key for `/webhooks/stripe` | a configured payment callback refuses to start |

Keep the directory readable by the controller's user alone (`0700`, files
`0600`); a Kubernetes projected volume with `defaultMode: 0400` does this.

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
The self-hosted controller runs without credit metering unless a signed license
grants `metering` or `multi-team`. It bills no runner or storage usage, exposes
no credit or team billing routes, and gives teams unlimited room in the
controller's storage-tier checks. Operator-set storage quotas still apply.
The dashboard hides Billing. Customers who need Sparkwing Cloud or enterprise
metering can contact Korey for help running the operator console and
sparkwing-ops. See
[Credits](auth.md#credits) for the licensed behavior.

### Storage class

When you deploy sparkwing in-cluster (Helm chart at `charts/sparkwing-full`),
the controller provisions a PersistentVolumeClaim for its state DB. A PVC
that omits `storageClassName` falls back to the cluster's default
StorageClass; on clusters without one (some bare-metal kubeadm installs,
fresh kind clusters with the local-path provisioner not installed, etc.)
the PVC sits `Pending` indefinitely with no clear error.

Set the class explicitly via the chart value:

```bash
helm install sparkwing charts/sparkwing-full \
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
    --from-literal=dsn='postgres://sparkwing:<password>@database.example:5432/sparkwing?sslmode=require'

helm upgrade --install sparkwing charts/sparkwing-full \
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

## Migrating from the Docker Compose example

Treat the Helm installation as a new deployment. Sparkwing provides no
in-place conversion or automatic import for the old Compose volumes. Keep
the controller and log data volumes until you decide whether
their history must be retained, then:

1. Deploy `sparkwing-full` with one compatible image set.
2. Recreate controller tokens and update remote profiles with the new URL.
3. Point workstation runners and webhooks at the new controller.
4. Retire the Compose stack only after the new path completes a real pipeline.

Pipelines that do not need shared history can move to direct local execution
instead.

## Add workstation capacity

Enrollment uses a named controller connection and a runner identity:

```bash
sparkwing runners add --profile prod --runner dev-laptop
```

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
live claim, repository permission and source-release audit. A stored team
credential also needs executor consent. See [team git credentials](git-credentials.md).

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
