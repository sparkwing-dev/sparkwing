# Authentication + authorization

A connection chooses the controller. A credential identifies the caller and its
exact authority. A claim confines execution to the work the controller awarded.

The controller enforces the shared capability policy through store-backed
authentication and transactional authorization. Native runner and deployment
verification remain separate from client transport tests. See
[Credential management](credentials.md) for the CLI surface.

## Controller authorization

The controller's shared policy routes use the store's authentication snapshot
and binding checker by default. Each request reads current capability ancestry
and account membership. Browser writes use the session-bound CSRF verifier by default. The header
and cookie must match the token derived from the persisted encrypted key, and
the request must come from the same origin. A forged or cross-session token
returns `forbidden`. Session key failures return `unavailable`. Configured operator
accounts use the default console-session adapter. Verified local peers use
`WithPeerIdentity`, whose callback returns only the transport-verified uid or SID
as a local subject. Request headers and loopback addresses supply neither
identity. The local store must implement peer authentication snapshots and
transactional authority checks before those presentations can execute work.

Embedded local controllers use `Server.HTTP()` for the API and dashboard
handlers in `wingd.LocalHTTP`. The API reads the verified socket identity from
Wingd's connection context. Explicit credentials keep precedence. Browser
requests supply their own authentication. These handlers expose the shared
policy routes and exclude the legacy route fallback.

`GET /api/v1/queue` uses the shared capability policy. A member or verified
local peer needs `runs.read` for its active team. Queue groups and advertised
runner headroom belong to that team. Machine roots and work claims cannot read
the team queue. The former `/api/v1/queue/state` path is removed.

Run intake, plan acceptance, job status reads, job transitions, finish,
cancellation, retry and run/job renewal use the shared capability policy. The
controller resolves the resource's team before authorization and derives
actors and fences from the credential. Unknown or ended credentials get
`unauthenticated`. A live
credential without the grant or binding gets `forbidden`.

The legacy `/api/v1/triggers` family and `/api/v1/runs/{run}/nodes`
family are removed. So is raw `POST /api/v1/runs/{run}/events`. Intake uses
`POST /api/v1/runs`, plan insertion uses `/runs/{run}/plan`, and job state
uses `/runs/{run}/jobs/{job}/transition`. Run and job renewal use their
`heartbeat` routes. Outputs use the authorized `/api/v1/data` upload,
commit and record operations. Retired claim tokens and caller-supplied
execution fences have no execution route.

An absent route returns the shared `not_found` error before credential
lookup. A method with no handler on a registered path returns
`method_not_allowed` and `Allow`, also before credential lookup. Neither
response renews browser sessions.

`GET /api/v1/runs/{run}/jobs` returns a status array from one committed
snapshot. It accepts member `runs.read` or the live run owner's `trigger.host`.
`GET /api/v1/runs/{run}/jobs/{job}` also accepts that job's live `data.read`
authority. An executor cannot list siblings or read another job through this
route. Both responses use the shared safe job status fields. They omit job
declarations, environment, claim identities and execution settings. The old
`GET /nodes` and `GET /nodes/{nodeID}` paths are removed.

`POST /api/v1/runs/{run}/jobs/{job}/cancel` accepts `runs.control` from a
member or `trigger.host` from the live owner of that run. The state transaction
requests cancellation of held jobs and preserves the holder's result rights.
The first cancellation reason is recorded separately from the holder's
terminal result and reaches the holder on renewal.

`GET /api/v1/runs/{run}/jobs/{job}/failure?generation=N` requires `data.read`
and a declared recovery read edge to that source generation. It returns the
persisted masked job failure, including its stage, or `null` when none was
recorded. Status text is never converted into a synthetic failure. Missing,
duplicate or nonpositive generation selectors return `bad_request` after
credential authentication.

`GET /api/v1/runs/{run}/snapshot` requires a live trigger owner with
`trigger.host` bound to that run. It returns one committed scheduling view,
including generated jobs and empty generator groups. Ordinary members and
work or plan claims cannot read this private owner view. Cancellation and
owner lease loss refuse new reads. A cancellation committed after
authentication appears in the returned snapshot. Accepted expansion bodies,
hashes and source generations travel in the same committed read. Replayed
expansion responses retain each generated job's current state and attempt.

