# Observability

Sparkwing tracks run health, failure reasons, and resource usage so you
can debug failures fast and right-size containers.

## Assisted-offer lifecycle

Enrolled-executor arbitration writes transition events to the run stream. The
events carry node requirements, the round priority target, safe executor
display fields (`executor_name`, `executor_kind`, and `executor_location`), and
effective scores. Events never carry a credential, token prefix, principal,
holder, membership ID, internal controller or executor ID, or reservation ID.

| Event | Meaning |
|---|---|
| `executor_offer_round_opened` | The controller opened a five-second round and recorded its exact highest attainable effective priority. |
| `executor_offer_received` | An eligible executor supplied a new capacity-backed offer. Refreshing the same offer does not append another event. |
| `executor_offer_expired` | The offer stopped refreshing for two seconds and was removed. |
| `executor_offer_declined` | Current enrollment or claim validation rejected the offer, or a higher-ranked offer won. The safe `reason` field distinguishes those cases. |
| `executor_offer_awarded` | The offer won at the recorded priority target or at the deadline. |
| `executor_offer_round_empty` | The deadline had no live eligible offer, so the existing coordinator fallback took ownership. |

## Failure reasons

A failed node carries a `failure_reason` when the controller could
classify the failure. The classification is automatic, so the common
infrastructure failures are named rather than left for you to find in
the logs.

| Reason | What happened | What to do |
|---|---|---|
| `oom_killed` | Container exceeded its memory limit and was killed by the kernel (exit 137). | Raise the runner memory limit or reduce the pipeline's memory use; check the resource chart. |
| `timeout` | Node exceeded its configured execution timeout. | Raise the timeout or optimize the pipeline. |
| `no_progress_timeout` | Node emitted no observable progress for its configured inactivity window. | Check where the node stopped, stream command output or report progress when work is healthy, and raise the inactivity window if the expected quiet period is longer. |
| `agent_lost` | The agent or gateway stopped heartbeating. The source node is terminal; a fresh linked run may retry it within `.Retry(n)`. | Check the executor and `agent_loss_*` events. A post-start retry is at-least-once and spends each acknowledged invocation. |
| `queue_timeout` | Either a node waited past its concurrency group's `OnLimit: Queue` timeout without getting a slot, or no runner claimed the node within the controller's queue deadline (default 15m). The node's error text names which. | For a concurrency wait, raise the group's capacity or its queue timeout. For an unclaimed node, ensure runners are up and their advertised `--label` set satisfies the pipeline's `requires:` / node `.Requires()`. |
| `runner_lease_expired` | The worker that claimed this run's *trigger* stopped renewing its lease. The controller returns the trigger to the pending queue and cascade-fails every node the run had not finished. | Check the worker that claimed the trigger. The trigger is re-claimable; this run is terminal. |
| `verify` | The node's action completed, but its `Verify` postcondition returned an error -- the failure is at the verify stage, not the action. | Inspect the `Verify` assertion and the action's actual output. |
| `logs_auth` | The runner's log-append calls were rejected (401/403) by the controller, so the run's structured logs are unrecoverable. | Check the runner token's `logs.write` scope; the run fails loud rather than reporting success with no output. |
| `logs_dropped` | The log store stayed unreachable past the append retry budget, so log lines were lost. The node's own work may have succeeded; its record of that work is incomplete. | Check the logs backend named in the run's `invocation.backends` -- for `s3`, the bucket, `AWS_REGION`, credentials, and `SPARKWING_S3_ENDPOINT`. The `logs_drop` event carries the lost-line count and the first error. Set `SPARKWING_LOGS_DROP_POLICY=warn` to keep such runs green instead. |

A plain pipeline-level failure (a failed test or command) carries no
structured `failure_reason` -- read the logs.

### How detection works

The Kubernetes runner polls its Job while the node runs, surfacing the
pod phase (including `ImagePullBackOff` and friends) as a status detail.
When the Job reaches a terminal condition and the pod did not write its
own terminal node row, the runner inspects the pod's terminated
containers: an `OOMKilled` container records `oom_killed` with the
container's exit code (137 when the API reports none), and any other
non-zero exit records the exit code with no structured reason. Either
way the node fails as soon as the Job terminates rather than waiting for
the heartbeat sweep.

