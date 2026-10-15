# Tenant limits

These free-tier shares, funded tiers, credit checks, and storage charges apply
to controllers with a signed metering license. A signed `multi-team` license
also grants metering. On an unlicensed self-hosted controller, teams have no
credit balance and storage counter routes report unlimited room. Ordinary
storage quotas set by its operator still apply.

A multi-team controller hosts teams that pay nothing. This page lists what such
a team can consume that costs the operator money or grows with time, and what
bounds each one. A team with no credits runs its work on its own machines, so
what it costs the operator is storage, bandwidth and controller time.

The credit ledger prices each claim at the smallest class that covers the
pipeline's pinned or measured resources, with a 60-second minimum. A Job on a
band pool then runs alone on a machine of that class, so a 0.25-core pin pays
for, and gets, a 2-vCPU, 8 GiB node, of which the Job can use about 5.4 GiB. See [Runner classes](auth.md#runner-classes).
Each metered heartbeat charges elapsed work and renews the claim together. If
the controller cannot check the token marker or charge the ledger, it refuses
the renewal with `409` and the runner stops.

## Free storage allowance

A team without credits keeps a free storage allowance of one gibibyte unless
the Sparkwing Cloud operator writes another with the private `sparkwing-ops`
tool. The allowance is split into three fixed shares, one per store. The controller
counts every team's bytes in its database, and each store asks it for room
before it writes:

| Store | Share | Counted by | Checked |
|---|---|---|---|
| Cache: compiled binaries, dependency archives, artifacts, source bundles, job outputs | 3/4, 768 MiB | the controller's `team_storage` row for the cache | before the upload's body is read |
| Logs: node log segments | 3/16, 192 MiB | the controller's `team_storage` row for logs | when a runner reports the segments it stored |
| Run events | 1/16, 64 MiB | the controller's per-team event bytes | in the transaction that appends the event |

One share never reads another's count, so a team whose cache is full can
still write logs and events, and a team that stores only artifacts gets 768
MiB rather than the whole gibibyte. The operator's own team and a team with
credits are never held to a share.

The controller owns storage reservations, committed bytes and download charges
in its database. Concurrent reservations lock the team's row, so two writers
cannot both reserve its last available bytes. The cache service's counter routes
and service credential have been removed.

The controller charges node log segments itself. When a runner asks for a
node's first upload URLs, the controller reads the team's standing, which
picks the attempt's retention class, and each later request reports the bytes
the runner has stored; the controller commits the growth once, so a retried
request never charges twice.

Storage usage comes from committed blob rows and tagged log-stream records.
Bucket listings do not replace those authoritative counts. The controller's
leased maintenance pass expires records, marks unreachable blobs and collects
eligible tombstones after the 24-hour grace period. References added before
collection preserve the bytes. Physical deletion failures retain the tombstone
for a later pass.

Log retention follows the run's stored class. S3 uses lifecycle rules. A
configured filesystem backend expires classed objects during leased maintenance.
The former bucket-reconciliation pass, free-log-share pruner and
`storage_pass` health field are removed. See [Storage](storage.md) for the
remaining authenticated transport and host bindings.

## Job outputs

A job's return value is stored as an object, not in the controller's
database. The node reserves the output's exact size with
`POST /api/v1/runs/{id}/nodes/{nodeID}/output-upload`, puts the bytes to the
URL it gets back, and commits them with `output-commit`; its finish or
attempt report then names the committed object. Outputs count toward the
cache share, so a free team whose cache is full has its node fail with the
storage refusal as the reason, but only for an output over 1 MiB: every
team may store outputs of up to 1 MiB each past its share, and they still
count toward its storage. Those small outputs may take a team at most 64 MiB
past its share (64 MiB in all for a team with no slot); past that, a small
output needs room like any other.

| Limit | Bound | Where |
|---|---|---|
| One output | 64 MiB, refused at reservation with `413` | `pkg/store/node_output.go` |
| One run's outputs | 1 GiB across every node and attempt, refused at reservation with `413` | `pkg/store/node_output.go` |
| Small outputs past the share | up to 1 MiB each, 64 MiB past the team's share in all | `pkg/store/team_storage.go` |
| Retention | 30 days after the run finishes; each pipeline's newest successful run keeps its outputs | `pkg/controller/node_output.go` |

A reader gets a one-minute URL and the SHA-256 the bytes must match. A
claim token reads only the outputs of its own node's transitive dependencies
in its own run, and the outputs of child runs it started. A coalesce
follower, a cache hit and a cross-pipeline ref its plan declares ask
`POST /api/v1/runs/{id}/nodes/{nodeID}/claim/input`, which picks the source
run itself and answers with the same kind of grant, plus a memoized source's
artifact manifest digest. An output whose run passed retention reads as
absent: a cross-pipeline ref reports no value, and a cache entry that pointed
at it is dropped, so the next run misses the cache and runs the node. Every
read checks the bytes against the recorded SHA-256: a direct read of missing
or changed bytes fails with an error, and a cache hit on them runs the node
as a miss.

A controller without `--cache-blob-store` keeps outputs in `outputs/` under
its data directory and serves them through URLs it signs itself, which only
that replica can honor.

`GET /api/v1/storage` shows a signed-up caller its team's tier, allowance,
the three shares, its event bytes, and what it holds in the cache and in log
segments under `team`.

## Free-team slots

The free tier is bounded by counting teams. A team without credits takes one
of `--free-team-slots` (default 200) with the first byte it commits, in the
transaction that counts it, and keeps it until the team is deleted. A
reservation takes no slot, so one nobody commits costs the tier nothing; it is
refused only once every slot is taken. A commit whose team cannot take a slot
is refused with `402`, so no team without a slot commits a byte beyond the
small outputs above: up to 1 MiB each and 64 MiB in all. A writer uploads before
settlement. A refused settlement records no receipt or live blob. Pending objects require cleanup separately from committed CAS
collection.
The free bytes a deployment holds are therefore at most slots times the
allowance, plus those in-flight writes. A team with a slot
keeps writing inside its shares after the slots run out; a team with neither a
slot nor credits is refused its cache, log and event writes with `402`:
`free storage is paused; buy credits or join the waitlist`.

Runs are never refused for billing. A team without credits, with no slot or
held over a disputed payment still starts runs, which its own machines
execute. A metered claim on cloud capacity needs credits and is refused while
the team is held.

Sparkwing Cloud operators admit a waitlisted account from the operator
console's Waitlist tab. To oversubscribe, raise `--free-team-slots`. A sign-up gate reads the tier as
closed once every slot is taken.

A multi-team controller refuses to start without `--bucket-store`, because the
shares are held over the object store: the cache's `--blob-store`, which also
holds node log segments. The volume-backed cache tree enforces no allowance. A cache that verifies grants and keeps a `--blob-store` refuses to
start without `--controller`, because only the controller can count its
teams' bytes. The cache keeps no count of its own, so a restart lists
nothing.

## What prunes automatically

Each pruner runs inside its service, never only from a CLI command, and a
second run over the same data removes nothing more.

| What | Rule | Where | Setting |
|---|---|---|---|
| Runs and operational children | finished runs past the run window, preserving each pipeline's newest success | controller, leased hourly maintenance | `run_retention_days`; Cloud defaults to 90 days, self-hosted defaults to disabled |
| Node metrics | samples past their independent window | controller, leased hourly maintenance | `node_metric_retention_days` |
| Node log segments | by retention class: `logs/r90/` 90 days for a funded or single-team controller, `logs/r30/` 30 days for a team the controller answered free when the attempt's log opened, `logs/r<days>/` for a team `--log-retention` names | the bucket's lifecycle rules, which the controller verifies before it issues URLs into a class; deleting a run or a team deletes its segments | `--log-retention team=days`; see [self-hosting.md](self-hosting.md#node-log-retention) |
| Blob records and their bytes | unpinned records expire; unreachable bytes pass a 24-hour tombstone grace period | controller, leased hourly maintenance with a configured object backend | record expiry and pins |
| Job outputs | expire through record retention, preserving each pipeline's newest successful output | controller, leased hourly maintenance | output record expiry and pins |
| Invitations | accepted, withdrawn or expired more than 30 days ago | controller, leased hourly maintenance | none |
| API, CLI and runner tokens | revoked or expired more than 30 days ago | controller, leased hourly maintenance | none |
| Browser sessions, GitHub runner credentials | expired | controller, leased hourly maintenance | none |
| Cron fire history | beyond 200 per schedule | controller, on each fire | none |
| Egress usage rows | older than 13 months | controller, monthly | none |
| Storage reservations | an hour after a writer took one it never committed or released | controller, on the team's next write and in the leased hourly maintenance | none |

Every rule keys on when a run finished or when a file or object was last
written, never on when a run was created, so a run that just ended is never
pruned for having started long ago.

## Everything a free team can grow

Every resource a team with no credits can make the operator pay for, and what
bounds it. The last column is where the bound is enforced or defaulted.

| Resource | Bound for a team with no credits | Where |
|---|---|---|
| Teams per user | three created over the account's life, the personal team included | `pkg/store/identity.go` |
| Free teams | `--free-team-slots`, 200 by default, released only when a team is deleted | `pkg/store/free_tier.go` |
| Run events | the event share, 64 MiB by default | `pkg/store/free_tier.go` |
| Log segments | retention class fixed by the run; expiry follows that class | `pkg/store/result_logs.go`, `internal/blob/log_retention.go` |
| Cache binaries, dependency archives and artifacts | the cache share, 768 MiB by default, refused before the body is read | `pkg/store/upload_runtime.go`, `pkg/store/quota_runtime.go` |
| Job outputs | the cache share, 64 MiB per output and 1 GiB per run, refused at reservation; 30 days after the run finished | `pkg/store/node_output.go` |
| Events per run | 256 KiB per event, 64 MiB and 50,000 events per run | `pkg/store/event_limits.go` |
| Annotations per run | 64 KiB per annotation, 1,000 per run, and 4 MiB per run JSON-encoded, node and step annotations together; past a bound the append answers `413` for size or `429` for count and stores nothing | `pkg/store/store.go` |
| Metric samples per node | 10,000, about five and a half hours of two-second samples; a later sample answers `429`, and the runner stops sampling that node and records one `metrics_stopped` event on it. Reads come in pages of 1,000 by default and 10,000 at most | `pkg/store/metrics.go` |
| Pending direct uploads | 100 uncommitted uploads per team, each at least one byte and pending for at most 24 hours; the next reservation answers `429` | `pkg/store/direct_upload.go` |
| Logs per node | 64 MiB per node attempt; the runner truncates past it | `pkg/storage/logseg/sink.go` |
| Log retention | 30 days for a team without credits (`logs/r30/`), 90 days for a funded team (`logs/r90/`), expired by the bucket's lifecycle rules | `pkg/storage/logseg/keys.go`, `pkg/controller/log_segments.go` |
| Run events past retention | removed with their bytes 30 days after the run finished on a multi-team controller | `pkg/controller/team_storage.go`, `pkg/store/storage_retention.go` |
| Team cache objects | unpinned records expire; unreachable bytes pass a 24-hour tombstone grace period | `pkg/store/blob_gc.go`, `pkg/controller/data_storage.go` |
| Bytes downloaded by one team | 5 GiB a UTC day for a free team (`--team-daily-download-free-bytes`), 50 GiB for a funded team (`--team-daily-download-funded-bytes`), counted per team in the controller's database across the cache, the controller's artifact and git proxy routes, and signed URLs; log reads are not counted. A download is charged the bytes it delivers, 8 MiB at a time as it streams; signing a URL charges the stored object's size. Only a team already at its cap is refused, so a download under way always finishes and the team's next request gets `429` until midnight UTC. A free team's request gets `503` while the controller cannot answer. A signed URL, for S3 or CloudFront, is charged once when it is minted and expires 60 seconds later; it can be fetched more than once inside that minute, so the counter is an abuse limit rather than an exact bandwidth meter | `pkg/store/team_storage.go`, `pkg/controller/data_download.go` |
| Cloud runner time | only an operator-metered token claims cloud capacity, and each claim needs credits | `pkg/store/credits.go` |
| Nodes per run | `max_global_nodes_per_run` when the operator sets it | `pkg/store/compute_limits.go` |
| Live log buffers in controller memory | per-node, total and node-count caps | `cmd/sparkwing-controller/main.go` |
| Cron schedules | 20 per repository and 10 declared repositories per team; withdrawn rows keep history without using a slot | `pkg/controller/crons.go` |
| Cron fire history | 200 per schedule | `pkg/store/crons.go` |
| Secrets | 100 per team, 128 KiB each as stored | `pkg/store/secrets.go` |
| Runner tokens | 100 live per team, each expiring after 90 days unused | `pkg/store/identity.go` |
| Spent tokens and invitations | deleted 30 days after they stopped admitting anyone | `pkg/store/identity_prune.go` |
| GitHub runner bindings | 20 per team, funded or not | `pkg/store/github_runner_bindings.go` |
| GitHub runner credentials | 20 live per team, each expiring after an hour and deleted once expired | `pkg/store/github_runner_bindings.go` |
| Invitations | 50 open and 100 sent per day per team, each expiring after 7 days | `pkg/store/identity.go` |
| Browser sessions | seven days after last use, 30 days at most; deleted once expired | `pkg/controller/auth_handlers.go` |
| Bytes the controller serves | the team's daily download cap for artifacts and git fetches, never for log reads; a daily alarm on a limits profile that refuses nothing | `pkg/controller/egress.go`, `pkg/controller/limits_profile.go` |
| Egress usage rows | 13 months | `pkg/store/egress.go` |
| Controller requests | per-token request budget, per-runner claim and heartbeat budgets, and the sign-in limiter, on a limits profile | `pkg/controller/limits_profile.go` |

A controller started without `--limits-profile` sets no request, flood or
egress budget, so a multi-team deployment runs with one.

## What still grows

These are rows, not stored bytes, and each grows only as fast as the limits
above let a team act.

- **Pinned runs.** Each pipeline keeps its newest successful run and outputs.
  Other run rows expire under the configured run-retention window.
- **Storage usage and the credit ledger.** Usage rows are additive per team,
  period and replica. Financial evidence survives run retention.
