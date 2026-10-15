<!-- GENERATED from the route registrations in pkg/controller/server.go by internal/apiref. Do not edit by hand; regenerate with `bash bin/gen-api-docs.sh`. -->
# HTTP API reference

Every route the controller registers, with the scope each requires, generated from the routing code. All paths are under the `/api/v1` base (webhook and `/metrics` excepted). Scope enforcement and the token model are in [auth.md](auth.md); `admin` satisfies any scope check except `credits.grant`, which only the checkout service's own token passes. `public` routes run with no bearer check (the GitHub and Stripe webhooks require their HMAC signatures); `authenticated` routes require live identity under their credential policy and no named grant. `claim` routes answer the claim token of one node claim, and only for that claim's own run and node. `operator` routes require an operator-console session through the shared route policy and CSRF proof for writes; no bearer reaches them.

## Controller

| Method | Path | Scope |
|---|---|---|
| `GET` | `/.well-known/jwks.json` | `public` |
| `GET` | `/.well-known/openid-configuration` | `public` |
| `GET` | `/api/v1/agents` | `runs.read` |
| `GET` | `/api/v1/approvals/pending` | `runs.read` |
| `POST` | `/api/v1/auth/logout` | `public` |
| `GET` | `/api/v1/auth/session` | `public` |
| `GET` | `/api/v1/auth/whoami` | `authenticated` |
| `POST` | `/api/v1/cache/store-ceiling/measure` | `admin` |
| `POST` | `/api/v1/cache/store-ceiling/thaw` | `admin` |
| `GET` | `/api/v1/capabilities` | `public` |
| `GET` | `/api/v1/capacity/profiles` | `runs.read` |
| `GET` | `/api/v1/capacity/profiles/explain` | `runs.read` |
| `POST` | `/api/v1/concurrency/{key}/acquire` | `claim` or `runs.state` |
| `POST` | `/api/v1/concurrency/{key}/cancel-waiter` | `claim` or `runs.state` |
| `POST` | `/api/v1/concurrency/{key}/force-release` | `admin` |
| `POST` | `/api/v1/concurrency/{key}/heartbeat` | `claim` or `runs.state` |
| `GET` | `/api/v1/concurrency/{key}/holder` | `claim` or `runs.state` |
| `POST` | `/api/v1/concurrency/{key}/release` | `claim` or `runs.state` |
| `GET` | `/api/v1/concurrency/{key}/resolve` | `claim` or `runs.state` |
| `GET` | `/api/v1/concurrency/{key}/state` | `claim` or `runs.read` |
| `GET` | `/api/v1/credits` | `runs.read` |
| `POST` | `/api/v1/credits/card-payments` | `credits.grant` |
| `POST` | `/api/v1/credits/card-refunds` | `credits.grant` |
| `POST` | `/api/v1/credits/cards` | `credits.grant` |
| `POST` | `/api/v1/credits/checkouts/closed` | `credits.grant` |
| `POST` | `/api/v1/credits/freezes` | `credits.grant` |
| `GET` | `/api/v1/credits/history` | `runs.read` |
| `GET` | `/api/v1/credits/payments/{reference}` | `credits.grant` |
| `POST` | `/api/v1/credits/reversals` | `credits.grant` |
| `GET` | `/api/v1/credits/settings` | `runs.read` |
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
| `GET` | `/api/v1/data/capabilities` | `authenticated` |
| `POST` | `/api/v1/data/commit` | `public` |
| `POST` | `/api/v1/data/download` | `public` |
| `POST` | `/api/v1/data/upload` | `public` |
| `POST` | `/api/v1/gitcache/git/register` | `admin` |
| `GET` | `/api/v1/gitcache/git/{path...}` | `admin` |
| `POST` | `/api/v1/gitcache/git/{path...}` | `admin` |
| `GET` | `/api/v1/health` | `public` |
| `POST` | `/api/v1/invitations/{id}/accept` | `authenticated` |
| `POST` | `/api/v1/launcher/claim` | `ScopeClaimsLaunch` |
| `POST` | `/api/v1/launcher/sync` | `ScopeClaimsLaunch` |
| `PUT` | `/api/v1/log-segments/{key...}` | `public` |
| `DELETE` | `/api/v1/me` | `authenticated` |
| `GET` | `/api/v1/me` | `authenticated` |
| `POST` | `/api/v1/me/active-team` | `authenticated` |
| `GET` | `/api/v1/me/identities` | `authenticated` |
| `DELETE` | `/api/v1/me/identities/{provider}` | `authenticated` |
| `GET` | `/api/v1/me/team-deletions` | `authenticated` |
| `POST` | `/api/v1/nodes/claim` | `nodes.claim` |
| `DELETE` | `/api/v1/operator/accounts/{account}` | `operator` |
| `PUT` | `/api/v1/operator/credits/settings` | `operator` |
| `DELETE` | `/api/v1/operator/github-app/installations/{installation}` | `operator` |
| `GET` | `/api/v1/operator/limits` | `operator.limits` or `operator` |
| `PUT` | `/api/v1/operator/limits` | `operator.limits` or `operator` |
| `POST` | `/api/v1/operator/maintenance/reconcile-orphans` | `operator` |
| `GET` | `/api/v1/operator/object-store` | `operator.object_store` or `operator` |
| `GET` | `/api/v1/operator/object-store/breaker` | `operator.object_store` or `operator` |
| `PUT` | `/api/v1/operator/object-store/quotas/{team}` | `operator.object_store` or `operator` |
| `PUT` | `/api/v1/operator/object-store/quotas/{team}/allowance` | `operator.object_store` or `operator` |
| `POST` | `/api/v1/operator/object-store/reset-breaker` | `operator.object_store` or `operator` |
| `PUT` | `/api/v1/operator/object-store/settings` | `operator.object_store` or `operator` |
| `GET` | `/api/v1/operator/signups` | `operator.limits` or `operator` |
| `PUT` | `/api/v1/operator/signups` | `operator.limits` or `operator` |
| `GET` | `/api/v1/operator/teams` | `operator` |
| `DELETE` | `/api/v1/operator/teams/{team}` | `operator` |
| `GET` | `/api/v1/operator/teams/{team}` | `operator` |
| `POST` | `/api/v1/operator/teams/{team}/freeze` | `operator` |
| `POST` | `/api/v1/operator/teams/{team}/grants` | `operator` |
| `POST` | `/api/v1/operator/teams/{team}/trust` | `operator` |
| `POST` | `/api/v1/operator/teams/{team}/unfreeze` | `operator` |
| `DELETE` | `/api/v1/operator/tokens/{token}` | `operator.tokens` or `operator` |
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
| `GET` | `/api/v1/queue/state` | `runs.read` |
| `POST` | `/api/v1/runners/github/exchange` | `public` |
| `GET` | `/api/v1/runs` | `runs.read` |
| `POST` | `/api/v1/runs` | `runs.write` or `children.start` |
| `GET` | `/api/v1/runs/grep` | `logs.read` |
| `DELETE` | `/api/v1/runs/{id}` | `admin` |
| `GET` | `/api/v1/runs/{id}` | `claim` or `runs.read` or `nodes.claim` or `triggers.claim` |
| `GET` | `/api/v1/runs/{id}/approvals` | `runs.read` |
| `GET` | `/api/v1/runs/{id}/approvals/{nodeID}` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/approvals/{nodeID}` | `approvals.write` |
| `POST` | `/api/v1/runs/{id}/approvals/{nodeID}/request` | `admin` |
| `GET` | `/api/v1/runs/{id}/attempts` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/cache-grant` | `claim` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/children` | `claim` |
| `GET` | `/api/v1/runs/{id}/children/{childID}` | `claim` |
| `GET` | `/api/v1/runs/{id}/children/{childID}/nodes/{nodeID}/output` | `claim` |
| `GET` | `/api/v1/runs/{id}/debug-pauses` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/debug-pauses` | `admin` |
| `GET` | `/api/v1/runs/{id}/events` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/events` | `runs.state` |
| `GET` | `/api/v1/runs/{id}/events/stream` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/gitcache/git/register` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/gitcache/git/{path...}` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/gitcache/git/{path...}` | `nodes.claim` |
| `GET` | `/api/v1/runs/{id}/logs` | `logs.read` |
| `GET` | `/api/v1/runs/{id}/logs/search` | `logs.read` |
| `GET` | `/api/v1/runs/{id}/logs/{node}` | `logs.read` |
| `GET` | `/api/v1/runs/{id}/logs/{node}/completeness` | `logs.read` |
| `GET` | `/api/v1/runs/{id}/logs/{node}/stream` | `logs.read` |
| `GET` | `/api/v1/runs/{id}/nodes` | `runs.read` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/runs/{id}/nodes` | `runs.state` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/activity` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/artifact-manifest` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/attempt` | `claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/auto-retry/reset` | `runs.state` |
| `GET` | `/api/v1/runs/{id}/nodes/{nodeID}/bounce` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/bounce` | `runs.control` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/bounce/consume` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/claim` | `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/claim/input` | `claim` |
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
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/log-manifest` | `claim` or `runs.state` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/log-segments` | `claim` or `runs.state` |
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
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/touch` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/nodes/{nodeID}/usage` | `claim` or `nodes.claim` |
| `POST` | `/api/v1/runs/{id}/oidc-token` | `claim` or `nodes.claim` or `triggers.claim` |
| `GET` | `/api/v1/runs/{id}/paused` | `runs.read` |
| `GET` | `/api/v1/runs/{id}/pending-triggers` | `triggers.read` or `nodes.claim` or `triggers.claim` |
| `GET` | `/api/v1/runs/{id}/receipt` | `runs.read` |
| `POST` | `/api/v1/runs/{id}/source-credential` | `claim` |
| `GET` | `/api/v1/runs/{id}/steps` | `runs.read` |
| `POST` | `/api/v1/runs/{run}/cancel` | `runs.control` |
| `POST` | `/api/v1/runs/{run}/finish` | `runs.state` or `data.write` |
| `POST` | `/api/v1/runs/{run}/heartbeat` | `trigger.host` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/annotations` | `node.report` or `node.report` or `data.write` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/expansion` | `expansion.submit` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/heartbeat` | `node.report` or `node.report` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/steps/annotations` | `node.report` or `node.report` or `data.write` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/steps/finish` | `node.report` or `node.report` or `data.write` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/steps/skip` | `node.report` or `node.report` or `data.write` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/steps/start` | `node.report` or `node.report` or `data.write` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/steps/summary` | `node.report` or `node.report` or `data.write` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/summary` | `node.report` or `node.report` or `data.write` |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/transition` | `node.result` or `node.result` or `data.write` |
| `POST` | `/api/v1/runs/{run}/plan` | `node.result` or `data.write` |
| `POST` | `/api/v1/runs/{run}/retry` | `runs.control` |
| `GET` | `/api/v1/secrets` | `runs.read` or `team.admin` |
| `POST` | `/api/v1/secrets` | `admin` or `team.admin` |
| `POST` | `/api/v1/secrets/rotate` | `admin` |
| `DELETE` | `/api/v1/secrets/{name}` | `admin` or `team.admin` |
| `GET` | `/api/v1/secrets/{name}` | `claim` or `secrets.read` or `team.admin` |
| `GET` | `/api/v1/services` | `authenticated` |
| `GET` | `/api/v1/storage` | `runs.read` |
| `DELETE` | `/api/v1/team` | `team.admin` |
| `PATCH` | `/api/v1/team` | `team.admin` |
| `GET` | `/api/v1/team/billing` | `runs.read` |
| `PUT` | `/api/v1/team/billing/budget` | `team.admin` |
| `POST` | `/api/v1/team/billing/card` | `team.admin` |
| `POST` | `/api/v1/team/billing/checkout` | `team.admin` |
| `POST` | `/api/v1/team/billing/pay` | `team.admin` |
| `GET` | `/api/v1/team/build-trust` | `runs.read` |
| `PUT` | `/api/v1/team/build-trust` | `team.admin` |
| `GET` | `/api/v1/team/git-credentials` | `runs.read` |
| `POST` | `/api/v1/team/git-credentials` | `team.admin` |
| `GET` | `/api/v1/team/git-credentials/releases` | `team.admin` |
| `DELETE` | `/api/v1/team/git-credentials/{host}` | `team.admin` |
| `POST` | `/api/v1/team/git-credentials/{host}/confirm` | `team.admin` |
| `GET` | `/api/v1/team/github-app` | `runs.read` |
| `DELETE` | `/api/v1/team/github-app/automation` | `team.admin` |
| `GET` | `/api/v1/team/github-app/automation` | `runs.read` |
| `PUT` | `/api/v1/team/github-app/automation` | `team.admin` |
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
| `DELETE` | `/api/v1/team/members/{account}` | `runs.read` |
| `PATCH` | `/api/v1/team/members/{account}` | `team.admin` |
| `GET` | `/api/v1/team/tokens` | `runs.read` |
| `POST` | `/api/v1/team/tokens` | `runs.read` |
| `PUT` | `/api/v1/team/tokens/{prefix}/git-credentials` | `team.admin` |
| `POST` | `/api/v1/teams` | `authenticated` |
| `GET` | `/api/v1/tokens` | `admin` |
| `POST` | `/api/v1/tokens` | `admin` |
| `POST` | `/api/v1/tokens/{prefix}/metered` | `admin` |
| `POST` | `/api/v1/tokens/{prefix}/rotate` | `admin` |
| `DELETE` | `/api/v1/tokens/{token}` | `runs.read` |
| `GET` | `/api/v1/tokens/{token}` | `admin` |
| `GET` | `/api/v1/trends` | `runs.read` |
| `GET` | `/api/v1/triggers` | `triggers.read` |
| `POST` | `/api/v1/triggers` | `runs.write` |
| `POST` | `/api/v1/triggers/claim` | `triggers.claim` |
| `GET` | `/api/v1/triggers/spawned-child` | `triggers.read` |
| `GET` | `/api/v1/triggers/{id}` | `triggers.read` or `nodes.claim` or `triggers.claim` |
| `POST` | `/api/v1/triggers/{id}/claim` | `triggers.claim` |
| `POST` | `/api/v1/triggers/{id}/done` | `triggers.claim` |
| `POST` | `/api/v1/triggers/{id}/heartbeat` | `triggers.claim` |
| `POST` | `/internal/downloads/charge` | `public` |
| `POST` | `/internal/egress/totals` | `public` |
| `POST` | `/internal/storage/commit` | `public` |
| `POST` | `/internal/storage/release` | `public` |
| `POST` | `/internal/storage/reserve` | `public` |
| `GET` | `/metrics` | `admin` |
| `POST` | `/webhooks/github-app` | `public` |
| `POST` | `/webhooks/stripe` | `public` |