For nodes where the pod disappears entirely (node failure, eviction),
the controller's heartbeat sweep catches the missed lease and marks the
node `agent_lost`.

An assisted executor records `execution_attempts` on the node, and so does the
local orchestrator for a node it runs itself: those entries carry executor kind
`local`, location `local`, and the host name as the executor. Each entry has
the global attempt ordinal, source or retry run, executor
name and kind, controller-owned location, timestamps, outcome, failure reason,
and retry link. Missing legacy attribution remains unknown. Node read responses
and `state.ndjson` node records omit claim generations and all coordinator,
membership, holder, token, reservation, and internal executor identifiers.
Public event reads also omit claim generations from executor-selection and
attempt-lifecycle payloads.

The ordered event stream records `executor_selected`,
`execution_attempt_started`, `agent_lease_lost`, pre-start requeue or
post-start scheduling with availability, and exhaustion or another explicit
no-retry reason. These payloads carry attribution, not credentials.

Remote logs use immutable substreams keyed by claim generation and execution
attempt. A stale append is rejected, while an append that had already passed
validation before lease loss can finish only in the old attempt's history. The
normal log read and stream merge those substreams; exact reads may select
`attempt` with `claim_generation`, `attempt` with `trigger_generation` for
trigger-owned node work, or `trigger_generation` alone for `_compile` output.

### API

`GET /api/v1/runs/{id}/nodes` returns each node with its reason and exit
code as top-level fields:

```json
{
  "nodes": [
    {
      "id": "build",
      "status": "done",
      "outcome": "failed",
      "error": "pod sparkwing-build-0 OOMKilled",
      "failure_reason": "oom_killed",
      "exit_code": 137
    }
  ]
}
```

Logs are not part of this payload; fetch them separately with
`sparkwing runs logs --run <id>` or from the logs service.

## Failure excerpts

A node that fails while running a command records a bounded excerpt of
that command's output -- the last 20 lines, at most 4 KiB, with resolved
secret values redacted. The node's `error` carries it as text, led by
the failure headline and, when output was dropped, a marker naming the
`sparkwing runs logs` command that prints the whole thing.

`sparkwing runs errors -o json` and `sparkwing runs status -o json` also
carry the excerpt as structured fields, so a consumer does not have to
parse the error string:

```json
{
  "node": "build",
  "outcome": "failed",
  "error": "build: command failed (exit 2): go build ./...\n… earlier output omitted (see: sparkwing runs logs --run run-... --node build)\npkg/thing/file_300.go:12: undefined: Helper\nFAIL",
  "log_excerpt": "pkg/thing/file_300.go:12: undefined: Helper\nFAIL",
  "log_excerpt_truncated": true
}
```

`log_excerpt` is the raw excerpt without the headline or marker;
`log_excerpt_truncated` reports whether output was dropped. Both fields
are **absent together** when there is nothing to excerpt -- a node that
failed with a plain error rather than a command, and any node that did
not fail on its own (a cancelled or upstream-failed node never gets an
excerpt, and nothing reads its logs to invent one). The failure itself
is always reported; only the excerpt can be missing.

Excerpts travel as a `node_failure_excerpt` run event, so they read back
identically from a local run store and from a controller.

### When an excerpt cannot be read

Absence normally means "this node published no excerpt". Where that
cannot be established, the failed node carries
`"log_excerpt_unavailable": true` instead, and never a fabricated
excerpt. Two cases produce it:

- **A run with more than ~50,000 events.** The lookup scans the run's
  event stream and stops after 50 pages of 1,000. It also stops as soon
  as every failed node has its excerpt, so only a run that is both
  enormous and failing late is affected.
- **An event stream that cannot be read** -- a controller that is down
  or rejects the request.

One case reports plain absence even though an excerpt might have
existed: a run **mirrored to S3-backed state** (`DumpRunState`) carries
its runs and nodes but no events, so a mirrored run reads back with no
excerpts at all. The node's `error` still carries the excerpt as text.

## Resource usage metrics

While a node runs, the runner samples the executing process in-process
every 2 seconds. Samples are stored and charted in the dashboard. No
cluster metrics-server is involved.

### What's measured

