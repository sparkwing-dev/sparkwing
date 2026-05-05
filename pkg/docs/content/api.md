# Controller HTTP API

The sparkwing controller exposes an HTTP API on port 8080. All endpoints return JSON unless otherwise noted.

## Authentication

Most endpoints require a bearer token:

```
Authorization: Bearer <SPARKWING_API_TOKEN>
```

**Exempt paths** (no auth required):
- `GET /health`
- `GET /metrics` (Prometheus)
- `GET /badge/{app}`
- `POST /webhooks/github` (HMAC verified via `GITHUB_WEBHOOK_SECRET`)

**Scoped tokens**: Created via `POST /tokens`, restricted to specific environments. Enforced on `/trigger`, `/authorize`, and `/secrets` endpoints.

**Rate limiting**:
- `/trigger`: 20 requests per minute per IP+pipeline
- Auth failures: 10 per IP per minute, then blocked 5 minutes (HTTP 429)

See [Security](security.md) for full details.

## Jobs

### POST /trigger

Enqueue a new pipeline job.

| Param | Type | Description |
|-------|------|-------------|
| `pipeline` | query (required) | Pipeline name |
| `branch` | query | Git branch (default: `main`) |
| `source` | query | `upload` if triggered with uploaded code |
| `upload_ref` | query | Upload tarball reference (from gitcache) |
| `repo` | query | Repository name |
| `github_sha` | query | Commit SHA |
| `github_owner` | query | GitHub repo owner |
| `github_repo` | query | GitHub repo name |
| `sparkwing_hash` | query | `.sparkwing/` content hash (for verified mode) |
| `environment` | query | Environment scope (enforced by scoped tokens) |
| `direct` | query | `true` for direct CLI invocation (bypasses taints) |
| `prefer` | query | Legacy agent preference selector |
| `require` | query | Legacy agent requirement selector |
| `concurrency_group` | query | Deduplication group name |
| `concurrency_limit` | query | Max concurrent jobs in group |
| `capture` | query | `true` to sync files back after execution |
| `claim` | query | Agent name to pre-claim the job |
| `env.*` | query | Environment variables (e.g. `env.FOO=bar`) |
| `arg.*` | query | Pipeline arguments (e.g. `arg.version=v1`) |

Response: Job object.

### GET /jobs

List jobs with pagination.

| Param | Type | Description |
|-------|------|-------------|
| `limit` | query | Max results (default: 50) |
| `offset` | query | Skip N results |
| `paginated` | query | `true` for `{jobs, total, limit, offset}` envelope |

### GET /jobs/{id}

Fetch a single job's full details.

### GET /jobs/{id}/status

Concise job status snapshot with computed fields.

### POST /jobs/{id}/status

Update job status and detail.

Request body: `{"status": "string", "detail": "string"}`

### POST /jobs/{id}/complete

Mark a job as complete.

Request body:
```json
{
  "success": true,
  "duration": 12.5,
  "exit_code": 0,
  "stdout": "...",
  "stderr": "...",
  "error": "..."
}
```

Response: `{"status": "complete"}` or `{"status": "failed"}`

### POST /jobs/{id}/heartbeat

Agent heartbeat to keep a running job alive. Must be sent every 5 seconds. Jobs without a heartbeat for 40 seconds are marked `agent_lost` (60-second startup grace).

### POST /jobs/{id}/cancel

Cancel a running job. Kills the runner pod if running. Reports cancellation to GitHub.

Response: `{"status": "cancelled"}`

### POST /jobs/{id}/retry

Re-enqueue a failed job with the same parameters. Cancels original if still active.

Response: `{"original_job": "id", "new_job": "id", "status": "retried"}`

### GET /jobs/{id}/metrics

Fetch CPU/memory resource usage data points for a job.

Response:
```json
{
  "points": [
    {"ts": "2026-04-12T10:00:00Z", "cpu_millicores": 450, "memory_bytes": 536870912}
  ],
  "memory_limit_bytes": 2147483648,
  "cpu_limit_millicores": 2000
}
```

### POST /jobs/{id}/meta

Update job metadata (commit SHA, repo name).

Request body: `{"commit": "abc123", "repo_name": "myapp"}`

### POST /jobs/{id}/job-status

Post sub-job status to GitHub commit status API.

Request body: `{"job": "test", "status": "success"}`

## Agent Polling

### GET /jobs/next

Long-polling endpoint for agents to claim pending jobs. Waits up to 30 seconds.

| Param | Type | Description |
|-------|------|-------------|
| `name` | query | Agent name |
| `type` | query | Agent type (e.g. `warm-runner`) |
| `labels` | query | Comma-separated `key:value` labels |
| `taints` | query | Comma-separated taints |
| `active` | query | Number of currently active jobs |
| `max_concurrent` | query | Agent's max concurrent capacity |

Response: Job object or 204 No Content.

The controller uses labels, taints, and tolerations to match jobs to agents. See [Scheduling](scheduling.md).

