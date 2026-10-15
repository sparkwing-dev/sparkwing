# Self-hosting Sparkwing

For the use cases that the former Compose example covered, choose one of these
supported paths:

- Run pipelines and the dashboard directly on one machine when you do not need
  a shared controller.
- Deploy the complete controller, dashboard, and runner stack to Kubernetes
  with the `sparkwing-full` Helm chart.

The former Docker Compose example was not a released or acceptance-tested
distribution. It depended on private image coordinates and stale controller
settings, so Sparkwing no longer presents it as an install path.

## Direct local execution

Run a pipeline on the machine where you invoke Sparkwing:

```bash
sparkwing run build
sparkwing dashboard start
```

This path needs no controller or Kubernetes cluster. Runs, logs, and cache
metadata stay under `~/.sparkwing/` unless the selected profile routes a
backend elsewhere. See [Local execution](local-execution.md) for profiles,
remote backends, and the distinction between running and triggering a
pipeline.

## Shared controller and dashboard

Use the [`sparkwing-full` Helm chart](../charts/sparkwing-full/README.md) for a
shared controller, dashboard, cache, and Kubernetes runner. The
chart requires Kubernetes and an explicitly compatible image set; its README
lists the required values and install command. The controller starts with
`--require-auth` by default, so the render refuses unless
`controller.bootstrapAdminToken.name` names a Secret holding the first admin
token, or `controller.allowOpenBootstrap=true` accepts a controller that
serves every route unauthenticated until a token exists.

The repository's opt-in `k8s-e2e` pipeline exercises this deployment against
an explicit cluster and caller-supplied images. It does not create or delete a
cluster.

### Node log retention

A controller with `--cache-blob-store` keeps node log segments in that
bucket under one prefix per retention class: `<prefix>/logs/r90/` for a
team with credits and for a single-team install, `<prefix>/logs/r30/` for a
team without credits, and `<prefix>/logs/r<days>/` for each team
`--log-retention team=days` names. The bucket's lifecycle rules expire
them; the controller never writes rules, because a put replaces the
bucket's whole configuration. It reads them on its first log request and
again every 5 minutes while one is missing, and issues no upload URLs into
a class without an enabled rule that expires the class prefix after
exactly its days, naming the rule in its log and refusal; the runner then
keeps that node's log on its own disk.
Grant the controller `s3:GetLifecycleConfiguration` on the bucket and add
the rules, merged with any the bucket already has:

```bash
aws s3api put-bucket-lifecycle-configuration --bucket my-cache-bucket \
  --lifecycle-configuration '{"Rules": [
    {"ID": "sparkwing-logs-r90", "Status": "Enabled",
     "Filter": {"Prefix": "cache/logs/r90/"}, "Expiration": {"Days": 90}},
    {"ID": "sparkwing-logs-r30", "Status": "Enabled",
     "Filter": {"Prefix": "cache/logs/r30/"}, "Expiration": {"Days": 30}}]}'
```

A controller without a cache bucket keeps segments in its output
directory and expires nothing; deleting a run or a team deletes its logs.
A controller with neither a cache bucket nor an output directory keeps no
node logs: its runners keep each log on
their own disk, and a laptop run whose profile keeps its state there fails
at start naming the controller. `sparkwing profiles show --profile NAME`
inspects only the saved connection. Verify log retention by reading a completed
run's logs through the controller.
On a multi-team controller a segment's stored bytes count against the
team's log storage, and the hourly storage pass lists the segments to
correct the count and, for a team without credits past its log share,
deletes its least recently written finished runs' logs.

### Overlapping controllers during an upgrade

Two controllers on one PostgreSQL store can overlap while a deployment
replaces one pod with the next. The maintenance sweep, which fails runs and
claims whose runners went silent, times out approvals and queued nodes, and
records why waiting nodes have no agent, runs in one of them at a time under
a store lease, as the cron tick and the storage pass do. A controller that
dies mid-sweep holds the lease for at most a minute. A sweep that runs past
that can overlap the next one; its writes are conditional on the state they
read, and GitHub reports are guarded per run. The sweep judges a
waiting node from the runners connected to the controller holding the lease,
so a runner reporting only to the other one reads as offline until it
reconnects. GitHub check runs and commit statuses are written under a
per-run lock in the store, so the two create one check run per run between
them. The overlap is meant to last a deploy: runner presence, the egress
meter and the object-store budget still belong to each process, so run one
controller otherwise. A SQLite store serves one controller process.

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
| `credits-token` | The checkout service's own token (`sws_`), seeded as a `credits.grant` service token; see [Buying credits](auth.md#buying-credits) | the checkout service has no token |
| `cache-grant-key` | Key cache grants are signed with, the same value as the cache's | no cache grants |

