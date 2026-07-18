# Observability

Sparkwing tracks job health, failure reasons, and resource usage so you
can debug failures fast and right-size containers.

## Failure reasons

Every failed job carries a `failure_reason` in its result. The
controller classifies failures automatically -- you never have to grep
logs to figure out *why* a build died.

| Reason | What happened | What to do |
|---|---|---|
| `oom_killed` | Container exceeded its memory limit and was killed by the kernel (exit 137). | Raise the runner memory limit or reduce the pipeline's memory use; check the resource chart. |
| `timeout` | Job exceeded its configured execution timeout. | Raise the timeout or optimize the pipeline. |
| `agent_lost` | Runner stopped heartbeating (crashed, evicted, or lost network). | Check pod events with `kubectl describe pod`; may indicate node pressure or a pipeline bug. |
| `queue_timeout` | No runner claimed the job within the queue timeout (default 15m). | Ensure runners are up and their advertised `--label` set satisfies the pipeline's `requires:` / node `.Requires()`. |
| `runner_lease_expired` | The runner holding the node's claim stopped renewing its lease, so the controller reclaimed it. | Check the runner's health; the node is safe to retry. |
| `verify` | The node's action completed, but its `Verify` postcondition returned an error -- the failure is at the verify stage, not the action. | Inspect the `Verify` assertion and the action's actual output. |
| `logs_auth` | The runner's log-append calls were rejected (401/403) by the controller, so the run's structured logs are unrecoverable. | Check the runner token's `logs.write` scope; the run fails loud rather than reporting success with no output. |

A plain pipeline-level failure (a failed test or command) carries no
structured `failure_reason` -- read the logs.

### How detection works

The Kubernetes runner polls its Job/pod status while a node runs. When
it sees a terminated container (e.g. `OOMKilled`, non-zero exit), it
fails the node **immediately** with the specific reason rather than
waiting for the heartbeat timeout.

For nodes where the pod disappears entirely (node failure, eviction),
the controller's heartbeat sweep catches the missed lease and marks the
node `agent_lost`.

### API

The failure reason is available in all job responses:

```json
{
  "result": {
    "success": false,
    "failure_reason": "oom_killed",
    "exit_code": 137,
    "logs": "Container \"runner\" was killed by the kernel OOM killer..."
  }
}
```

## Resource usage metrics

While a node runs, the runner samples its own CPU and memory in-process
(reading `/proc`) roughly every 2 seconds. Samples are stored and
charted in the dashboard. No cluster metrics-server is involved.

### What's measured

- **CPU**: millicores, derived from the runner process's CPU time.
- **Memory**: resident bytes (RSS).

The dashboard charts the samples over time with peak and average in the
header.

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
2. Open the job detail in the dashboard and expand **Resources**
3. Compare peak usage to your pod's configured limits:
   - If peak memory is close to the limit → increase the limit or
     optimize memory usage
   - If peak CPU is well below the limit → you can safely lower requests
     to save cluster resources
   - If CPU is consistently at the limit → the pipeline is CPU-bound;
     increase the limit for faster builds

## Dashboard

The dashboard shows failure information at every level:

- **Home page**: failure reason badges in the recent builds table
- **Pipelines page**: failure reason badge in the summary header, plus
  a prominent banner with contextual help text
- **Resources section**: collapsible CPU/memory charts in the job
  detail panel (auto-refreshes for running jobs)

## Data retention

Finished runs (and their metrics) are kept until you prune them. There
is no automatic time-based cleanup; use `sparkwing runs prune` to delete
runs past a threshold or by id (see [cli-reference.md](cli-reference.md)).

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
