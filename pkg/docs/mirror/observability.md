# Observability

Sparkwing tracks run health, failure reasons, and resource usage so you
can debug failures fast and right-size containers.

## Finding a run and its logs

Use `sparkwing runs list --pipeline check --status failed --since 7d` to find
failed runs. Add `--profile NAME` to select a controller. Filters apply before
the page limit. JSON includes compact run records and a page record with
`next_cursor` and `truncated`. Pass the cursor with the same filters to continue.
Plain output prints run IDs, while quiet JSON prints an ID object per run.

`--by-pipeline` totals all matching pages. `--group-by job` groups failures in
the selected page by job, and `--group-by step` uses job/step identities.
Both failure views require `--status failed`. `--follow` emits newer matching
runs, including a run that fails after a later run has already failed.

Read a selected run with `sparkwing runs status --run RUN --steps`. Use
`sparkwing runs logs --run RUN --follow` for its logs, or add `--job JOB` for
one job. JSON preserves job attempts and trigger, plan or eval log subjects.
Log follow completes only after the controller reports final persisted logs.
An interrupted or incomplete observation exits 3.

Search several runs with `sparkwing runs grep --grep PATTERN --pipeline check`.
Search output retains its continuation even when a page has no matching lines.
Each log read or search request scans at most 256 MiB of log bytes for at most
10 seconds, and a run selection is capped at 1000 runs. A scan that reaches a
bound returns a truncated page with a continuation that resumes where it stopped.
Use `--quiet -o plain` for matching run IDs. Annotation and approval commands
under `runs` use the same selected controller and preserve structured failures.

The rewrite's CLI adapters are implemented against the shared controller
contract. Final server read adapters, annotation policy and persisted log
completion remain integration requirements.

## Run statistics and sizing

`sparkwing runs stats --pipeline check --since 7d` reads every matching root-run
page. JSON contains separate successful, failed, cancelled and active counts.
Success percentage counts terminal runs, including cancellations. Mean duration
and nearest-rank P95 use runs with both timestamps. Plain output emits pipeline
names. A failed page read produces no partial totals.

`sparkwing runs stats --sizing` reads the controller's resource measurements,
including job rows, CPU and memory distributions, wait times, memo exclusions,
demand floors, resource pins and drift. Add `--profile NAME` to select a
controller. JSON preserves the measurements and controller-selected source.
Plain output emits repository/pipeline names.

Sizing reset selects `--reset --pipeline NAME` or
`--reset --all-pipelines --yes`. The reset contract preserves resource pins.
Its HTTP adapter returns `policy_missing` because the route contract has no
authorized reset operation. It changes no state.

## Incomplete logs

Log storage runs independently of plan acceptance, expansion acceptance, run
completion and node reporting. Those commits never wait for log uploads or closure.
Flush effort is bounded by the existing one-minute log tail window. Log I/O holds
no lock shared across jobs. Log subjects use the committed attempt, without
logging-only principal refreshes or node lookups. The store creates each stream
in the transaction that starts its attempt or claims its owner unit. Hosts open
spools without a registration request. Hosts also seal streams that produced no
bytes, so quiet units do not keep log follow waiting after completion. Completed
trigger, plan and eval claims can seal their own stream during the same tail window.
If a retry refuses the prior attempt's
detached flush, that stream stays visible in run details and maintenance seals it
incomplete.
The host emits a `run logs incomplete` operator error with the run identity,
node when known, and
a masked error. A failed stream is recorded as incomplete. Streams that cannot publish
their final manifest are sealed incomplete by log maintenance after the writer
ends. If the controller cannot be reached, the host reports the loss in its
operator log and makes a bounded attempt to record the incomplete stream.

Secret masking and required output verification still govern execution. A log
outage does not relax either check.

## Observational state-write failures

The node log emits a `state_write_failed` warning when a step lifecycle,
annotation, or summary cannot be written to the state backend. The record names
the node and, for step-scoped records, the step, with `run_id`, `operation`, and
a bounded, secret-masked `error` in its attributes. Inspect these warnings when
stored state disagrees with the execution log.

The original event and work outcome remain unchanged because these records
observe execution rather than govern it. The warning does not repair the
missing state. It uses the underlying log path, so loss of that log path can
also prevent delivery of this diagnostic.