Keep the directory readable by the controller's user alone (`0700`, files
`0600`); a Kubernetes projected volume with `defaultMode: 0400` does this.

### Signed downloads through CloudFront

For object-store downloads through a public ingress, configure the controller
with a CloudFront distribution domain, trusted key pair ID, and private key.
Point `--cache-blob-store` at the cache service's S3 bucket and prefix; the
controller announces `data_download_url` to in-cluster callers when that store
is configured, and to public-ingress callers when CloudFront signing is also
configured.
Pass `--cloudfront-domain` and `--cloudfront-key-pair-id`, then put the
signing key in the `cloudfront-key`
[credential](#controller-credentials). Keep it out of Helm values, command
arguments, and logs. The distribution must serve the private data bucket through its origin
access control configuration.

The controller returns a 60-second CloudFront URL for requests arriving
through the public ingress. Requests through the in-cluster Service receive a
regional S3 URL instead, so runner pods in the bucket's region can use the S3
gateway endpoint. Local filesystem storage keeps its direct byte-serving path and
does not need CloudFront signing keys. See [Data downloads](api.md#data-downloads)
for route behavior.
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

Remote `--working-tree` submissions need an S3 cache bucket configured through
`--cache-blob-store` to hold their uploaded source bundles. A filesystem-only
controller runs submitted commits from the repository's pushed history.

### Backups and upgrades

[Backup, restore, and upgrade](backup-restore.md) covers both database
shapes: what a restore needs beside the database, how to prove a backup
is good before relying on it, and what rollback means at each stage of a
controller upgrade.

## Migrating from the Docker Compose example

Treat the Helm installation as a new deployment. Sparkwing provides no
in-place conversion or automatic import for the old Compose volumes. Keep
`controller-data`, `gitcache-data`, and `logs-data` until you decide whether
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

The native runner dispatcher and service templates still need final startup
integration. Legacy agent-format installers are not a substitute for that
binding. Windows enrollment prints manual supervision steps rather than
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

**The runner returns `401 unauthorized`.** Confirm that `token:` in the `agent`
section of `~/.config/sparkwing/config.yaml` is a valid controller token.

**The runner does not claim work.** Confirm that the pipeline was remotely
triggered and that the runner can reach the configured controller and logs
URLs.

**A private repository cannot be cloned.** Confirm that the controller has a
cache URL, that the agent's claim is still live, and that the cache service can
reach the repository with its configured Git credentials.

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

Read policy with `GET /api/v1/storage` and replace it with
`PUT /api/v1/storage/settings` (scope `admin`):

```bash
curl -sS -X PUT "$CONTROLLER/api/v1/storage/settings" \
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
in the `admin` view of `GET /api/v1/storage`.

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
team policy with `PUT /api/v1/storage/quotas/{team}` (scope `admin`):

```bash
curl -sS -X PUT "$CONTROLLER/api/v1/storage/quotas/acme" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"tier":"paid"}'
```

Naming a tier with zero write limits selects that tier's limits. Unknown teams
return `404`. Reads report the authenticated caller's team. An operator also
receives the explicit quotas and the largest teams for the month.

Monthly reports sum each team's replica counters. They are historical usage,
so deleting a run does not lower them. `retained_bytes` measures the team's
unique committed blobs and stored log segments. Multiple references to the
same blob count once. Each team accounts for its own copy.

Output, manifest and memo pointers add no quota usage. Their underlying blobs
consume bytes and objects when a verified upload first commits them.

### Storage allowances

`storage_allowance_bytes` caps the retained byte count used for billing. Zero
leaves that count uncapped. Set it independently of write limits with
`PUT /api/v1/storage/quotas/{team}/allowance` (scope `admin`):

```bash
curl -sS -X PUT "$CONTROLLER/api/v1/storage/quotas/acme/allowance" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"storage_allowance_bytes":53687091200}'
```

Quota and allowance writes preserve each other's fields. Creating an allowance
for a team without a quota preserves its effective default-tier limits.
[Retained storage billing](auth.md#retained-storage) describes the rate, free
allowance and billing interval.
