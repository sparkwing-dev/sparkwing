# Credential model

Status: in progress. This page is a design for contributors. Sections marked
"landed" describe behavior the code has; the rest describe work that has not
started. The decisions at the end are either decided or still open.

The controller accepts four kinds of credential, and every request it
authenticates names one of them:

| Kind | Who holds it | Format | Minted by | Bound to |
|---|---|---|---|---|
| Human session | a person in a browser | `__Host-sw_session` cookie or `Authorization: Session` | Google or GitHub sign-in | an account and its active team; scopes come from the membership on every request |
| Team token | a terminal or a machine | `swt_` bearer (`swu_` and `swr_` rows minted before the merge keep working) | `POST /api/v1/team/tokens` by a member | one team; scopes chosen at mint time from one family |
| Claim token | one node's process | `swc_` bearer | the controller when a node is claimed | team, run, node and claim generation; dies with the claim |
| Operator | the deployment's operator | bootstrap admin token (`swu_`, principal `bootstrap:admin`), other `admin` tokens, and operator console sessions | provisioning, `POST /api/v1/tokens`, sign-in by a listed operator account | the deployment, not a team |

Everything else a request can carry today is either derived from one of
these (a cache grant derives from a claim) or one of the leftovers this page
plans to fold in.

## Human sessions and operator sign-in (landed)

Password users are gone. A browser signs in one of two ways:

- **Google or GitHub.** The session belongs to an account; its scopes are
  the account's role in its active team, read on every request.
- **Operator token.** The `/login` form takes a user token holding `admin`,
  such as the bootstrap admin token. The session row stores the token's
  prefix and no scopes; every lookup joins the token row and refuses the
  session once the token is revoked or expired. A runner, service or
  non-admin token opens no session.

The first operator comes from the bootstrap admin token
(`--bootstrap-admin-token-file`), never from the first web visitor. That was
already the only path on a multi-team controller; the single-team
first-admin form, which let whoever reached an open controller first become
its administrator, is removed with password users.

Google and GitHub sign-in is offered only under a multi-team license. An
unlicensed single-team controller therefore has exactly one browser sign-in,
the operator token, and no way to give a second person narrower dashboard
access. Decision D1 below asks whether that is the intended product.

## Team tokens (landed)

One kind, `team`, replaces team CLI tokens and team runner tokens. A token
draws its scopes from exactly one family at mint time:

- **Member family:** a subset of the scopes the minter's role allows a CLI
  token (everything the role holds except `team.admin`). The principal is the
  minter, the lifetime is 90 days fixed, ten live per member, and the token
  dies with the minter's membership or a demotion that drops its scopes.
- **Machine family:** a subset of `nodes.claim`, `triggers.claim`,
  `runs.state` and `logs.write` that includes a claim scope.
  The minter must be an editor; the token is named (`agent:<name>`, unique in
  the team across old runner tokens and new machine tokens), lists the
  repositories the machine may build, and slides its 90-day lifetime on use.

Mixing families is refused, because a credential that can both claim work
and act as a member is the escalation the split exists to prevent. Code that
used to branch on `kind = runner` or `kind = user` asks which family the
credential belongs to instead; an old `swr_` row is machine family and an old
team-minted `swu_` row is member family, so nothing is re-issued.

## Claim tokens and what derives from them (landed)

Every claim returns a claim token bound to the team, run, claim and its
generation. Cache grants, data-plane downloads and output uploads derive from
a claim and are never mintable from a bearer alone. There are four kinds:

- **`plan` and `work`**: the launcher's claims on a controller-dispatched
  run. Each answers only the routes written for its kind and expires with its
  Job's deadline.
- **`node`**: a runner's claim on one node, returned as `claim_token` by
  `POST /api/v1/nodes/claim` and `POST /api/v1/runs/{id}/nodes/{nodeID}/claim`.
  It authenticates as the runner that made the claim, confined to the claim:
  it reaches only the claim's run, writes only to its own node, never acts
  as the trigger claimant, and every request carries the claim's own fence
  whatever headers it sent. It drops `admin`, keeping only `nodes.claim`,
  `runs.state` and `logs.write` of what the runner holds. It stops when the
  claim ends (finish, lapse, a later claim of the node) or when the runner's
  own credential is revoked.