Pipeline sizing uses `/api/v1/pipelines/{name}/sizing`. Members need
`runs.read`. Executable claims need `source.read` and the bound run's exact
sealed repository and pipeline. Bare runner and Actions roots cannot read
sizing. Bootstrap credentials can acquire source but cannot read sizing.
The `job` query selects one job. Omitting it selects the pipeline rollup.
An authorized missing measurement returns `not_found`.

Pins, observations, contention and waits use the `pin`, `observations`,
`contention` and `waits` suffixes on that path. They require `node.report`
and the same pipeline binding. Cancellation permits reporting while the
claim remains live. Member and admin grants cannot bypass this binding.
Writes require an authority-bearing sizing transaction adapter. Without one,
the controller returns `policy_missing` before storing a measurement.
The old `/profile` paths are removed.

Token revocation and member updates use the same policy router. A team token is
revoked at `DELETE /api/v1/tokens/{token}`. Deployment service/operator roots use
`DELETE /api/v1/operator/tokens/{token}` with `operator.tokens` or an operator
console session. Team routes derive their active team from the committed
credential. Header and query hints cannot select another team.

`GET /api/v1/team/members` returns the active team's member directory and
requires `runs.read`. An empty team returns `[]`.

`GET /api/v1/credits` and `GET /api/v1/credits/history` require a live member
or verified local peer with `runs.read`. The committed credential selects the
team. Machine roots, executable claims and bootstrap credentials cannot read
the team ledger. Both routes use the common error envelope and authenticate
before checking whether credit billing is enabled. An authorized caller gets
`not_found` when billing is unavailable. History retains the `limit` query
and returns empty grants and charges as arrays.

`PATCH /api/v1/team/members/{account}` accepts a role of `owner`, `editor` or
`reader`. `DELETE` on that path removes the membership. The store rechecks the
authenticated authority and updates the membership generation in the same
transaction. A missing member returns `not_found`. A last-owner change returns
`conflict`. Browser mutations require CSRF proof. Revocation stops descendants'
new controller operations, including those parented to a session. Previously
issued external credentials and signed URLs keep their documented lifetimes.

`POST /api/v1/runs` accepts a `request` and `idempotency_key`. The request names
its team. A bound job creating a child must name its own run and job as the
parent. Intake returns the committed run number and first seal. A here bearer
is returned once. Replaying intake returns the committed response without
another bearer.

`POST /api/v1/runs/{run}/plan` accepts `declarations`, `resolved` and `plan`.
The store validates the second seal against the persisted first seal, including
a retry's preserved source seal. Raw plan snapshots are no longer accepted.
Plan, job-transition and finish requests from ended claims require the exact
recorded body, actor and fence to replay. Mutations recheck authority inside
the store transaction. A lost fence gets `lock_held`.

Job transitions use `/api/v1/runs/{run}/jobs/{job}/transition`. Finish ends the
run and its owner unit together. Published local runs use a separate transition
edge and receive no executable owner. Cancellation carries a reason. Retry
carries an idempotency key and leaves source seals unchanged. Run/job heartbeat
responses carry the committed lease expiry, cancellation and watched jobs.
Every request uses JSON. Bodies cannot replace authority, holders or fences.

Job and step annotations, summaries and step transitions use the same policy
under `/api/v1/runs/{run}/jobs/{job}`. A job claim reports only its own job.
A trigger owner may report its run's jobs, subject to the transaction's holder
check. A local publisher uses its separate run-bound publication authority.
Cancellation permits reporting while the claim remains live. An ended claim
cannot write these reports.

Step reports carry a positive `source_attempt`, `step_id`, state and an
optional masked inner error. The controller derives the actor and claim fence
from authorization. The store owns stale-attempt refusal and committed timestamps. Annotations and summaries accept at most
128 KiB of masked text. The old `/nodes/` reporting paths are removed.

## Data read authority

