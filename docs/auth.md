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

A route can narrow a field below its route scope. The node dispatch reads
(`GET /api/v1/runs/{id}/nodes/{nodeID}/dispatch` and `/dispatches`) admit
`runs.read`, but fill `env_json` only for an `admin` principal. Every reader
still gets `redacted_keys`, the names the snapshot dropped as credentials.

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
the dashboard uses to establish, validate, and end a session (login,
logout, session), the bootstrap probe, and the GitHub webhook, which
is HMAC-verified instead of bearer-authenticated. The logs service
opens its health and metrics probes the same way. Every registered
route is listed in
[api-reference.md](api-reference.md).

With controller-backed dashboard login enabled, the browser authenticates
same-origin dashboard requests with its `HttpOnly` session cookie. The
dashboard validates that session before its server-side proxy adds the service
bearer to an upstream controller request; the service credential never enters
browser HTML or JavaScript. CLI and automation clients should authenticate
directly to the controller through a profile rather than send a bearer to the
browser-facing dashboard proxy.

## Dashboard authorization

The dashboard proxies a fixed list of controller routes: the run, node,
approval, agent, and trend reads the SPA renders, plus the trigger, cancel,
retry, debug-release, approval-resolve, and run-delete writes its buttons
issue. A second list covers the logs service and carries reads only, so the
browser cannot delete a run's logs or append a forged line through the web pod.
Every other path under `/api/v1/` answers `404` at the web pod and never
reaches the upstream, so a signed-in tab cannot mint a token, read a secret, or
create a user through the proxy. Both lists live in
`internal/web/proxy_routes.go`, and a test holds each entry to the scope
`pkg/controller/server.go` and `pkg/logs/server.go` register for that route.

A browser session carries the scopes of the user who signed in. The proxy
checks them against the target route before forwarding, so an account holding
only `runs.read` reads runs and gets `403` on cancel. Create narrower accounts
with `sparkwing cluster users add --scope runs.read,logs.read`; omitting
`--scope` grants `admin`. The first-visit bootstrap admin is always `admin`,
and `sparkwing cluster users list` prints the scope set of every account.

The web pod's own service token needs `runs.read` plus `logs.read`. Add
`runs.write` where the UI cancels, retries, or releases a debug pause, and
`approvals.write` where it resolves approval gates. That token bounds what the
proxy can reach at all; the session's scopes bound what one signed-in user
reaches through it.

Deleting a run from the dashboard needs `admin` on both sides, because the
controller registers `DELETE /api/v1/runs/{id}` at `admin`: the web pod's token
must carry `admin` and so must the signed-in account. Leave `admin` off that
token where operators should delete runs with the CLI instead; the dashboard
button then reports `delete needs the admin scope` and nothing is removed.

`sparkwing-web --require-login` needs a controller session backend. Pass
`--controller URL`, or select a `--profile` whose `controller.url` is set. A
state-only configuration such as `--state-spec=postgres://... --require-login`
now fails at startup instead of silently serving an unauthenticated dashboard.
The controller URL must be an absolute `http` or `https` URL without embedded
credentials, a query, or a fragment.

Login throttling uses the TCP peer address and ignores forwarded headers by
default. When a reverse proxy fronts `sparkwing-web`, pass its egress networks
as `--trusted-proxy-cidrs=<CIDR,...>` or set the chart's
`web.trustedProxyCIDRs`. Sparkwing accepts `X-Forwarded-For` only from a trusted
peer and walks append-style chains from right to left until it reaches the
nearest untrusted address. Values to its left are ignored. A malformed entry in
the trusted suffix or an untrusted immediate peer falls back to the TCP peer.
IPv4-mapped CIDRs with prefix lengths `/96` through `/128` normalize to IPv4;
broader mapped prefixes fail startup. List proxy networks, not client networks.

