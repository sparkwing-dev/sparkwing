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
| Cache: compiled binaries, dependency archives, artifacts, source bundles | 3/4, 768 MiB | the controller's `team_storage` row for the cache | before the upload's body is read |
| Logs: live runs on the volume and the archive | 3/16, 192 MiB | the controller's `team_storage` row for logs | after the node and run caps cut the append, before it is written |
| Run events | 1/16, 64 MiB | the controller's per-team event bytes | in the transaction that appends the event |

One share never reads another's count, so a team whose cache is full can
still write logs and events, and a team that stores only artifacts gets 768
MiB rather than the whole gibibyte. The operator's own team and a team with
credits are never held to a share.

A write reserves its size with `POST /internal/storage/reserve`, commits what
it stored with `POST /internal/storage/commit`, and gives the room back with
`POST /internal/storage/release` when it stored nothing. The reserve locks
the team's row, so writers on any number of cache or logs replicas see each
other's reservations, and two writers racing for the last bytes of a share
cannot both win. A reservation a crashed writer never settled expires after an
hour. A commit with a reservation ID counts once for 24 hours; hourly
storage maintenance prunes older receipts, after which a replay counts
again. A renewal retry returns the same next reservation while it is
active. The cache calls these routes with its operator token and makes one
reserve and one commit or release per object. The logs service forwards the
appending caller's credential, which counts only its own team's logs. A
forwarded commit must name a reservation and a nonnegative byte count. The
cache's operator token can commit a negative overwrite delta when an object
shrinks. The logs service reserves a 1 MiB block per team and run, or the
room left when that is smaller. Appends draw from the block without asking
the controller. When a block runs out, the service commits what the run
wrote and takes the next block in one call to the commit route
(`next_bytes`); every minute it does
the same for a run still appending, and it commits and gives the rest back
for a run that wrote nothing that minute, and at shutdown. A run therefore
costs about one controller call per MiB or per minute, and a team holds at
most one uncommitted block per live run. A crashed logs service leaves its
blocks reserved until they expire after an hour. It also confirms an
append's claim with the controller at most once every 30 seconds per run,
node, credential and claim; an append that names no claim generation is
confirmed every time.

A write past a share is refused with `413` and a reason that names the share,
what the team holds and what the write needs. A log write past the share
also names `evict_bytes`: the logs service then deletes the team's least
recently written archived runs, never a run in use or written since its
archive, until it has freed that much, and asks again saying it evicted. The
controller then counts the team's logs from a listing of the archive, never
from the caller's word, and grants the write only if the recount makes room;
otherwise, or when nothing could be evicted, the append is refused. The
recount, like the hourly pass, counts only what the archive holds, so bytes of
a run not yet archived drop out of the count until the next pass. The cache judges a declared
`Content-Length` before it reads one byte, and stages a binary only after the
check. An upload of unknown length gets the room the team has left, up to the
per-object cap, and is cut past it; the cut aborts the upload, so no partial
object remains. An overwrite commits the difference from what it replaced.

When the controller cannot answer, the cache and the logs service refuse a
free team's write with `503` and a `Retry-After`: an outage never lifts a
limit. The operator's team never asks, and a team the controller answered
funded within the last five minutes proceeds. A team that buys credits is
unblocked by its next write.

The hourly storage pass replaces each team's count with a listing of the
cache's and the logs service's buckets plus whatever was committed while it
listed, so a count that drifted, from a delete or a crash between a write and
its commit, is corrected within the hour. It lists the buckets the controller
names with `--cache-blob-store` and `--logs-archive-store`. The logs service
commits a byte when it is appended and the pass counts only what the archive
holds, so for up to an hour after a pass a team's live, unarchived log bytes
are not counted against its share. One replica runs the pass an hour, under a lease
in the database. A pass that cannot list a bucket leaves that store's counts
as they were, and `GET /api/v1/health` reports `storage_pass.ok: false` with a
`problems` entry until a pass succeeds.

`GET /api/v1/storage` shows a signed-up caller its team's tier, allowance,
the three shares, its event bytes, and what it holds in the cache and the
logs service under `team`.

## Free-team slots

The free tier is bounded by counting teams. A team without credits takes one
of `--free-team-slots` (default 200) the first time it stores anything, in
the transaction that reserves the bytes, and keeps it until the team is
deleted. The free bytes a deployment holds are therefore at most slots times
the allowance at every instant, without measuring anything. A team with a slot
keeps writing inside its shares after the slots run out; a team with neither a
slot nor credits is refused its cache, log and event writes with `402`:
`free storage is paused; buy credits or join the waitlist`.

Runs are never refused for billing. A team without credits, with no slot or
held over a disputed payment still starts runs, which its own machines
execute. A metered claim on cloud capacity needs credits and is refused while
the team is held.

Sparkwing Cloud operators admit a team through the private `sparkwing-ops`
tool. To oversubscribe, raise `--free-team-slots`. A sign-up gate reads the tier as
closed once every slot is taken.

A multi-team controller refuses to start without `--bucket-store`, because the
shares are held over the object store: the cache's `--blob-store` and the logs
service's `--archive-store`. The volume-backed cache and log trees enforce no
allowance. A cache that verifies grants and keeps a `--blob-store` refuses to
start without `--controller`, because only the controller can count its
teams' bytes. Neither service keeps a count of its own, so a restart lists
nothing.

