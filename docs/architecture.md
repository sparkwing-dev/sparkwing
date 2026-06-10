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
Kubernetes. The stack is five pods plus an in-cluster registry.

## Components

```
┌──────────────────────────────────────────────────────────────────┐
│                      Kubernetes Cluster                           │
│                                                                  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐           │
│  │  Controller   │  │   Cache      │  │   Web        │           │
│  │ (API + queue  │  │  (git HTTP + │  │  (dashboard) │           │
│  │  + webhooks   │  │   blob store │  │              │           │
│  │  + pool mgmt  │  │   + pkg proxy│  │              │           │
│  │  + dispatcher)│  │            │  │              │           │
│  └──────┬───────┘  └──────────────┘  └──────────────┘           │
│         │                                                        │
│  ┌──────┴───────┐  ┌──────────────┐  ┌──────────────┐           │
│  │  Runner (k8s  │  │   DinD       │  │   Logs       │           │
│  │  Job)         │  │ (Docker in   │  │  (log store) │           │
│  │              │  │  Docker)     │  │              │           │
│  └──────────────┘  └──────────────┘  └──────────────┘           │
│                                                                  │
│  ┌──────────────┐                                                │
│  │  Registry    │                                                │
│  │ (container   │                                                │
│  │  images)     │                                                │
│  └──────────────┘                                                │
└──────────────────────────────────────────────────────────────────┘
         ▲                    ▲
         │                    │
    ┌────┴────┐          ┌────┴────┐
    │  sparkwing   │          │  git    │
    │  (CLI)  │          │ (push)  │
    └─────────┘          └─────────┘
```

Five pods: sparkwing-controller, sparkwing-cache, sparkwing-web,
sparkwing-logs, and dind.

### Controller

The central coordinator. Receives job triggers, queues work, and
dispatches runners.

- **API server** (port 8080): HTTP endpoints for triggers, run status,
  agent polling, secrets, and authorization
- **Job queue**: in-memory queue with SQLite persistence
  (`/data/state.db`) for run state, metadata, secrets, and tokens
- **Webhooks**: receives GitHub webhook payloads, verifies HMAC
  signatures, and triggers matching pipelines
- **Pool management**: maintains a pool of PVCs pre-loaded with Docker
  build cache; handles checkout and return for runner jobs
- **Dispatcher**: background goroutine that claims pending jobs and
  creates Kubernetes Jobs to run them
- **Heartbeat monitor**: reclaims a node whose runner stops renewing its
  lease (default 5-minute timeout, 2-minute startup grace)
- **Queue timeout**: fails pending nodes that exceed their `queue_timeout`
  (default 15 minutes)
- **Metrics collector**: stores the per-node CPU/memory samples runners
  push as they execute (no cluster metrics-server involved)

### Runner

Executes pipeline binaries. The dispatcher creates a Kubernetes Job
running the unified `sparkwing-runner` binary (its `runner`, `worker`,
and `agent` subcommands cover the warm pool, single-claim, and
off-cluster cases). The runner downloads code from the cache, compiles
and runs the pipeline, reports results, exits.

Off-cluster runners (laptops, bare-metal) connect to the controller and
claim nodes through its claim API; the route set and scopes are in
[api-reference.md](api-reference.md).

### Cache

Git HTTP server, blob store, and package proxy. Mirrors bare repositories
from GitHub with a background fetch loop (every 30 seconds). Serves git
clones over HTTP so runners do not need SSH keys.

Also stores:

- **Code uploads**: tarballs from `sparkwing pipeline trigger` invocations
- **Artifacts**: job output files
- **Binary cache**: compiled pipeline binaries
- **Dependency cache**: saved / restored by pipelines (gems, node_modules, etc.)
- **Package proxy**: caching reverse proxy for npm, PyPI, Go modules,
  RubyGems, and Alpine packages

See [Cache](gitcache.md) for endpoints and configuration.

### Dashboard

Next.js web app showing pipeline runs, logs, node status, and
documentation.

### DinD (Docker-in-Docker)

Shared Docker daemon for building container images. Runner jobs connect
to the DinD service, optionally with a warm PVC mounted for Docker cache.

### Registry

Container image registry (NodePort 30500). Images push here from builds.
Pipelines can also push to external registries (ECR, GCR, Docker Hub,
etc.) - that is up to the pipeline author.

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
Controller ────► k8s API           create / watch Jobs
Runner ────────► Controller        claim node; heartbeat; report finish; fetch details
Runner ────────► Cache             clone repo, download code + packages
Runner ────────► Logs              stream step output
Runner ────────► DinD              Docker builds (tcp://localhost:2375)
Runner ────────► Registry          docker push (localhost:30500)
Dashboard ─────► Controller        read runs / agents / pipelines
Dashboard ─────► Logs              live log stream (SSE)

Cache ─────────► GitHub            git fetch (background, every 30s)

sparkwing CLI ──────► Cache             POST /upload (code tarball, incremental sync)
                                   POST /sync/negotiate (ancestor negotiation)
```

### Network policies

Default-deny ingress is applied to the sparkwing namespace. Each
component has explicit allow rules:

| Component | Accepts traffic from |
|-----------|---------------------|
| Controller | External (webhooks), Dashboard, Runners |
| Cache | Controller, Runners |
| DinD | Runners, Controller (cache warmers) |
| Dashboard | External (port 3100) |
| Logs | Runners, Dashboard |
| Registry | Runners, Nodes (image pulls) |

### Internal service addresses

All components discover each other via k8s DNS. No hardcoded IPs.

| Service | Internal address | Port |
|---------|-----------------|------|
| Controller | `sparkwing-controller.sparkwing.svc.cluster.local` | 80 -> 8080 |
| Cache | `sparkwing-cache.sparkwing.svc.cluster.local` | 80 -> 8090 |
| Logs | `sparkwing-logs.sparkwing.svc.cluster.local` | 80 -> 8091 |
| DinD | `dind.sparkwing.svc.cluster.local` | 2375 |
| Dashboard | `sparkwing-web.sparkwing.svc.cluster.local` | 3100 |
| Registry | `registry.registry.svc.cluster.local` | 5000 (NodePort 30500) |

### Environment variables set on runners

The dispatcher injects these into every runner pod:

| Variable | Purpose |
|----------|---------|
| `SPARKWING_CONTROLLER_URL` | Controller base URL |
| `SPARKWING_LOGS_URL` | Logs service URL |
| `SPARKWING_RUN_ID` | The run this node belongs to |
| `SPARKWING_NODE_ID` | The node being executed |
| `SPARKWING_HOME` | State / cache / logs root |
| `SPARKWING_AGENT_TOKEN` | Bearer token for controller + logs calls |

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
  2. sparkwing uploads code tarball to cache (incremental when possible)
  3. sparkwing POST /trigger to controller (with upload_ref)
  4. controller enqueues run
  5. dispatcher creates a k8s Job
  6. runner downloads code from cache
  7. runner compiles and runs the pipeline binary
  8. runner streams logs to logs service
  9. runner sends heartbeats every 5s to controller
  10. runner reports completion to controller
  11. sparkwing run polls the controller for run state and displays result
```

### Git Push Trigger

```
git push origin main
  1. GitHub sends webhook to sparkwing-controller (external)
  2. Controller verifies HMAC signature
  3. Controller matches push against sparkwing.yaml triggers
  4. Controller enqueues matching runs
  5. Same dispatch flow as steps 5-11 above
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

The Helm chart for the cluster topology ships separately from this
repo (which holds the CLI + SDK only). Once the chart is on disk:

```bash
helm install sparkwing <path-to-chart> -n sparkwing --create-namespace
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