## Lifecycle events

The store writes job lifecycle events in the transaction that changes the job.
Application retries retain their claim and advance the attempt ordinal.
Lease-lapse recovery records a separate disruption. Hosts do not emit a second
terminal event after reporting a result.

| Event | Meaning |
| --- | --- |
| `job_started` | The awarded job began an execution attempt. |
| `job_retrying` | An application failure started the next attempt under the same claim. |
| `job_requeued` | Lease recovery released the job for another scheduling pass. |
| `job_succeeded`, `job_failed`, `job_cancelled` | A terminal execution result committed. |
| `job_skipped`, `job_superseded`, `job_memoized` | A scheduling or reuse decision committed. |

Exact replay creates no second event. Native host and controller integration
must preserve these store decisions.

A funding refusal or hold records a metered job cancellation naming the balance
shortfall or hold and its remedy. The owner and keeper stop when they receive
it. Its lease cannot renew past the funded horizon or the durable drain
deadline. Add funds, enable automatic refill or resolve the named hold before
re-running the cancelled work.

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
| `agent_lost` | The runner holding the job's claim stopped renewing it. Started work requeues in the same run until three disruptions have occurred. | Check the runner and `job_requeued` events. Recovery preserves the application retry budget. |
| `queue_timeout` | Either a node waited past its concurrency group's `OnLimit: Queue` timeout without getting a slot, or no agent claimed the node within its claim wait (default 24h; the error starts `unclaimable:` and names why). The node's error text names which. | For a concurrency wait, raise the group's capacity or its queue timeout. For an unclaimed node, ensure runners are up and their advertised `--label` set satisfies the pipeline's `requires:` / node `.Requires()`. |
| `credits_exhausted` | The team's prepaid balance or a funding hold refused the next execution slice, and the job stopped within its drain deadline. | Add credits or resolve the funding hold, then start the run again. |
| `unpriced_cpu_class` | The node asks for more cpu than the largest class the credit rate table prices, so no claim can be billed. | A Sparkwing Cloud operator can add the class with the private `sparkwing-ops` tool. Otherwise lower the node's cpu request, then re-run. The terminal job event records the `unpriced_cpu_class` reason. |
| `runner_lease_expired` | The worker that claimed this run's *trigger* stopped renewing its lease. The controller returns the trigger to the pending queue and cascade-fails every node the run had not finished. | Check the worker that claimed the trigger. The trigger is re-claimable; this run is terminal. |
| `verify` | The node's action completed, but its `Verify` postcondition returned an error -- the failure is at the verify stage, not the action. | Inspect the `Verify` assertion and the action's actual output. |
| `logs_auth` | The runner's log-append calls were rejected (401/403), so the run's structured logs are unrecoverable. | Check the runner token's scopes (`nodes.claim`, `triggers.claim`, `runs.state`, `secrets.read`); the run fails loud rather than reporting success with no output. |
| `source_unavailable` | A launcher Job's init container was refused the run's source credential. The refusal answers the same on every attempt, so the node is not retried. | Check the source credential the controller holds for the repository, then re-run. |
| `source_fetch` | A launcher Job's init container could not check out the run's source for a reason a later attempt may not meet. The node is retried within its budget, a planning node included. | If every attempt fails, check that the repository and commit are reachable from the cluster. |
| `logs_dropped` | The log store stayed unreachable past the append retry budget, so log lines were lost. The node's own work may have succeeded; its record of that work is incomplete. | Check the logs backend named in the run's `invocation.backends` -- for `s3`, the bucket, `AWS_REGION`, credentials, and `AWS_ENDPOINT_URL_S3`. The `logs_drop` event carries the lost-line count and the first error. Set `logs.drop_policy: warn` in [config.yaml](machine-config.md) to keep such runs green instead. |

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

A runner records `execution_attempts` on the node, and so does the
local orchestrator for a node it runs itself: those entries carry executor kind
`local`, location `local`, and the host name as the executor. Each entry has
the global attempt ordinal, source or retry run, executor
name and kind, controller-owned location, timestamps, outcome, failure reason,
and retry link. Missing legacy attribution remains unknown. Node read responses
omit claim generations and all coordinator,
membership, holder, token, reservation, and internal executor identifiers.
Public event reads also omit claim generations from attempt-lifecycle
payloads.

