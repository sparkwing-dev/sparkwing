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

## Enabling the pool

The pool is **off by default**. Start sparkwing-controller with `--pool`
(which also requires `--pool-namespace`, or `POD_NAMESPACE`) to enable it.
With the pool disabled, the `/api/v1/pool` routes 404 and builds run
without warm caches.

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

### HTTP endpoints

The controller serves the pool API under `/api/v1/pool` on its own bind
address (`--addr`, default `127.0.0.1:4344`), and only when the pool is
enabled (see [Enabling the pool](#enabling-the-pool)); otherwise these
routes return 404. Each returns 503 until the warming/reconcile loops
report ready.

| Endpoint | Method | Scope | Description |
|----------|--------|-------|-------------|
| `GET /api/v1/pool` | List | `runs.read` | Full pool state with all PVCs |
| `POST /api/v1/pool/checkout?job_id=<id>` | Checkout | `admin` | Atomically claim a `clean` PVC for a job (200 with an empty `pvc` field when none is free; 409 only on an underlying claim error) |
| `POST /api/v1/pool/return?pvc=<name>` | Return | `admin` | Release a PVC back to `clean` |
| `POST /api/v1/pool/heartbeat?pvc=<name>&job_id=<id>` | Heartbeat | `admin` | Renew the checkout lease |

### Configuration

The controller reads pool config from the `config.yaml` key of a
ConfigMap named `sparkwing-cache-config`. The warming parameters
(`warm_images` and `refresh_interval`) are re-read continuously by the
warming loop, so changes take effect without a restart. The remaining
parameters (`pool_size`, `pvc_size`, `heartbeat_timeout`, and
`startup_grace`) are read once at controller startup and require a
restart to change. The YAML below is the value stored under
`data.config.yaml`:

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

## Consuming a warm PVC

A consumer claims a PVC through the checkout API for the duration of a
build:

1. `POST /api/v1/pool/checkout?job_id=<id>` returns HTTP 200 with a
   `clean` PVC's name, or HTTP 200 with an empty `pvc` field when none is
   free — consumers must check for an empty name. A 409 is returned only
   when the underlying claim fails.
2. The build mounts that PVC as its Docker layer cache and renews the
   lease via `POST /api/v1/pool/heartbeat` so the reconcile loop doesn't
   reclaim it mid-build.
3. On completion the consumer calls `POST /api/v1/pool/return`; the PVC
   goes back to `clean` (cache preserved) regardless of build outcome.

The pod spec that mounts the claimed PVC and wires the Docker daemon is
part of the cluster deployment (the Helm chart ships separately from this
repo), not the CLI. The pool is optional: an empty PVC name on checkout
means the build runs without a warm cache rather than failing.

## Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `sparkwing.pool.reconcile_duration` | Histogram | Reconciliation loop time |
| `sparkwing.pool.warm_duration` | Histogram | Warm operation time (with success/failed label) |
| `sparkwing.pool.checkouts` | Counter | PVC checkouts |
| `sparkwing.pool.returns` | Counter | PVC returns |

## Monitoring

Check pool status against the controller's bind address:

```bash
curl http://127.0.0.1:4344/api/v1/pool
```

```json
{
  "pool_size": 2,
  "pvc_size": "20Gi",
  "pvcs": [
    {"name": "sparkwing-cache-pool-0", "state": "clean", "warmed_at": "2026-04-12T14:30:00Z"},
    {"name": "sparkwing-cache-pool-1", "state": "in-use", "checked_out_by": "job-abc123", "checked_out_at": "2026-04-12T14:35:00Z"}
  ]
}
```