## What prunes automatically

Each pruner runs inside its service, never only from a CLI command, and a
second run over the same data removes nothing more.

| What | Rule | Where | Setting |
|---|---|---|---|
| Run events, node metrics | finished runs past the retention window | controller, hourly storage pass | `event_retention_days`, `node_metric_retention_days`; a multi-team controller writes 30 days where the operator set none |
| A run's event bytes | released from the team's event share once the run finished more than the retention window ago | controller, hourly storage pass | `event_retention_days` |
| Log files | a run's logs once they have gone unwritten for the retention window, on the volume and in the archive; a run the controller answered free when its logs were written keeps its archived logs 30 days at most, recorded with the run so a restart keeps the class | logs service | `--retention` on `sparkwing-logs`; 90 days with `--archive-store` unless set, off otherwise |
| Cache binaries, dependency archives and artifacts | written more than 30 days ago, including the default team and operator token's cache root | controller, hourly storage pass | `--cache-blob-store` |
| Registry proxy entries | past `--proxy-max-age`, and least recently served first past the byte cap | cache, hourly and on each store | `--proxy-max-age`, `--proxy-max-bytes` (`SPARKWING_CACHE_PROXY_MAX_BYTES`, 2 GiB) |
| Invitations | accepted, withdrawn or expired more than 30 days ago | controller, hourly storage pass | none |
| API, CLI and runner tokens | revoked or expired more than 30 days ago | controller, hourly storage pass | none |
| Browser sessions, GitHub runner credentials | expired | controller, hourly storage pass | none |
| Cron fire history | beyond 200 per schedule | controller, on each fire | none |
| Egress usage rows | older than 13 months | controller, monthly | none |
| Team download days | older than 7 days | controller, hourly storage pass | none |
| The cache's egress totals | days and months before last month | controller, hourly storage pass | none |
| Storage reservations | an hour after a writer took one it never committed or released | controller, on the team's next write and in the hourly storage pass | none |

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
| Logs, live and archived | the log share, 192 MiB by default, kept by evicting the team's least recently written archived runs | `pkg/store/team_storage.go`, `pkg/logs/log_quota.go` |
| Cache binaries, dependency archives and artifacts | the cache share, 768 MiB by default, refused before the body is read | `pkg/store/team_storage.go`, `internal/cache/blobquota.go` |
| Events per run | 256 KiB per event, 64 MiB and 50,000 events per run | `pkg/store/event_limits.go` |
| Logs per run | 64 MiB per node and 1 GiB per run on the logs service | `pkg/logs/limits.go` |
| Log files | 30 days after the last write for a team without credits, and the logs service's `--retention`, 90 days with an archive, for a funded team | `cmd/sparkwing-logs/main.go`, `pkg/logs/archive.go` |
| Run events past retention | removed with their bytes 30 days after the run finished on a multi-team controller | `pkg/controller/team_storage.go`, `pkg/store/storage_retention.go` |
| One cache object | 500 MiB per artifact or archive and 100 MiB per compiled binary, inside the share | `internal/cache/cache.go`, `internal/cache/blobstore.go` |
| Team cache objects | 30 days after they were written | `pkg/controller/storage_pass.go` |
| Git mirrors | registered by the operator only; a team's grant cannot add one | `internal/cache/gitcache.go` |
| Registry proxy directory | shared by every team; 2 GiB, least recently served evicted first, and 7 days per entry | `internal/cache/proxycap.go` |
| Registry proxy churn | the proxy takes no credential, because runner pods carry none, and is served inside the cluster only: the runner-bundle chart refuses a cache Service other than `ClusterIP` unless `cache.dependencyProxy.enabled=false`, which starts the cache with `--disable-proxy`. The cache's daily egress alarm reports what callers pull through it and refuses nothing | `internal/cache/cache.go`, `charts/sparkwing-runner-bundle/templates/validate.yaml`, `cmd/sparkwing-cache/main.go` |
| Bytes downloaded by one team | 5 GiB a UTC day for a free team (`--team-daily-download-free-bytes`), 50 GiB for a funded team (`--team-daily-download-funded-bytes`), counted per team in the controller's database across the cache, the controller's artifact and git proxy routes, and signed URLs; log reads are not counted. A download is charged the bytes it delivers, 8 MiB at a time as it streams; signing a URL charges the stored object's size. Only a team already at its cap is refused, so a download under way always finishes and the team's next request gets `429` until midnight UTC. A free team's request gets `503` while the controller cannot answer. A 60-second URL can be fetched more than once, so the counter is an abuse limit rather than an exact bandwidth meter | `pkg/store/team_storage.go`, `pkg/controller/data_download.go` |
| Bytes the cache serves | an alarm, not a refusal, at 200 GiB a day per cache pod that verifies grants unless the operator names another threshold; with `--controller` the day's and the month's totals survive a restart, without one they are per process | `cmd/sparkwing-cache/main.go`, `internal/cache/egress_day.go` |
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

- **Run rows.** Retention removes a run's events and releases its bytes, but
  the run and its nodes stay, so run history grows with every run a team
  starts; the per-principal hourly run budget on a limits profile bounds it.
- **Monthly storage totals and the credit ledger.** One row per team per month
  and one row per charge, kept for billing.
