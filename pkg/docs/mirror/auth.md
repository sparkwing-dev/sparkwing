# Authentication + authorization

Sparkwing uses a shared-secret bearer token model with typed principals
and per-endpoint scope annotations.

## Token format

Raw tokens are `<prefix>_<entropy>`:

- `swu_...` -- user. Created for humans (`sparkwing cluster tokens create --type user`).
- `swr_...` -- runner. Created for remote machine agents or pool replicas.
- `sws_...` -- service. Created for in-cluster back-channel callers.

The **prefix segment** is the first 12 characters of a raw token. It's
a non-secret identifier used in `sparkwing cluster tokens list`, `revoke`, and
audit logs. The remaining ~35 characters carry the secret entropy.

## Scopes

The scope constants live in `pkg/controller/auth.go`; the full route-to-scope
mapping is in the generated [api-reference.md](api-reference.md):

| Scope             | Unlocks                                                                                           |
|-------------------|---------------------------------------------------------------------------------------------------|
| `runs.read`       | GET `/api/v1/runs`, `/runs/{id}`, `/runs/{id}/nodes`, `/runs/{id}/events`, `/trends`, `/agents`, `/queue/state`, per-node metrics GETs, and similar deployment-wide reads. `/runs/{id}` alone also admits a `nodes.claim` or `triggers.claim` token holding a live claim on that run |
| `runs.write`      | POST `/api/v1/triggers`, `/runs/{id}/cancel`, `/runs/{id}/retry`, `/runs/{id}/nodes/{id}/bounce`, `/runs/{id}/nodes/{id}/release`, `/gitcache/refresh` |
| `nodes.claim`     | POST `/nodes/claim`, `heartbeat`, the per-node write routes, GET claimed node data, GET the claimed run and trigger, and read-only Git proxy routes scoped to a live claimed run |
| `logs.read`       | GET on logs-service (`/api/v1/logs/*`, `/api/v1/logs/search`)                                      |
| `logs.write`      | POST + DELETE on logs-service (`/api/v1/logs/{runID}/{nodeID}`, `/api/v1/logs/{runID}`)            |
| `triggers.read`   | GET `/api/v1/triggers`, `/triggers/{id}`, `/triggers/spawned-child`. `/triggers/{id}` alone also admits a `nodes.claim` or `triggers.claim` token holding a live claim on that run |
| `triggers.claim`  | POST `/api/v1/triggers/claim`, `/triggers/{id}/heartbeat`, `/triggers/{id}/done`, and GET the live claimed trigger and its run. The heartbeat and the done name a trigger, and each is bound to the claimant that trigger's row records |
| `runs.state`      | POST `/api/v1/runs`, `/runs/{id}/finish`, `/runs/{id}/plan`, `/runs/{id}/nodes`, `/runs/{id}/events`, per-node `start`, `finish`, `deps`, `status`, and PUT `/pipelines/{name}/profile/pin`. Every write naming a run is bound to a run the caller owns; the pin names a pipeline and is bound to a live claim on a run of it |
| `secrets.read`    | GET `/api/v1/secrets/{name}`, resolved against the repository of the run the caller holds a claim in |
| `approvals.write` | POST `/api/v1/runs/{id}/approvals/{nodeID}` (approve / deny a gate)                                |
| `admin`           | tokens / users / secrets CRUD, node deps / status / mark-ready / revoke-ready, run delete, gitcache seed, warm-pool checkout / return / heartbeat, and the mutating concurrency routes -- see [api-reference.md](api-reference.md) for the per-route mapping |

Scope checks are set membership. `admin` is a superset -- any handler's
scope check passes if the principal carries `admin`.

A runner needs `nodes.claim`, `triggers.claim`, `runs.state`, `secrets.read`,
and `logs.write`. That set claims work, drives the run it claimed from plan to
finish, reads the secrets its repository owns, and ships logs. It mints no
token, reads no user, lists no secret, and cannot read cached source for an
unclaimed run. A pool replica that executes
already-created nodes still needs `runs.state`, because `start`, `finish`, and
event append are its own writes; it can drop `triggers.claim` when a separate
dispatcher claims triggers.

The set carries neither `runs.read` nor `triggers.read`, and a node process
opens with exactly those two reads: `GET /api/v1/runs/{id}` and
`GET /api/v1/triggers/{id}`. Both admit a caller holding a live claim on that
run in place of the scope, so the claim the runner already took is what opens
them. Add `runs.read` only to give a token the deployment-wide view.

Marking a node ready stays `admin`, so a warm-pool dispatcher that hands nodes
to a pool keeps an `admin` token. That is the one process in the runner path
above the runner scope set.