The store records `job_started`, `job_retrying`, `job_requeued` and terminal job
events in each transition's transaction. Owner recovery records `run_requeued`.
A started lease lapse closes the attempt with `agent_lost` and consumes one
disruption within the same run. An unstarted lapse consumes none. These payloads
carry attribution, not credentials.

Remote logs use immutable substreams keyed by claim generation and execution
attempt. A stale append is rejected, while an append that had already passed
validation before lease loss can finish only in the old attempt's history. The
normal log read and stream merge those substreams; exact reads may select
`attempt` with `claim_generation`, `attempt` with `trigger_generation` for
trigger-owned node work, or `trigger_generation` alone for `_compile` output.

### API

`GET /api/v1/runs/{run}` returns a safe public run view with `jobs`,
`attempts`, optional `steps` and tagged log subjects. Job detail uses
`GET /api/v1/runs/{run}/jobs/{job}`. Display fields include status, outcome,
state sequence, generation, attempt and timestamps. Raw declarations and
execution credentials are excluded.

Recovery reads the persisted masked failure through
`GET /api/v1/runs/{run}/jobs/{job}/failure` with `source_generation` and the
authorized recovery read edge. A status string cannot substitute for that
record. The failure contains its canonical error, job and stage.

Fetch logs separately with `sparkwing runs logs --run RUN`.

Controller log reads use the same configured object backend as signed uploads.
Each read rechecks the authenticated actor and its holder/generation before
fetching committed bytes. Log streams return `Cache-Control: no-store`.

## Log completeness

A log page contains `records`, `next_cursor`, `truncated` and `complete`.
An incomplete page retains a cursor even when it has no records. A truncated
page requires another read immediately. Follow polls only after reaching a
nontruncated incomplete page. Completion requires both persisted terminal state
and final log segments.

Each output record names exactly one tagged subject. Job-attempt subjects retain
team, run, job, generation and positive attempt ordinal. Trigger, plan and eval
logs use owner-unit subjects with their own generation. Eval logs also name the
terminal source job and source generation. They never invent a job or attempt
for work that ran before one existed.

Lifecycle event records carry their event kind and optional job/step attribution
separately from output subjects. The cursor retains every selected subject,
including descendant runs when a tree read is requested.

### Node logs kept as segments

The log contract cuts masked segments at the shared size or interval, flushes
on completion and records a manifest. Signing uses
`POST /api/v1/claims/{claim}/log-segments` under the current tagged subject and
claim fence. Each request names one segment or the final manifest with its
exact stored size and SHA-256, and the signed PUT accepts only those bytes. The
controller selects retention class and storage keys. Segment PUT URLs grant no
read authority.

Reads through `GET /api/v1/runs/{run}/logs` and
`GET /api/v1/runs/{run}/jobs/{job}/logs` reauthorize each page. Live ring gaps
fall back to persisted segments. Log reads remain controller-proxied and exempt
from the team's daily data-download cap. Retention classes expire independently
of the newest-successful run pin.

Native log signing, cursor persistence, segment fallback and final completeness
still need integration evidence. An incomplete follow returns exit 3 even if a
separate status read already sees success.

## Failure excerpts

A node that fails while running a command records a bounded excerpt of
that command's output -- the last 20 lines, at most 4 KiB, with resolved
secret values redacted. The node's `error` carries it as text, led by
the failure headline and, when output was dropped, a marker naming the
`sparkwing runs logs` command that prints the whole thing.

`sparkwing runs status --view errors -o json` and `sparkwing runs status -o json` also
carry the excerpt as structured fields, so a consumer does not have to
parse the error string:

