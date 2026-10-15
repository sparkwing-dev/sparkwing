# Authentication design

A connection chooses the controller. A credential identifies the caller and its
exact authority. A claim confines execution to the work the controller awarded.

This page specifies the target authentication contract. Controller provisioning,
route enforcement and native runner configuration still need integration.
Client fixtures do not establish those server guarantees. The implemented
client surface is documented in [Credential management](credentials.md).

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

The controller verifies Stripe webhook signatures. It derives the team, amount,
checkout identity and credit grant from verified payment provenance. Settlement
uses the committed checkout and deduplicates the payment event in its
transaction. A caller's supplied team or amount is not payment proof.

Test and live Stripe keys select configuration for the same implementation.
Checkout, payment history, card operations, payment holds and reversals retain
their account authorization. A signed payment event authorizes only its declared
payment operation. It grants no general team or operator power.

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