Download signing and batched presence use the shared capability policy.
A live claim needs its data grant and sealed run/repository/ref binding.
A repository reader needs existing repository authority. Cache grants,
ordinary member tokens and machine roots cannot substitute for either.
Each operation resolves current authority again and checks the read set
before reading metadata. Presence queries omit unreadable digests. Download
signing applies the team's daily byte cap and returns a 60-second URL for
one immutable object. The returned storage headers contain no controller bearer.

## Credential discovery

`GET /api/v1/auth/whoami` uses the shared policy verifier. It returns the
committed subject ID as `principal`, the subject kind as `kind`, and grants
as `scopes`. `team` is present when the credential has an active team.
`token_prefix` names a capability. Sessions and verified local peers omit it.
The response excludes session IDs, ancestry, claim fences and billing facts.
An empty grant set returns `"scopes":[]`.

`GET /api/v1/services` and `GET /api/v1/data/capabilities` use the same verifier
and require an active team. They announce configured services and data transfer
support. Availability is checked after authorization. An authorized caller gets
`not_found` when nothing is configured. Missing or dead credentials receive
`unauthenticated` regardless of service configuration. Bootstrap and eval
credentials cannot use these discovery routes or `whoami`.

All three routes return `Cache-Control: no-store`. Unsupported methods return
`method_not_allowed` with `Allow: GET`. Identity lookup requires an explicit
credential or a verified local peer. An address, query parameter or identity
header cannot supply authority.

The OIDC discovery document at `/.well-known/openid-configuration` and public
key set at `/.well-known/jwks.json` also use the policy router. Providers can
read them without a Sparkwing credential. Both retain their one-hour public
cache header. A controller without an issuer returns `not_found`, and
unsupported methods return `method_not_allowed` with `Allow: GET`.

## Generator expansion authority

`POST /api/v1/runs/{run}/jobs/{job}/expansion` uses the shared capability
policy. It requires `expansion.submit`, the bound source job and generation,
and a live owner or eval lease. Eval also requires a live controller ownership
lease. The ended source claim cannot submit an expansion.

The body supplies `source_generation` and exactly one of `body` or `failure`.
A successful empty expansion uses `"body":{"jobs":[]}`. A failure uses the
shared inner error object with `code` and `message`. The controller derives
the actor, holder and lease generation from authorization. Success and failure
reach the same store transaction, which rechecks authority before acceptance
and owns replay. Unsupported methods return a JSON `method_not_allowed` error
with `Allow: POST`.

## Capability inventory and rotation

`GET /api/v1/tokens` lists credentials within the authenticated team. Members
need `runs.read` and see their own credentials. A team admin can see the team's
credentials. `GET /api/v1/tokens/{token}` checks ownership of that prefix.
Deployment service and operator credentials use the corresponding collection
and detail routes under `/api/v1/operator/tokens`, requiring `operator.tokens`
or an operator console session.

The collection returns `{tokens:[...]}`. Each row carries `prefix`, `team`,
`subject`, `audience`, `grants`, `created_by`, `issued_at`, `git_credentials`,
`metered`, and optional `expires_at`, `last_used_at` and `revoked_at`. Times are RFC3339
timestamps. The response omits bearer secrets, digests, bindings and ancestry.
`kind` filters by `account`, `machine`, `service` or `operator`.
`include_revoked=1` includes revoked rows. Duplicate or invalid filters fail.

`POST /api/v1/tokens/{token}/rotate` replaces an owned credential. Deployment
credentials use `/api/v1/operator/tokens/{token}/rotate`. The body accepts
`grace_secs` and optional `expires_at`. Grace defaults to 24 hours and accepts
zero through seven days. An omitted expiry keeps the original expiry. An
explicit expiry must pass the original parent's ceilings or the current root
origin policy. The transaction preserves the original
authority and ends the old credential after the grace window. Explicit
revocation ends that window at once.

The rotation response returns `capability` once, `new` public metadata,
`old_prefix` and the RFC3339 `old_revoked_at`. Do not replay a rotation after
losing its response. Browser mutations require session-bound CSRF proof.
All inventory and rotation responses use `Cache-Control: no-store` and the
common error envelope. Embedded controllers provide `WithCapabilityManager`
for authority-bearing inventory and rotation transactions.