```json
{
  "node": "build",
  "outcome": "failed",
  "error": "build: command failed (exit 2): go build ./...\n… earlier output omitted (see: sparkwing runs logs --run run-... --job build)\npkg/thing/file_300.go:12: undefined: Helper\nFAIL",
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

A dedicated node process samples its observed process tree every 2 seconds
and takes a final reading before execution returns. Samples are stored and
charted in the dashboard. No cluster metrics-server is involved.

A sample delivery failure leaves execution and retries unchanged. Sampling
continues with a five-second timeout per write; the next recorded interval
spans any missed intervals to preserve CPU time. At node finish, losses of at
most 1% of attempted interval and command samples produce a `metrics_partial`
run event with the counts, without a warning or excluding capacity learning.
Larger losses produce one node log warning and a `resource_samples_incomplete`
run event, and the executor makes one bounded, best-effort partial-marker write
independent of execution cancellation. Partial measurements are lower bounds:
capacity learning can raise a profile from them, but cannot lower any resource
dimension or evict an older observation unless the partial measurement raises
one dimension. Partial measurements never count toward graduation, so they
cannot end the measuring safety margin. A partial contended run can raise the
demand floor without decaying it. Exact exit CPU time and maximum memory remain
trustworthy. A backend that refuses the partial marker gets the exclusion
marker instead.

Some samples exist to exclude a measurement: the marker for an unowned node or
an unrecorded attempt, the parent marker before a spawn (the parent's
measurement includes child work), and interval samples flagged invalid. When
one of those is lost, the executor writes an exclusion marker at finish
regardless of the loss ratio. Markers carry no reading and may exceed the
per-node sample cap by a small headroom, so a measurement that filled the cap
can still be labeled. Reaching the sample cap is not a delivery failure. Exact exit accounting is recorded separately, and billing
uses claim duration and class rates rather than resource samples. Learned
profiles can influence the resource class reserved for future claims.

### What's measured

- **CPU**: millicores from the change in cumulative CPU time divided by the
  matching elapsed interval, clamped to the host's core count. The counters
  include observed live descendants and reaped child work.
- **Memory**: summed resident bytes across the observed process tree, using
  `smaps_rollup` on Linux and process resource counters on macOS. Shared
  pages can be counted more than once. Process reads are not simultaneous.

Two snapshots check process identity, parentage and counter consistency.
Unavailable or inconsistent readings mark the sample unknown. Any usable
reading remains available for diagnostics, but an unknown sample prevents
a contributing uncached node's observations and the run rollup from training
resource costs. Cached nodes are excluded from learning.
A successful zero reading remains measured zero.

An observed process disappearing or changing identity or parentage makes
attribution unknown, including an ordinary child reap. Descendants still
present at final collection also make attribution unknown. Work before
attachment, short-lived descendants absent from every snapshot and escaped
processes cannot be fully accounted for by polling.

Only a node that owns its dedicated process receives interval attribution.
Embedded execution and nested work sharing another node's process report
unknown attribution; process readings are not divided among attached nodes.

Sampling misses peaks between readings. The first interval retains the
initial memory reading, and final collection can produce an interval even
when a node finishes before the first timer tick. Supervised local processes
also report exit CPU time, peak resident set and process lifetime. The local
capacity fold uses CPU time divided by that matching lifetime as a floor
under sampled CPU. Exit RSS is a process high-water mark, not simultaneous
process-tree memory. Pods and embedded nodes do not provide these supervised
exit figures.

The process lifetime includes startup and teardown. Retries accumulate CPU
and occupancy, while exit memory retains the largest reported peak. Profile
learning excludes multiple execution attempts whose combined measurement
coverage cannot be established.

Each stored point has a `kind`:

- `interval`: a sampled CPU rate and memory reading, including measured zero.
- `command`: a completed command's lifetime CPU and memory report, including
  commands whose reported CPU time is zero.
- Empty or omitted: unknown measurement kind. These points remain readable,
  but a node's unknown points prevent its observations and the run rollup
  from updating learned profiles.

Only interval rates contribute to sampled CPU peaks and percentiles.
A command's lifetime CPU cannot be assigned to its completion interval.
Command memory contributes the largest command peak in a window; this does
not establish the combined memory of overlapping children. These limits
must be considered before using learned costs to reduce resource pins.

### API

`GET /api/v1/runs/{run}/jobs/{job}/metrics` (see
[api-reference.md](api-reference.md)) returns the sample points:

```json
{
  "points": [
    { "kind": "interval", "ts": "2026-04-12T10:00:00Z", "cpu_millicores": 450, "memory_bytes": 536870912 },
    { "kind": "interval", "ts": "2026-04-12T10:00:02Z", "cpu_millicores": 1200, "memory_bytes": 1073741824 }
  ]
}
```

### Using metrics to right-size containers

1. Run representative workloads, including overlapping work and retries.
2. Open the run detail in the dashboard and expand **Resources**.
3. Compare measured usage with configured requests and limits. Investigate
   memory pressure and CPU throttling where usage approaches a limit.
4. Before reducing requests or limits, verify measurement coverage and leave
   headroom for workload variation and peaks between samples.

## Dashboard

The dashboard shows failure information where a run's detail is:

- **Runs page**: a failure-reason badge on each failed node, both in the
  node list and in the selected-node panel, with the exit code when one
  was recorded.
- **Resources**: a collapsible CPU/memory view per node on the run detail. It
  separates the admission reservation, interval samples, exact process totals,
  and command totals instead of treating them as one measurement. A cached node
  says that its body did not execute. Exact totals remain visible for short
  nodes that finish before a second sampler tick. The samples refresh while the
  node is running.

It also shows what admission is doing with the machine:

- **Queue page**: the live admission queue -- every resource with its
  headroom, every holder, every lease connected without holding resources,
  and every waiter in order with its ETA. A five-minute chart, accumulated
  while the page is open, separates Sparkwing job reservations, the host
  reserve, measured external applications, and total capacity. An external
  sensor gap remains a gap rather than becoming zero. Mirrors `sparkwing
  queue`.
- **Fleet section**: the caller's team's granted runner identities, accepted
  observations, active jobs and measured headroom from one store snapshot.
  Distinct credentials with the same display name stay separate. `last_seen`
  records an accepted poll or renewal, never a future lease deadline. A busy
  runner's heartbeat does not advertise a free slot or refresh headroom.
  Runner and queue views share the same snapshot. Embedded controllers supply
  `WithRunnerInventory`; missing adapters refuse the read with `policy_missing`.
  Home reads recently failed pipelines from the caller's team-scoped runs.
  Database, object-store, auth and slow-response problems still degrade their
  service. The controller health API keeps its own status.

The run node list and DAG show a small location icon for known execution sites.
Hover or focus the icon to see the runner or repository. Machine, Sparkwing
Cloud, GitHub Actions, and cluster execution use distinct icons; unknown
locations leave the space empty. Node names use the available row width and
keep their full name in a tooltip.
Selecting a node shows its execution history in the run detail's Summary tab,
below the run and node summary. A single attempt occupies one compact row.
Every durable attempt retains its executor kind and name, run link, timestamps,
outcome, and retry link when the controller recorded one. In-process trigger
nodes record the trigger claimant. Claimed nodes record the selected runner or
the claim holder; Kubernetes Jobs record their pod hostname; GitHub Actions jobs
record the repository and workflow run ID. Local runs record the machine
hostname. Metered runner attempts record cloud placement. The controller also
derives execution sites for older attempts from stored claim holders and, while
the claim still matches, the credential. A recorded platform appears with its
attempt; a missing platform remains unknown. The panel says unknown only when
the stored attempt and matching claim have no usable executor identity.

- **Capacity page**: the same host ledger with the subtraction behind
  each Available cell written out, then every measured pipeline with the
  charge it resolves to (the live form of `sparkwing runs stats
  --sizing`), and, per pipeline, the stored sample window with the run
  each percentile charge was ranked out of marked. Use it to check a
  charge by hand: the p95 the panel marks and the price it shows come
  from the rows on screen, so a charge no sample supports is visible
  rather than inferred. Host figures refresh every 2 seconds; learned
  pricing every 10. With no daemon running the host section reports that
  and the pricing table still reads from the runs store.

## Data retention

Leased maintenance removes eligible finished runs and metrics under separate
retention policies. A self-hosted run window defaults to disabled. Each pipeline's
newest successful run and output remain pinned, while log classes expire
independently. Explicit pruning uses `sparkwing runs prune`. See
[tenant limits](limits.md#what-prunes-automatically) for policy and native
integration boundaries.

## Service logs

`sparkwing-controller` and the runner's `start` and internal `launch` modes
write service logs to stderr.
`--log-format` picks `text` (the default) or `json`, and `--log-level` picks the
lowest level written: `debug`, `info` (the default), `warn` or `error`. A value
the service does not know stops it at startup. With an OTLP logs endpoint
configured, the same records also go to the collector.

## OpenTelemetry

Every sparkwing service initializes OpenTelemetry and exposes a
Prometheus `/metrics` endpoint. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to
additionally export traces and structured logs via OTLP.

### Prometheus /metrics

Always active on every service; scrape it with your Prometheus.

The controller serves `/metrics` to a deployment bearer with `operator.limits`
or an authorized operator-console session, because the run counters name every
team's pipelines. The same policy applies on either listener.
`sparkwing-controller --metrics-addr 127.0.0.1:9090` moves the endpoint onto its own listener,
and the API listener then answers `404` for `/metrics`. In `sparkwing-full`,
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
| `sparkwing_pending_nodes` | Gauge | (none) | Claim-queue depth (ready, unclaimed nodes of active runs) |
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
| `sparkwing_principal_throttled_total` | Counter | `route_class` | Requests a request budget refused with `429` (`claim`, `heartbeat`, `idle_poll`, `token`) |
| `sparkwing_request_rate_alarm_total` | Counter | (none) | Minutes in which the controller served more requests than `--requests-per-minute-alarm`; it refuses nothing |
| `sparkwing_egress_day_bytes` | Gauge | (none) | Bytes the process has sent to clients in the current UTC day |
| `sparkwing_egress_daily_alarm` | Gauge | (none) | 1 while the process is past `--egress-daily-alarm-bytes`; it refuses nothing |
| `sparkwing_signups_total` | Counter | `outcome`, `reason` | New accounts: `admitted` (reason `none`, or `invitation` for a waitlisted account that joined a team by invitation) or `waitlisted` with the reason (`deployment`, `operator`, `free_tier_closed`, `free_tier_unreadable`, `hourly_signups`, `daily_signups`, `github_account_age`) |
| `sparkwing_signup_gate_closed_total` | Counter | `reason` | Times the sign-up gate closed itself because new accounts crossed the hourly or daily limit (`hourly_signups`, `daily_signups`) |
| `sparkwing_signup_velocity_warnings_total` | Counter | (none) | Times the last hour's new accounts crossed the sign-up warn threshold, once per crossing; it closes nothing |
| `sparkwing_queue_depth` | Gauge | `state` | Each unfinished job of an active run counts once: `waiting`, `ready`, `claimed`, `running`, `approval_pending`. A held job stays claimed until recovery commits, including after lease expiry. Jobs retained by a finished run do not count. |
| `sparkwing_claim_unavailable_total` | Counter | (none) | Claim requests answered `503`, which a runner retries after the interval the response names |
| `sparkwing_claim_empty_total` | Counter | `claimant` | Queued claim polls that matched no unit, by claimant (`launcher`, `runner`, `here`). A launcher that only polls empty while runs wait matches none of their units |
| `sparkwing_runners_live` | Gauge | `label_set` | Runners that polled for a claim inside the liveness window, by the label set they advertised |
| `sparkwing_live_runners` | Gauge | (none) | Runners that polled for a claim inside the liveness window, across every label set |
| `sparkwing_node_seconds_total` | Counter | `placement` | Node execution seconds: `cloud` is what the ledger has finished charging for, `local` is what this controller process settled for unmetered credentials |
| `sparkwing_credits_balance_micro` | Gauge | (none) | Micro-credits left to spend |
| `sparkwing_credits_granted_micro_total` | Counter | `kind` | Micro-credits granted over the ledger's life, by whether the operator paid for the grant (`free`, `paid`) |
| `sparkwing_credits_reversed_micro_total` | Counter | (none) | Micro-credits refunded payments took back out of the ledger, reported positive; a reversal never joins the granted series, so summing that series over `kind` stays the money paid in |
| `sparkwing_credits_reserved_micro_total` | Counter | (none) | Micro-credits claims reserved up front |
| `sparkwing_credits_charged_micro_total` | Counter | (none) | Micro-credits execution billed |
| `sparkwing_credits_refunded_micro_total` | Counter | (none) | Micro-credits returned to claims whose machine never started, or whose setup the platform failed |
| `sparkwing_credits_storage_micro_total` | Counter | (none) | Micro-credits retained bytes billed; this series buys no runner time, so the charged series does not carry it |
| `sparkwing_requests_by_principal_total` | Counter | `credential` | Requests that authenticated, by capability subject: `account`, `machine`, `service`, `operator`, `local`, `anonymous` or `other` |

`sparkwing_node_seconds_total{placement="cloud"}` is the billing line, and it
comes from the credit ledger rather than from a request handler. A metered claim
reserves its first 60-second slice up front, and each renewal reserves the next. The series
leaves a node's reservation out until billing starts on the machine that runs
it, and a trigger's until its claim settles, so queueing and Kubernetes
provisioning add no cloud seconds and a claim whose machine never started
returns its whole reservation. Once billing starts, renewals charge the
seconds used, and final settlement refunds the unused part of the last slice. Two refunds return billed seconds and lower the series: a
node setup the platform fails before execution, and a trigger claim requeued
before its run started, which also returns what its heartbeats charged. A node stopped
by a funding refusal settles at its drain deadline at the latest.

The `local` series counts what this controller process settled for an unmetered
credential, read from the claiming credential recorded on the node, and it
restarts at zero with the process. A node whose claiming credential has since
been revoked counts under neither placement, because guessing would bill the
operator's own capacity for cloud work. Only a metered credential moves
credits, so the `sparkwing_credits_*` totals are paid work by construction and
carry no free-versus-paid split; the split an operator wants is
`sparkwing_node_seconds_total{placement}` for work and
`sparkwing_credits_granted_micro_total{kind}` for funding.

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

## Object-store request budget

Every object-store client a Sparkwing process builds draws on one
request budget. The budget counts `put`, `get`, `list` and `delete`
separately, each against a per-minute rate and an optional per-day total. A class
that spends either budget trips: further requests of that class are
refused with an error naming the class and the limit, and nothing is
queued. Classes are independent, so a tripped `put` budget leaves reads
serving until their own budget trips.

The budget counts billed attempts, not API calls. It sits inside the AWS
SDK's retry loop, so a call the SDK re-sends four times spends four
units, which is what the object store charges for.

The guard exists because a retry loop against a failing bucket bills per
request and the bill arrives hours later. Defaults sit far above normal
load and far below a loop with no sleep. Shared daily stops are off by
default: ordinary enforcement uses per-team budgets. Operators can enable
a shared daily stop as an emergency cost brake:

| Class | Per minute | Per day |
|--------|------|-------------|
| `put` | 1200 | unlimited |
| `get` | 3000 | unlimited |
| `list` | 600 | unlimited |
| `delete` | 600 | unlimited |

Override any of them on the controller with `--object-store-budget`, a
comma-separated list of `class:window=count` entries such as
`put:minute=600,put:day=200000`, where the class is `put`, `get`, `list` or
`delete` and the window is `minute` or `day`. A count of `0` removes that
budget. Every other process runs on the built-in budgets.
A class tripped by its per-minute rate clears when that minute rolls, so
one burst costs a minute of refusals rather than a day of them. An opt-in day
budget bounds the total. A class tripped by its per-day budget
clears when the day window rolls, or earlier with a reset.

### One budget per process

The budget belongs to a process, not to a cluster or a bucket. A
controller, each `sparkwing run`, each worker and each runner agent
builds its own, and they do not aggregate: N
processes on the same bucket can spend N budgets. Size the numbers for
what one process should ever need, and read the AWS-side budget action
and CloudWatch alarm as the layer that sees the whole account.

The same split decides the remedies. `sparkwing operator object-store
reset-breaker` reaches the controller process and nothing else, so a
refusal inside a local `sparkwing run` clears when its window rolls or
the process restarts. The refusal message names the controller's remedies.

The controller reports its own budget on `GET /api/v1/health` under
`object_store`, names every tripped class in `problems`, and exports the
three `sparkwing_object_store_*` metrics above. The health summary names
the classes and not their limits, because that route answers without a
token.

Read a controller's budget, and clear a tripped one, with:

```bash
sparkwing operator object-store status --profile prod
sparkwing operator object-store reset-breaker --profile prod
```

A reset clears every tripped class and both window counters. Lifetime request
and trip totals survive so the
metrics keep their history. A budget that keeps tripping wants a larger
limit or a caller that stops retrying, not a repeated reset.

## Storage ceilings

Per-team quotas and deployment ceilings have separate scopes. The controller's
object-store policy can freeze writes at a byte or object-count ceiling while
retaining reads and deletes. A refusal names the ceiling and measurement.

Public health exposes freeze, warning and incomplete-measurement flags without
revealing stored totals. Deployment object-store authority opens detailed state
through `GET /api/v1/operator/object-store/breaker`. Native backend enforcement
and deployment configuration remain integration checks.

### The controller's bucket ceiling

Every ceiling is unlimited by default, so an install that sets nothing
sees no change. Set the controller's with:

| Flag | Meaning |
|--------|-------------|
| `--max-bucket-bytes` | Stored bytes at or above which object writes freeze |
| `--max-bucket-objects` | Objects at or above which object writes freeze |
| `--warn-bucket-bytes` | Stored bytes at which health reports a warning |
| `--warn-bucket-objects` | Objects at which health reports a warning |
| `--bucket-reconcile` | Gap between bucket measurements, hourly by default |
| `--bucket-store` | Store URL the measurement reads, such as `s3://bucket/prefix` |
| `--bucket-measure-pages` | Listings one measurement may spend, 1000 by default |

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
controller measures it on the interval whether or not a ceiling is set,
so its totals are the bucket's rather than zeros, and serves none of it.
A controller pointed at no store, or at a backend that cannot total
itself, keeps the running count. The running count starts at zero on
restart, so a controller with no measurement source sees only what it
has written since it started.

