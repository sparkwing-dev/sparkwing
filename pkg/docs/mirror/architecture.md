# Architecture

**This page describes the production deployment** - the sparkwing stack
running in a shared Kubernetes cluster, where webhooks arrive from GitHub,
a team looks at a central dashboard, and runners are pooled for work.

**For local dev, almost none of this applies.** On a laptop, `sparkwing`
compiles and runs your pipeline as a host subprocess and records each
run under `~/.sparkwing/`. `sparkwing dashboard start` spawns a detached
local web server (`pkg/localws`, embedded in the CLI); it owns the
SQLite store, the log files, and the dashboard on one port (default
`http://127.0.0.1:4343`) - no controller pod, no cache, no runner
pods, no separate logs service. See [native-mode.md](native-mode.md).

The rest of this page is about the in-cluster shape you deploy once per
team, not once per developer.

---

Sparkwing (prod deployment) is a self-hosted CI/CD platform that runs on
Kubernetes. The stack is five pods: a controller, cache, web, runner,
and logs. Building container images (Docker-in-Docker) and hosting an
image registry, when a pipeline needs them, are external infrastructure
the chart does not deploy.

## Components

```
┌──────────────────────────────────────────────────────────────────┐
│                      Kubernetes Cluster                            │
│                                                                    │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐             │
│  │  Controller  │  │   Cache      │  │   Web        │             │
│  │ (API + queue │  │  (git HTTP + │  │  (dashboard) │             │
│  │  + webhooks  │  │   blob store │  │              │             │
│  │  + pool mgmt)│  │   + pkg proxy│  │              │             │
│  └──────┬───────┘  └──────────────┘  └──────────────┘             │
│         │                                                          │
│  ┌──────┴───────┐  ┌──────────────┐                               │
│  │  Runner      │  │   Logs       │                               │
│  │  (warm pool, │  │  (log store) │                               │
│  │   polls +    │  │              │                               │
│  │   claims)    │  │              │                               │
│  └──────────────┘  └──────────────┘                               │
└──────────────────────────────────────────────────────────────────┘
         ▲                    ▲
         │                    │
    ┌────┴────┐          ┌────┴────┐
    │  sparkwing   │          │  git    │
    │  (CLI)  │          │ (push)  │
    └─────────┘          └─────────┘
```

Five pods: sparkwing-controller, sparkwing-cache, sparkwing-web,
sparkwing-runner, and sparkwing-logs.

### Controller

The central coordinator. Receives job triggers, queues work, and
serves it to runners that poll and claim.

- **API server** (port 4344): HTTP endpoints for triggers, run status,
  agent polling, secrets, and authorization
- **Job queue**: in-memory queue with SQLite persistence
  (`/data/state.db`) for run state, metadata, secrets, and tokens
- **Webhooks**: receives GitHub webhook payloads, verifies HMAC
  signatures, and triggers matching pipelines
- **Pool management**: maintains a pool of PVCs pre-loaded with Docker
  build cache; handles checkout and return for runner jobs
- **Run backend**: holds pending runs for runners to poll and claim; the
  controller does not push work to runners
- **Heartbeat monitor**: reclaims a node whose runner stops renewing its
  lease (default 3-minute lease)
- **Queue timeout**: fails pending nodes that exceed their `queue_timeout`
  (default 15 minutes)
- **Metrics collector**: stores the per-node CPU/memory samples runners
  push as they execute (no cluster metrics-server involved)

### Runner

Executes pipeline binaries. A standing warm-pool Deployment runs the
unified `sparkwing-runner` binary, which polls the controller and claims
pending nodes. For per-node isolation it launches a Kubernetes Job that
runs `sparkwing run-node`. The runner downloads code from the cache,
compiles and runs the pipeline, and reports results.

Off-cluster runners (developer machines, workstations, and servers) connect to
the controller and claim nodes through its claim API; the route set and scopes
are in [api-reference.md](api-reference.md).

### Cache

Git HTTP server, blob store, and package proxy. Mirrors bare repositories
from GitHub with a background fetch loop (every 30 seconds). Serves git
clones over HTTP so runners do not need SSH keys.

Also stores:

- **Source snapshots**: SHA-scoped Git bundles for unpublished commits and
  opt-in working-tree triggers
- **Artifacts**: job output files
- **Binary cache**: compiled pipeline binaries
- **Dependency cache**: saved / restored by pipelines (gems, node_modules, etc.)
- **Package proxy**: caching reverse proxy for npm, PyPI, Go modules,
  RubyGems, and Alpine packages

