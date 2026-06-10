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
| `queue_timeout` | No runner claimed the job within the queue timeout (default 10m). | Ensure runners are up and their advertised `--label` set satisfies the pipeline's `requires:` / node `.Requires()`. |
| `runner_lease_expired` | The runner holding the node's claim stopped renewing its lease, so the controller reclaimed it. | Check the runner's health; the node is safe to retry. |
| `verify` | The node's action completed, but its `Verify` postcondition returned an error -- the failure is at the verify stage, not the action. | Inspect the `Verify` assertion and the action's actual output. |
| `logs_auth` | The runner's log-append calls were rejected (401/403) by the controller, so the run's structured logs are unrecoverable. | Check the runner token's `logs.write` scope; the run fails loud rather than reporting success with no output. |

A plain pipeline-level failure (a failed test or command) carries no
structured `failure_reason` -- read the logs.

### How detection works

The controller dispatcher polls pod container statuses every 3 seconds.
When it sees a terminated container (e.g. `OOMKilled`, non-zero exit),
it fails the job **immediately** with a specific reason -- no waiting for
the 40-second heartbeat timeout.

For jobs where the pod disappears entirely (node failure, eviction), the
heartbeat monitor catches it within 40 seconds and marks the job as
`agent_lost`.

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

The controller collects CPU and memory samples from the Kubernetes
metrics API every 10 seconds while a job is running. These are stored
in SQLite and displayed as charts in the dashboard.

### Requirements

- **metrics-server** must be installed in the cluster (`kubectl top pods`
  should work). Most managed Kubernetes clusters include it by default.
  If yours does not, install with `kubectl apply -f
  https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml`.

### What's measured

- **CPU**: total millicores across all containers (runner + DinD sidecar
  if present)
- **Memory**: total bytes across all containers

The charts show:

- Data points over time (area chart)
- Resource **limits** as dashed lines (2 CPU / 2Gi for runner, plus DinD
  if applicable)
- Peak and average values in the header

### API

```
GET /jobs/{id}/metrics
```

Returns:

```json
{
  "points": [
    { "ts": "2026-04-12T10:00:00Z", "cpu_millicores": 450, "memory_bytes": 536870912 },
    { "ts": "2026-04-12T10:00:10Z", "cpu_millicores": 1200, "memory_bytes": 1073741824 }
  ],
  "memory_limit_bytes": 2147483648,
  "cpu_limit_millicores": 2000
}
```

Data is retained for 7 days and cleaned up automatically.

### Using metrics to right-size containers

1. Run your pipeline a few times
2. Open the job detail in the dashboard and expand **Resources**
3. Compare peak usage to the limit lines:
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

## Data Retention

- **Resource metrics** (CPU/memory data points): 7 days, cleaned automatically
- **Jobs and audit logs**: 30 days by default, configurable via `SPARKWING_RETENTION_DAYS` env var on the controller

## OpenTelemetry

Every sparkwing service initializes OpenTelemetry and exposes a Prometheus
`/metrics` endpoint. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to additionally
export traces and structured logs via OTLP.

### Prometheus /metrics

Always active on every service. VictoriaMetrics (or your own Prometheus)
can scrape these directly.

### OTLP export

When `OTEL_EXPORTER_OTLP_ENDPOINT` is set:

- **Traces**: Exported via `otlptracehttp` directly to Tempo (spans for job lifecycle, HTTP requests)
- **Logs**: Exported via `otlploghttp` directly to Loki (structured logs with trace correlation)
- **Metrics**: Served via Prometheus `/metrics` - VictoriaMetrics scrapes them

There is no in-cluster OTEL collector. Services export traces and logs
directly to Tempo/Loki, and VictoriaMetrics scrapes `/metrics` endpoints.

### Metrics reference

**Controller** (`sparkwing-controller`):

| Metric | Type | Description |
|--------|------|-------------|
| `sparkwing.jobs.triggered` | Counter | Total jobs enqueued |
| `sparkwing.jobs.completed` | Counter | Terminal jobs by status |
| `sparkwing.job.duration` | Histogram | Execution time |
| `sparkwing.job.queue_wait` | Histogram | Time from enqueue to claim |
| `sparkwing.heartbeat.age` | Histogram | Heartbeat freshness |
| `sparkwing.jobs.active` | Gauge | Active jobs by status |
| `sparkwing.agents.connected` | Gauge | Connected agents |
| `sparkwing.http.requests_total` | Counter | HTTP requests by route, method, status |
| `sparkwing.http.request_duration` | Histogram | HTTP latency by route |

**Cache** (`sparkwing-cache`):

| Metric | Type | Description |
|--------|------|-------------|
| `sparkwing.gitcache.archives_served` | Counter | Archive downloads |
| `sparkwing.gitcache.files_served` | Counter | Single-file downloads |
| `sparkwing.gitcache.fetch_duration` | Histogram | Background fetch time |
| `sparkwing.gitcache.cache_hits` | Counter | Binary/dependency cache hits |
| `sparkwing.gitcache.cache_misses` | Counter | Binary/dependency cache misses |

**Logs** (`sparkwing-logs`):

| Metric | Type | Description |
|--------|------|-------------|
| `sparkwing.logs.ingest_bytes` | Counter | Bytes of logs ingested |
| `sparkwing.logs.sse_connections` | Gauge | Active SSE connections |

Webhook metrics (`sparkwing.webhook.*`) and pool metrics (`sparkwing.pool.*`)
are now part of the controller, since webhook handling and pool management
were merged into sparkwing-controller.

Package proxy metrics (`sparkwing.proxy.*`) are now part of sparkwing-cache,
since the proxy was merged into the cache service.