The ceiling is the controller's alone; no other process takes one.

The controller measures on its ordinary timer. Measurement requests sit outside
the object-store request budget, because totalling a million-object bucket would
otherwise spend the per-minute list budget and refuse other readers. The walk
is bounded at `--bucket-measure-pages` listings (1000 by default, a thousand
objects each) and at half the reconciliation interval, whichever comes first.

A measurement that stops at either bound is discarded rather than folded
in, because a total short of the truth would thaw a store that is still
full. A measurement that fails, such as a listing the bucket's policy
denies, is discarded the same way. The ceiling then reports
`measurement_incomplete` on health, with a `problems` entry, and in
`sparkwing_object_store_bucket_ceiling_measurement_incomplete`; the
controller log and the breaker route's `measure_error` name the error.
It keeps counting writes until a measurement finishes. A bucket that keeps
reporting incomplete wants a higher `--bucket-measure-pages`, which is
the bound that binds first, and then a narrower prefix or S3 Inventory
in place of the listing.

`GET /api/v1/health` reports `object_store.ceiling` as `enforced`,
`frozen`, `warning`, `measurement_incomplete` and `measured_at` alone,
whenever a ceiling is set or a bucket is measured, because that route
answers without a token; the totals
and the ceilings sit behind the admin-scoped
`GET /api/v1/operator/object-store/breaker` and on `sparkwing operator object-store status`, which also reports `counted_at` (when the running
count last moved) and `reconciled_at` (when the bucket was last
measured). Alarm on
`sparkwing_object_store_bucket_ceiling_frozen` for the freeze and on
`sparkwing_object_store_bucket_bytes` against
`sparkwing_object_store_bucket_ceiling` for the approach; S3 publishes
`BucketSizeBytes` and `NumberOfObjects` daily at no charge, which is the
same signal from the account side.

To clear a storage freeze, raise the ceiling or delete objects until the next
measurement puts the bucket under it. With `--bucket-reconcile 0`, restart the
controller to take a new measurement after cleanup. Request-budget resets leave
the storage ceiling in force.

## Egress budgets

The controller authorizes logical data reads before signing short-lived download
URLs. A signed URL is charged when issued and remains usable for its lifetime.
The download counter is an abuse bound, because it cannot count every reuse of
an already-issued URL.

Log reads stay controller-proxied, reauthorize each read and meter exact egress.
They remain exempt from the team's daily data-download cap so a team can read
why its work failed.

Run-rate and usage quotas use store-wide counters. Request-rate, stream and
download overload guards are per replica. A deployment-wide usage limit must
not be presented as a process-local limiter. The supported topology is one
controller replica while cross-replica live fan-out remains unfinished.

Operator object-store settings use deployment authority. A team-admin credential
cannot change those ceilings. See [tenant limits](limits.md) and
[self-hosting](self-hosting.md#bound-storage-growth). Native accounting and
backend composition require separate acceptance evidence.
