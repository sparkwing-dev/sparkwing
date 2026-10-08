# Credential model

Status: proposal. This page is a design for contributors. The sections
marked "landed" describe released-to-main behavior; the rest describe work
that has not started, and the decisions at the end are either decided or still open.

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

## Team tokens

One kind, `team`, replaces team CLI tokens and team runner tokens. A token
draws its scopes from exactly one family at mint time:

- **Member family:** a subset of the scopes the minter's role allows a CLI
  token (everything the role holds except `team.admin`). The principal is the
  minter, the lifetime is 90 days fixed, ten live per member, and the token
  dies with the minter's membership or a demotion that drops its scopes.
- **Machine family:** a subset of `nodes.claim`, `triggers.claim`,
  `runs.state`, `secrets.read` and `logs.write` that includes a claim scope.
  The minter must be an editor; the token is named (`agent:<name>`, unique in
  the team across old runner tokens and new machine tokens), lists the
  repositories the machine may build, and slides its 90-day lifetime on use.

Mixing families is refused, because a credential that can both claim work
and act as a member is the escalation the split exists to prevent. Code that
used to branch on `kind = runner` or `kind = user` asks which family the
credential belongs to instead; an old `swr_` row is machine family and an old
team-minted `swu_` row is member family, so nothing is re-issued.

## Claim tokens and what derives from them

The controller mints a claim token when it hands a node to a launcher Job
and binds it to the team, run, node and claim generation. Cache grants,
data-plane downloads and output uploads already derive from a claim and are
never mintable from a bearer alone.

Two paths still let a machine bearer act for a node it holds without a
claim token: self-hosted runners claim with their `swr_`/machine bearer and
the controller mints them no claim token, and the bearer secret release
below. Closing both is one change: every claim, launcher or runner, returns
a claim token, and the node's process uses that token, never the machine
bearer, for everything run-scoped.

### Secret release becomes claim-bound

Today `GET /api/v1/secrets/{name}` has two node paths:

- with a claim token, `ReleaseClaimSecret` checks the work claim, checks
  that the run's plan declares the secret, applies the sensitive fence and
  records a `secret_released` event;
- with a bearer holding `secrets.read`, the controller finds the run the
  principal holds a claim in (`ClaimedRunFor`, or the only one it holds) and
  returns any secret of that pipeline, with no declaration check and no
  event.

Plan:

1. Runner claims (`POST /api/v1/nodes/claim` and the trigger claim) return a
   claim token next to the claim, minted by the same store call the launcher
   path uses. The runner hands it to the node process the way it hands the
   launcher's token today.
2. The node's secret client sends the claim token.
3. The bearer branch of the secret route goes: a machine
   bearer reading a secret answers `403 claim_required`, and `secrets.read`
   leaves the machine family. Operator and team-admin reads by name stay;
   they are management reads, audited as such.
4. Every node read then passes the declaration check and leaves an event,
   which is what the audit log promises.

Steps 1 to 3 land together: a runner older than this change cannot read
secrets from an upgraded controller, which the migration note says.

## Service credentials

Three static secrets authenticate services to each other. None is a
principal in the tokens table, and each grants more than its caller needs.

| Secret | Direction | Used for | Grants |
|---|---|---|---|
| logs-delete token (`SPARKWING_LOGS_DELETE_TOKEN`) | controller to logs service | deleting a deleted team's logs | a controller token holding only `logs.delete` |
| cache operator token (`cache-token`; the controller's `SPARKWING_CACHE_TOKEN`) | controller to cache, and cache to controller | the controller's git-cache proxy and team purge on the cache; the cache's storage reserve, commit and release calls on the controller | every cache route, every team's tree, and the storage counters for every team |
| logs service's controller bearer (`controller.tokenSecret` in the runner bundle) | logs service to controller | resolving a caller's credential before serving a log | the scopes of whatever runner token the operator mounted |

Plan:

- **Logs-delete token and the logs service's bearer** go with the logs
  service. With logs stored as objects the controller writes and deletes
  through its own data plane, team deletion removes a prefix the controller
  already holds credentials for, and nothing needs to call back.
- **Cache operator token** splits by direction into two service
  credentials that each do one thing:
  - controller to cache: the controller calls the cache with a short-lived
    signed service grant (the `swcg1.` format the cache already verifies,
    with an operator scope and no team), minted from the grant key the two
    already share. The static bearer stops being accepted on cache routes.
  - cache to controller: the storage counter routes accept a service token
    of a new `service` row with exactly one scope, `storage.count`, minted by
    the operator and mounted on the cache like any other token. The
    constant-time compare against `SPARKWING_CACHE_TOKEN` in
    `counterCaller` goes.
  The result is one fewer shared secret, a counter credential that can be
  revoked and listed, and no credential that both reads every team's cache
  and moves every team's storage counters.

### What is left after the logs service is removed

Once the logs service and its credentials are gone, the service-to-service
surface is the cache in both directions, the launcher's `claims.launch`
token, the hosted checkout's `credits.grant` token and the billing callback
(`SPARKWING_BILLING_TOKEN`). The last three are already ordinary rows in the
tokens table with a single operator-only scope, which is the shape the cache
moves to. `logs.write` stays a claim-derived capability for the node's own
log upload.

## Enrollment bearers and the assisted-offer protocol

The assisted-offer protocol (`POST /api/v1/nodes/claim/prepare`, the
executor offer table, the fleet authority and `PUT /api/v1/agents/{name}`
enrollment) lets a foreground coordinator started with `sparkwing run
--sw-fleet` hand nodes to enrolled helper executors. The coordinator side
ships; the helper side has no shipped client, so the protocol runs only in
tests. An enrolled credential is nevertheless forced onto it server-side:
`rejectEnrolledLegacyClaim` refuses an enrolled token's ordinary claim.

The protocol carries its own credential path (enrollment binds a token
prefix to an executor record, and offers are fenced by that binding), which
is a second way for a bearer to reach node work alongside the claim path
above. The credential model has no place for it.

Plan (decided): remove the prepare route, the offer and
executor tables, the fleet authority, the enrollment route,
`rejectEnrolledLegacyClaim`, the `sparkwing fleet` command group and
`--sw-fleet`, with their docs, as one breaking change with a migration
note. A future multi-machine foreground run would be built on claim tokens
from the start.

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

- **Assisted-offer protocol: cut whole.** The prepare route, the offer and
  executor tables, the fleet authority, enrollment bearers and the
  enrollment route go; the ordinary claim protocol is the one protocol and
  loses its "legacy" name.
- **Cache service credential: one service-token kind.** The cache calls the
  controller with a hashed, scoped, revocable service token; the cache
  accepts cache grants and that token only, and the static cache operator
  token goes under both of its names. People act through the operator
  identity.
- **One installation-token route.** `POST /api/v1/runs/{id}/source-credential`,
  claim-bound, is the only route that mints a GitHub App installation token;
  a repository a run names by hand qualifies only when the team has
  subscribed it. `source-token` and `git-credential` go.
- **Bearer secret release: removed.** A node reads secrets with its claim
  token, under the plan declaration and with an audit event; a team token
  keeps no `secrets.read`, and management reads by name stay with the
  operator and team admins.
- **Claim tokens for every pod.** Kubernetes Jobs carry the node's claim
  token, not the runner's bearer, so reading a Job spec yields a credential
  bound to one node's claim.

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