- **CPU**: millicores from `getrusage`, covering the runner process and
  the commands it spawned, clamped to the host's core count so a large
  reaped subtree cannot register as an impossible rate.
- **Memory**: resident bytes -- `/proc/self/statm` on Linux, `ps -o rss=`
  on macOS, and the Go runtime's system reservation where neither is
  available. Both platform sources report the footprint at the moment of
  the sample, not a high-water mark.

One sampler runs per process, and each interval's reading is split
evenly among the nodes attached to it. A plan-level node of a local run
is its own process and a cluster node is its own pod, so there is one
attachment and the chart is that node's exact usage. Several nodes do
share one process in three shapes: a `JobSpawn` child runs inside its
parent's process while the parent is still attached; `sparkwing cluster
worker --runner inprocess` and `sparkwing handle-trigger --runner
inprocess` (the default runner kind for both) run every node of a
claimed trigger in the one worker process; and a test binary or a
program embedding the SDK runs nodes inside itself. There each node's
chart is an estimate of its share -- the shares still sum to what the
process drew, which is what right-sizing and admission need -- and a
node joining or leaving between ticks can land up to one interval of
its cost on the nodes beside it.

Sampling every 2 seconds cannot see everything. A node shorter than one
interval produces no samples at all, and CPU burned by a command that
started and finished between two ticks may not appear in either. So a
node's row also records what the kernel charged its process at exit --
total CPU time, peak resident set, and the span the process existed for
-- which the capacity fold reads as a floor under the sampled figures.
Those exit figures exist only where sparkwing supervised a process,
which today means local runs; a pod reports none and prices from its
samples alone.

The exit figures cover the whole process, including runtime startup,
plan rebuild, and teardown, which is why the span is recorded with them
rather than taken from the node's own start and finish timestamps: those
are stamped from inside the process once startup is done, and dividing
the process's whole CPU by that narrower window would report a draw the
machine never gave. The node's recorded duration is the process's whole
life for the same reason -- it is how long the box was occupied. A node
retried in place accumulates every attempt's CPU and occupancy, since
the machine ran them all, while the peak stays a high-water.

Per-command reports and sampler ticks are folded differently when a run
is priced. A tick is a rate that already covers its window, so
concurrent nodes' ticks add up. A command report is a rate over the
command's own span, so the fold integrates it instead: four 400ms
commands at two cores each, run back to back inside one window, are 1.6
cores of that window rather than the eight their rates would add to.
Command memory is a lifetime high-water, so one node's several reports
in a window contribute the largest, not the sum.

### API

`GET /api/v1/runs/{id}/nodes/{nodeID}/metrics` (see
[api-reference.md](api-reference.md)) returns the sample points:

```json
{
  "points": [
    { "ts": "2026-04-12T10:00:00Z", "cpu_millicores": 450, "memory_bytes": 536870912 },
    { "ts": "2026-04-12T10:00:02Z", "cpu_millicores": 1200, "memory_bytes": 1073741824 }
  ]
}
```

### Using metrics to right-size containers

1. Run your pipeline a few times
2. Open the run detail in the dashboard and expand **Resources**
3. Compare peak usage to your pod's configured limits:
   - If peak memory is close to the limit → increase the limit or
     optimize memory usage
   - If peak CPU is well below the limit → you can safely lower requests
     to save cluster resources
   - If CPU is consistently at the limit → the pipeline is CPU-bound;
     increase the limit so the run is not throttled

## Dashboard

The dashboard shows failure information where a run's detail is:

- **Runs page**: a failure-reason badge on each failed node, both in the
  node list and in the selected-node panel, with the exit code when one
  was recorded.
- **Resources**: a collapsible CPU/memory chart per node on the run
  detail, with peak and average in the header; it refreshes while the
  node is running.

It also shows what admission is doing with the machine:

- **Queue page**: the live admission queue -- every resource with its
  headroom, every holder, every lease connected without holding resources,
  and every waiter in order with its ETA. Mirrors `sparkwing queue`.
- **Fleet section**: registered executors with their configured policy, observed
  liveness and headroom, and current slot and run activity in separate panels.
  Legacy executors inferred from recent activity stay visible without invented
  policy. The API does not expose a distinct headroom observation time, so the
  view reports whether the controller considers headroom live, stale, or
  absent without fabricating a timestamp.

The run node list and DAG mark execution location with both text and color.
Selecting a node shows every durable execution attempt, including the executor
kind and name, timestamps, outcome, and retry link when the controller recorded
one. A recorded platform appears with its attempt; a missing platform remains
unknown. The dashboard reads this history from explicit public execution
attribution. It does not derive location from transient claim ownership; an
older record with no attribution is shown as unknown.

- **Capacity page**: the same host ledger with the subtraction behind
  each Available cell written out, then every measured pipeline with the
  charge it resolves to (the live form of `sparkwing runs stats
  --capacity`), and, per pipeline, the stored sample window with the run
  each percentile charge was ranked out of marked. Use it to check a
  charge by hand: the p95 the panel marks and the price it shows come
  from the rows on screen, so a charge no sample supports is visible
  rather than inferred. Host figures refresh every 2 seconds; learned
  pricing every 10. With no daemon running the host section reports that
  and the pricing table still reads from the runs store.

## Data retention

Finished runs (and their metrics) are kept until you prune them. There
is no automatic time-based cleanup; use `sparkwing runs prune` to delete
runs past a threshold or by id (see [cli-runs.md](cli-runs.md)).

## OpenTelemetry

Every sparkwing service initializes OpenTelemetry and exposes a
Prometheus `/metrics` endpoint. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to
additionally export traces and structured logs via OTLP.

### Prometheus /metrics

Always active on every service; scrape it with your Prometheus.

The controller serves `/metrics` unauthenticated on its API listener, so
an ingress fronting that listener publishes pipeline names to anyone who
asks. `sparkwing-controller --metrics-addr 127.0.0.1:9090`
(`$SPARKWING_METRICS_ADDR`) moves the endpoint onto its own listener,
which you can bind to a pod-local or cluster-internal address and leave
out of the ingress. The API listener then answers `401` for `/metrics`
when authentication is on, and `404` when it is off. In `sparkwing-full`,
`controller.metricsPort` passes that flag and exposes the port on the
controller container only; the controller Service does not publish it,
so an in-cluster scraper reaches it by pod address. The controller
refuses to start when that port is already taken.

### OTLP export

When `OTEL_EXPORTER_OTLP_ENDPOINT` is set, services export over OTLP/HTTP
to that endpoint:

- **Traces** via `otlptracehttp` (run + HTTP spans).
- **Logs** via `otlploghttp` (structured logs with trace correlation).

Metrics stay on the Prometheus `/metrics` endpoint. There is no
in-cluster OTEL collector required; point the OTLP endpoint at whatever
backend you run (e.g. Tempo for traces, Loki for logs).

### Metrics reference

**Controller** (`sparkwing-controller`, Prometheus):

| Metric | Type | Description |
|--------|------|-------------|
| `sparkwing_runs_total` | Counter | Runs that reached a terminal state, by pipeline and status |
| `sparkwing_run_duration_seconds` | Histogram | End-to-end wall time from create to finish |
| `sparkwing_nodes_claimed_total` | Counter | Successful node claims |
| `sparkwing_pending_nodes` | Gauge | Claim-queue depth (ready, unclaimed nodes) |
| `sparkwing_active_runners` | Gauge | Distinct runners with a non-expired lease in the last 2 minutes |
| `sparkwing_http_requests_total` | Counter | HTTP requests by route, method, status |
| `sparkwing_http_request_duration_seconds` | Histogram | HTTP latency by route and method |
| `sparkwing_object_store_requests_total` | Counter | Object-store requests by class (`put`, `get`, `list`, `delete`) and whether the budget let them reach the store |
| `sparkwing_object_store_trips_total` | Counter | Times a request class exhausted its budget and began refusing requests |
| `sparkwing_object_store_tripped` | Gauge | 1 while a request class is refusing requests |
| `sparkwing_object_store_bucket_bytes` | Gauge | Bytes the bucket holds, counted per write and replaced by each measurement |
| `sparkwing_object_store_bucket_objects` | Gauge | Objects the bucket holds, counted per write and replaced by each measurement |
| `sparkwing_object_store_bucket_ceiling` | Gauge | The configured ceiling, by `unit` (`bytes`, `objects`). Absent while the bucket is unlimited |
| `sparkwing_object_store_bucket_ceiling_frozen` | Gauge | 1 while the bucket is over its ceiling and object writes are refused |
| `sparkwing_object_store_bucket_ceiling_freezes_total` | Counter | Times the bucket crossed its ceiling and began refusing object writes |
| `sparkwing_object_store_bucket_ceiling_refused_total` | Counter | Object writes the ceiling refused |

The `route` label is the pattern the controller registered the request
against, so every path parameter reaches Prometheus in its declared form
(`/api/v1/concurrency/{key}/state`, `/api/v1/runs/{id}`) and a run id,
concurrency key or artifact digest never becomes a label value. A request
that matches no route is labeled `other`, and so is any `method` outside
the seven the controller answers, so neither an unrecognized path nor an
invented request method can grow the series count.

**Cache** (`sparkwing-cache`, OTEL meter):

| Metric | Type | Description |
|--------|------|-------------|
| `sparkwing.gitcache.archives_served` | Counter | Archive downloads |
| `sparkwing.gitcache.files_served` | Counter | Single-file downloads |
| `sparkwing.gitcache.fetch_duration` | Histogram | Background fetch time |
| `sparkwing.gitcache.cache_hits` | Counter | Cache hits (git archive and binary/dependency, distinguished by `type` attribute) |
| `sparkwing.gitcache.cache_misses` | Counter | Cache misses (git archive and binary/dependency, distinguished by `type` attribute) |
| `sparkwing.gitcache.recovery_reclones` | Counter | Full mirror re-downloads after a failed fetch, by `repo` hash. Should be near zero -- a repo that keeps appearing here has a persistent fetch failure (see [Cache](gitcache.md#recovery-reclone-circuit-breaker)) |

## Object-store request budget

Every object-store client a Sparkwing process builds draws on one
request budget. The budget counts `put`, `get`, `list` and `delete`
separately, each against a per-minute rate and a per-day total. A class
that spends either budget trips: further requests of that class are
refused with an error naming the class and the limit, and nothing is
queued. Classes are independent, so a tripped `put` budget leaves reads
serving until their own budget trips.

The budget counts billed attempts, not API calls. It sits inside the AWS
SDK's retry loop, so a call the SDK re-sends four times spends four
units, which is what the object store charges for.

The guard exists because a retry loop against a failing bucket bills per
request and the bill arrives hours later. Defaults sit far above normal
load and far below a loop with no sleep:

| Class | Per minute | Per day |
|--------|------|-------------|
| `put` | 1200 | 200000 |
| `get` | 3000 | 500000 |
| `list` | 600 | 100000 |
| `delete` | 600 | 100000 |

Override any of them with `SPARKWING_OBJECT_STORE_<CLASS>_PER_MINUTE`
and `SPARKWING_OBJECT_STORE_<CLASS>_PER_DAY`, where `<CLASS>` is `PUT`,
`GET`, `LIST` or `DELETE`. A value of `0` removes that budget.
A class tripped by its per-minute rate clears when that minute rolls, so
one burst costs a minute of refusals rather than a day of them; the day
budget still bounds the total. A class tripped by its per-day budget is
the one `SPARKWING_OBJECT_STORE_TRIP_RESET` governs: `day` (the default)
clears it when the day window rolls, `manual` keeps it until a reset.
`SPARKWING_OBJECT_STORE_BREAKER=off` counts requests without refusing
any, which is the escape hatch for a process that must finish past a
tripped budget.

### One budget per process

The budget belongs to a process, not to a cluster or a bucket. A
controller, each `sparkwing run`, each worker and each runner agent
builds its own from the same environment, and they do not aggregate: N
processes on the same bucket can spend N budgets. Size the numbers for
what one process should ever need, and read the AWS-side budget action
and CloudWatch alarm as the layer that sees the whole account.

The same split decides the remedies. `sparkwing cluster object-store
reset-breaker` reaches the controller process and nothing else, so a
refusal inside a local `sparkwing run` clears with
`SPARKWING_OBJECT_STORE_BREAKER=off` or a raised limit on that process.
The refusal message names both.

The controller reports its own budget on `GET /api/v1/health` under
`object_store`, names every tripped class in `problems`, and exports the
three `sparkwing_object_store_*` metrics above. The health summary names
the classes and not their limits, because that route answers without a
token. A state outbox that has given up replaying appears there too,
under `object_store.stalled` and as a problem naming the path and when
it stalled.

Read a controller's budget, and clear a tripped one, with:

```bash
sparkwing cluster object-store status --profile prod
sparkwing cluster object-store reset-breaker --profile prod
```

A reset clears every tripped class and both window counters, and thaws a
frozen bucket ceiling; lifetime request and trip totals survive so the
metrics keep their history. A budget that keeps tripping wants a larger
limit or a caller that stops retrying, not a repeated reset.

## Storage ceilings

Per-team quotas bound each team; a thousand teams under quota still add
up, and one quota bug reaches every team at once. A storage ceiling
bounds the total. Each service that stores what pipelines produce holds
its own store to a byte ceiling and an object-count ceiling, and freezes
writes once a measurement reaches either one: existing runs finish,
further writes are refused with an error naming the ceiling and the
measurement, and health reports the freeze. Reads and deletes keep
working, because deleting is how a store gets back under its ceiling.

| Service | What it bounds | Refusal | State on |
|--------|------|-------------|------|
| `sparkwing-cache` | the artifact, dependency-archive and upload trees | `507` on upload | `GET /health` (`store_ceiling`), `sparkwing.cache.store_*` metrics |
| `sparkwing-logs` | the whole log store | `507` on append | `GET /api/v1/health` (`store_ceiling`), `sparkwing_logs_store_*` metrics |
| `sparkwing-controller` | the object store it writes through, on the BYO-backend path | the write fails with the ceiling error | `GET /api/v1/health` (`object_store.ceiling`), `sparkwing_object_store_bucket_*` metrics |

Each of the three reports `frozen`, `warning` and
`measurement_incomplete` on its health route and raises a `problems`
entry for each, so a frozen or warning store shows as degraded wherever
health is read. The services also carry their counted bytes and objects
and the time of the last measurement; the controller keeps its totals on
the admin-scoped breaker route, because its health answers without a
token.

The three are independent: each measures the store it owns and freezes
only its own writes, so no service waits on another to decide. The
controller's ceiling is the one that reaches an S3 bucket; the services'
ceilings bound the volumes they write.

### The controller's bucket ceiling

Every ceiling is unlimited by default, so an install that sets nothing
sees no change. Set the controller's with:

| Flag | Environment | Meaning |
|--------|------|-------------|
| `--max-bucket-bytes` | `SPARKWING_OBJECT_STORE_MAX_BUCKET_BYTES` | Stored bytes at or above which object writes freeze |
| `--max-bucket-objects` | `SPARKWING_OBJECT_STORE_MAX_BUCKET_OBJECTS` | Objects at or above which object writes freeze |
| `--warn-bucket-bytes` | `SPARKWING_OBJECT_STORE_WARN_BUCKET_BYTES` | Stored bytes at which health reports a warning |
| `--warn-bucket-objects` | `SPARKWING_OBJECT_STORE_WARN_BUCKET_OBJECTS` | Objects at which health reports a warning |
| `--bucket-reconcile` | `SPARKWING_OBJECT_STORE_BUCKET_RECONCILE` | Gap between bucket measurements, hourly by default |
| `--bucket-store` | `SPARKWING_OBJECT_STORE_URL` | Store URL the measurement reads, such as `s3://bucket/prefix` |