## Browser session lifecycle

`GET /api/v1/auth/session` accepts a live account session from one session
cookie or one `Authorization: Session <id>` header. An Authorization header
takes precedence over cookies. Duplicate headers, duplicate session cookies,
unknown or ended sessions and HEAD are refused. The response includes the
current role, narrowed scopes, session-bound CSRF token and renewed expiry.
Renewal cannot extend the absolute session lifetime. A disabled account-session
license or failed proof cannot renew the session.

`POST /api/v1/auth/logout` takes no body. It ends only the presented session,
requires the session's CSRF header and cookie plus same-origin proof, and
clears the browser cookies on success. A body or query parameter cannot select
another session. Both endpoints return `Cache-Control: no-store` and the
canonical error envelope. Session-key and deletion failures return `unavailable`.

Other policy routes accept the same Session header or one session cookie and
check the configured absolute lifetime without renewing. A malformed header
cannot fall back to a cookie. Dashboard requests reach that proof boundary
before session renewal, so a failed CSRF check cannot extend a live session.

The browser form posts to `POST /logout`. It requires a live session and a
`csrf_token` field matching both the CSRF cookie and the stored session, plus
same-origin proof. It deletes only that session, clears its browser cookies and
redirects to `/login`. It never renews before checking proof. Refusals use the
canonical error envelope. An unavailable session returns `401`; a key or
deletion failure returns `503` and leaves the cookies in place.

A session renewal extends expiry by seven days, but never past 30 days from
creation. Ordinary policy reads and writes check that lifetime without
renewing. The renewal endpoint refuses an over-age or future-created session
and removes its row. An embedder changes the cap with
`controller.Server.WithSessionMaxLifetime`.

<!-- design:begin -->

## Token format

Controller capabilities start with `swk_`. Node-host capabilities start with
`swn_`. Each contains 256 random bits. The prefix selects the audience. It does
not identify a human, runner or administrator. The issuer records the subject,
team, exact grants, binding and lifetime.

The lookup key is SHA-256 of the complete reference token. Metadata carries a
public `prefix` for selection and audit. Creation and rotation return the raw
bearer once. Metadata lists contain no bearer.

Credentials travel in memory, a private file read by the process, or the private
protocol pipe. Runner and controller processes take a credentials directory.
A job receives a single-use bootstrap file. Credentials never travel in
process environment variables. The node-host hello sends its process-lifetime
credential over the protocol pipe.

A connection profile belongs in the user's private machine file. A project file
cannot choose a controller connection or capture its credential.

## Scopes

Grants are exact names. There is no wildcard grant and no `admin` implication.
Each route declares its accepted credential classes, grants, binding and
liveness checks. A deployment credential cannot stand in for a team member or
an operator-console session.

The shared credential contract defines these recognized grant names:

<!-- credential-grants:begin -->
| Grant |
| --- |
| `runs.read` |
| `runs.write` |
| `runs.control` |
| `runs.state` |
| `nodes.claim` |
| `triggers.claim` |
| `claims.launch` |
| `logs.read` |
| `secrets.read` |
| `approvals.write` |
| `team.admin` |
| `secrets.release` |
| `source.read` |
| `data.read` |
| `data.write` |
| `oidc.mint` |
| `node.report` |
| `node.result` |
| `log.tail` |
| `refs.read` |
| `children.start` |
| `trigger.host` |
| `claim.exchange` |
| `expansion.submit` |
| `operator.limits` |
| `operator.object_store` |
| `operator.tokens` |
| `operator.console` |
<!-- credential-grants:end -->

A runner enrollment requests `nodes.claim`, `triggers.claim` and `runs.state`.
The issuer validates provisioning authority. Granted repository access, reserved
pools and metering are server facts. A runner's free labels and a proposed
repository name grant no authority. `triggers.claim` remains a permission name.
Its public work queue is `/api/v1/runners/claim`.