- **`trigger`**: a runner's trigger claim, returned as `claim_token` by both
  trigger claim routes and stored in `trigger_claim_tokens` (schema v98),
  because the run row does not exist when the trigger is claimed. It answers
  the secret route and the source credential route alone and stops with the
  trigger claim.

The machine bearer stays in the runner process. A pooled node's supervisor
and the brokered child behind it run on the node's claim token. A Kubernetes
Job carries its node's claim token in `SPARKWING_AGENT_TOKEN`, so reading a
Job spec yields a credential bound to one node's claim. Every trigger child,
including a Kubernetes or warm runner's and `sparkwing cluster worker`'s,
reaches the controller through the runner's trigger broker, which forwards
the child's secret reads with the trigger's claim token and its other run
routes with the runner's bearer. On an open controller, which mints no claim
token for an anonymous claim, the runner holds no bearer either, and the
broker forwards the read with none, as the controller answers it directly. A trigger child that runs a node in its own
process does so under the trigger claim, which is why a trigger claim reads
secrets at all.

### Secret release is claim-bound (landed)

`GET /api/v1/secrets/{name}` has one node path. A claim token (work, runner
node or trigger) names its own run; `ReleaseClaimSecret` checks the claim,
checks that the run's plan declares the secret, applies the sensitive fence
and records a `secret_released` event, so every node read is declared and
audited.

A bearer reads by name only as the operator (`admin`) or a team's owner
(`team.admin`), a management read; a masked row still answers the owner
`403 write_only`. Any other bearer, a runner's token holding `secrets.read`
included, answers `403 claim_required`, even while it holds the run's claim.
`secrets.read` left the machine token family and the runner scope sets
(`sparkwing cluster runners add`, the dashboard, the GitHub Actions
exchange); a token minted with it keeps authenticating, and the scope
releases nothing. `ClaimedRunsFor` went with the bearer path, and a runner
older than the claim tokens above reads no secret from this controller.

### One source credential route (landed)

Three routes used to mint a `contents: read` App installation token for a
run's source, each with its own caching, scoping and validation:
`source-credential` for a Cloud Job's claim token, and `source-token` and
`git-credential` for a runner's bearer. The two bearer routes minted for
whatever repository a run named, so an editor who typed a private repository
of the installation into a run got a token for it.

`POST /api/v1/runs/{id}/source-credential` is now the only one. It answers
the claim token of a live plan, work, node or trigger claim on the run and
nothing else; a bearer, a runner's included, gets `403 claim_token_required`.
A runner's claim must still trace to a machine credential that authenticates.
The App token is minted by GitHub repository ID for:

- the repository the App's webhook recorded on the trigger
  (`github_repo_id`), which a retry and a child run of the same repository
  inherit; or
- a repository the run names by hand, only when one of the team's App
  trigger subscriptions (`github_app_triggers`) names it, under the ID that
  subscription recorded. Two IDs under one name, a rename GitHub has not
  reported yet, qualify neither.

