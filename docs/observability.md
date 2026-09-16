# Observability

Sparkwing tracks run health, failure reasons, and resource usage so you
can debug failures fast and right-size containers.

## Step-state recording failures

The node log emits a `state_write_failed` warning when a step start, finish,
or skip cannot be written to the state backend. The record names the node and
step, with `run_id`, `operation`, and a bounded, secret-masked `error` in its
attributes. Inspect these warnings when stored step state disagrees with the
execution log.

The original lifecycle event and work outcome remain unchanged because the
write records work that already happened. The warning does not repair the
missing state. It uses the underlying log path, so loss of that log path can
also prevent delivery of this diagnostic.

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
| `credits_unpriced_class` | A claim was refused because the node's cpu request is above the largest class the credit rate table prices. Carries `cores` and `max_cores`. |

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
| `credits_exhausted` | The controller's prepaid credit balance ran out and the node was cancelled after the grace period. Its claim is released and its offers are withdrawn. | Add credits, then re-run. A node cancelled this way held no slot afterwards, so nothing is left to reclaim. |
| `unpriced_cpu_class` | The node asks for more cpu than the largest class the credit rate table prices, and the runner that tried to take it reported nothing smaller, so no claim could be billed. | Add the class with `sparkwing cluster credits settings --rate-table`, or lower the node's cpu request, then re-run. The `credits_unpriced_class` event carries the request and the ceiling. |
| `runner_lease_expired` | The worker that claimed this run's *trigger* stopped renewing its lease. The controller returns the trigger to the pending queue and cascade-fails every node the run had not finished. | Check the worker that claimed the trigger. The trigger is re-claimable; this run is terminal. |
| `verify` | The node's action completed, but its `Verify` postcondition returned an error -- the failure is at the verify stage, not the action. | Inspect the `Verify` assertion and the action's actual output. |
| `logs_auth` | The runner's log-append calls were rejected (401/403) by the controller, so the run's structured logs are unrecoverable. | Check the runner token's `logs.write` scope; the run fails loud rather than reporting success with no output. |
| `credits_exhausted` | The credit balance stayed at zero past the grace period, so the controller cancelled the node. | Grant credits (`sparkwing cluster credits grant`) and rerun. The run's `credits_exhausted` event carries the balance and how long it had been spent. |
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

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `sparkwing_runs_total` | Counter | `pipeline`, `status` | Runs that reached a terminal state |
| `sparkwing_run_duration_seconds` | Histogram | `outcome`, `pipeline` | End-to-end wall time from create to finish |
| `sparkwing_nodes_claimed_total` | Counter | `pipeline` | Successful node claims |
| `sparkwing_pending_nodes` | Gauge | (none) | Claim-queue depth (ready, unclaimed nodes) |
| `sparkwing_active_runners` | Gauge | (none) | Distinct runners with a non-expired lease in the last 2 minutes |
| `sparkwing_http_requests_total` | Counter | `method`, `route`, `status` | HTTP requests the controller answered |
| `sparkwing_http_request_duration_seconds` | Histogram | `method`, `route` | HTTP handling latency |
| `sparkwing_object_store_requests_total` | Counter | `class`, `outcome` | Object-store requests by class (`put`, `get`, `list`, `delete`) and whether the budget let them reach the store |
| `sparkwing_object_store_trips_total` | Counter | `class` | Times a request class exhausted its budget and began refusing requests |
| `sparkwing_object_store_tripped` | Gauge | `class` | 1 while a request class is refusing requests |
| `sparkwing_object_store_bucket_bytes` | Gauge | (none) | Bytes the bucket holds, counted per write and replaced by each measurement |
| `sparkwing_object_store_bucket_objects` | Gauge | (none) | Objects the bucket holds, counted per write and replaced by each measurement |
| `sparkwing_object_store_bucket_ceiling` | Gauge | `unit` | The configured ceiling, by `unit` (`bytes`, `objects`); 0 while that unit is unlimited |
| `sparkwing_object_store_bucket_ceiling_frozen` | Gauge | (none) | 1 while the bucket is over its ceiling and object writes are refused |
| `sparkwing_object_store_bucket_ceiling_freezes_total` | Counter | (none) | Times the bucket crossed its ceiling and began refusing object writes |
| `sparkwing_object_store_bucket_ceiling_refused_total` | Counter | (none) | Object writes the ceiling refused |
| `sparkwing_object_store_bucket_ceiling_measurement_incomplete` | Gauge | (none) | 1 while the last measurement stopped early and was discarded |
| `sparkwing_auth_token_cache_total` | Counter | `result` | Bearer verifications by how the verified-token cache answered them (`hit`, `miss`, `coalesced`) |
| `sparkwing_auth_hashing_rejected_total` | Counter | (none) | Credential verifications the argon2id memory budget shed rather than queued, answered `503` with a `Retry-After` |
| `sparkwing_principal_throttled_total` | Counter | `route_class` | Requests a request budget refused with `429` (`claim`, `heartbeat`, `idle_poll`, `token`) |
| `sparkwing_request_rate_alarm_total` | Counter | (none) | Minutes in which the controller served more requests than `--requests-per-minute-alarm`; it refuses nothing |
| `sparkwing_queue_depth` | Gauge | `state` | Nodes short of a terminal outcome: `waiting`, `ready`, `claimed`, `running`, `approval_pending` |
| `sparkwing_node_claim_wait_seconds` | Histogram | (none) | Seconds a node waited between becoming claimable and its first runner taking it |
| `sparkwing_claim_unavailable_total` | Counter | (none) | Claim requests answered `503`, which a runner retries after the interval the response names |
| `sparkwing_runners_live` | Gauge | `label_set` | Runners that polled for a claim inside the liveness window, by the label set they advertised |
| `sparkwing_live_runners` | Gauge | (none) | Runners that polled for a claim inside the liveness window, across every label set |
| `sparkwing_node_seconds_total` | Counter | `placement` | Node execution seconds: `cloud` is what the ledger has finished charging for, `local` is what this controller process settled for unmetered credentials |
| `sparkwing_credits_balance_micro` | Gauge | (none) | Micro-credits left to spend |
| `sparkwing_credits_granted_micro_total` | Counter | `kind` | Micro-credits granted over the ledger's life, by whether the operator paid for the grant (`free`, `paid`) |
| `sparkwing_credits_reversed_micro_total` | Counter | (none) | Micro-credits refunded payments took back out of the ledger, reported positive; a reversal never joins the granted series, so summing that series over `kind` stays the money paid in |
| `sparkwing_credits_reserved_micro_total` | Counter | (none) | Micro-credits claims reserved up front |
| `sparkwing_credits_charged_micro_total` | Counter | (none) | Micro-credits execution billed |
| `sparkwing_credits_refunded_micro_total` | Counter | (none) | Micro-credits returned from the unused tail of a claim reservation |
| `sparkwing_credits_storage_micro_total` | Counter | (none) | Micro-credits retained bytes billed; this series buys no runner time, so the charged series does not carry it |
| `sparkwing_requests_by_principal_total` | Counter | `credential` | Requests that authenticated, by the kind of credential behind them: `user`, `runner`, `service` or `other` |