Operational deployment credentials carry `operator.limits`,
`operator.object_store` or `operator.tokens` for their selected operations.
Trust changes, grants, freezes, refunds and account deletion require an
operator-console session. Those powers do not follow from a deployment token.

## Claim ownership

The controller derives the run, job, team, holder, generation, attempt,
repository, ref, reserved pools and billing facts from the awarded row. Caller
headers, labels and request fields cannot replace those facts. A mutation
rechecks the fence in the same transaction as its write. The store clock judges
whether the lease is live.

A live credential with insufficient authority receives `403`. A dead credential
receives `401`. Clients branch on `error.code` and use `details.reason` for the
specific refusal. Every boundary uses the same envelope:

```json
{"error":{"code":"forbidden","message":"The credential cannot perform this operation.","details":{"reason":"grant_missing"}}}
```

A root that can claim work does not gain arbitrary execution writes. Awarded
credentials carry the operation grants and exact binding needed by their unit.
Run-host ownership and job ownership remain separate. A member's management
rights do not bypass a competing execution holder.

### Claim tokens

The issuer has three operations:

- **Delegate** narrows its parent's grants, binding, repositories, data scope and
  lifetime. It cannot widen authority.
- **Award** creates the fixed credential for work selected under the claim
  transaction. Only launcher authority may select work across teams.
- **Exchange** consumes a bootstrap once and recovers its original unit's
  credential. A concurrent or repeated exchange cannot issue a second one.

Operational units are trigger, plan, eval and job. A here award retains trigger
ownership and follows the requesting capability or session. An ordinary claim
follows its claimant root. A delegated job-process credential follows its
parent and dies when its process exits. Revocation checks capability and session
ancestry, including the nearest here award.

Same-uid pipeline code may still reach a runner's credential. A private pipe or
filtered environment does not isolate hostile processes sharing an OS account.
Cloud jobs use root and privileged Docker on team-dedicated nodes. Those are
accepted deployment boundaries, separate from the claim's API authority.

A bootstrap has only source-read and exchange grants. Source initialization can
use it before exchange. Exchange revokes it and retains its unit, binding and
billing provenance. The job receives no runner root credential.

Sensitive reads require a live claim. A terminal job has only its bounded window
to finish its own already-started log upload. Expansion submission uses its
live expansion authority. Neither exception reopens general source, secret or
data access.

## Secret ownership

Management access and execution release are separate decisions. An authorized
management read can return a value. Lists contain metadata only. Execution
release requires the bound claim and the accepted plan's secret declarations.
Secret values are encrypted in persistence and masked before rendering.

A secret restricted with `--only-on` names a granted reserved pool. The
controller checks that restriction at plan acceptance and again at release.
Here execution cannot use a pool-restricted secret. A self-asserted label that
spells the pool name grants nothing.

Anyone who can change and run a pipeline can make that pipeline use the secrets
it is authorized to read. Assign that access as carefully as access to the
secrets themselves. Registered secret values are refused in job, step, group
and approval IDs because those identifiers cannot be masked after storage.

## Metered runners

Metering follows server-recorded provenance. Runner labels, request fields and
audience prefixes cannot select billing. Here work inherits the requester and
keeps trigger-to-host billing. A normal award copies the registered claimant's
billing facts. Operational trigger work maps to host billing. Job work maps to
job billing. Plan and eval are separate billable units.

Reservation, charging, claim renewal and settlement happen in store transactions.
An unstarted claim refunds its reservation. A job that starts consumes the
minimum. A host has a separate minimum. Plan and eval have zero minimum and
charge their actual compute. A validated plan-cache hit incurs no planning
charge. Customer-owned runner wait intervals are excluded.

Billing reconciliation must distinguish cancellation, lease loss and execution
start. Repeating a finish or payment event cannot create another charge.
The CLI's transport tests do not establish these ledger properties.

## Credits

Credit availability and billing policy are team-scoped server facts. Claiming
work cannot bypass an exhausted balance by changing labels or supplying a
billing flag. An insufficient-credit refusal keeps its canonical structured
error. Pricing and compute measurements come from the controller's observations.

## Buying credits

