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
| `admin`           | tokens CRUD, cache PUT, state mutation (Create/Start/Finish Job, Events, Locks, Pool, etc.)        |

Scope checks are set membership. `admin` is a superset -- any handler's
scope check passes if the principal carries `admin`.

Per-endpoint scope annotations live in `pkg/controller/server.go`. If
you add a new route, annotate it with `requireScope`.

## Unauthenticated endpoints

Always open, regardless of auth config:

- `GET /api/v1/health` on the controller -- k8s livenessProbe /
  readinessProbe target. httpGet probes can't carry `Authorization`.
- `GET /api/v1/health` on the logs-service -- same reasoning.
- `GET /metrics` on the controller -- Prometheus scrape target.
- `GET /metrics` on the logs-service -- Prometheus scrape target.
- `GET /api/v1/auth/whoami` -- authenticated via the middleware just
  like any other endpoint, but without a scope check. Used by the
  logs-service to resolve tokens via the controller.
- `GET /api/v1/auth/bootstrap-needed` -- probe for the first-visit
  signup path (see below). Returns `{"needed": true}` while the
  users table is empty.

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