`sparkwing_node_seconds_total{placement="cloud"}` is the billing line, and it
comes from the credit ledger rather than from a request handler. A claim
reserves a minute up front and a finish refunds the part the node did not use,
so the series counts a reservation as the node consumes it rather than all at
once: the figure only ever grows, which is what a counter has to do. A node the
credit-exhaustion sweep cancels settles there. A node whose lease expires keeps
every second its reservation charged, because the requeue writes no refund, and
it books the unconsumed part at once rather than over the minute: the cloud
series overstates real runner time by up to one reservation per lease expiry. A
node whose run fails with no finish reaching the controller keeps the same
seconds, and the reserved minute enters the total as the clock passes it.

The `local` series counts what this controller process settled for an unmetered
credential, read from the claiming credential recorded on the node, and it
restarts at zero with the process. A node whose claiming credential has since
been revoked counts under neither placement, because guessing would bill the
operator's own capacity for cloud work. Only a metered credential moves
credits, so the `sparkwing_credits_*` totals are paid work by construction and
carry no free-versus-paid split; the split an operator wants is
`sparkwing_node_seconds_total{placement}` for work and
`sparkwing_credits_granted_micro_total{kind}` for funding.

`sparkwing_node_claim_wait_seconds` measures from `placement_hold_from`, the
instant the node became claimable, which survives the `ready_at` bump a
label-mismatched claim applies. A node re-claimed after a lease-expiry requeue is not
observed, because the only wait it could report is the previous attempt's. An
automatic retry is observed, because the retry clears the hold instant and the
claim generation, which makes its next claim a first claim.