A route can narrow a field below its route scope. The node dispatch reads
(`GET /api/v1/runs/{id}/nodes/{nodeID}/dispatch` and `/dispatches`) admit
`runs.read`, but fill `env_json` only for an `admin` principal. Every reader
still gets `redacted_keys`, the names the snapshot dropped as credentials.

## Claim ownership

Scope decides which routes a token may call; the claim decides which node it
may write. `POST /api/v1/nodes/claim` binds the claim to the **claiming
token**: the controller records that token's prefix segment alongside the
principal name and the client-supplied `holder_id`. The prefix is what the
gate matches on, because it is unique per token while a principal name is a
free-form label two tokens may share; the name stays for display. Afterwards
the per-node write routes require that token plus the exact holder,
membership, reservation, and claim generation while the lease is unexpired.
A missing fence gets `403` with `"error": "claim_required"`; an expired or
stale fence gets `409 Conflict`. Another runner token is refused, and
`POST /runs/{id}/nodes/{nodeID}/heartbeat` answers `409` unless the token, the
principal, and the holder id all match. `admin` bypasses the check, which is
what lets a dispatcher mark a node ready, start it, and finish it.

The lease is an authorization window, so the claimant does not choose how long
it lasts. `lease_secs` above the server cap of 10 minutes is clamped, on the
claim and on every heartbeat; a runner renews well inside that.

`POST /api/v1/triggers/claim` binds the same way and increments a claim
generation. A trigger's id is the id of the run it creates, so trigger-driven
node mutations carry that exact generation and are accepted only while the
same token holds the live claim for that run. A stale generation gets `409`.

`GET /api/v1/runs/{id}` accepts `runs.read` or a live `nodes.claim` or
`triggers.claim` owner of that run. `GET /api/v1/triggers/{id}` likewise
accepts `triggers.read` or either live claim. Claim-scoped access expires with
the lease and never widens list routes.

Run-definition writes -- run create and finish, plan snapshot, node create, and
run-level events -- require the source trigger's exact live generation.
Per-node writes accept either the node's exact live claim or that source
trigger generation. A run heartbeat likewise accepts one exact live node claim
from the run or the source trigger generation. A runner with `runs.state` but
without the applicable fence gets `403 claim_required`; a stale generation
gets `409 Conflict`.

An assisted executor acknowledges its claim generation and next monotonic
attempt ordinal immediately before each job-body invocation. Node log appends
must carry that started ordinal in addition to the exact claim fence. The logs
service validates it against the controller and stores it in an immutable
attempt substream. Trigger-owned node logs carry the trigger generation and
started attempt ordinal; trigger-generation-only logs are reserved for the
coordinator's `_compile` output.
Ordinary node reads return executor and attempt attribution but remove holder
and reservation values; claim responses still return the fence the winner must
present.

`PUT /pipelines/{name}/profile/pin` is the one `runs.state` write that names a
pipeline instead of a run, and a pin becomes a hard Kubernetes limit for every
later run of it. The caller must hold a live claim on some run of that
pipeline, so a token executing one pipeline cannot pin another's. `admin`
bypasses, which is what lets a dispatcher pin a pipeline it is not running.

The two reads a node process opens with, `GET /api/v1/runs/{id}` and
`GET /api/v1/triggers/{id}`, run the ownership check the other way: a caller
without `runs.read` or `triggers.read` is admitted when it holds a live claim
on that run, and refused with `403 missing_scope` otherwise.

`mark-ready` and `revoke-ready` both require `admin`. Readiness is a dispatcher
decision on both sides, and `revoke-ready` writes only an *unclaimed* node, so
holding a claim can never stand in for the scope.

A `nodes.claim` token also reaches only the runs it is working on. The node
read routes (`GET nodes/{id}`, `nodes/{id}/output`, `nodes/{id}/bounce`) and
`POST /runs/{id}/heartbeat` answer `403 claim_required` unless the caller holds
an unexpired claim on some node of that run. `admin` bypasses; so does
`runs.read` on the reads, which already grants the wider view through
`GET /runs/{id}/nodes`.

Node mutations validate the exact fence in the same transaction as the write.
If the lease expires or another generation wins while an old executor is
paused, the old write cannot land. An append already accepted by the log
service remains confined to the old attempt substream rather than entering the
replacement's log.

