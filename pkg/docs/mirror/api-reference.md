<!-- GENERATED from the route registrations in pkg/controller/server.go and pkg/logs/server.go by internal/apiref. Do not edit by hand; regenerate with `bash bin/gen-api-docs.sh`. -->
# HTTP API reference

Every route the controller and logs service register, with the scope each requires, generated from the routing code. All paths are under the `/api/v1` base (webhook and `/metrics` excepted). Scope enforcement and the token model are in [auth.md](auth.md); `admin` is the superset that satisfies any scope check. `public` routes run with no bearer check (the GitHub webhook is HMAC-verified instead); `authenticated` routes take any valid bearer and check no further scope.

## Controller

| Method | Path | Scope |
|---|---|---|
| `GET` | `/.well-known/jwks.json` | `public` |
| `GET` | `/.well-known/openid-configuration` | `public` |
| `DELETE` | `/api/v1/accounts/{account}` | `admin` |
| `GET` | `/api/v1/admin/usage-metrics` | `admin` |
| `GET` | `/api/v1/agents` | `runs.read` |
| `PUT` | `/api/v1/agents/{name}` | `admin` |
| `POST` | `/api/v1/agents/{name}/heartbeat` | `nodes.claim` |
| `GET` | `/api/v1/approvals/pending` | `runs.read` |
| `GET` | `/api/v1/artifacts/{key}` | `runs.read` |
| `GET` | `/api/v1/auth/bootstrap-needed` | `public` |
| `POST` | `/api/v1/auth/login` | `public` |
| `POST` | `/api/v1/auth/logout` | `public` |
| `POST` | `/api/v1/auth/oauth/github/exchange` | `public` |
| `POST` | `/api/v1/auth/oauth/github/start` | `public` |
| `POST` | `/api/v1/auth/oauth/google/exchange` | `public` |
| `POST` | `/api/v1/auth/oauth/google/start` | `public` |
| `GET` | `/api/v1/auth/session` | `public` |
| `GET` | `/api/v1/auth/whoami` | `authenticated` |
| `GET` | `/api/v1/capabilities` | `public` |
| `GET` | `/api/v1/compute-limits` | `runs.read` |
| `PUT` | `/api/v1/compute-limits` | `admin` |
| `POST` | `/api/v1/concurrency/{key}/acquire` | `runs.state` |
| `POST` | `/api/v1/concurrency/{key}/cancel-waiter` | `admin` |
| `POST` | `/api/v1/concurrency/{key}/force-release` | `admin` |
| `POST` | `/api/v1/concurrency/{key}/heartbeat` | `runs.state` |
| `GET` | `/api/v1/concurrency/{key}/holder` | `runs.state` |
| `GET` | `/api/v1/concurrency/{key}/notify` | `runs.read` |
| `POST` | `/api/v1/concurrency/{key}/release` | `runs.state` |
| `GET` | `/api/v1/concurrency/{key}/resolve` | `runs.state` |
| `GET` | `/api/v1/concurrency/{key}/state` | `runs.read` |
| `GET` | `/api/v1/credits` | `runs.read` |
| `POST` | `/api/v1/credits/freezes` | `credits.grant` |
| `POST` | `/api/v1/credits/grants` | `credits.grant` |
| `GET` | `/api/v1/credits/history` | `runs.read` |
| `POST` | `/api/v1/credits/reversals` | `credits.grant` |
| `GET` | `/api/v1/credits/settings` | `runs.read` |
| `PUT` | `/api/v1/credits/settings` | `admin` |
| `GET` | `/api/v1/credits/teams/{team}` | `admin` |
| `GET` | `/api/v1/credits/units` | `credits.grant` |
| `GET` | `/api/v1/crons` | `runs.read` |
| `DELETE` | `/api/v1/crons/repos` | `runs.control` |
| `PUT` | `/api/v1/crons/repos` | `runs.control` |
| `GET` | `/api/v1/crons/{id}` | `runs.read` |
| `POST` | `/api/v1/crons/{id}/disarm` | `runs.control` |
| `DELETE` | `/api/v1/crons/{id}/override` | `runs.control` |
| `PUT` | `/api/v1/crons/{id}/override` | `runs.control` |
| `POST` | `/api/v1/crons/{id}/pause` | `runs.control` |
| `POST` | `/api/v1/crons/{id}/resume` | `runs.control` |
| `POST` | `/api/v1/crons/{id}/run` | `runs.control` |
| `GET` | `/api/v1/egress` | `admin` |
| `POST` | `/api/v1/gitcache/git/register` | `admin` |
| `GET` | `/api/v1/gitcache/git/{path...}` | `admin` |
| `POST` | `/api/v1/gitcache/git/{path...}` | `admin` |
| `POST` | `/api/v1/gitcache/refresh` | `admin` |
| `POST` | `/api/v1/gitcache/seed` | `admin` |
| `DELETE` | `/api/v1/github-app/installations/{installation_id}` | `admin` |
| `GET` | `/api/v1/health` | `public` |
| `POST` | `/api/v1/invitations/{id}/accept` | `authenticated` |
| `POST` | `/api/v1/maintenance/reconcile-orphans` | `admin` |
| `DELETE` | `/api/v1/me` | `authenticated` |
| `GET` | `/api/v1/me` | `authenticated` |
| `POST` | `/api/v1/me/active-team` | `authenticated` |
| `GET` | `/api/v1/me/team-deletions` | `authenticated` |
| `POST` | `/api/v1/nodes/claim` | `nodes.claim` |
| `POST` | `/api/v1/nodes/claim/prepare` | `nodes.claim` |
| `GET` | `/api/v1/object-store/breaker` | `admin` |
| `POST` | `/api/v1/object-store/reset-breaker` | `admin` |
| `GET` | `/api/v1/pipelines` | `runs.read` |
| `GET` | `/api/v1/pipelines/{name}/latest` | `runs.read` |
| `GET` | `/api/v1/pipelines/{name}/profile` | `nodes.claim` |
| `POST` | `/api/v1/pipelines/{name}/profile/contention` | `runs.state` |
| `POST` | `/api/v1/pipelines/{name}/profile/observations` | `runs.state` |
| `PUT` | `/api/v1/pipelines/{name}/profile/pin` | `runs.state` |
| `POST` | `/api/v1/pipelines/{name}/profile/waits` | `runs.state` |
| `GET` | `/api/v1/pool` | `runs.read` |
| `POST` | `/api/v1/pool/checkout` | `admin` |
| `POST` | `/api/v1/pool/heartbeat` | `admin` |
| `POST` | `/api/v1/pool/return` | `admin` |
| `GET` | `/api/v1/queue/state` | `runs.read` |
| `POST` | `/api/v1/runners/github/exchange` | `public` |
| `GET` | `/api/v1/runs` | `runs.read` |
| `POST` | `/api/v1/runs` | `runs.state` |
| `DELETE` | `/api/v1/runs/{id}` | `admin` |
| `GET` | `/api/v1/runs/{id}` | `runs.read` or `nodes.claim` or `triggers.claim` |
| `GET` | `/api/v1/runs/{id}/approvals` | `runs.read` |
| `GET` | `/api/v1/runs/{id}/approvals/{nodeID}` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/approvals/{nodeID}` | `approvals.write` |
| `POST` | `/api/v1/runs/{id}/approvals/{nodeID}/request` | `admin` |
| `GET` | `/api/v1/runs/{id}/attempts` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/cache-grant` | `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/cancel` | `runs.control` |
| `GET` | `/api/v1/runs/{id}/debug-pauses` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/debug-pauses` | `admin` |
| `GET` | `/api/v1/runs/{id}/events` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/events` | `runs.state` |
| `POST` | `/api/v1/runs/{id}/finish` | `runs.state` |
| `POST` | `/api/v1/runs/{id}/gitcache/git/register` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/gitcache/git/{path...}` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/gitcache/git/{path...}` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/heartbeat` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/log-access` | `logs.read` or `logs.write` or `runs.read` or `nodes.claim` or `triggers.claim` |
| `GET` | `/api/v1/runs/{id}/nodes` | `runs.read` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/nodes` | `runs.state` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/activity` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/annotations` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/artifact-manifest` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/auto-retry/reset` | `runs.state` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/bounce` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/bounce` | `runs.control` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/bounce/consume` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/claim` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/claim/validate` | `logs.write` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/debug-pause` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/deps` | `runs.state` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/dispatch` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/dispatch` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/dispatches` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/execution-finish` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/execution-start` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/finalize-ready` | `runs.state` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/finish` | `runs.state` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/heartbeat` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/logs` | `runs.read` or `logs.read` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/logs` | `runs.state` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/logs/stream` | `runs.read` or `logs.read` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/mark-ready` | `runs.state` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/metrics` | `runs.read` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/metrics` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/output` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/release` | `runs.control` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/revoke-ready` | `runs.state` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/start` | `runs.state` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/status` | `runs.state` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/annotations` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/finish` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/skip` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/start` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/summary` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/summary` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/touch` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/usage` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/oidc-token` | `nodes.claim` or `triggers.claim` |
| `GET` | `/api/v1/runs/{id}/paused` | `runs.read` |
| `GET` | `/api/v1/runs/{id}/pending-triggers` | `triggers.read` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/plan` | `runs.state` |
| `GET` | `/api/v1/runs/{id}/receipt` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/retry` | `runs.control` |
| `POST` | `/api/v1/runs/{id}/source-token` | `nodes.claim` or `triggers.claim` |
| `GET` | `/api/v1/runs/{id}/steps` | `runs.read` |
| `GET` | `/api/v1/secrets` | `runs.read` or `team.admin` |
| `POST` | `/api/v1/secrets` | `admin` or `team.admin` |
| `POST` | `/api/v1/secrets/rotate` | `admin` |
| `DELETE` | `/api/v1/secrets/{name}` | `admin` or `team.admin` |
| `GET` | `/api/v1/secrets/{name}` | `secrets.read` or `team.admin` |
| `GET` | `/api/v1/services` | `authenticated` |
| `GET` | `/api/v1/signups` | `admin` |
| `PUT` | `/api/v1/signups` | `admin` |
| `GET` | `/api/v1/signups/waitlist` | `admin` |
| `POST` | `/api/v1/signups/waitlist/approve` | `admin` |
| `GET` | `/api/v1/storage` | `runs.read` |
| `PUT` | `/api/v1/storage/quotas/{principal}` | `admin` |
| `PUT` | `/api/v1/storage/quotas/{principal}/allowance` | `admin` |
| `PUT` | `/api/v1/storage/settings` | `admin` |
| `PUT` | `/api/v1/storage/teams/{team}/free-slot` | `admin` |
| `DELETE` | `/api/v1/team` | `team.admin` |
| `PATCH` | `/api/v1/team` | `team.admin` |
| `GET` | `/api/v1/team/billing` | `runs.read` |
| `POST` | `/api/v1/team/billing/checkout` | `team.admin` |
| `GET` | `/api/v1/team/cli-tokens` | `runs.read` |
| `POST` | `/api/v1/team/cli-tokens` | `runs.read` |
| `DELETE` | `/api/v1/team/cli-tokens/{prefix}` | `runs.read` |
| `GET` | `/api/v1/team/github-app` | `runs.read` |
| `POST` | `/api/v1/team/github-app/connect` | `team.admin` |
| `POST` | `/api/v1/team/github-app/connect/complete` | `team.admin` |
| `DELETE` | `/api/v1/team/github-app/installations/{installation_id}` | `team.admin` |
| `GET` | `/api/v1/team/github-app/installations/{installation_id}/repositories` | `runs.read` |
| `DELETE` | `/api/v1/team/github-app/triggers` | `team.admin` |
| `GET` | `/api/v1/team/github-app/triggers` | `runs.read` |
| `PUT` | `/api/v1/team/github-app/triggers` | `team.admin` |
| `GET` | `/api/v1/team/github-runners` | `runs.read` |
| `POST` | `/api/v1/team/github-runners` | `team.admin` |
| `DELETE` | `/api/v1/team/github-runners/{repository_id}` | `team.admin` |
| `GET` | `/api/v1/team/invitations` | `team.admin` |
| `POST` | `/api/v1/team/invitations` | `team.admin` |
| `DELETE` | `/api/v1/team/invitations/{id}` | `team.admin` |
| `GET` | `/api/v1/team/members` | `runs.read` |
| `DELETE` | `/api/v1/team/members/{user_id}` | `runs.read` |
| `PATCH` | `/api/v1/team/members/{user_id}` | `team.admin` |
| `GET` | `/api/v1/team/runner-tokens` | `runs.write` |
| `POST` | `/api/v1/team/runner-tokens` | `runs.write` |
| `DELETE` | `/api/v1/team/runner-tokens/{prefix}` | `runs.write` |
| `POST` | `/api/v1/teams` | `authenticated` |
| `DELETE` | `/api/v1/teams/{team}` | `admin` |
| `GET` | `/api/v1/tokens` | `admin` |
| `POST` | `/api/v1/tokens` | `admin` |
| `DELETE` | `/api/v1/tokens/{prefix}` | `admin` |
| `GET` | `/api/v1/tokens/{prefix}` | `admin` |
| `POST` | `/api/v1/tokens/{prefix}/metered` | `admin` |
| `POST` | `/api/v1/tokens/{prefix}/rotate` | `admin` |
| `GET` | `/api/v1/trends` | `runs.read` |
| `GET` | `/api/v1/triggers` | `triggers.read` |
| `POST` | `/api/v1/triggers` | `runs.write` |
| `POST` | `/api/v1/triggers/claim` | `triggers.claim` |
| `GET` | `/api/v1/triggers/spawned-child` | `triggers.read` |
| `GET` | `/api/v1/triggers/{id}` | `triggers.read` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/triggers/{id}/claim` | `triggers.claim` |
| `POST` | `/api/v1/triggers/{id}/done` | `triggers.claim` |
| `POST` | `/api/v1/triggers/{id}/heartbeat` | `triggers.claim` |
| `GET` | `/api/v1/users` | `admin` |
| `POST` | `/api/v1/users` | `admin` |
| `DELETE` | `/api/v1/users/{name}` | `admin` |
| `DELETE` | `/api/v1/webhooks/github/bindings` | `admin` |
| `POST` | `/api/v1/webhooks/github/bindings` | `admin` |
| `POST` | `/internal/downloads/charge` | `public` |
| `POST` | `/internal/egress/totals` | `public` |
| `POST` | `/internal/storage/commit` | `public` |
| `POST` | `/internal/storage/release` | `public` |
| `POST` | `/internal/storage/reserve` | `public` |
| `GET` | `/metrics` | `public` |
| `POST` | `/webhooks/github-app` | `public` |
| `POST` | `/webhooks/github/{pipeline}` | `public` |

## Logs service

| Method | Path | Scope |
|---|---|---|
| `GET` | `/api/v1/health` | `public` |
| `GET` | `/api/v1/logs/search` | `logs.read` |
| `DELETE` | `/api/v1/logs/{runID}` | `logs.write` or `logs.delete` |
| `GET` | `/api/v1/logs/{runID}` | `logs.read` |
| `GET` | `/api/v1/logs/{runID}/{nodeID}` | `logs.read` |
| `POST` | `/api/v1/logs/{runID}/{nodeID}` | `logs.write` |
| `GET` | `/api/v1/logs/{runID}/{nodeID}/seal` | `logs.read` |
| `POST` | `/api/v1/logs/{runID}/{nodeID}/seal` | `logs.write` |
| `GET` | `/api/v1/logs/{runID}/{nodeID}/stream` | `logs.read` |
| `DELETE` | `/api/v1/teams/{team}/logs` | `admin` or `logs.delete` |
| `GET` | `/metrics` | `public` |