Per-run and per-team attribution lives in the ledger, not in the metrics:
`GET /api/v1/credits/history` returns each charge with its run and node. A run
id would mint one series per run, so the exported counters sum the seconds and
name only the placement.

The `route` label is the pattern the controller registered the request
against, so every path parameter reaches Prometheus in its declared form
(`/api/v1/concurrency/{key}/state`, `/api/v1/runs/{id}`) and a run id,
concurrency key or artifact digest never becomes a label value. A request
that matches no route is labeled `other`, and so is any `method` outside
the seven the controller answers, so neither an unrecognized path nor an
invented request method can grow the series count.

`sparkwing_runners_live` carries a label a runner writes for itself, so the
exporter sorts and deduplicates each advertised label set and collapses a set
longer than 120 bytes onto `label_set="other"`. When more than 32 distinct sets
are live, the busiest 31 keep their own series and the rest are summed into
`other`, so a set invented to sort first cannot displace a real fleet. A tie in
size goes to the set already reported, so a newcomer cannot displace a fleet
that was there first. Each sweep deletes the series for a set it no longer
sees, which is what keeps the metric from growing with every label array a
runner has ever sent.

A gauge with no children is absent from the exposition, so
`sparkwing_runners_live == 0` never evaluates when the fleet is empty. Alert on
`sparkwing_live_runners`, which carries no labels and is always present, and
read `sparkwing_runners_live` for the composition of the fleet rather than for
its size.

Principal identity never becomes a label. `sparkwing_requests_by_principal_total`
carries the credential's kind; principal names, token prefixes, holder ids and
run ids stay out of every series.

Sampled series do not refresh at scrape time. `sparkwing_queue_depth`,
`sparkwing_runners_live`, `sparkwing_pending_nodes` and
`sparkwing_active_runners` refresh on the controller's reaper loop every 10
seconds, bounded by an index over the nodes that have not finished. The
`sparkwing_credits_*` family and `sparkwing_node_seconds_total{placement="cloud"}`
refresh every 5 minutes, because the ledger sums scan a table that grows with
every charge and is never pruned; read them as a slow-moving billing figure
rather than a live gauge. Those totals come from the ledger rather than an
in-process counter, so they survive a controller restart.

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
| `--bucket-measure-pages` | `SPARKWING_OBJECT_STORE_BUCKET_MEASURE_PAGES` | Listings one measurement may spend, 1000 by default |

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
of the minute. And the walk itself is bounded, at `--bucket-measure-pages` listings
(1000 by default, a thousand objects each) and at half the
reconciliation interval, whichever comes first.

A measurement that stops at either bound is discarded rather than folded
in, because a total short of the truth would thaw a store that is still
full. The ceiling then reports `measurement_incomplete` on health and in
`sparkwing_object_store_bucket_ceiling_measurement_incomplete`, and
keeps counting writes until a measurement finishes. A bucket that keeps
reporting incomplete wants a higher `--bucket-measure-pages`, which is
the bound that binds first, and then a narrower prefix or S3 Inventory
in place of the listing.

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
startup). The walk is local file I/O rather than billed requests, it
stops when the service's context does and reports the total as partial
rather than folding a short one in, and a
measurement that finds the store back under its ceiling thaws it.
`--warn-store-bytes` and `--warn-store-objects` mark the store as
warning on health without refusing anything. The chart carries all of
these as `cache.limits.*` and `logs.limits.*`.