Counting costs nothing per request. Every write the process sends adds
its own bytes to a running total, and the controller replaces that total
with a measured one on the reconciliation interval, because the running
count drifts: an overwrite counts its key twice and a delete cannot know
what it removed. A multipart upload counts its bytes on the parts and
its key on the completion, so one upload counts once; an abort counts
nothing and is never refused, because aborting is how a frozen bucket
sheds an upload in flight.

The measurement is one paginated listing of the artifact store, which
object stores bill per thousand keys, so an install that wants the
ceiling without the listing sets `--bucket-reconcile 0` and accepts the
drift. `--bucket-store` names the store the measurement reads; the
controller reads it on the interval and serves none of it, and a
controller pointed at no store, or at a backend that cannot total
itself, keeps the running count. The running count starts at zero on
restart, so a controller with no measurement source sees only what it
has written since it started.

Every Sparkwing process reads the same environment, so a runner handed
`SPARKWING_OBJECT_STORE_MAX_BUCKET_BYTES` refuses its own writes above
the ceiling too. It counts only what it has written since it started,
because the measurement belongs to the controller; treat that as a
backstop on one process rather than a second view of the bucket.

Three properties keep the measurement from becoming the cost it bounds.
One replica measures per window: the controllers claim a store-wide
lease, so N replicas cost one listing rather than N. The measurement's
requests sit outside the object-store request budget, because totalling
a million-object bucket spends twice the per-minute list budget in one
pass and would otherwise leave every other reader refused for the rest
of the minute. And the walk itself is bounded, at a thousand listings
and at half the reconciliation interval, whichever comes first.

