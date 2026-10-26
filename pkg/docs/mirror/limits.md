# Tenant limits

Limits, operator ceilings and setting precedence have shared declarations.
Controller, host, storage and billing integration still need native acceptance
evidence. This page describes the target policy. It does not establish that
every deployed controller enforces it.

<!-- design:begin -->
## Explicit values and ceilings

An absent value uses its declared default. An explicit zero, false or empty
collection retains its documented meaning. Invalid input fails with its source.
An author value above an operator ceiling is refused rather than clamped.

Intake seals request inputs once. Plan acceptance resolves checkout and code
declarations against that request and the operator ceilings in one transaction.
Retry and resume retain the accepted values.

## Operational guards

Read deployment guards and request budgets with
`sparkwing operator limits show --profile deployment -o json`. Set a guard with
`sparkwing operator limits set --profile deployment --setting max_jobs_per_run --value 256`.
These commands use `/api/v1/operator/limits`. The controller validates supported
setting names and returns the applied values. An explicit zero retains the
setting's documented meaning. Missing observations stay absent.

Read storage request budgets with
`sparkwing operator object-store status --profile deployment`. The result includes
per-class counters, bucket ceilings and whether the last measurement completed.
An incomplete measurement keeps its error and running totals.
`sparkwing operator object-store reset-breaker --profile deployment` resets the
breaker with one idempotency key, retained across transport retries.

Each command supports `-o pretty|json|plain`. Deployment credentials require
`operator.limits` or `operator.object_store` for the corresponding operation.
These grants do not confer operator-console powers.

## Execution and records

| Concern | Shared policy |
| --- | --- |
| Local execution | No new execution deadline. Queue wait, approval wait, inactivity and execution have separate budgets. |
| Remote run host | Six-hour default execution budget, configurable by the operator. Child budgets fit inside the owner's remaining budget. |
| Queue wait | Twenty-four-hour default, also bounded by a finite owner deadline. |
| Approval wait | Zero is indefinite where the owner can wait. A finite owner deadline bounds it. |
| Application retries | At most ten declared retries under the same claim. The attempt ordinal advances. |
| Started lease lapses | Separate disruption count, capped at three. An unstarted lapse consumes no attempt or disruption. |
| Generated fan-out | At most 256 jobs in an accepted expansion. |
| Typed output | At most 16 MiB. |
| Error display | Masked, then bounded to an 8 KiB head and tail. |
| Annotation or summary | Masked UTF-8, bounded to 128 KiB. Storage also refuses an oversized raw record. |
| Memo lifetime | Seven-day default, at most thirty days. |
| Schedule catch-up | At most twenty-four hours. |
| Child-run depth | At most ten levels, counting the root run. A deeper child is refused with `409`, conflict detail `limit: max_child_depth`. GitHub reusable workflows also nest ten levels. |
| Children per root | The team setting `max_children_per_root` defaults to 50 descendants across every depth; retries do not count. Team admins set a ceiling from 1 to 1,000 in Team settings or with `sparkwing team settings set --profile work --max-children-per-root 200`; changes are audited. Each child is checked when created. When the root has reached its allowance, creation returns `409`, `limit: max_children_per_root`, with the team's setting, the 1,000 maximum, and instructions to raise the setting or split the work into separate runs. GitHub limits 50 unique reusable workflow definitions per run, rather than invocations. |
| Child run budget | A child spends the hourly run (`max_runs_per_hour`), flood and queue-depth budgets of whoever created its root, not the runner holding the parent's claim. |
| Cloud scratch | Fixed aggregate 20 GiB, including Docker-in-Docker. |
| Cloud Docker | `docker build`, `docker run`, Compose and `--memory` work; privileged and host-level features do not. |

Execution and inactivity deadlines remain distinct. Time spent waiting for
customer approval or unstarted capacity is not execution time.

## Docker on Cloud

Cloud Jobs run as root inside their own Kubernetes user namespace, with a
Docker daemon that is root there too, so `apt-get install` and the Docker
socket work while job code never holds root on the node. `docker build`,
`docker run`, `docker compose`, container-to-container networking, published
ports and `docker run --memory` work.

- `docker run --privileged` is refused: the daemon cannot mount sysfs for it.
- `--cap-add` is accepted, but a container never gets more than the daemon
  holds: `SYS_ADMIN`, `NET_ADMIN` and `SYS_PTRACE` plus the default set, all
  confined to the Job's user namespace. Capabilities such as `SYS_MODULE`,
  `SYS_TIME` or `SYS_RAWIO` have no effect on the node.
