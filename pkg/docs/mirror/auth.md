# Authentication + authorization

Sparkwing uses a shared-secret bearer token model with typed principals
and per-endpoint scope annotations.

## Token format

Raw tokens are `<prefix>_<entropy>`:

- `swu_...` -- user. Created for humans (`sparkwing cluster tokens create --type user`).
- `swr_...` -- runner. Created for laptop agents or pool replicas.
- `sws_...` -- service. Created for in-cluster back-channel callers.

The **prefix segment** is the first 12 characters of a raw token. It's
a non-secret identifier used in `sparkwing cluster tokens list`, `revoke`, and
audit logs. The remaining ~35 characters carry the secret entropy.

## Scopes

The scope constants live in `pkg/controller/auth.go`; the full route-to-scope
mapping is in the generated [api-reference.md](api-reference.md):

| Scope             | Unlocks                                                                                           |
|-------------------|---------------------------------------------------------------------------------------------------|
| `runs.read`       | GET `/api/v1/runs`, `/runs/{id}`, `/runs/{id}/nodes`, `/trends`, `/agents`, per-node metrics GETs  |
| `runs.write`      | POST `/api/v1/triggers`, `/runs/{id}/cancel`, `/runs/{id}/retry`                                   |
| `nodes.claim`     | POST `/nodes/claim`, `mark-ready`, `revoke-ready`, `heartbeat`; GET `nodes/{id}`, `nodes/{id}/output`, POST `/nodes/{nid}/metrics` |
| `logs.read`       | GET on logs-service (`/api/v1/logs/*`, `/api/v1/logs/search`)                                      |
| `logs.write`      | POST + DELETE on logs-service (`/api/v1/logs/{runID}/{nodeID}`, `/api/v1/logs/{runID}`)            |
| `triggers.read`   | GET `/api/v1/triggers`, `/triggers/{id}`, `/triggers/spawned-child`                               |
| `approvals.write` | POST `/api/v1/runs/{id}/approvals/{nodeID}` (approve / deny a gate)                                |
| `admin`           | tokens / users / secrets CRUD, run + node state mutation (create, start, finish, deps, events, delete), trigger lifecycle (claim, heartbeat, done), gitcache seed, warm-pool checkout / return / heartbeat, and the mutating concurrency routes -- see [api-reference.md](api-reference.md) for the per-route mapping |

Scope checks are set membership. `admin` is a superset -- any handler's
scope check passes if the principal carries `admin`.

Token creation validates scopes against that same set: a scope the
controller does not honor is rejected with a `400` naming the offending
scope and the valid set, so a typo fails at mint time instead of
producing a token that authenticates and then fails every scope check.
A token with no scopes is still legal; it just unlocks nothing.

Per-endpoint scope annotations live in `pkg/controller/server.go`. If
you add a new route, annotate it with `requireScope`.

`GET /api/v1/auth/whoami` is authenticated by the middleware like any
other route but carries no scope check, so any valid token can read
back its own principal, kind, scopes, and prefix. The logs service uses
it to resolve tokens against the controller. It shows as `public` in
[api-reference.md](api-reference.md) because that table is generated
from `requireScope` wrappers -- there, `public` means no scope check,
not no authentication.

## Unauthenticated endpoints

Routes registered on the controller's outer router are matched before
the auth middleware runs, so they are open regardless of auth config:
the health and metrics probes (k8s httpGet probes and Prometheus
scrapes can't carry `Authorization`), the service-discovery endpoint
the runner uses to find the cache pod, the browser session endpoints
the dashboard needs before it holds a token (login, logout, session),
the bootstrap probe and the bootstrap-or-admin `POST /api/v1/users`
(see below), and the GitHub webhook, which is HMAC-verified instead of
bearer-authenticated. The logs service opens its health and metrics
probes the same way. Every registered route is listed in
[api-reference.md](api-reference.md).

## First-visit signup

A freshly-installed sparkwing cluster has no users, so there is
nothing to log in *as*. Browsing to `/login` on an empty cluster
renders a "Create first admin" form (matching the Grafana / ArgoCD /
Prometheus first-visit pattern). Submitting it creates the first
admin user via an unauthenticated `POST /api/v1/users`, then signs
the new admin in automatically.

The bootstrap path is one-shot and latched: once any user exists,
the controller serves `{"needed": false}` to the probe, the login
page reverts to the standard sign-in form, and `POST /api/v1/users`
goes back to requiring an admin token. There is no way to reopen
the bootstrap path short of restarting the controller against a
freshly emptied database.

After the first admin is created, additional users are added via
`sparkwing cluster users add` (admin-scoped) like any other operator
account.

## CLI

Every `sparkwing` command that talks to a remote controller reads
connection info from a profile. Register one first:

```sh
# Register a prod profile (controller URL + admin bearer).
sparkwing configure profiles add --name prod \
    --controller https://sparkwing.example.com \
    --logs https://sparkwing-logs.example.com \
    --token "$ADMIN_TOKEN"

# Optional: set it as the default so you don't need --profile on every call.
sparkwing configure profiles use --name prod
```

Then the tokens commands are terse:

```sh
# Mint a user admin token. Emits the raw token ONCE. Stash it.
sparkwing cluster tokens create --type user --principal alice --scope admin --profile prod

# List all active tokens (omits --profile because prod is the default).
sparkwing cluster tokens list

# List including revoked, for audit.
sparkwing cluster tokens list --include-revoked

# Revoke a token by its non-secret prefix.
sparkwing cluster tokens revoke --prefix swu_6cF9r2Kp

# Look up metadata for a prefix.
sparkwing cluster tokens lookup --prefix swu_6cF9r2Kp

# Rotate: mint a replacement, with a grace window before the old one 401s.
sparkwing cluster tokens rotate --prefix swu_6cF9r2Kp --grace 48h
```

Profiles are the only path for targeting a remote cluster, which keeps
it hard to accidentally point at the wrong one. The
`SPARKWING_CONTROLLER_URL` environment variable is a fallback only for
the local dashboard dev flow, not for remote-cluster targeting.

## Argon2 parameters

Hash parameters (`pkg/store/tokens.go`):

- `time = 1`
- `memory = 64 MiB`
- `threads = 4`
- key length = 32 bytes

Measured on an arm64 laptop: ~8-15ms per `argon2.IDKey`. Token lookup on
the hot path is prefix-indexed + cached in-process for 60s, so argon2
only runs on cold lookups.

## Extension points

- **OIDC / SSO**: not implemented. The `users` + `sessions` tables are
  shape-compatible; an OIDC callback can populate sessions directly.
- **Audit trail**: the principal name is stamped onto the OTel trace
  span. There is no dedicated audit database.
- **Per-user multi-tenancy**: principals are a free-form label. Adding a
  roles model is orthogonal and doesn't require a wire-shape change.
- **Fine-grained `admin` split**: the `admin` scope is intentionally
  broad. It can be split into `cache.write`, `locks.admin`, etc. when a
  real caller needs that narrower trust.
