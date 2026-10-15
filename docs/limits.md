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
| Cloud scratch | Fixed aggregate 20 GiB, including Docker-in-Docker. |

Execution and inactivity deadlines remain distinct. Time spent waiting for
customer approval or unstarted capacity is not execution time.

## Uploads and storage accounting

The controller authorizes `/api/v1/data/upload` and reserves quota before signing
a PUT. `/api/v1/data/commit` verifies physical bytes and rechecks authority before
committing existence, receipts and accounting. Aborted or expired reservations
release pending capacity. A raw digest never grants read or attachment rights.

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

An elected controller holds a maintenance lease and deletes eligible records in
bounded batches. Run retention preserves each pipeline's newest successful run
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
