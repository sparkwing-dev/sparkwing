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

### Console help contact

`--support-contact` names an email address or an http(s) URL. The dashboard's
Help button, at the top right of every page, and its limit messages show it.
Without it they tell people to contact their Sparkwing administrator. Startup
refuses any other value.

### Node log retention

The log storage contract assigns a retention class before signing segment
uploads. Object-store lifecycle rules must cover each class before URLs are
issued and expire it no sooner than the class: Sparkwing deletes log objects at
the class age itself, so set each rule longer (for example the class plus seven
days) as a backstop. The shared free and funded classes are thirty and ninety days.
Filesystem storage uses its backend's retention mechanism.

Log reads reauthorize at the controller and cover tagged job-attempt and
trigger, plan or eval subjects. Log expiry is independent of newest-successful
run and output pins. Verify retention through persisted reads after completion.
Native signing, lifecycle verification and final completeness still need
integration evidence. See [log completeness](observability.md#log-completeness).

### One active controller

A store has one active controller. Before migrations, startup writes or serving,
a PostgreSQL controller takes a session advisory lock derived from the database
and Sparkwing schema on a dedicated connection. A second controller blocks as a
standby. Promotion bumps the `controller_epoch` row in the same transaction
that takes the lock. Every write transaction holds a shared lock on that row
and checks its promoted epoch. Promotion waits for in-flight writes to finish.
The former controller's later writes abort and trigger fail-stop, even before
its lock watchdog runs. The active controller checks the connection and lock every second,
with a five-second query timeout. Losing either exits the process non-zero
immediately, without draining. Its supervisor restarts it, and a waiting standby
can take over. SQLite controllers hold the home's `wingd/d.lock` OS file lock
before opening the store, through shutdown. Another controller or wingd on the
same home fails fast. Break-glass admin can still use the store.

Webhook ordering, bucket measurement and write guards, request budgets and live
log tails use the active process's state. Cross-controller webhook ordering and
storage-state propagation are therefore outside this topology. Several active
controllers require a separately reviewed design. Standbys using filesystem
data must share the active controller's `SPARKWING_HOME`. S3 data is shared
through the bucket.

`--payment-mode` remains a shared setting written at startup. Every controller
on one store must declare the same value.

### Declared deployment

The controller is told what it is; it never infers it from which files happen
to exist, and it settles the answer once at startup.

| Flag | Values | Meaning |
|---|---|---|
| `--deployment` | `local` (default), `team`, `cloud` | `local` holds one team. `team` holds many teams under a multi-team license and meters usage. `cloud` is the hosted service. |
| `--store` | `sqlite` (default), `postgres` | Where state lives. `postgres` reads the `pg-url` credential. |
| `--external-url` | an `https` URL, an `http` URL on loopback or a Kubernetes Service (`*.svc`), or empty | The origin runners, OIDC tokens and GitHub Actions workflows name. Runners accept the same addresses. |
| `--payment-mode` | `test`, `live` or empty | Which Stripe credentials settle payments. Test can switch once to live; live cannot switch back to test. `--billing-url` needs it, so the controller settles every payment it opens. |

Configuration is validated before startup creates the home directory, credential
epoch or store. This includes sign-up mode, Google sign-in and callback URLs,
OIDC keys, storage URLs and download signing configuration.

Startup refuses, before it opens the store or writes any setting, when:

- `--deployment=team` or `cloud` has no license, or the license does not verify,
  has expired or does not grant multi-team;
- a `local` deployment has a license that grants multi-team, or one that does not
  verify;
- `--store=postgres` has no `pg-url`, a `pg-url` is present under `--store=sqlite`,
  or `--deployment=cloud` is not on PostgreSQL;
- `--deployment=cloud` has no `--external-url`, or a `cloud` limits profile runs
  under another kind;
- any other flag is invalid, or `--addr`, `--trusted-proxy-addr` or
  `--metrics-addr` cannot be bound. Every listener is bound before the store is
  opened. The bootstrap seed is written after configuration checks pass. Payment
  mode, deployment identity and free team slots commit together only after the
  deployment record is written successfully. A failed database commit restores
  the previous deployment record.

A license that expires while the controller runs does not change the
deployment. Metering, the free tier and sign-in keep working, and
`GET /api/v1/operator/health` reports `degraded` with a `license: expired`
problem while the controller logs an error every hour. The next start refuses until the
license is renewed.

`GET /api/v1/health` answers without a credential and reports liveness and
readiness only: `{"status":"ok","auth":"enabled"}`, or HTTP 503 when the
database is unreachable. License, secret rotation, bucket, database size,
egress and GitHub App state are on `GET /api/v1/operator/health`, which
requires `operator.limits` or an operator console session.

Without `--external-url`, the GitHub runner binding routes answer
`capability_unsupported` instead of handing out a workflow that points at
whatever host name the request used.

### Controller credentials

`sparkwing-controller` reads every secret from one directory, named with
`--credentials-dir`, holding one file per credential under a fixed name. A file
that is absent leaves the feature it guards off, and a file that is present
but empty or only whitespace stops startup, naming the file. GitHub App and
GitHub OAuth credential errors instead disable those features and appear in
operator health and doctor (see [GitHub App configuration](github-app.md#operator-configuration)). A file whose name
is not in the table below also stops startup, so a misspelled `pg_url` cannot
leave the controller on an empty SQLite store. A `.previous` or `.published`
file without its current key stops startup too, except for the optional GitHub
App webhook secret, which disables the App. There is no environment or
flag form of any of them. The chart projects its Secret values into this
directory, and `controller.credentialsSecret` names a Secret whose keys are
these file names, mounted whole.

| File | Holds | Without it |
|---|---|---|
| `pg-url` | PostgreSQL connection string for the state store | `--store=postgres` refuses to start |
| `secrets-key` | Key stored secrets are sealed under, 32 raw bytes or their base64 | see [Security](security.md#secrets-at-rest) |
| `credential-epoch` | Live bearer and session epoch, when kept beside `secrets-key` | created on startup; preserve the latest value through a restore |
| `secrets-key.previous` | Key values were sealed under before `secrets-key`, during a rotation | one key |
| `bootstrap-admin-token` | First admin token, stored before the listener binds | no token is provisioned |
| `license` | Signed multi-team license | `--deployment=team` and `cloud` refuse to start |
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

The controller writes `credential-epoch` beside `secrets-key`, or beside
`state.db` when no credentials directory is configured. If credentials are
read-only, set `--credential-epoch-dir` to a separate persistent writable
directory. Use the same flag for startup and every restore command. The
controller restricts that directory to its owner and applies the credential
file checks above to the epoch. On a pod volume whose root is setgid under one
of the controller's own groups (the pod's `fsGroup`), it keeps that group's
access, and the epoch file may be group-writable by that group. The Helm chart always mounts a dedicated,
retained PVC at `/var/lib/sparkwing/credential-epoch` and selects it with this
flag, including when `/data` uses `emptyDir`, and sets the pod's
`fsGroupChangePolicy` to `OnRootMismatch` so the kubelet does not relabel the
volume on every mount.
Preserve the latest epoch when
restoring a database. Retain its volume independently of database backups.
Declared restore preparation holds schedules until operator acknowledgment and
cancels restored unfinished scheduled runs. A normal restore keeps sessions,
tokens, runner credentials and schedule consent valid. Keep the controller's
audit log stream outside the database so revocations, member removals and
demotions since the backup can be reapplied by identifier and team. Session
logout and replacement lines include the revoked session digest, account and
team, independently of the account signing in.
Use `restore prepare --sign-out-everyone` for security incidents such as a stolen
database or leaked keys. It rotates the epoch, invalidates earlier credentials
and API/local schedule consent, and requires a fresh `bootstrap-admin-token`
seed and reissued credentials. See [Backup and restore](backup-restore.md#restore).
Payment recovery is manual; follow the operator runbook kept outside this repository.

Controller log messages and attribute values, including nested groups and errors,
replace Sparkwing credential shapes with `[credential:<digest>]` fingerprints
before writing to stderr or telemetry. Public token prefixes remain readable.

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
listeners require `--external-url` pointing at an address runners can reach;
startup refuses them without it. The key that signs these URLs is kept in `$SPARKWING_HOME/data-signing.key`,
so a restart does not invalidate transfers in flight. Filesystem storage needs
no CloudFront signing keys.
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
    --set controller.store=postgres \
    --set controller.databaseSecret.name=sparkwing-database \
    --set controller.databaseSecret.key=dsn \
    --set controller.storage.type=emptyDir
```

The chart passes `--store=postgres`, and the controller reads the Secret as its
`pg-url` credential. With `controller.storage.type=emptyDir`, `my-values.yaml`
must also set `controller.dataStore.s3URL`; the chart refuses an ephemeral
`/data` that would hold blobs whose records outlive it. Keep the DSN out of Helm
values and command arguments. Initialize and verify the PostgreSQL data before
starting the controller against it.

Filesystem node outputs use signed URLs on the controller. Behind a proxy, set
the controller's `--external-url` to the address runners can reach. Without that
setting, the URLs use the controller's listener address. Uploads and downloads
use the signed URL's authority and carry no runner token.

Working-tree source bundles use the source-bundle authority and upload contract.
Native intake and bundle ownership remain integration requirements. The bundle
can seed acquisition, which still verifies the sealed commit.

### Backups and upgrades

[Backup, restore, and upgrade](backup-restore.md) records schema compatibility.
Recovery is manual; follow the operator runbook kept outside this repository.

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
and platform observations grant no authority beyond the controller's enrollment.

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

A runner's credential fixes which runs it accepts:

- A runner that holds a pool with a secret reserved to it takes only runs from
  a verified push to the repository's default branch. Pull request, feature
  branch, manual and scheduled runs wait for another runner.
- Any other runner takes any run its repository patterns and labels admit.
- A team admin sets a runner's trust class when registering it:
  `sparkwing runners add --profile prod --runner deploy-1 --trust=verified-default` applies the restriction to a
  runner without reserved secrets, and `--trust=any` lifts it from a pool with
  reserved secrets. Pools holding reserved secrets still require a launcher
  with `--trust=any`. The class is stored with the runner's credential, so the
  runner, or anyone holding its token, cannot change it.

For a verified-default pool, enroll a launcher-only credential with
`--reserved-for deploy --trust verified-default --launcher` on `runners add`.
On Kubernetes, set `launcher.enabled=true` and reference that credential in
`launcher.tokenSecret` in the runner bundle chart. The server requires launcher credentials for verified-default pools and for
every pool holding reserved secrets, including with `--trust=any`. Pool and trust settings live on the
credential. A run-level `--requires pool=deploy` uses controller dispatch so
its plan and jobs can be claimed by the launcher.
The launcher runs each plan, evaluation and job claim in a fresh pod through the
same admission policy, bootstrap exchange and quarantine as Cloud.
Only the launcher mounts the pool credential. Each job receives a one-time
bootstrap, its own scratch storage and no persistent cache volumes. Self-hosted
pool claims consume no Cloud credits. See the [runner chart](../charts/sparkwing-runner-bundle/README.md)
for enrollment and node prerequisites.

On plain machines (VMs and bare metal), pipeline code runs under the runner's
Unix uid and can read its credential, rewrite its caches, inspect concurrent
jobs and leave files or processes for later claims. A trust class restricts
admission; it does not isolate jobs. Keep machines that serve untrusted branches
apart from pools that hold deployment secrets. GitHub documents the same risk
for [persistent self-hosted runners](https://docs.github.com/en/actions/reference/security/secure-use#hardening-for-self-hosted-runners).

A cached pipeline binary runs only as a private copy whose bytes matched the
digest recorded when it was compiled. Jobs on a shared runner run under the
runner's account and can rewrite both the binary and its digest, so the check
catches corruption rather than tampering on a persistent machine runner. Builds
on a host the run's owner does not own set
`GOENV=off`. A run's submitted environment reaches the runner only under names
the runner's own `run.submit_env_allow` lists; see
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
sets `database.alarm` on `GET /api/v1/operator/health`. Sizes and table names appear only
on `GET /api/v1/operator/object-store` with `operator.object_store`.

SQLite samples allocated pages minus its free pages. Removing data can lower
the sample while the file keeps its size until `VACUUM`. PostgreSQL samples
relation sizes, including indexes.

### Per-team storage quotas

Quotas belong to registered teams. Token principal labels do not select a
quota. `default_tier` supplies limits only when a team has no explicit row.
An empty default leaves those teams unlimited on a self-hosted deployment.
Sparkwing Cloud instead gives them the free tier, or the paid tier once the team
has bought credits ([limits](limits.md#uploads-and-storage-accounting)). An
allowance alone does not set write limits. A zero write limit is unbounded.

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

Monthly reports read each team's usage counters. They are historical usage,
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
