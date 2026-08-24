# Observability

Sparkwing tracks run health, failure reasons, and resource usage so you
can debug failures fast and right-size containers.

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
| `agent_lost` | Runner stopped heartbeating (crashed, evicted, or lost network). | Check pod events with `kubectl describe pod`; may indicate node pressure or a pipeline bug. |
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

One sampler runs per process and splits each interval's reading evenly
across the nodes running in it. A node's chart is therefore an estimate
of that node's share rather than a measurement of it -- but the nodes of
a parallel fan-out sum to what the process really used, which is what
right-sizing and admission both need. A node joins and leaves on tick
boundaries, so up to one interval of its cost can land on the nodes
beside it.

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
  headroom, every holder, every waiter in order with its ETA. Mirrors
  `sparkwing queue`.
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

**Cache** (`sparkwing-cache`, OTEL meter):

| Metric | Type | Description |
|--------|------|-------------|
| `sparkwing.gitcache.archives_served` | Counter | Archive downloads |
| `sparkwing.gitcache.files_served` | Counter | Single-file downloads |
| `sparkwing.gitcache.fetch_duration` | Histogram | Background fetch time |
| `sparkwing.gitcache.cache_hits` | Counter | Cache hits (git archive and binary/dependency, distinguished by `type` attribute) |
| `sparkwing.gitcache.cache_misses` | Counter | Cache misses (git archive and binary/dependency, distinguished by `type` attribute) |
| `sparkwing.gitcache.recovery_reclones` | Counter | Full mirror re-downloads after a failed fetch, by `repo` hash. Should be near zero -- a repo that keeps appearing here has a persistent fetch failure (see [Cache](gitcache.md#recovery-reclone-circuit-breaker)) |