Neither service waits out the interval to recover. Deleting a run with
`DELETE /api/v1/logs/{runID}`, or letting the sweeper delete it under
`--retention`, starts a fresh measurement of the log store, so appends
resume shortly after the delete answers rather than at the end of the
interval. The cache
serves no delete of its own, so it carries two bearer-gated admin
routes: `POST /admin/store-ceiling/measure` starts a walk now, which is
what turns freeing space on the volume into uploads flowing again, and
`POST /admin/store-ceiling/thaw` accepts uploads until the next
measurement. The measure route answers `202` with the state as it stands
and walks off the request path, so the caller pays no latency for a
large store and a disconnect cannot abandon the walk; one runs at a
time. The thaw is refused with `409` when no measurement is scheduled,
and the refusal points at the measure route. Deleting a run on the logs
service triggers the same off-request walk. Neither service drops a
request that arrives while a walk is running: it re-runs once when that
walk finishes, so a delete the walk had already passed still lands.
A reset clears every tripped class and both window counters; lifetime
request and trip totals survive so the metrics keep their history. A
budget that keeps tripping wants a larger limit or a caller that stops
retrying, not a repeated reset.

## Egress budgets

Egress is the bytes a Sparkwing service sends to clients: artifact and
cache-archive downloads, log reads, the live log stream, and git proxy
fetches. Object-store egress is about nine cents per gigabyte, so a
terabyte a month is ninety dollars nobody authorised, and a user can
spend it without writing a single pipeline.

The controller, the logs service, and the cache each count the response
bodies of those routes twice: once against the principal that asked for
them, and once against the process total. Every budget below is
unlimited until an operator sets one.

| Flag | Controller | Logs | Cache | What it does |
|------|-----------|------|-------|--------------|
| `--egress-monthly-bytes` | yes | yes | no | Bytes one principal may download in a UTC month. Past it, its downloads answer `429` with a `Retry-After` naming the wait until the month rolls. |
| `--egress-max-downloads` | yes | yes | no | Metered downloads one caller may hold open at once. Past it, a further one answers `429`. |
| `--egress-max-log-streams` | yes | yes | no | Live log streams one caller may hold open at once. Past it, a further stream answers `429`. |
| `--egress-daily-alarm-bytes` | yes | yes | yes | Bytes the process may send in a UTC day before it raises the egress alarm. It refuses nothing. |