The execution view (`GET /api/v1/runs/{id}?include=secret_values`) follows the
same rule: it returns plaintext argument values to an `admin` principal, or to
a `nodes.claim` principal holding an unexpired claim on one of the run's nodes.
A trigger claimant may read the run record but receives redacted secret values
until it holds a node claim.
A controller serving unauthenticated returns **plaintext**, because the whole
API is open in that mode and handing a runner `***` as a real argument value
would corrupt the run rather than protect it. Once authentication is on, a
request that carries no principal is refused.

## Secret ownership

A secret carries an owning repository slug, or none. Store one with
`sparkwing secrets set --name DEPLOY_KEY --file ./key --repo acme/web
--profile prod`. A secret stored with neither `--repo` nor `--shared` answers
`admin` callers only; `--shared` opens an unscoped secret to **every run in the
cluster**, so reserve it for values that are genuinely shared, such as a
registry pull token.

`GET /api/v1/secrets/{name}` resolves differently per principal:

- An `admin` principal reads any row. `?repo=<slug>` selects a repository's
  row, `?run=<id>` selects the repository of that run, and without either the
  unscoped row answers.
- A `secrets.read` principal without `admin` cannot name a repository. It names
  the run it is executing with `?run=<id>`, and the controller answers only
  when the caller holds that run's claim; the name then resolves against that
  run's repository, falling back to an unscoped row only when that row is
  shared. A caller holding no claim reads nothing. A caller holding claims in
  one repository may omit `?run`; holding claims in two, it must name the run.

So one runner token cannot lift another repository's deploy credential by
asking for it by name, and a token working two runs cannot read the wrong one's
credential by accident. `GET /api/v1/secrets` (the list) and the secret writes
stay `admin`; the list carries each row's repository and shared flag.

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
`--scope` grants `admin`. The first-visit bootstrap account defaults to
`admin` and may carry more scopes beside it, but a scope set that omits
`admin` is rejected with `400`, and `sparkwing cluster users list` prints the
scope set of every account.

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

Every dashboard response carries `Content-Security-Policy`
(`default-src 'self'` plus a per-response nonce for the bundle's inline
scripts), `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, and
`Referrer-Policy: same-origin`, and adds `Strict-Transport-Security` when the
request carries evidence of TLS: the listener terminates TLS itself, a peer
inside `--trusted-proxy-cidrs` forwarded `X-Forwarded-Proto: https`, or the
operator passed `--hsts` because TLS terminates somewhere that forwards no
trusted header. That same evidence decides the scheme the CSRF origin check
expects, so a dashboard behind an HTTPS proxy keeps `Secure` cookies without
the insecure-cookie override. The page reads its configuration from
`/sparkwing-runtime.js`, which carries the dashboard version and the login
mode. The service bearer stays in the web process and rides only its
server-side proxy, so the browser talks to one origin and `connect-src 'self'`
holds.

A dashboard that carries `--token`, runs without `--require-login`, and binds a
non-loopback address refuses to start, because every caller that reaches the
listener would drive the controller with that token. Pass `--require-login`,
bind a loopback address (chart: `web.addr`), or accept the exposure with
`--allow-unauthenticated-remote` (chart: `web.allowUnauthenticatedRemote`).
`--token` with no controller, logs, or profile backend is a startup error too:
nothing would authenticate with it, so the dashboard would serve
unauthenticated while the flag suggested otherwise.

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
through the dashboard. `sparkwing-web` forwards each browser's resolved
address to the controller as `X-Forwarded-For`, so the controller's list must
include the web pod's source; otherwise the controller ignores the header and
keys every dashboard login on the web pod's own address. See
[security.md](security.md#login-and-hashing-budgets) for its budgets, the
per-prefix bearer budget, and the argon2 memory bound.

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
user. The controller answers `5xx` when the state store or the session signing
key is unreadable, so only an unknown or expired session reaches the browser as
`401`. Browser redirects preserve the original path and query as one encoded
`next` value and accept only same-origin absolute paths.

Login cookies are `Secure` by default, so a login-required dashboard must be
served over HTTPS. A plain `http://localhost` port-forward can reach health
endpoints but cannot retain those cookies. For a loopback-only development
process, `SPARKWING_WEB_INSECURE_COOKIES=1` permits HTTP cookies. The dashboard
reads that variable once at startup and refuses a non-loopback bind with it
set. That check reads the bind address only: a proxy or sidecar in front of a
loopback bind still carries the cookie unencrypted to everything it publishes.
An operator who publishes the dashboard over plain HTTP through a proxy or
ingress adds `--allow-insecure-cookies-remote` to accept cookies that travel
without TLS; the chart renders that flag with the variable whenever
`ingress.allowInsecure` opts a TLS-less ingress in.

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
`sparkwing cluster users add` to create the first dashboard user. That
first account has to be an admin, so leave `--scope` off, or name a list
that contains `admin`; a narrower list is refused with `400` while the
users table is empty.

