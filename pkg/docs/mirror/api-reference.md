<!-- GENERATED from the route registrations in pkg/controller/server.go and pkg/logs/server.go by internal/apiref. Do not edit by hand; regenerate with `bash bin/gen-api-docs.sh`. -->
# HTTP API reference

Every route the controller and logs service register, with the scope each requires, generated from the routing code. All paths are under the `/api/v1` base (webhook and `/metrics` excepted). Scope enforcement and the token model are in [auth.md](auth.md); `admin` is the superset that satisfies any scope check. `public` routes run with no bearer check (the GitHub webhook is HMAC-verified instead; `POST /api/v1/users` self-selects unauthenticated bootstrap vs admin-scoped create).

## Controller

| Method | Path | Scope |
|---|---|---|
| `GET` | `/api/v1/agents` | `runs.read` |
| `GET` | `/api/v1/approvals/pending` | `runs.read` |
| `GET` | `/api/v1/artifacts/{key}` | `runs.read` |
| `GET` | `/api/v1/auth/bootstrap-needed` | `public` |
| `POST` | `/api/v1/auth/login` | `public` |
| `POST` | `/api/v1/auth/logout` | `public` |
| `GET` | `/api/v1/auth/session` | `public` |
| `GET` | `/api/v1/auth/whoami` | `public` |
| `POST` | `/api/v1/concurrency/{key}/acquire` | `admin` |
| `POST` | `/api/v1/concurrency/{key}/cancel-waiter` | `admin` |
| `POST` | `/api/v1/concurrency/{key}/force-release` | `admin` |
| `POST` | `/api/v1/concurrency/{key}/heartbeat` | `admin` |
| `GET` | `/api/v1/concurrency/{key}/holder` | `admin` |
| `GET` | `/api/v1/concurrency/{key}/notify` | `runs.read` |
| `POST` | `/api/v1/concurrency/{key}/release` | `admin` |
| `GET` | `/api/v1/concurrency/{key}/resolve` | `admin` |
| `GET` | `/api/v1/concurrency/{key}/state` | `runs.read` |
| `GET` | `/api/v1/health` | `public` |
| `POST` | `/api/v1/nodes/claim` | `nodes.claim` |
| `GET` | `/api/v1/pipelines/{name}/latest` | `runs.read` |
| `GET` | `/api/v1/pipelines/{name}/profile` | `nodes.claim` |
| `PUT` | `/api/v1/pipelines/{name}/profile/pin` | `nodes.claim` |
| `GET` | `/api/v1/pool` | `runs.read` |
| `POST` | `/api/v1/pool/checkout` | `admin` |
| `POST` | `/api/v1/pool/heartbeat` | `admin` |
| `POST` | `/api/v1/pool/return` | `admin` |
| `GET` | `/api/v1/runs` | `runs.read` |
| `POST` | `/api/v1/runs` | `admin` |
| `DELETE` | `/api/v1/runs/{id}` | `admin` |
| `GET` | `/api/v1/runs/{id}` | `runs.read` |
| `GET` | `/api/v1/runs/{id}/approvals` | `runs.read` |
| `GET` | `/api/v1/runs/{id}/approvals/{nodeID}` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/approvals/{nodeID}` | `approvals.write` |
| `POST` | `/api/v1/runs/{id}/approvals/{nodeID}/request` | `admin` |
| `GET` | `/api/v1/runs/{id}/attempts` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/cancel` | `runs.write` |
| `GET` | `/api/v1/runs/{id}/debug-pauses` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/debug-pauses` | `admin` |
| `GET` | `/api/v1/runs/{id}/events` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/events` | `admin` |
| `POST` | `/api/v1/runs/{id}/finish` | `admin` |
| `POST` | `/api/v1/runs/{id}/heartbeat` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/nodes` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/nodes` | `admin` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/activity` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/annotations` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/artifact-manifest` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/debug-pause` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/deps` | `admin` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/dispatch` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/dispatch` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/dispatches` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/finish` | `admin` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/heartbeat` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/mark-ready` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/metrics` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/metrics` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/output` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/release` | `runs.write` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/revoke-ready` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/start` | `admin` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/status` | `admin` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/annotations` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/finish` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/skip` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/start` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/summary` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/summary` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/touch` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/paused` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/plan` | `admin` |
| `GET` | `/api/v1/runs/{id}/receipt` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/retry` | `runs.write` |
| `GET` | `/api/v1/runs/{id}/steps` | `runs.read` |
| `GET` | `/api/v1/secrets` | `admin` |
| `POST` | `/api/v1/secrets` | `admin` |
| `DELETE` | `/api/v1/secrets/{name}` | `admin` |
| `GET` | `/api/v1/secrets/{name}` | `admin` |
| `GET` | `/api/v1/services` | `public` |
| `GET` | `/api/v1/tokens` | `admin` |
| `POST` | `/api/v1/tokens` | `admin` |
| `DELETE` | `/api/v1/tokens/{prefix}` | `admin` |
| `GET` | `/api/v1/tokens/{prefix}` | `admin` |
| `POST` | `/api/v1/tokens/{prefix}/rotate` | `admin` |
| `GET` | `/api/v1/trends` | `runs.read` |
| `GET` | `/api/v1/triggers` | `triggers.read` |
| `POST` | `/api/v1/triggers` | `runs.write` |
| `POST` | `/api/v1/triggers/claim` | `admin` |
| `GET` | `/api/v1/triggers/spawned-child` | `triggers.read` |
| `GET` | `/api/v1/triggers/{id}` | `triggers.read` |
| `POST` | `/api/v1/triggers/{id}/done` | `admin` |
| `POST` | `/api/v1/triggers/{id}/heartbeat` | `admin` |
| `GET` | `/api/v1/users` | `admin` |
| `POST` | `/api/v1/users` | `public` |
| `DELETE` | `/api/v1/users/{name}` | `admin` |
| `GET` | `/metrics` | `public` |
| `POST` | `/webhooks/github/{pipeline}` | `public` |

## Logs service

| Method | Path | Scope |
|---|---|---|
| `GET` | `/api/v1/health` | `public` |
| `GET` | `/api/v1/logs/search` | `logs.read` |
| `DELETE` | `/api/v1/logs/{runID}` | `logs.write` |
| `GET` | `/api/v1/logs/{runID}` | `logs.read` |
| `GET` | `/api/v1/logs/{runID}/{nodeID}` | `logs.read` |
| `POST` | `/api/v1/logs/{runID}/{nodeID}` | `logs.write` |
| `GET` | `/api/v1/logs/{runID}/{nodeID}/stream` | `logs.read` |
| `GET` | `/metrics` | `public` |

