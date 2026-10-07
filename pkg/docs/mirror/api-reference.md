<!-- GENERATED from the route registrations in pkg/controller/server.go and pkg/logs/server.go by internal/apiref. Do not edit by hand; regenerate with `bash bin/gen-api-docs.sh`. -->
# HTTP API reference

Every route the controller and logs service register, with the scope each requires, generated from the routing code. All paths are under the `/api/v1` base (webhook and `/metrics` excepted). Scope enforcement and the token model are in [auth.md](auth.md); `admin` is the superset that satisfies any scope check. `public` routes run with no bearer check (the GitHub webhook is HMAC-verified instead); `authenticated` routes take any valid bearer and check no further scope. `claim` routes answer the claim token of one node claim, and only for that claim's own run and node. `operator` routes answer only the signed-in session of an account the controller lists with --operator-accounts; no token reaches them.

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
| `POST` | `/api/v1/concurrency/{key}/acquire` | `claim` or `runs.state` |
| `POST` | `/api/v1/concurrency/{key}/cancel-waiter` | `claim` or `runs.state` |
| `POST` | `/api/v1/concurrency/{key}/force-release` | `admin` |
| `POST` | `/api/v1/concurrency/{key}/heartbeat` | `claim` or `runs.state` |
| `GET` | `/api/v1/concurrency/{key}/holder` | `claim` or `runs.state` |
| `GET` | `/api/v1/concurrency/{key}/notify` | `claim` or `runs.read` |
| `POST` | `/api/v1/concurrency/{key}/release` | `claim` or `runs.state` |
| `GET` | `/api/v1/concurrency/{key}/resolve` | `claim` or `runs.state` |
| `GET` | `/api/v1/concurrency/{key}/state` | `claim` or `runs.read` |
| `GET` | `/api/v1/credits` | `runs.read` |
| `POST` | `/api/v1/credits/card-payments` | `credits.grant` |
| `POST` | `/api/v1/credits/card-refunds` | `credits.grant` |
| `POST` | `/api/v1/credits/cards` | `credits.grant` |
| `POST` | `/api/v1/credits/checkouts/closed` | `credits.grant` |
| `POST` | `/api/v1/credits/freezes` | `credits.grant` |
| `POST` | `/api/v1/credits/grants` | `credits.grant` |
| `GET` | `/api/v1/credits/history` | `runs.read` |
| `GET` | `/api/v1/credits/payments/{reference}` | `credits.grant` |
| `POST` | `/api/v1/credits/reversals` | `credits.grant` |
| `GET` | `/api/v1/credits/settings` | `runs.read` |
| `PUT` | `/api/v1/credits/settings` | `admin` |
| `GET` | `/api/v1/credits/teams/{team}` | `admin` |
| `GET` | `/api/v1/credits/units` | `credits.grant` |
| `POST` | `/api/v1/credits/warnings` | `credits.grant` |
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
| `GET` | `/api/v1/data/capabilities` | `public` |
| `POST` | `/api/v1/data/commit` | `public` |
| `POST` | `/api/v1/data/download` | `public` |
| `POST` | `/api/v1/data/upload` | `public` |
| `GET` | `/api/v1/egress` | `admin` |
| `POST` | `/api/v1/gitcache/git/register` | `admin` |
| `GET` | `/api/v1/gitcache/git/{path...}` | `admin` |
| `POST` | `/api/v1/gitcache/git/{path...}` | `admin` |
| `POST` | `/api/v1/gitcache/refresh` | `admin` |
| `POST` | `/api/v1/gitcache/seed` | `admin` |
| `DELETE` | `/api/v1/github-app/installations/{installation_id}` | `admin` |
| `GET` | `/api/v1/health` | `public` |
| `POST` | `/api/v1/invitations/{id}/accept` | `authenticated` |
| `POST` | `/api/v1/launcher/claim` | `ScopeClaimsLaunch` |
| `POST` | `/api/v1/launcher/sync` | `ScopeClaimsLaunch` |
| `POST` | `/api/v1/maintenance/reconcile-orphans` | `admin` |
| `DELETE` | `/api/v1/me` | `authenticated` |
| `GET` | `/api/v1/me` | `authenticated` |
| `POST` | `/api/v1/me/active-team` | `authenticated` |
| `GET` | `/api/v1/me/identities` | `authenticated` |
| `DELETE` | `/api/v1/me/identities/{provider}` | `authenticated` |
| `POST` | `/api/v1/me/identities/{provider}/link` | `authenticated` |
| `POST` | `/api/v1/me/identities/{provider}/link/complete` | `authenticated` |
| `GET` | `/api/v1/me/team-deletions` | `authenticated` |
| `POST` | `/api/v1/nodes/claim` | `nodes.claim` |
| `POST` | `/api/v1/nodes/claim/prepare` | `nodes.claim` |
| `GET` | `/api/v1/object-store/breaker` | `admin` |
| `POST` | `/api/v1/object-store/reset-breaker` | `admin` |
| `GET` | `/api/v1/operator/session` | `operator` |
| `GET` | `/api/v1/operator/teams` | `operator` |
| `GET` | `/api/v1/operator/teams/{team}` | `operator` |
| `POST` | `/api/v1/operator/teams/{team}/freeze` | `operator` |
| `POST` | `/api/v1/operator/teams/{team}/grants` | `operator` |
| `POST` | `/api/v1/operator/teams/{team}/trust` | `operator` |
| `POST` | `/api/v1/operator/teams/{team}/unfreeze` | `operator` |
| `GET` | `/api/v1/operator/waitlist` | `operator` |
| `POST` | `/api/v1/operator/waitlist/approve` | `operator` |
| `GET` | `/api/v1/outputs/objects/{key...}` | `public` |
| `PUT` | `/api/v1/outputs/uploads/{id}` | `public` |
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
| `GET` | `/api/v1/runs/{id}` | `claim` or `runs.read` or `nodes.claim` or `triggers.claim` |
| `GET` | `/api/v1/runs/{id}/approvals` | `runs.read` |
| `GET` | `/api/v1/runs/{id}/approvals/{nodeID}` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/approvals/{nodeID}` | `approvals.write` |
| `POST` | `/api/v1/runs/{id}/approvals/{nodeID}/request` | `admin` |
| `GET` | `/api/v1/runs/{id}/attempts` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/cache-grant` | `claim` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/cancel` | `runs.control` |
| `POST` | `/api/v1/runs/{id}/children` | `claim` |
| `GET` | `/api/v1/runs/{id}/children/{childID}` | `claim` |
| `GET` | `/api/v1/runs/{id}/children/{childID}/nodes/{nodeID}/output` | `claim` |
| `GET` | `/api/v1/runs/{id}/debug-pauses` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/debug-pauses` | `admin` |
| `GET` | `/api/v1/runs/{id}/events` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/events` | `runs.state` |
| `POST` | `/api/v1/runs/{id}/finish` | `runs.state` |
| `POST` | `/api/v1/runs/{id}/git-credential` | `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/gitcache/git/register` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/gitcache/git/{path...}` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/gitcache/git/{path...}` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/heartbeat` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/log-access` | `logs.read` or `logs.write` or `runs.read` or `nodes.claim` or `triggers.claim` |
| `GET` | `/api/v1/runs/{id}/nodes` | `runs.read` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/nodes` | `runs.state` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/activity` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/annotations` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/artifact-manifest` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/attempt` | `claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/auto-retry/reset` | `runs.state` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/bounce` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/bounce` | `runs.control` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/bounce/consume` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/claim` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/claim/input` | `claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/claim/validate` | `claim` or `logs.write` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/debug-pause` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/deps` | `runs.state` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/dispatch` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/dispatch` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/dispatches` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/execution-finish` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/execution-start` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/finalize-ready` | `runs.state` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/finish` | `runs.state` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/heartbeat` | `claim` or `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/logs` | `runs.read` or `logs.read` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/logs` | `claim` or `runs.state` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/logs/stream` | `runs.read` or `logs.read` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/mark-ready` | `runs.state` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/metrics` | `runs.read` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/metrics` | `claim` or `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/output` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/output-commit` | `claim` or `runs.state` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/output-upload` | `claim` or `runs.state` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/release` | `runs.control` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/revoke-ready` | `runs.state` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/start` | `claim` or `runs.state` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/status` | `runs.state` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/annotations` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/finish` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/skip` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/start` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/steps/summary` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/summary` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/touch` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/usage` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/oidc-token` | `claim` or `nodes.claim` or `triggers.claim` |
| `GET` | `/api/v1/runs/{id}/paused` | `runs.read` |
| `GET` | `/api/v1/runs/{id}/pending-triggers` | `triggers.read` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/plan` | `claim` or `runs.state` |
| `GET` | `/api/v1/runs/{id}/receipt` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/retry` | `runs.control` |
| `POST` | `/api/v1/runs/{id}/source-credential` | `claim` |
| `POST` | `/api/v1/runs/{id}/source-token` | `nodes.claim` or `triggers.claim` |
| `GET` | `/api/v1/runs/{id}/steps` | `runs.read` |
| `GET` | `/api/v1/secrets` | `runs.read` or `team.admin` |
| `POST` | `/api/v1/secrets` | `admin` or `team.admin` |
| `POST` | `/api/v1/secrets/rotate` | `admin` |
| `DELETE` | `/api/v1/secrets/{name}` | `admin` or `team.admin` |
| `GET` | `/api/v1/secrets/{name}` | `claim` or `secrets.read` or `team.admin` |
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
| `PUT` | `/api/v1/team/billing/budget` | `team.admin` |
| `POST` | `/api/v1/team/billing/card` | `team.admin` |
| `POST` | `/api/v1/team/billing/checkout` | `team.admin` |
| `POST` | `/api/v1/team/billing/pay` | `team.admin` |
| `GET` | `/api/v1/team/build-trust` | `runs.read` |
| `PUT` | `/api/v1/team/build-trust` | `team.admin` |
| `GET` | `/api/v1/team/cli-tokens` | `runs.read` |
| `POST` | `/api/v1/team/cli-tokens` | `runs.read` |
| `DELETE` | `/api/v1/team/cli-tokens/{prefix}` | `runs.read` |
| `GET` | `/api/v1/team/git-credentials` | `runs.read` |
| `POST` | `/api/v1/team/git-credentials` | `team.admin` |
| `GET` | `/api/v1/team/git-credentials/releases` | `team.admin` |
| `DELETE` | `/api/v1/team/git-credentials/{host}` | `team.admin` |
| `POST` | `/api/v1/team/git-credentials/{host}/confirm` | `team.admin` |
| `GET` | `/api/v1/team/github-app` | `runs.read` |
| `POST` | `/api/v1/team/github-app/connect` | `team.admin` |
| `POST` | `/api/v1/team/github-app/connect/available` | `team.admin` |
| `POST` | `/api/v1/team/github-app/connect/complete` | `team.admin` |
| `POST` | `/api/v1/team/github-app/connect/select` | `team.admin` |
| `GET` | `/api/v1/team/github-app/extra-repos` | `runs.read` |
| `PUT` | `/api/v1/team/github-app/extra-repos` | `team.admin` |
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
| `PUT` | `/api/v1/team/runner-tokens/{prefix}/git-credentials` | `team.admin` |
| `POST` | `/api/v1/teams` | `authenticated` |
| `DELETE` | `/api/v1/teams/{team}` | `admin` |
| `PUT` | `/api/v1/teams/{team}/repos/{owner}/{name}/dispatch` | `admin` |
| `GET` | `/api/v1/teams/{team}/trust` | `admin` |
| `POST` | `/api/v1/teams/{team}/trust` | `admin` |
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
| `POST` | `/internal/downloads/charge` | `public` |
| `POST` | `/internal/egress/totals` | `public` |
| `POST` | `/internal/storage/commit` | `public` |
| `POST` | `/internal/storage/release` | `public` |
| `POST` | `/internal/storage/reserve` | `public` |
| `GET` | `/metrics` | `admin` |
| `POST` | `/webhooks/github-app` | `public` |

## Logs service

| Method | Path | Scope |
|---|---|---|
| `GET` | `/api/v1/health` | `public` |
| `GET` | `/api/v1/logs/search` | `logs.read` |
| `DELETE` | `/api/v1/logs/{runID}` | `logs.delete` |
| `GET` | `/api/v1/logs/{runID}` | `logs.read` |
| `GET` | `/api/v1/logs/{runID}/{nodeID}` | `logs.read` |
| `POST` | `/api/v1/logs/{runID}/{nodeID}` | `logs.write` or `logs.claim` |
| `GET` | `/api/v1/logs/{runID}/{nodeID}/seal` | `logs.read` |
| `POST` | `/api/v1/logs/{runID}/{nodeID}/seal` | `logs.write` or `logs.claim` |
| `GET` | `/api/v1/logs/{runID}/{nodeID}/stream` | `logs.read` |
| `DELETE` | `/api/v1/teams/{team}/logs` | `admin` or `logs.delete` |
| `GET` | `/metrics` | `public` |