The controller verifies Stripe signatures at `POST /webhooks/stripe`. Checkout
payments, failures and expiry carry the committed provider session identity.
Card setup and card payment outcomes, individual refund updates, early fraud
warnings and opened or closed disputes carry their provider identities and
verified status. The verifier reads card details from Stripe when needed and
checks their identity, customer and test/live mode. Metadata grants no team,
credit, attempt or billing authority.

Settlement derives the team and ledger effect from committed billing records
and deduplicates provider events in its transaction. Checkout settlement is
implemented. Settlement of card, refund, warning and dispute events still needs
the store integration. A provider retry cannot safely be treated as a new
credit grant.

Test and live Stripe keys select configuration for the same implementation.
Checkout, payment history, card operations, payment holds and reversals retain
their account authorization. A signed payment event authorizes only its declared
payment operation. It grants no general team or operator power.

## Removed credential routes

GitHub Actions enrollment uses `GET` and `POST /api/v1/runners/github` and
`DELETE /api/v1/runners/github/{repository}`. The three
`/api/v1/team/github-runners` registrations are removed. The dashboard uses
the final paths. `POST /api/v1/tokens/{prefix}/metered` is removed. Runner
metering uses the console-only
`POST /api/v1/operator/runners/{runner}/metered`, selecting an enrolled root's
capability prefix.

The eight `credits.grant` service registrations for cards, card payments,
refunds, freezes, warnings, unit reports and payment lookup are removed.
Automated outcomes enter through the signed provider callback. No service
bearer can submit a team or credit grant through the removed routes.

## Deployment bootstrap

The mounted `bootstrap-admin-token` must be a `swk_` controller capability.
Startup supplies it to the once-only bootstrap transaction before opening the
listener. That transaction stores only its digest and issues operational
limits, object-store and token administration grants. It must refuse to issue
another administrator or revive a revoked seed. It grants no operator-console
or claim authority.

The token-table bootstrap path is removed. Startup refuses a configured seed
when the store has no seeded bootstrap transaction. That transaction remains a
store integration requirement. An empty file input provisions no credential.
Startup enables authentication even before credentials exist. An empty or
unreadable token table cannot open private routes. `--require-auth` additionally
requires a live deployment administrator capability and refuses startup when
that capability read is unavailable. Local access uses verified peer identity.

## Operator console

Operator-console sessions own trust changes, credit grants, freezes, refunds
and account deletion. Deployment tokens manage operational limits, object-store
settings and deployment service credentials through their separate routes.
They cannot exchange themselves for console powers.

### Break-glass

When console access is unavailable, the deployment operator can use
`sparkwing-controller admin` inside the controller pod. This is an in-pod
administrative path with its own authority and audit. A remote deployment
credential does not open it.

## Retained storage

The controller owns `/data` authorization, records and signed transfers. Reads
check the caller's permitted records and digests. An uploaded digest is not a
team-wide permission to attach it to an unrelated manifest. Every reference
checks integrity, existence and authority.

Signed downloads expire after 60 seconds and verify the digest at EOF.
Revocation does not recall a URL already issued. Retention uses record
reachability and the declared collection window. Missing or corrupt bytes fail
as an error or cache miss. A separate cache service or cache credential is not
part of this model.

## Runner classes

Runner identity, reserved pools, metering and capacity remain separate fields.
The controller returns granted class and location alongside free labels,
repository fences, active jobs and measured headroom. Missing headroom means
there is no observation. It is not a zero-capacity sample.

A here runner is reserved for its run. Pool access comes from granted
reservations and verified repository authority. The first eligible matching
claim wins under the same matcher used for queued and named work.

## Compute guards

Operator values are ceilings. A pipeline value above a ceiling is refused with
both values and the setting's source. It is never silently clamped.
Execution, queue wait and approval wait are separate bounds. Local runs gain
no execution deadline. A queued job with a bounded owner also ends at the
owner's deadline. Approval zero remains indefinite wherever the owner can wait.

Deployment-wide counters and singleton maintenance leases live in the store.
One supported controller replica does not justify process-local financial or
authorization state.

## Teams and sign-in