The controller's two concurrency caps are also supplied as a set by
`--limits-profile`, which a hosted controller runs with instead of naming
each guard; see [security.md](security.md#limits-profiles).

Each service reads its own environment variables:
`SPARKWING_CONTROLLER_EGRESS_MONTHLY_BYTES`,
`SPARKWING_LOGS_EGRESS_MONTHLY_BYTES`,
`SPARKWING_CACHE_EGRESS_DAILY_ALARM_BYTES` and the rest, spelled
`SPARKWING_<SERVICE>_EGRESS_<BUDGET>`. They are separate on purpose: one
variable on a shared ConfigMap read by three processes is one cap applied
three times, which admits three times the bytes the operator wrote down.
**The per-team monthly cap is the controller's**, and the other services'
budgets bound their own traffic.

### Refusing needs a principal the service can tell apart

The controller resolves a bearer to a named principal on every download
route, and the logs service resolves one through the controller's
whoami, so their monthly and concurrency caps fall on the caller that
spent the bytes.

The cache authenticates one shared token, so every credentialed caller
resolves to the same name. It therefore **meters and alarms and never
refuses**: a cap it could enforce would answer `429` to the bearer every
runner in the fleet shares, stopping every checkout and cache read at
once, for up to a month, with no recovery but a pod restart. Its health
reports `egress.enforced: false` to say so. The cap that protects the
bill belongs to the controller, which knows who each bearer is.

A service running with auth off resolves every request to `anonymous`,
which is one shared budget for the same reason; that is the laptop-local
shape, where no budget is set anyway.

### A bearer can be a pool

A runner pool shares one token, so twenty pods are one principal. The
byte budget is keyed on the principal deliberately: the bill is the
team's, however many pods spent it. The **concurrency caps are not**.
They are keyed on the pod behind the request, taken from the
`X-Sparkwing-Runner` identity the shipped runners already set for the
controller's claim budgets, then from the claim-holder header, falling
back to the principal only when nothing names a pod. A cap of eight keyed
on the principal would refuse twelve of a twenty-pod pool while the byte
budget sat untouched.

That identity is cooperative: a caller that invents a pod name gets its
own slots. The concurrency caps therefore bound an honest pool's burst
and the blast radius of a stuck client; the monthly byte budget, which
keys on the principal and cannot be moved by a header, is what bounds a
caller that is trying to get around it.

For the same reason the gitcache proxy routes take no slot at all. They
are the checkout path every node walks, so a cap there refuses the clone
rather than the download it was meant to bound. Those routes are still
byte-metered, and still refused when the monthly byte budget is spent.

### The alarm

The monthly budget refuses; the daily threshold only alarms. The
distinction is deliberate: one principal's spend is that principal's
problem to answer for, and a process's daily total is the operator's.
The alarm appears as `egress.alarm` on each service's `/api/v1/health`
(the cache serves it on `/health`), as a line in `problems`, and as a
`warn`-level log line carrying `day_bytes` and `threshold_bytes`, which
is what a deployment's alerting keys on. Point CloudWatch alarms on the
bucket's `BytesDownloaded` metric and the instance's `NetworkOut` at the
same page, so the bill has a second witness that does not depend on a
Sparkwing process being up.

### What is and is not charged

A byte counts when it is written to a `2xx` response to a request whose
method carries a body. A `HEAD` charges nothing, because net/http
discards what the handler writes to one, and an error body charges
nothing, because it is not the download the budget is for.

A budget is checked before a response starts, not during it, so a
principal at zero can still finish whatever it already has in flight.
The concurrency caps bound that overshoot only as far as they reach. The
most a team can take past its monthly budget is

```text
(--egress-max-downloads + --egress-max-log-streams)
  x  the largest object those slotted routes serve
  x  the number of pods behind the bearer
```

because each pod holds its own slots, plus whatever the gitcache proxy
routes serve, which hold no slot at all. A caller that invents pod names
multiplies the pod count itself, so treat the formula as the bound on an
honest fleet and the byte budget as the bound on the rest. Leave the caps
unlimited and the overshoot is unbounded.

### Persistence and history

Counting is in memory, and only the controller persists it. It writes
each principal's month total to its store on the maintenance sweep and
reloads it at startup, so a restart resumes the month rather than handing
everyone a fresh budget, and no response costs a store write; that sweep
also prunes totals older than thirteen months, once a month rather than
on every tick. The logs service and the cache count in memory alone: they
park nothing for a flush that will never come, and their counters start
over on a restart.

Read the controller's meter, including the principals that have
downloaded the most this month, with `GET /api/v1/egress` on an `admin`
token.

### One meter per process

Like the object-store budget, an egress meter belongs to a process. Two
controller replicas each count their own bytes, so a per-principal
budget sized for one replica admits twice that across two.

The persisted number is the high-water mark of any one writer, not the
sum of them. Within a writer the total only rises, which is what makes a
restart safe; across writers the row reflects the busier replica and the
quieter one's bytes are not added to it. Size the budget for one
process, and run one controller. The chart enforces that:
`controller.replicas` above 1 fails to render, because the same second
replica that would corrupt the local state DB would also double a
per-principal budget and split the daily alarm below its threshold.