See [Cache](gitcache.md) for endpoints and configuration.

### Dashboard

Next.js web app showing pipeline runs, logs, node status, and
documentation.

Its services panel (`GET /api/v1/health/services`) probes the health
endpoint of each service it has been given a URL for: the controller
and logs service from `--controller` / `--logs`, and the cache from
`--cache` (probe-only; omit it and the cache is left off the panel).
The `sparkwing-full` chart fills all three in: `web.cache.url` defaults
to the runner-bundle's cache Service the same way `web.logs.url`
defaults to its logs Service, and a release that deploys no cache
starts the web pod without the flag.

Services report partial failure in the body while still answering
HTTP 200 -- a filling disk, a stalled fetch loop, an unwritable cache
directory -- and reserve a 5xx for a total outage. The panel decodes
that body, so a service reporting `{"status":"degraded","problems":
[...]}` shows amber with its problems listed, not green. Slowness is
measured here rather than reported by the service, so a service that is
both slow and degraded lists both.
`sparkwing configure profiles test` applies the same rule from the CLI,
and additionally fails when a health body cannot be read at all: it
answers an operator once, where the panel repaints on a cycle.

### DinD (Docker-in-Docker)

Optional, external infrastructure the chart does not deploy. When a
pipeline builds container images, point runners at a shared Docker
daemon; runner jobs connect to it, optionally with a warm PVC mounted for
Docker cache.

### Registry

Optional, external infrastructure the chart does not deploy. Pipelines
that build images push them to a registry you provide - an in-cluster one
you run yourself, or an external service (ECR, GCR, Docker Hub, etc.).
That is up to the pipeline author.

### Logs

Dedicated log storage and streaming service. Runners send step output via
HTTP; the dashboard reads live logs via SSE.

## Component Communication

All in-cluster communication uses Kubernetes service DNS names. Every
component talks over HTTP - there are no custom protocols.

### Who talks to whom

```
sparkwing CLI ──────► Controller   trigger a run; poll until terminal
GitHub ────────► Controller        push webhook (HMAC verified)
Controller ────► k8s API           warm PVC pool (PVCs, warmer pods)
Runner ────────► k8s API           create / watch per-node Jobs
Runner ────────► Controller        claim node; heartbeat; report finish; fetch details
Runner ────────► Cache             clone repo, download code + packages
Runner ────────► Logs              stream step output
Runner ────────► DinD              Docker builds (tcp://localhost:2375)
Runner ────────► Registry          docker push (localhost:30500)
Dashboard ─────► Controller        read runs / agents / pipelines
Dashboard ─────► Logs              live log stream (SSE)

Cache ─────────► GitHub            git fetch (background, every 30s)

sparkwing CLI ──────► Cache             refresh or seed an exact Git commit
sparkwing CLI ──────► Controller        seed/query source through authenticated proxy
```

### Network policies

The charts deploy no NetworkPolicy. On a cluster that runs default-deny
ingress, each component needs these allow rules:

| Component | Accepts traffic from |
|-----------|---------------------|
| Controller | External (webhooks), Dashboard, Runners |
| Cache | Controller, Runners |
| DinD | Runners, Controller (cache warmers) |
| Dashboard | External (port 4343) |
| Logs | Runners, Dashboard |
| Registry | Runners, Nodes (image pulls) |

### Internal service addresses

All components discover each other via k8s DNS. No hardcoded IPs.

| Service | Internal address | Port |
|---------|-----------------|------|
| Controller | `sparkwing-controller.sparkwing.svc.cluster.local` | 80 -> 4344 |
| Cache | `sparkwing-cache.sparkwing.svc.cluster.local` | 80 -> 8090 |
| Logs | `sparkwing-logs.sparkwing.svc.cluster.local` | 80 -> 4345 |
| DinD | `dind.sparkwing.svc.cluster.local` | 2375 |
| Dashboard | `sparkwing-web.sparkwing.svc.cluster.local` | 80 -> 4343 |
| Registry | `registry.registry.svc.cluster.local` | 5000 (NodePort 30500) |

### Environment variables set on runners

These are set on every runner pod:

| Variable | Purpose |
|----------|---------|
| `SPARKWING_CONTROLLER_URL` | Controller base URL |
| `SPARKWING_LOGS_URL` | Logs service URL |
| `SPARKWING_RUN_ID` | The run this node belongs to |
| `SPARKWING_NODE_ID` | The node being executed |
| `SPARKWING_HOME` | State / cache / logs root |
| `SPARKWING_AGENT_TOKEN` | Bearer token for controller + logs calls |