After the first admin is created, additional users are added via
`sparkwing cluster users add`. Pass `--scope` to bound what that account's
dashboard sessions reach; omitting it grants `admin`.

## CLI

Every `sparkwing` command that talks to a remote controller reads
connection info from a profile. Register one first:

```sh
# Register a prod profile (controller URL + admin bearer).
# --token-stdin prompts without echo on a terminal and reads a pipe otherwise.
sparkwing configure profiles add --name prod \
    --controller https://sparkwing.example.com \
    --token-stdin
```

`--token` accepts the bearer on the command line instead, but every process
on the machine can read it from the process list and the shell records it in
history. Use it only where a prompt or a pipe is impossible.

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

`--grace` is capped at 7 days; a larger value is rejected with `400`.
Revoking the old prefix cuts an open grace window short, so a rotation
you started before learning the old token leaked can still be stopped.

Deleting a user removes the user row, deletes every session that user
holds, and revokes every token whose principal is that name, in one
transaction. The token the delete request authenticates with is left
alone, so an operator whose admin token shares a name with the account
being deleted keeps working. Principals are free-form labels, so any
other token minted under the same name is revoked too, including one
minted for an unrelated caller; keep human account names and service
principal names distinct.

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
only runs on cold lookups. Concurrent hashing is capped by a memory
budget, and a hash that waits more than 250ms for a slot is shed with
`503` and a `Retry-After` instead of queueing.

## How long revocation takes to bite

Revoking a token, rotating one, and deleting a user all drop the
affected prefixes from the controller replica that served the request,
so the next request on that replica re-reads the row and gets `401`.
A cached entry also carries the row's `expires_at` and `revoked_at`,
which are rechecked on every hit, so a token that expires or whose
rotation grace closes mid-cache stops authenticating on time rather
than at the end of the cache window. An authentication that was already
reading the row when the revoke landed does not install its entry, so it
cannot put the revoked row back into the cache.

Three windows remain:

- **Other controller replicas.** Invalidation is in-process. A replica
  that did not serve the revoke keeps its cached entry for up to 60
  seconds. Restart or scale the controller to zero to close it now.
- **The loopback controller each run starts.** A local run serves the
  admin API from the orchestrator process over the same tokens table,
  behind its own 60-second cache that a controller restart does not
  reach. It is bound to loopback and exits with the run.
- **The logs service.** `sparkwing-logs` resolves callers through the
  controller's `whoami` and caches the answer for its own TTL (60s by
  default), on top of whatever the controller replica held. Its worst
  case is the sum of the two.

Sessions carry no cache: the controller reads the `sessions` row on
every request and the dashboard resolves the session on every protected
request, so deleting a session or a user logs that browser out on its
next request.

A session expires 12 hours after its last use and the controller renews it when
under an hour remains, but never past seven days from the moment it was
created. Reaching that age deletes the row and answers `401`, so the browser
signs in again. An embedder changes the cap with
`controller.Server.WithSessionMaxLifetime`.

## Extension points

- **OIDC / SSO**: not implemented. The `users` + `sessions` tables are
  shape-compatible; an OIDC callback can populate sessions directly by writing
  `sha256(session id)` into `sessions.hash` and keeping the raw id only in the
  browser cookie. There is no `csrf_token` column: Sparkwing derives that token
  per request as an HMAC of the session id under a key in `sparkwing_meta`.
- **Audit trail**: the principal name is stamped onto the OTel trace
  span. There is no dedicated audit database.
- **Per-user multi-tenancy**: principals are a free-form label. Adding a
  roles model is orthogonal and doesn't require a wire-shape change.
- **Fine-grained `admin` split**: `triggers.claim`, `runs.state`, and
  `secrets.read` carved the runner's work out of `admin`. What remains can be
  split further into `cache.write`, `locks.admin`, and similar when a real
  caller needs that narrower trust.
- **Execution capabilities beyond assisted nodes**: workstation and gateway
  agents keep their enrollment bearer in the supervisor and give each
  job-body child a process-lifetime loopback capability for its exact run,
  node, and acknowledged attempt log/lifecycle. Schema 30 is the internal
  current-node dependency; schema 31 adds the current-attempt mutation fence and
  durable grants for `Memoize`, `Concurrency`, `ToolSlot`, `RunAndAwait`,
  cross-pipeline references, and dynamic `SpawnNode` before this path can ship.
  Other execution modes retain their documented credential boundary. A future
  capability service could make the same split portable across container and
  process boundaries that do not share one supervisor.