A measurement that stops at either bound is discarded rather than folded
in, because a total short of the truth would thaw a store that is still
full. The ceiling then reports `measurement_incomplete` on health and in
`sparkwing_object_store_bucket_ceiling_measurement_incomplete`, and
keeps counting writes until a measurement finishes. A bucket that keeps
reporting incomplete wants a longer `--bucket-reconcile`, a narrower
prefix, or S3 Inventory in place of the listing.

`GET /api/v1/health` reports `object_store.ceiling` as `frozen` and
`warning` alone, because that route answers without a token; the totals
and the ceilings sit behind the admin-scoped
`GET /api/v1/object-store/breaker` and on `sparkwing cluster
object-store status`, which also reports `counted_at` (when the running
count last moved) and `reconciled_at` (when the bucket was last
measured). Alarm on
`sparkwing_object_store_bucket_ceiling_frozen` for the freeze and on
`sparkwing_object_store_bucket_bytes` against
`sparkwing_object_store_bucket_ceiling` for the approach; S3 publishes
`BucketSizeBytes` and `NumberOfObjects` daily at no charge, which is the
same signal from the account side.

Clearing a freeze is an operator decision:

```bash
sparkwing cluster object-store reset-breaker --profile prod
```

A thaw is refused while no measurement is scheduled
(`--bucket-reconcile 0`), because nothing would ever end it and the
ceiling would be off until the process restarts; raise the ceiling
instead. Otherwise it thaws the bucket and holds the thaw until the next
measurement: writes counted in between do not freeze it again, so the
operator gets the whole window to act. The measurement then decides, and freezes again
while the bucket is still over the ceiling. Raise the ceiling on the
controller to keep writes flowing, or delete objects until the
measurement falls back under it.