The owner's extra repositories are added by their recorded IDs, as before.
The team's stored host credential, the documented feature for hosts other
than github.com and for repositories the App does not reach, is served from
the same route rather than a route of its own, because it answers the same
question (what does this claim fetch the run's source with) under the same
claim checks, and a second route would be a second policy to audit. It keeps
its opt-in (a metered runner, or a machine an owner opted in), its host-key
confirmation, its audit row and its per-minute limit, and it reaches only a
runner's node or trigger claim; a Cloud Job's claim never receives it.

One policy applies to every claim: at most ten asks a minute, an audit row in
`git_credential_releases` for every token issued and every credential
released, and, for a claim with a node of its own, at most three credentials
and none once its attempt has started, since the node's own process holds the
same token. The in-memory token reuse the bearer routes kept is gone. A
runner's trigger claim has no node, so it gets the per-minute limit alone; its
token never leaves the runner process. The trigger loop fetches with the
trigger's claim token, and a pooled node's supervisor and a Kubernetes Job
with the node's claim token. `source-token` and `git-credential`
are removed and answer 404, and a runner older than this release fetches no
source straight from a controller of it.

## Service credentials

Three static secrets authenticated services to each other. None was a
principal in the tokens table, and each granted more than its caller needed.
The cache operator token is gone (landed); the two logs-service secrets go
with the logs service.

| Secret | Direction | Used for | Grants |
|---|---|---|---|
| logs-delete token (`SPARKWING_LOGS_DELETE_TOKEN`) | controller to logs service | deleting a deleted team's logs | a controller token holding only `logs.delete` |
| logs service's controller bearer (`controller.tokenSecret` in the runner bundle) | logs service to controller | resolving a caller's credential before serving a log | the scopes of whatever runner token the operator mounted |

What landed for the cache operator token, which split by direction into two
credentials that each do one thing:

- **Controller to cache:** the controller signs a five-minute operator grant
  for each call of its own (the git-cache proxy, team purges, and the new
  `POST /api/v1/cache/store-ceiling/measure` and `/thaw` routes that forward
  to the cache's admin routes). It is the `swcg1.` format the cache already
  verified, with an operator flag and no team, run, claim or scope, signed with
  the grant key both already held; a payload that is both an operator grant
  and a team's grant is refused. The cache accepts grants and nothing else, so
  it needed no new secret. Asking the controller to verify a service token was
  the other option; it was not taken because it puts a controller round trip
  on every cache request and makes the cache's availability depend on the
  controller's.
- **Cache to controller:** the storage counter routes accept a `service` row
  (prefix `sws_`, hashed, listable and revocable like every token) holding the
  new operator-only `storage.count` scope, which no team token may carry. The
  operator mints it with
  `sparkwing cluster tokens create --type service --principal cache --scope storage.count`
  and mounts it on the cache as the file `controller-token`; the cache refuses
  to start with `--controller` and no such file. The constant-time compare
  against `SPARKWING_CACHE_TOKEN` in `counterCaller` is gone, and the routes
  still take a forwarded `logs.write` credential for its own team's logs.
- **People:** nobody holds a cache credential. Operators register mirrors and
  drive the store ceiling through the controller's `admin` routes, which
  forward under the operator grant. `--allow-unauthenticated` still opens a
  cache that has no grant key, for a bootstrap or laptop install.

The result is no standing shared secret between the two services, a counter
credential that can be revoked and listed, and no credential that both reads
every team's cache and moves every team's storage counters.

### What is left after the logs service is removed

Once the logs service and its credentials are gone, the service-to-service
surface is the cache in both directions, the launcher's `claims.launch`
token, the hosted checkout's `credits.grant` token and the billing callback
(`SPARKWING_BILLING_TOKEN`). The last three are already ordinary rows in the
tokens table with a single operator-only scope, which is the shape the cache
moves to. `logs.write` stays a claim-derived capability for the node's own
log upload.

## Enrollment bearers and the assisted-offer protocol (landed)

The assisted-offer protocol let a foreground coordinator started with
`sparkwing run --sw-fleet` hand nodes to enrolled helper executors. The
coordinator side shipped; the helper side never had a shipped client, so the
protocol ran only in tests. An enrolled credential was nevertheless forced
onto it server-side: `rejectEnrolledLegacyClaim` refused an enrolled token's
ordinary claim. The protocol carried its own credential path (enrollment bound
a token prefix to an executor record, and offers were fenced by that binding),
a second way for a bearer to reach node work alongside the claim path above.

It was removed whole as one breaking change. Gone are
`POST /api/v1/nodes/claim/prepare`, the executor-offer body of
`POST /api/v1/nodes/claim` (its fields are now refused as unknown),
`PUT /api/v1/agents/{name}` enrollment, the enrolled executor heartbeat at
`POST /api/v1/agents/{name}/heartbeat`, offer arbitration by priority,
`rejectEnrolledLegacyClaim`, the controller authority id, and the foreground
fleet coordinator. Schema v97 drops the `executors` and `node_claim_offers`
tables. On the CLI, the `fleet` command group, `run --sw-fleet` and
`cluster agents enroll` are gone, and a `fleet:` section in `config.yaml`
fails to parse. The ordinary claim protocol, `POST
/api/v1/nodes/claim` with a holder id, labels, headroom and capacity, is the
one claim protocol. Machines that were enrolled helpers run as ordinary
runners through `sparkwing cluster runners add`. A future multi-machine
foreground run would be built on claim tokens from the start.

## GitHub Actions runner credential

`POST /api/v1/runners/github/exchange` trades a GitHub Actions ID token for
a one-hour runner bearer (kind `runner`, principal `github:<id>:<slug>`)
confined by `githubRunnerFence`. In this model it becomes a claim-class
credential: the exchange claims the run's work and returns claim tokens,
and the fence becomes the claim binding. Until then it stays a runner-kind
row outside the team token families.

## Decided

The owner decided these on the recommendations below, and the work lands
with this design:

- **Assisted-offer protocol: cut whole (landed).** The prepare route, the
  offer and executor tables, the fleet authority, enrollment bearers and the
  enrollment route are gone; the ordinary claim protocol is the one protocol
  and lost its "legacy" name.
- **Cache service credential: one service-token kind (landed).** The cache
  calls the controller with a hashed, scoped, revocable `storage.count`
  service token; the cache accepts cache grants only, the controller's own
  calls included, and the static cache operator token is gone under both of
  its names. People act through the operator identity.
- **One installation-token route (landed).** `POST /api/v1/runs/{id}/source-credential`,
  claim-bound, is the only route that mints a GitHub App installation token
  and the one that releases a team's stored host credential; a repository a
  run names by hand qualifies only when the team has subscribed it.
  `source-token` and `git-credential` are gone.
- **`source-token` removed now (landed).** The compatibility window for
  runners and controllers older than v0.66 ends on 2026-10-15, and the
  release carrying the removal ships after it.
- **Bearer secret release: removed (landed).** A node reads secrets with its
  claim token, under the plan declaration and with an audit event; a team
  token keeps no `secrets.read`, and management reads by name stay with the
  operator and team admins.
- **Claim tokens for every pod (landed).** Every runner claim returns a
  claim token, the node's process uses it in place of the runner's bearer,
  and a Kubernetes Job carries its node's claim token, so reading a Job spec
  yields a credential bound to one node's claim.

## Decisions still open

Each item is a choice the work above cannot make on its own, with the
recommendation an agent would follow if no answer came.

- **D1. Identity providers on unlicensed controllers.** Google and GitHub
  sign-in need the multi-team license, so a self-hoster without one signs
  in only with an operator token and cannot give a colleague read-only
  dashboard access. Options: (a) keep it license-gated; (b) offer Google and
  GitHub sign-in on a single-team controller, with accounts joining the one
  team by invitation from the operator; (c) let the sign-in form accept any
  member-family team token, opening a session with that token's scopes.
  Recommendation: (b). It gives every self-hoster the human-session path
  the model promises and keeps tokens for machines; (c) is the small
  fallback if (b) is a licensing question.
- **D2. Operator-token sign-in scope.** The form accepts only user tokens
  holding `admin`. Widening it later is easy; narrowing it breaks people.
  Recommendation: keep `admin`-only.
- **D3. Team token families.** Mixing member and machine scopes in one
  token is refused. Recommendation: keep refusing; a CI job that needs both
  holds two tokens.
- **D4. Machine-family minting role.** Editors mint machine tokens and team
  admins revoke any of them, as runner tokens worked. Recommendation: keep.
- **D5. GitHub Actions exchange as a claim-class credential.**
  Recommendation: do it next, since it reuses runner claim tokens.