### GET /agents

List all connected agents with capacity and health.

## Breakpoints

### GET /jobs/{id}/breakpoint

Poll for breakpoint signal. Returns `{"status": "paused" | "continue" | "abort"}`.

### POST /jobs/{id}/breakpoint

Runner reports it hit a breakpoint.

Request body: `{"job": "id", "status": "paused"}`

### POST /jobs/{id}/breakpoint-continue

Resume a paused job. Response: `{"status": "continued"}`

## Log Streaming

### POST /logs/{jobID}

Runner posts log lines.

Request body: `{"lines": ["line1", "line2"]}`

### GET /logs/{jobID}

Subscribe to live log stream via Server-Sent Events (SSE). Sends buffered lines first, then streams new ones. 5-minute timeout.

Auth: Accepts `?token=` query param (EventSource can't set headers).

## Webhooks

### POST /webhooks/github

Receives GitHub webhook payloads. The controller verifies HMAC signatures directly using `GITHUB_WEBHOOK_SECRET`.

Handles GitHub `push`, `pull_request`, and `ping` events. Matches against `pipelines.yaml` trigger rules. Supports concurrency groups and cancel-in-progress.

Response: `{"job_ids": ["id1", "id2"], "status": "triggered"}`

## Authorization

### POST /authorize

Pre-deploy authorization check. Verifies commit is on the protected branch. Enforces scoped token environment restrictions. Logs to audit trail.

| Param | Type | Description |
|-------|------|-------------|
| `pipeline` | query (required) | Pipeline name |
| `environment` | query | Environment scope |
| `commit` | query | Commit SHA to verify |
| `repo` | query | Repository name |
| `branch` | query | Branch to check against (default: `main`) |

Response: `{"status": "authorized", "pipeline": "...", "commit": "..."}`

### GET /audit

List the last 100 authorization/denial events with timestamps and IPs.

## Secrets

Requires `SPARKWING_SECRET_KEY` env var on the controller.

### POST /secrets

Store an encrypted secret. Scoped token environment restrictions apply.

Request body: `{"name": "DATABASE_URL", "value": "...", "environment": "production"}`

### GET /secrets

List secret names and environments (values never returned).

### DELETE /secrets

Delete a secret. Scoped token environment restrictions apply.

Request body: `{"name": "DATABASE_URL", "environment": "production"}`

## Tokens

Root token only. See [Security](security.md) for scoped token details.

### POST /tokens

Create a scoped API token.

Request body: `{"name": "staging-cd", "environments": ["staging"]}`

Response includes the raw token (shown only once).

### GET /tokens

List all scoped tokens with environments and creation date.

### DELETE /tokens

Revoke a scoped token.

Request body: `{"id": "abc12345"}`

## Pipelines

### GET /pipelines

List all pipelines with argument schemas and tags.

### GET /pipelines/{name}/args

Get argument schema for a specific pipeline.

### POST /pipelines/{name}/args

Store/update argument schema for a pipeline.

### POST /pipelines/{name}/tags

Store/update tags metadata for a pipeline.

## Work Locks (Deduplication)

Used by runners to coordinate shared work across concurrent jobs.

### POST /work/claim

Attempt to claim a work lock.

| Param | Type | Description |
|-------|------|-------------|
| `key` | query (required) | Work lock key |
| `job` | query (required) | Job ID claiming the lock |

Response: `{"owner": true}` if this job should execute, or `{"owner": false, "owner_job_id": "..."}` if another job owns it.

### POST /work/complete

Mark claimed work as complete.

Request body: `{"key": "...", "success": true, "result": {...}}`

### GET /work/status

Poll status of a work lock.

| Param | Type | Description |
|-------|------|-------------|
| `key` | query (required) | Work lock key |

Response: Work lock entry with status/owner/result, or `{"status": "none"}`.

## Monitoring

### GET /health

Health check. Returns `{"status": "ok"}` or `{"status": "degraded"}`.

### GET /metrics

Prometheus metrics endpoint. Always active. See [Observability](observability.md) for metric names.

### GET /api/metrics

Dashboard-friendly aggregated metrics with 10-second cache.

Response:
```json
{
  "total_jobs": 1234,
  "by_status": {"complete": 1000, "failed": 100, ...},
  "by_app": {"myapp": 500, ...},
  "avg_duration_ms": 12500,
  "cache_hit_rate": 0.45,
  "last_24h": {"total": 50, "passed": 45, "failed": 5}
}
```

### GET /api/trends

Time-series job trends.

| Param | Type | Description |
|-------|------|-------------|
| `pipeline` | query | Filter by pipeline name |
| `hours` | query | Lookback window (default: 72) |

Response: Array of trend points with total, passed, failed, cached counts and avg/p95 duration.

### GET /api/health/services

Health of all sparkwing services. Cached 10 seconds.

Response: `{"services": [{"name": "gitcache", "status": "healthy", "latency_ms": 5}]}`