### Environment variables set on a local node process

A local run executes each node as its own process, so the same
variables above are set on it. A run whose state lives behind the
admission daemon also gets `SPARKWING_API_SOCKET` and no
`SPARKWING_AGENT_TOKEN`: the node sends its state and concurrency calls
down that unix socket and the daemon takes its peer uid as the
principal. `SPARKWING_CONTROLLER_URL` is set either way, but on that
path it is a placeholder host the socket transport ignores, so a step
that dials it reaches nothing. A run that opens the store itself points
that variable at a loopback controller the dispatcher mounts for the
run and passes that controller's token. More variables describe the
process boundary itself. Sparkwing sets all of them; they are not
knobs.

| Variable | Purpose |
|----------|---------|
| `SPARKWING_API_SOCKET` | The admission daemon's controller API socket, when the run reaches its state through the daemon |
| `SPARKWING_PARENT_LIVENESS_FD` | Descriptor the node reads to notice its dispatcher died, so an abandoned node stops rather than running on against a run nobody owns |
| `SPARKWING_RUNNER_NAME` | `local` -- the runner name `Runtime().Runner` reports |
| `SPARKWING_RUNNER_TYPE` | `local` -- the runner type `Runtime().Runner` reports |
| `SPARKWING_RUNNER_LABELS` | The labels the local runner advertises, comma-separated; what `WhenRunner` matches against |

`SPARKWING_PARENT_LIVENESS_FD` in particular should never be set by
hand: it is the node's authority to read and close that descriptor, and
naming one sparkwing did not open points the node at another
subsystem's file.

### Controller API endpoints

The controller's full route set, methods, and required scopes are in
[api-reference.md](api-reference.md).

## Data Flow

### Local Development

```
sparkwing run build-deploy
  → compiles .sparkwing/ into a binary
  → runs the binary locally
  → pipeline does whatever its code says (build, test, deploy, etc.)
```

### Remote Execution (pipeline trigger)

```
sparkwing pipeline trigger build-deploy --profile <cluster>
  1. sparkwing resolves the profile -> controller URL
  2. sparkwing refreshes or seeds the exact Git commit in the cache
  3. sparkwing POSTs the trigger with that commit SHA
  4. controller enqueues run
  5. a runner polls the controller and claims the run
  6. runner clones the exact SHA from cache
  7. runner compiles and runs the pipeline binary
  8. runner streams logs to logs service
  9. runner sends periodic heartbeats to controller to hold its claim
  10. runner reports completion to controller
  11. sparkwing pipeline trigger follows controller state and displays result
```

`--working-tree` replaces step 2 with a mandatory synthetic-commit bundle
seed. The trigger is not admitted if that upload fails. Off-cluster runners can
read source through the controller's authenticated Git proxy, so they need only
outbound HTTPS; a private direct cache remains an alternative, uses only
`SPARKWING_CACHE_TOKEN` for writes, and never receives the controller bearer.
Login-enabled dashboard ingress exposes the
same machine-bearer proxy path without browser-session authentication.

### Git Push Trigger

```
git push origin main
  1. GitHub sends webhook to sparkwing-controller (external)
  2. Controller verifies HMAC signature
  3. Controller matches push against sparkwing.yaml triggers
  4. Controller enqueues matching runs
  5. Same execution flow as steps 5-11 above
```

## Storage

| Component | Storage | Contents |
|-----------|---------|----------|
| Controller | SQLite at `/data/state.db` | Run state, metadata, secrets, tokens, audit log |
| Cache | PVC at `/data/` | Bare repos, uploads, artifacts, binary cache, dependency cache, package proxy |
| DinD | PVC | Docker layers and build cache |
| Logs | PVC at `/data/` | Append-only log files per run |
| Registry | PVC | Container images |

## Cluster Setup

The Helm chart for the cluster topology lives in this repo under
`charts/sparkwing-full`:

```bash
helm install sparkwing ./charts/sparkwing-full -n sparkwing --create-namespace
```

Then add a profile pointing at the controller's URL:

```yaml
# ~/.config/sparkwing/profiles.yaml
profiles:
  prod:
    controller:
      url: https://sparkwing.example.com
      token: <api-token>
```

Select it per run with `--profile prod`, or make it the project default
by setting `defaults.profile: prod` in `.sparkwing/sparkwing.yaml`. The
same pipelines run against any sparkwing controller without changes;
only the profile and registries differ.