- `--security-opt seccomp=unconfined` is accepted but cannot lift the Job's
  own filter, which refuses io_uring, keyrings, bpf, perf events,
  userfaultfd, kexec and module loading for every container.
- Host namespaces are unavailable: `--network host`, `--pid host` and
  `--ipc host` give the Docker daemon's namespaces, not the node's.
- `--device` cannot reach node devices, and host paths, node sysctls and
  kernel modules are out of reach.

The Job's own CPU and memory budget bounds everything its containers use, and
all its processes and threads together are limited to 32768 per Job.

GitHub Actions and CircleCI give root only inside a per-job VM. Run work that
needs those features on a self-hosted runner.

## Uploads and storage accounting

Storage usage comes from committed blob rows and tagged log-stream records.
`GET /api/v1/storage` reports retained event, blob and tagged-log bytes plus
live blob-upload holds. Historical usage counters and bucket listings do not
replace those authoritative counts. Exact ticket replay never charges twice.
Deployment reports sum usage by tenant and period.

Expired staging uploads still count until garbage collection removes them.

The controller's maintenance pass expires records, marks unreachable
blobs and collects tombstones after the 24-hour grace period. New references
preserve their bytes. Physical deletion failures retain the tombstone for retry.
S3 uses log lifecycle rules. A filesystem backend expires classed objects during
maintenance. The former bucket-reconciliation pass, free-log-share pruner
and `storage_pass` health field are removed.

The controller authorizes `/api/v1/data/upload` and reserves quota before signing
a PUT. `/api/v1/data/commit` verifies physical bytes and rechecks authority before
committing existence, receipts and accounting. Aborted or expired reservations
release pending capacity. A raw digest never grants read or attachment rights.

Sparkwing Cloud applies a storage tier to every team without an explicit quota.
A team that has bought credits at least once gets the paid tier; any other team
gets the free tier. The deployment's default tier and a per-team quota set by
the operator take precedence over both. Every write that grows stored data,
whether a run event, a blob or source upload, or a log segment, passes the same
check. A held team cannot grow its storage, and a team with an empty balance
cannot grow past the free storage allowance (1 GiB unless the operator sets
another) while storage is priced. Cloud prices stored bytes beyond that
allowance at the published rate.

| Tier | Per run | Per month | Objects per run |
| --- | --- | --- | --- |
| Free | 10 MiB | 1 GiB | 1,000 |
| Paid | 1 GiB | 100 GiB | 100,000 |

For comparison, GitHub Actions caches 10 GB per repository by default, and GitLab
gives free namespaces 10 GiB of storage. A write past a limit is refused with the
limit it hit and "contact support to raise this limit". Self-hosted deployments
apply no tier unless the operator sets a default tier or a team quota.

Quotas count the team's unique stored bytes. Repeated authorized references do
not multiply that byte count. A team's copy has its own accounting. Financial
and historical usage records survive run deletion.

Read the authenticated team's storage projection with `GET /api/v1/storage`.
Deployment operators manage storage policy through
`PUT /api/v1/operator/object-store/settings`,
`PUT /api/v1/operator/object-store/quotas/{team}` and
`PUT /api/v1/operator/object-store/quotas/{team}/allowance`. These require
deployment object-store authority or the operator console. Team-admin authority
alone cannot change a deployment ceiling.

Read-only remote cache access is an explicit profile opt-in. It permits authorized
verified-default-branch memo, artifact and dependency-cache reads. Publication
has its own policy and accounting. Pipeline binaries stay in the local cache.
See [data uploads](data-uploads.md) and [caching](caching.md).

## What prunes automatically

The store's one active controller deletes eligible records in bounded batches. Run retention preserves each pipeline's newest successful run
and output. Metric retention has its own window. Log retention classes expire
independently of run and output pins. Unreachable blobs pass a tombstone grace
before collection.

Cloud run retention defaults to ninety days. Self-hosted run retention defaults
to disabled. The output window is thirty days, with the newest-success pin.
Free and funded log retention classes are thirty and ninety days respectively.
See [self-hosting](self-hosting.md#bound-storage-growth).

## Billing and overload protection

Each metered award snapshots its payer, rate, executor and start facts. Job
compute and host coordination have separate sixty-second minima. Plan and eval
units bill per second without those minima. Storage charges add no compute
seconds. Reports read persisted awards and billing checkpoints without advancing
them. See [billing](auth.md).

Run-rate and usage quotas use store-wide counters. Request-rate, ring, stream
and download overload guards are per replica. Their scope must remain visible
when sizing a deployment. The supported controller topology is one replica.
<!-- design:end -->