### The cache and logs service ceilings

The hosted write path does not run through the controller: a runner
uploads artifacts and dependency archives to `sparkwing-cache` and posts
log lines to `sparkwing-logs`, and each service stores them itself. Each
therefore carries its own ceiling and refuses its own writes with `507`
and an error naming its own flags.

| Service | Flags | Environment |
|--------|------|-------------|
| `sparkwing-cache` | `--max-store-bytes`, `--max-store-objects`, `--warn-store-bytes`, `--warn-store-objects`, `--store-reconcile` | `SPARKWING_CACHE_MAX_STORE_BYTES` and the matching names |
| `sparkwing-logs` | `--max-store-bytes`, `--max-store-objects`, `--store-reconcile` | `SPARKWING_LOGS_MAX_STORE_BYTES` and the matching names |

Each service counts what it stores as it stores it and walks its own
trees on `--store-reconcile` (hourly by default, `0` measures once at
startup). The walk is local file I/O rather than billed requests, and a
measurement that finds the store back under its ceiling thaws it.
`--warn-store-bytes` and `--warn-store-objects` mark the store as
warning on health without refusing anything. The chart carries all of
these as `cache.limits.*` and `logs.limits.*`.

Neither service waits out the interval to recover. Deleting a run with
`DELETE /api/v1/logs/{runID}`, or letting the sweeper delete it under
`--retention`, measures the log store again on the spot. The cache
serves no delete of its own, so it carries two bearer-gated admin
routes: `POST /admin/store-ceiling/measure` walks the trees now, which
is what turns freeing space on the volume into uploads flowing again,
and `POST /admin/store-ceiling/thaw` accepts uploads until the next
measurement. Both answer with the ceiling state, and the thaw is refused
with `409` when no measurement is scheduled.
