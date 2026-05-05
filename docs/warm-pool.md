# Warm PVC Pool

The warm pool pre-loads Docker build caches onto PVCs so pipeline builds start with warm caches instead of pulling and compiling everything from scratch.

## How it works

The controller maintains a pool of PVCs, warms them with Docker images, and handles checkout/return. All jobs dispatch as k8s Jobs - the controller's dispatcher creates a one-shot k8s Job and optionally attaches a warm PVC for Docker cache.

## PVC lifecycle

Each PVC goes through these states:

```
dirty → warming → clean → in-use → clean (returned)
                            ↓
                          dirty (reclaimed after timeout)
```

| State | Meaning |
|-------|---------|
| `dirty` | Needs warming (new or reclaimed) |
| `warming` | Warmer pod is pulling images into it |
| `clean` | Ready for checkout |
| `in-use` | Checked out by a running job |

Key design: when a job finishes, the PVC goes back to `clean` (not `dirty`). The Docker cache is still valid. Periodic age-based re-warming handles staleness.

## Pool Management

Pool management runs inside sparkwing-controller in the sparkwing namespace.

### Reconciliation loop (every 15 seconds)

- Ensures the configured number of PVCs exist (creates missing ones)
- Scans all `in-use` PVCs for abandoned ownership
- Reclaims PVCs where the heartbeat is older than `heartbeat_timeout` (default 5 minutes)
- New checkouts get a `startup_grace` period (default 2 minutes) before the first heartbeat is expected

### Warming loop (continuous)

- Picks the stalest PVC that needs warming:
  1. `dirty` PVCs first (no warm timestamp)
  2. `clean` PVCs older than `refresh_interval` (default 1 hour)
- Launches an ephemeral warmer pod that:
  - Mounts the target PVC at `/var/lib/docker`
  - Runs a privileged DinD container
  - Pulls all images listed in the ConfigMap
  - Timeout: 30 minutes per warm cycle
- On success: marks PVC `clean`, updates `warmed-at`
- On failure: marks PVC `dirty` for retry

### HTTP endpoints (port 8090)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `POST /checkout?job_id=<id>` | Checkout | Atomically claim a `clean` PVC for a job |
| `POST /return?pvc=<name>` | Return | Release PVC back to `clean` state |
| `POST /heartbeat?pvc=<name>&job_id=<id>` | Heartbeat | Keep PVC alive (every 5s) |
| `GET /pool` | Status | Full pool state with all PVCs |
| `GET /health` | Health | Liveness check |

### Configuration

The controller reads pool config from a ConfigMap (`sparkwing-cache-config`), reloaded every reconciliation cycle:

```yaml
# Images to pre-pull into each pool PVC
warm_images:
  - docker.io/library/golang:1.25-alpine
  - docker.io/library/alpine:3.21
  - docker.io/library/node:22-alpine
  - docker.io/library/docker:27
  - docker.io/library/docker:27-dind

pool_size: 2            # Number of PVCs to maintain
pvc_size: 20Gi          # Storage per PVC
refresh_interval: 1h    # Re-warm clean PVCs older than this
heartbeat_timeout: 5m   # Reclaim in-use PVCs with no heartbeat
startup_grace: 2m       # Grace period before first heartbeat expected
```

### PVC annotations (source of truth)

| Annotation | Purpose |
|-----------|---------|
| `sparkwing.dev/pool-state` | Current state (clean/in-use/dirty/warming) |
| `sparkwing.dev/warmed-at` | Last successful warm timestamp |
| `sparkwing.dev/checked-out-by` | Job ID that owns the PVC |
| `sparkwing.dev/checked-out-at` | When checkout happened |
| `sparkwing.dev/heartbeat-at` | Last heartbeat timestamp |

## Dispatcher PVC usage

When the dispatcher creates a one-shot k8s Job, it checks out a warm PVC:

1. Dispatcher calls the internal pool checkout for the job
2. If a PVC is available: the dispatcher creates a DinD **sidecar** on the runner pod, mounting the warm PVC
3. If no PVC available: the runner uses the **shared** DinD service instead

```
PVC available:
  Runner pod -> DinD sidecar (tcp://localhost:2375) -> warm PVC at /var/lib/docker

No PVC:
  Runner pod -> shared DinD service (tcp://dind.sparkwing.svc.cluster.local:2375)
```

The dispatcher heartbeats the PVC every 5 seconds while the job runs, and returns it on completion (success or failure - the cache is preserved either way).

## Graceful degradation

The warm pool is optional. If all PVCs are in use:

- The dispatcher falls back to shared DinD
- Jobs still run, just without warm Docker caches
- No jobs are lost or delayed

## Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `sparkwing.pool.reconcile_duration` | Histogram | Reconciliation loop time |
| `sparkwing.pool.warm_duration` | Histogram | Warm operation time (with success/failed label) |
| `sparkwing.pool.checkouts` | Counter | PVC checkouts |
| `sparkwing.pool.returns` | Counter | PVC returns |

## Monitoring

Check pool status:

```bash
curl http://sparkwing-controller:8080/pool
```

```json
{
  "total": 2,
  "clean": 1,
  "in_use": 1,
  "dirty": 0,
  "warming": 0,
  "pvcs": [
    {"name": "sparkwing-cache-pool-0", "state": "clean", "warmed_at": "2026-04-12T14:30:00Z"},
    {"name": "sparkwing-cache-pool-1", "state": "in-use", "checked_out_by": "job-abc123"}
  ]
}
```