The controller throttles `POST /api/v1/auth/login` the same way and takes the
same `--trusted-proxy-cidrs` flag, because it is reachable without going
through the dashboard. See
[security.md](security.md#login-and-hashing-budgets) for its budgets and the
argon2 memory bound.

The login, first-admin, and logout forms carry a CSRF token in both a
`SameSite=Strict` cookie and a hidden field. Sparkwing rejects a missing,
cross-origin, or mismatched token with `403` before it calls the controller.
Unsafe browser API requests (`POST`, `PUT`, `PATCH`, and `DELETE` under
`/api/v1/`) also require a same-origin request whose `X-CSRF-Token` header
matches both the browser's `sw_csrf` cookie and the live controller session.
The dashboard proxy removes browser cookies and the CSRF header before adding
its server-side bearer to controller or logs-service requests.
Logout also verifies the token against the live controller session. It clears
the browser session only after the controller confirms revocation; a controller
failure returns `502` and leaves the cookies in place so the browser does not
claim a session was revoked when it was not.

The dashboard resolves the controller session on every HTML, data, and API
request. Hashed files under `/_next/static/` contain no tenant data and do not
touch the session backend. Deleting a session on another web replica or at the
controller therefore takes effect on the next protected data request rather
than after a local cache expires. A controller `401` authoritatively clears the
browser session; a controller outage, `5xx`, or malformed response returns
`502` and preserves the cookies so a transient failure cannot log out every
user. Browser redirects preserve the original path and query as one encoded
`next` value and accept only same-origin absolute paths.

Login cookies are `Secure` by default, so a login-required dashboard must be
served over HTTPS. A plain `http://localhost` port-forward can reach health
endpoints but cannot retain those cookies. For a loopback-only development
process, `SPARKWING_WEB_INSECURE_COOKIES=1` permits HTTP cookies; never set that
override on a shared pod, ingress, or non-loopback listener.

## First-visit signup

Controller authentication is enabled at startup when the tokens table contains
an active token. `--require-auth` makes startup fail when it does not; see the
[security operator checklist](security.md#operator-checklist).

A freshly-installed sparkwing cluster has no users, so there is
nothing to log in *as*. While controller authentication is disabled,
browsing to `/login` on an empty cluster renders a "Create first admin"
form. Submitting it creates the first admin user via `POST
/api/v1/users`, then signs the new admin in automatically.

The bootstrap path is one-shot and latched: once any user exists,
the controller serves `{"needed": false}` to the probe, the login
page reverts to the standard sign-in form, and `POST /api/v1/users`
goes back to requiring an admin token. There is no way to reopen
the bootstrap path short of restarting the controller against a
freshly emptied database.

When controller authentication is enabled, the bootstrap probe reports
`{"needed": false}` and `POST /api/v1/users` requires an admin token even
if the users table is empty. An operator can use that token with
`sparkwing cluster users add` to create the first dashboard user.

After the first admin is created, additional users are added via
`sparkwing cluster users add`. Pass `--scope` to bound what that account's
dashboard sessions reach; omitting it grants `admin`.

## CLI

Every `sparkwing` command that talks to a remote controller reads
connection info from a profile. Register one first:

```sh
# Register a prod profile (controller URL + admin bearer).
sparkwing configure profiles add --name prod \
    --controller https://sparkwing.example.com \
    --token "$ADMIN_TOKEN"
```

Then the tokens commands are terse:

```sh
# Mint a user admin token. Emits the raw token ONCE. Stash it.
sparkwing cluster tokens create --type user --principal alice --scope admin --profile prod

# List all active tokens.
sparkwing cluster tokens list --profile prod

# List including revoked, for audit.
sparkwing cluster tokens list --include-revoked --profile prod

# Revoke a token by its non-secret prefix.
sparkwing cluster tokens revoke --prefix swu_6cF9r2Kp --profile prod

# Look up metadata for a prefix.
sparkwing cluster tokens lookup --prefix swu_6cF9r2Kp --profile prod

# Rotate: mint a replacement, with a grace window before the old one 401s.
sparkwing cluster tokens rotate --prefix swu_6cF9r2Kp --grace 48h --profile prod
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