A signed-in account's active team and membership determine its authority.
Requests cannot select another team merely by naming it. A demotion or removal
changes the membership generation checked by subsequent authorization.
Team-admin authority remains confined to that team.

### Team tokens

Member credentials act for an account in its team. Runner credentials act for a
registered machine. Issuance checks the account or registration and cannot
exceed its authority. Principal text is a display label. It cannot establish
membership, machine identity, reserved pools or metering.

### Linked sign-ins

Google and GitHub identities can be linked from an authenticated account.
A matching email address alone must not merge two accounts. Linking and
unlinking identities require the account's authorized session. A provider's
verified identity and the application's team membership remain separate facts.

Linking starts at `POST /auth/{provider}/link` with one `csrf_token` form field,
the matching CSRF and session cookies, and same-origin proof. It requires the
account's direct provider session from the last ten minutes. An account-token
session cannot add sign-in methods. The flow binds its account, session,
provider and PKCE verifier, expires after ten minutes, and can complete once.
The existing limit is ten link attempts per account per minute.

`GET /auth/{provider}/link/complete` uses that same session and flow cookie.
It consumes the flow before provider access and attaches only the verified
provider identity. Neither endpoint renews the session or accepts an
Authorization header. Failures return the canonical error envelope. Embedded
controllers supply `WithIdentityLinkWriter` for committing session and nonce
checks. A missing writer returns `policy_missing`.

A nonlocal dashboard requires a live session for its private pages, even when
legacy bearer authentication is disabled. Its login page remains available to
signed-out browsers. `Dashboard.Local` selects local pages without browser login.

### Sign-up gate

The deployment's sign-up settings decide whether a new account proceeds or
waits for admission. The operator controls those settings. A team role or a
supplied email does not grant an exception. Authentication and tenant isolation
still apply after admission.

## Unauthenticated endpoints

Public discovery, sign-in, provider callbacks, webhooks and signed data URLs use
their declared proof requirements. A route being reachable without a bearer is
not an authorization bypass. A signed URL, verified webhook or provider proof
is valid only for its own operation. Every registered route declares its proof.

## Dashboard authorization

Browser sessions use account and team authority. State-changing browser requests
also require the session's CSRF protection. A session cookie is not a deployment
service credential. Operator-console powers require the console's session
checks. Membership and session ancestry remain live authorization inputs.

### Google and GitHub sign-in

Provider sign-in verifies the callback state, proof-key flow and provider token
or identity response. It checks the configured redirect origin and the provider's
identity and verification claims. Linking a provider does not grant repository
trust. GitHub App intake separately verifies its webhook and clones its exact
SHA. A subsequent branch-head lookup cannot replace that proof.

## Signing in

Sign in on the controller's configured dashboard origin, select the team you
belong to, and create the credential appropriate for the CLI or machine.
Controller enrollment checks account and machine authority. Possessing a
repository URL or typing a team name supplies neither.

<!-- design:end -->

## CLI

See [Credential management](credentials.md) for the implemented client surface.

<!-- design:begin -->

## Credential lookup

Capability lookup uses SHA-256 of the complete random token. Password hashing
remains a separate account concern. Cached authorization lasts at most five
seconds and also requires fresh ancestry and membership generations. A failed
generation read refuses cache reuse. Expiry is checked within a cache window.

## How long revocation takes to bite

Revocation advances the affected authority generation in the same transaction.
Every descendant depends on live ancestry. The five-second cache TTL does not
replace the generation check.

Revocation cannot recall a signed URL or an external GitHub or OIDC credential
already issued. Those retain their separate expirations. Stopping a runner is
not proof that an external credential was recalled.

## When a runner's token dies

A dead root prevents new claims and invalidates descendant authority. Work that
cannot renew its claim stops. Replace the machine's private credential and
restart its supervised runner.

The final machine configuration uses `runner`, `credentials_dir`, `repos` and
`keep_free`. Native enrollment and service refresh must use that schema before
they can establish recovery behavior. Client fixtures do not establish config
hot reload, hourly parking or automatic resumption.

<!-- design:end -->
