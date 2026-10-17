# C2 credentials contract

<!-- design:begin -->

## Amendment 1: selected routes and trusted adapters

### Credential files and proxy attribution

`fssecure.OpenCredential(directory, name)` is the shared file boundary for
credential values and keys. The resolved target stays inside the resolved
configured directory, is regular, belongs to the process user or root and
has no group or other write bit. Kubernetes `..data` links and other in-directory
links are accepted. The opened handle is checked before any credential bytes
are read. Windows uses the current user, LocalSystem or Administrators owner
and refuses write grants to other principals. Operator configuration documents
retain their stricter private-config policy.

Directory credentials, Stripe API/webhook keys, runner and launcher tokens,
local encryption keys, serve tokens, retained here credentials, CLI secret input
files and filesystem secret sources
use this boundary. Standalone file settings use their parent as the boundary.
The redundant credentials.ReadPrivate and FileBearer machine-policy parameter
are removed because both service and machine files share the same policy.

`--trusted-proxy-cidrs` accepts a comma-separated IPv4/IPv6 CIDR list and the
full chart exposes `controller.trustedProxyCIDRs` as a list. Empty ignores
X-Forwarded-For. Invalid configuration fails startup. Only matching TCP peers
can supply forwarded clients. Walk X-Forwarded-For right to left across trusted
hops, select the first untrusted address and use it for request logs and all
per-IP limits. Malformed hops in the trusted portion fall back to the TCP peer. The separate trusted
listener retains its existing X-Real-IP contract.

### Operator team creation (decision 114)

POST `/api/v1/operator/teams` is a deployment-owned AuthCapability route with
RetryNever. It accepts deployment `operator.tokens` or a direct configured
console session with `operator.console` and CSRF. Team/member, machine,
execution and local-peer authority cannot create a team through this operation.
The CLI is `sparkwing operator teams create --team SLUG [--display-name NAME]
[--profile NAME]`. Plain and pretty output emit the one-time capability.

The strict OperatorTeamCreate wire object contains `slug` and optional
`display_name`. OperatorTeamCreated returns `team`, `account_id`, `capability`,
`prefix` and `grants`. OperatorManager.CreateOperatorTeam uses the shared
transport and never retries. Server.New binds its route to the concrete
Store.CreateOperatorTeam without an optional production adapter.

The store re-resolves operator authority in its transaction, generates a
dedicated account without an OAuth identity and creates its owner membership.
It calls Store.Issue with the trusted ProvisionTeamAdmin (`operator_team_admin`)
origin. That origin requires the exact team and account just created in the
same transaction, the current operator and the complete owner grant table.
Caller-selected subjects, grants, metering, scope and expiry are refused.
Ordinary ProvisionCapability cannot use this origin to mint another admin for
an existing team. Rotation retains the origin and uses member rotation rules.
The root has no revocation parent and its created_by names the operator.

Creation, membership, root issuance and the team.created business audit commit
together. The audit's account is the new administrator and its actor is the
operator. A used or retired slug returns conflict and rolls everything back.
No schema, OAuth identity, verification claim or financial credit is added.
The operation is the Cloud operations/support and test-environment path.

### Lead decisions: payment and bootstrap boundaries

C2 extends PaymentEvent with verified non-checkout facts. The accepted provider
inventory is:

| Provider type | Kind | Required facts |
| --- | --- | --- |
| setup_intent.succeeded | card_setup | succeeded setup ID, customer, payment method, event creation time, fetched card fingerprint/brand/last4 |
| payment_intent.succeeded | card_paid | succeeded intent, positive amount equal to received amount, currency, latest charge and fetched fingerprint |
| payment_intent.payment_failed | card_failed | failed intent in requires_payment_method, positive amount, currency and optional decline code |
| refund.created / refund.updated / refund.failed | refund | individual refund ID, payment, charge, positive amount, currency and explicit pending/requires_action/succeeded/failed/canceled status |
| radar.early_fraud_warning.created / updated | warning | warning ID, payment, charge, explicit actionable and fetched fingerprint |
| charge.dispute.created | dispute_opened | dispute ID, payment, charge, positive amount, currency, reason and open status |
| charge.dispute.closed | dispute_closed | same identities and amount, with won/lost/warning_closed status |

The existing four checkout policies and their projection remain unchanged.
PaymentEvent stays comparable. ObjectID carries the setup, intent, refund,
warning or dispute ID. PaymentID is the payment intent. CustomerID and MethodID
identify the card setup. ChargeID selects the verified charge. Status, Reason,
DeclineCode, Fingerprint, Brand, Last4, Created (provider event Unix seconds)
and Actionable retain only provider facts. No team, credit value, local charge
ID, attempt ID, or metadata field is accepted as authority.

Signature, timestamp and configured event mode precede object decoding and
provider access. Every non-checkout object must match the configured mode and
carry a positive event creation time. Required references accept provider IDs
or expanded objects. Card setup reads the payment method using the configured
private API key and checks the exact method, customer, mode and card shape.
Paid-card and warning events read the charge and check its exact payment-intent
relation and mode before taking a fingerprint. Reads use fixed HTTPS Stripe
collections, path-safe IDs, no redirects and the existing one-MiB JSON budget.
Provider failure returns a safe unavailable error. No proof reaches settlement
after a failed read. The provider has no SQL, ledger write or retry policy.
The object forms follow Stripe's [setup](https://docs.stripe.com/api/setup_intents/object),
[refund](https://docs.stripe.com/api/refunds/object),
[warning](https://docs.stripe.com/api/radar/early_fraud_warnings/object) and
[dispute](https://docs.stripe.com/api/disputes/object) contracts.

W1b owns SettlePayment for these kinds. It must derive the team and local
charge/attempt from committed provider identities, retain event-ID deduplication
by payment mode, distinguish pending/failed refunds from completed reversals,
and prevent several events for one refund from reversing twice. Card setup
must retain stale-setup refusal. Warning-before-payment, refund-before-payment,
dispute holds and closed-dispute outcomes keep the existing ledger rules.
Unknown payments must never become metadata-selected grants. Checkout and
payment-intent events for one payment must not credit it twice. W2's shared
HTTP route forwards VerifiedPaymentEvent and acknowledges only after commit.
Store.SettlePayment dispatches non-checkout kinds through W1b's transaction
owners. Their final receipt/correlation schema and trusted setup association
writer remain W1 integration work. No raw financial fallback is added.

ProvisionSpec gains Seed, excluded from JSON in both directions.
ValidateProvisionSeed permits a supplied presentation only for
ProvisionBootstrap and only in the strict swk_ reference encoding. W1 must call
it in Issue and reject a supplied seed on every other origin.
controller.BootstrapAdminIssuer declares
EnsureBootstrapCapability(ctx, ProvisionSpec) (created bool, error).
EnsureBootstrapAdminToken accepts context, BootstrapAdminIssuer and raw seed.
It fixes the unbound operator subject bootstrap:admin and grants only
operator.tokens, operator.limits and operator.object_store. It forwards no
caller clock and never calls the legacy token table. Empty input is a no-op.
Missing BootstrapAdminIssuer returns policy_missing before any issuance.

Store implements the interface as one gated Store.Issue transaction. It locks
the deployment bootstrap gate and commits the settings marker with the seeded
root. Only the presentation digest and indexed prefix persist. Reusing a seed
is a no-op, including after revocation. Native SQLite issuance, readiness and
revocation properties exercise this adapter. Restart and concurrent native
topology evidence remain with the acceptance driver.

EnableAuthFromStore always installs credential verification. It no longer
consults ListTokens or opens legacy private routes when that read is empty or
fails. The legacy credential verifier and routes are removed. The controller's
--require-auth readiness check consumes
HasDeploymentAdministrator(ctx) (bool, error), refusing a missing reader.
Store implements that read under its store clock with complete live ancestry.
A deployment administrator is an unbound controller operator/service capability
in the deployment scope with operator.tokens. The public health handler pings the opened
database rather than calling the legacy run-list query. License-before,
license-after and single-team HTTP properties all refuse anonymous private
requests and report enabled authentication before any seed exists.

Server.HTTP and EngineController.SyncLauncher were already implemented before
this continuation. Their in-process tests pass. Local HTTP supplies shared
API/dashboard handlers and consumes Wingd's verified peer context. Sync passes
the authenticated authority and cloned observations to LauncherSyncReader,
then strips bearer material from a copied response. Operator.ReadLauncherSync now supplies the native snapshot. Verified peer reads
use the default-tenant local snapshot. Authority-bearing local mutations remain
C1 integration work.
No native launcher or peer matrix cell is inferred from those component tests.


### Native log read composition

Log signing, commits and dashboard reads use the configured data object backend.
Reads retain the shared engine actor projection, including the authenticated
holder and generation. Store.ReadLogSegments rechecks current authority before
physical I/O. A refused request produces no log bytes and no egress charge.
Stream responses use Cache-Control: no-store. HEAD remains unregistered.
Segment commits carry decoded Size separately from compressed StoredBytes and
Digest. Both byte counts participate in immutable replay.

The retained egress tests now use native awards, sealed account sessions and
committed tagged segments. Generated mutations cover foreign paths, stale
holder/generation, unknown presentations and membership loss. Native root
revocation prevents the next read before object I/O. The object bytes are fake
in these HTTP tests. Native topology evidence remains with T1.

### Legacy execution credential cut

The legacy trigger and node execution route families, raw POST run events,
claim-token router, runner-claim principal conversion, here-token issuance,
direct node output transport and claim-specific log mutation handlers are
removed. Final intake, plan, transitions, renewals and data operations keep
their existing policy/transaction interfaces. No legacy credential fallback
exists on these execution paths.

The remaining business-route fallback matches a registered method before
authentication. Missing paths and methods use PolicyRouter's shared error
writer. Browser middleware likewise bypasses session lookup for missing
methods and paths. The removed raw event method answers 405 because GET
events still exists. PostgreSQL contention states 40001, 40P01 and 55P03
map to the shared unavailable envelope with one-second retry advice on final
engine operations. Database diagnostics remain private.

Final child transport, claim-input/read-set transport, job log operations,
debug resume and the old clients that still call retired paths remain
integration work. Their policy rows stay declared. This cut does not supply
their missing wire/transaction adapters or count them as acceptance evidence.
See [the execution cut report](../W2-execution-cut-handoff.md).

### OIDC claim hard cut

POST `/api/v1/runs/{run}/oidc-token` now registers through PolicyRouter with
OIDCMint/BindRun/LiveSensitive and RetryNever. It accepts only audience and
returns OIDCTokenResponse. Machine roots, account credentials and bootstraps
cannot mint. Missing/dead authority answers unauthenticated. Live authority
without the grant or run binding answers forbidden. Minting has no legacy
scope or claimed-run lookup fallback. Both old handlers are deleted.

The local C2 `controller.OIDCReader` interface is Team() store.Team and
ReadOIDCFacts(ctx, Authorized) (capability.OIDCFacts, error).
WithOIDCReader supplies its resolver. The default requires Tenant to implement
it and refuses missing adapters with policy_missing. No C1 signature changes.

ReadOIDCFacts must use one store-clock transaction to recheck the complete
live ancestry, awarded/delegated claim, oidc.mint grant, exact team/run binding,
current holder/generation, cancellation and unit liveness. Derive the pipeline,
ref, exact SHA, repository identity and original trigger from immutable intake
and accepted seals, preserving them on retry/child lineage. Only verified App
delivery facts may yield push, pull_request, release, create or delete and a
positive repository ID. A PR ref is refs/pull/<number>/head even when its branch
is main. A tag push keeps refs/tags/<tag>. Only a committed controller schedule
may yield cron. Unverified/manual origins never gain an App event or repository
ID. RunnerKind comes from persisted award/enrollment provenance, never request
headers or the current runner process. The historical OIDC subject spellings
remain runner, github-actions, user and service.

Return Now and a finite NotAfter no later than every authority ancestor's
expiry/revocation or binding deadline. Refuse an ended claim with a typed
unauthenticated error, and leave backend diagnostics private. The HTTP adapter
also checks the returned team/run and caps expiry by its authenticated leaf
and binding. It passes only the requested audience to the signer. The existing
RS256 subject grammar, key rotation, ten-minute default and per-claim 30-token,
five-minute refill budget remain. The budget is replica-local overload control.
Already issued provider tokens retain the accepted expiry residual.

C1's production reader remains an integration requirement. Reading legacy
Trigger/ClaimedRun rows would bypass the final capability boundary. Reading
RunRow alone cannot supply the transactional live-authority ceiling and all
verified original event/provenance facts. Those paths are not fallbacks.
HTTP tests use committed-fact fakes and the real signer/verifier/client.

### Browser login and transport identity

GET and POST `/login` use PolicyRouter with AuthBrowserFlow. The bounded form
requires same-origin proof, one CSRF cookie and one matching form token. It
accepts one token, optional next and csrf_token. Unknown or duplicate fields,
Authorization headers and duplicate session cookies fail. Only a live, unbound
controller account capability without a data scope can open a session. The existing
CreateCapabilitySession transaction rechecks that parent and preserves its
ancestry. The response handler ends the prior session and sets private cookies.
The old admin-token/argon2 login and its middleware registration are deleted.
Errors use the common envelope. GET verifies existing sessions without renewal.

The local C2 interpretation follows C1's account-capability session contract.
The form accepts account capabilities without requiring the retired admin scope.
Deployment operator, machine, service and executable credentials cannot sign in.
Account-session licensing stays the same. Direct provider sessions are the
operator's own sign-in. A capability-parented session cannot acquire console
powers through an operator account ID.

The default authenticator selects console proof only when the matched route
contains CredentialOperator. It reads the same committed account-session
snapshot, checks licensing, absolute lifetime, direct ancestry and the current
WithOperatorAccounts configuration, then projects CredentialConsole with only
OperatorConsole. Other routes retain CredentialSession and member grants. The
session hash, active team, account ID and lifetime are preserved for CSRF.
BindNone has no tenant relation for console authority, so deployment targets do
not become limited to the account's active team. Other bindings still use C1.
C1's authority-bearing console mutation transactions remain required. The
controller's proof projection does not implement those committing rechecks.

WithPeerIdentity replaces WithPeerPrincipal. Its callback returns only the
transport-verified local subject. It cannot supply grants, tenancy, ancestry
or a credential class. Explicit headers and session cookies take precedence
and never fall back to the peer. The shared AuthenticationReader receives the
verified subject through CredentialLookup.Peer. Its snapshot and relational
checker derive authority. The api.sock caller now supplies its kernel-verified
uid through this interface. Store now returns its default-tenant peer snapshot
and checks native BindNone relations. Transactional local mutation authority
remains separate. The shared local HTTP test covers the default store adapter
and injected component adapter. Native Windows still refuses api.sock without SID verification.

Real SQLite login and console HTTP tests cover successful sessions, narrowed
grants, revocation, failed browser proofs, private errors, safe redirects,
unsupported methods, operator configuration changes, cross-team console reads
and capability-parent refusal. Generated form, console and peer properties
cover forged proofs and transport selectors. No native credential matrix cell
is claimed complete.

### Runner administration and financial service hard cuts

GET and POST `/api/v1/runners/github` and DELETE
`/api/v1/runners/github/{repository}` register through PolicyRouter. Reads
require member runs.read. Mutations require member team.admin and the selected
session's CSRF proof. The authorized active team supplies tenancy and the
principal's subject supplies CreatedBy. Body/query/header selectors supply
neither. The existing GitHub App coverage check supplies numeric identities
when configured. Without it, positive repository and owner IDs are required.
Removal accepts a canonical positive numeric repository selector. Responses
retain bindings and workflow, with the existing public metadata fields.

C2 declares controller.GitHubRunnerRegistry: Team() Team,
GitHubRunnerBindings(ctx) ([]GitHubRunnerBinding, error),
RegisterGitHubRunner(ctx, Authorized, GitHubRunnerBinding)
(GitHubRunnerBinding, error), and RevokeGitHubRunner(ctx, Authorized, int64)
error. The registry must recheck live administration, tenant and actor inside
its store-clock transaction, preserve existing enrollment limits and canonical
repository identity, and make repeated enrollment safe. Revocation removes the
enrollment and ends its capability roots and descendants atomically with
concurrent root issuance. The controller has no legacy token-table fallback.
Default inventory uses C1's existing tenant reader. Missing mutation adapters
return policy_missing. WithGitHubRunnerRegistry supplies the same contract to
embedded controllers and tests.

POST `/api/v1/operator/runners/{runner}/metered` requires console proof and
session-bound CSRF. It accepts one explicit boolean metered and returns runner
and metered after commit. The runner selector is the enrolled root's public
capability prefix because machine names can repeat across teams. Unlicensed controllers return not_found after
authorization. C2's controller.RunnerMeteringWriter requires
SetRunnerMetered(ctx, Authorized, string, bool) error. The transaction must
recheck console authority, resolve only an enrolled machine root and update
future-award metering without changing existing award billing provenance.
WithRunnerMeteringWriter supplies this contract. Missing adapters return
policy_missing. No token-table metering or response adapter remains.

The three `/api/v1/team/github-runners` registrations and
`/api/v1/tokens/{prefix}/metered` are deleted. The dashboard uses the final
GitHub paths. HTTP properties cover tenant-selector independence, live
authority, console-only metering, explicit bodies, missing transactions and
private failures. Enrollment mutation and metering tests use fakes. Real
SQLite proves the empty default inventory and missing-writer refusal.
Tenant.AddGitHubRunnerBinding fails against the final schema because it omits
required repo_key. No W2 schema repair or SQL adapter is added.

The eight credits.grant service routes for reversals, freezes, cards, card
payments, warnings, card refunds, units and payment lookup are deleted, along
with their handlers and wire types. EnsureCheckoutToken, ScopeCreditsGrant,
requireServiceScope and credits-token startup loading/seeding are removed.
The signed provider callback remains the only automated HTTP settlement
boundary. Its verified card, refund, warning and dispute contract is declared
above. Their ledger settlement remains C1 integration work. No legacy bearer callback survives as
an interim implementation. Existing store regressions retain their financial
invariants. Their final provider-HTTP ports remain open.

### OAuth browser proof

GET `/auth/{provider}/start` and GET `/auth/{provider}/callback` register
through PolicyRouter with AuthBrowserFlow. Their verifiers apply the existing
login budgets and require a licensed, configured Google or GitHub provider
and an allowlisted redirect URI. Start creates state and a PKCE verifier.
Callback requires one flow cookie, the same provider and one matching state.
A successful callback requires one nonempty code. A declined callback requires
one nonempty error and no code. Ambiguous or malformed query selectors fail
before provider access. Unknown flow modes and completed link cookies cannot
enter the callback again.

The verifier redeems the sign-in code and calls C1's existing
CreateIdentityAccountSession. The response handler receives that verified
session, clears the flow, ends the browser's prior session, sets private session
cookies and renders a same-origin return path. Refused proofs preserve the
flow cookie. Provider codes, verifiers, access tokens, session cookies and
upstream diagnostics enter neither error responses nor audit logs. Existing
rate limits retain Retry-After through the canonical error envelope. HEAD and
unsupported methods return the shared 405.

The link callback carries a verified code/state flow to its same-site completion
page. It redeems no code and creates no session. Link start and completion use
the named browser proof below. Durable nonce consumption and the final identity
transaction remain C1 integration requirements.

Eight HTTP tests use the real GitHub client, a fake provider transport and real
SQLite session persistence. Properties generate invalid browser proofs and PKCE
verifiers. The new session authenticates through the default digest snapshot,
and replay creates no second session. These are controller component proofs.
Native OAuth/browser, Google provider and full route-matrix cells remain open.
C1 still needs roleless account-session creation and authority resolution.
A waitlisted account with no membership is verified by the provider but refused
by CreateIdentityAccountSession. No W2 SQL or substitute session issuer is added.

### Session lifecycle transport

Ordinary capability-policy routes accept one Bearer or Session authorization
header. A header selects its own presentation and cannot fall back to a cookie.
With no header, the default controller reader accepts one session cookie and
refuses duplicate or empty presentations. Its AuthenticationReader adapter uses
the existing single store snapshot, then checks account-session licensing and
the configured absolute lifetime against snapshot.Now for every session in
the ancestry. The adapter reads no additional SQL and receives no raw secret.
Noncanonical persistence faults become unavailable. Existing canonical
authentication refusals retain their code. These routes do not renew sessions.

The dashboard routes actual controller-policy registrations directly to the
API proof boundary, using its existing router inventory. Legacy middleware
cannot pre-renew those requests or replace canonical errors with a form error.
Session-header mutations use the selected header's committed session identity
for CSRF. A different session cookie supplies no authority.

POST `/logout` uses PolicyRouter with AuthBrowserFlow. Its verifier reads the
same live session proof without renewal, parses the bounded form, and requires
same-origin proof plus a csrf_token matching the cookie and stored session.
It shares the API logout's verified-session deletion and clears cookies only
after deletion succeeds. Success redirects to /login. Refusals use canonical
errors, including 401 for an unavailable session. Audit attribution comes
from the verified account. The old renewing browser logout handler is removed.

GET `/api/v1/auth/session` and POST `/api/v1/auth/logout` register through
PolicyRouter with the existing AuthBrowserFlow proof. One Session authorization
header or one session cookie supplies the presentation. A header takes
precedence. Bearers, duplicate headers/cookies and HEAD cannot reach renewal.
The proof uses C1 LookupSession to check committed session ancestry and current
membership, then checks the existing license and absolute lifetime. It does
not renew. Logout additionally requires the session-bound CSRF header/cookie
and same-origin proof before handler entry. It takes no body and deletes only
the verified presentation. Query selectors confer no authority.

The read verifier uses C1 LookupSessionAndRenew after checking proof, repeating
its transaction checks and preserving parent grant ceilings. Both endpoints return no-store
and canonical errors. The browser's generic middleware does not pre-renew
these paths or replace their error envelope. No new persistence interface,
clock, key, compatibility path or customer lifetime limit is added.

The old session handlers and logout body type are removed. The local C2 wire
correction replaces unauthenticated body-selected logout with session proof.
Real SQLite HTTP properties cover presentation ambiguity, CSRF, cross-session
selection, dead authority and absolute renewal ceilings. Roleless OAuth and
console sessions still require C1's authority representation. LookupSession
must mark every infrastructure failure with ErrSessionBackend so the boundary
can distinguish unavailable storage from a dead session.

[Amendments 1](amendments-1.md) resolves the provisioning and transport questions.
`DecodeInner` is implemented. The remaining runtime work stays with W2 and the
store/host adapters. Its inner object requires a nonempty string code and a
present string message. Empty messages are accepted. Optional hint/details must
be a string/object. Duplicate keys at any depth, null fields, outer envelopes and
trailing JSON fail without echoing input. Unknown codes and additive fields remain
readable. HTTP readers unwrap once and both SDK protocol readers use this decoder.

| Route | Authority and result |
| --- | --- |
| POST `/api/v1/runners/claim` | One ordered trigger/node scan. claimSelection requires the grant for every requested kind. Separate jobs/runs claim routes are removed. |
| POST `/api/v1/launcher/claim` | claims.launch independently authorizes plan/eval/node selection. A client routing option confers no authority. |
| POST `/api/v1/runs/{run}/jobs/{job}/cancel` | Member runs.control or the live run owner. Held rows receive a cancellation request only. |
| GET `/api/v1/runs/{run}/jobs/{job}/failure?generation=…` | data.read plus the declared recovery read edge and source generation. Returns persisted masked JobFailure. |
| POST `/node/v1/masks` | NodeReport, ownNode, live node process. `{value:string}` returns `{}` only after all host sinks and identifier matching register it. |
| POST `/node/v1/annotations` | Same process authority. AnnotationRequest carries optional step, file/line/end_line/column. |
| POST `/node/v1/summary` | Same process authority. SummaryRequest carries markdown and optional step. |

Claim and reconciliation endpoints do not automatically retry a lost response because new bearers
are delivered once and are not stored raw. A held unit recovers through its lease.
Mixed-kind requests cannot borrow a grant from one access alternative to authorize
another kind. Claim identity and pools come from enrollment, not the request.
The named trigger route accepts a here Award's trigger.host only for its reserved
run under LiveHereClaim. It never grants a general queue scan. Here intake replay
returns the same run without a new raw bearer. Clients suppress automatic here
intake/retry and report a lost initial bearer as a failed handoff requiring a fresh
trigger request. The unused reservation expires at its original claim_by.
Target.ClaimKinds carries the requested set to BindClaimSelection, and ClaimUnit
repeats every kind/grant check in its transaction.

Mask registration first checks the accepted job/step/group/approval identifiers
and subsequent generated declarations. A collision returns secret_in_identifier
with the field path, never the identifier or value, before acknowledging success.
Every nonempty registered value participates, including short values. Annotation
and summary writes validate step membership, mask before limits.Record, and
propagate delivery errors. No secret crosses a log frame as a registration message.

`authentication_types.go` declares AuthenticationReader and the authority,
binding, expansion and result snapshots used by NewAuthenticator. Persistence
receives the SHA-256 presentation digest, never the raw bearer. Presentation.Evidence
carries decoded selectors and the exact body digest. Route matching supplies the
method/pattern. Replay compares actor, operation/resource, fence, attempt and body.
The node host supplies equivalent in-memory process/delegation facts. Endpoint and
PolicyRouter declarations bind actual registrations to this verifier. Authenticate
before returning existence-sensitive decoding errors. Session mutations require
CSRF and external-proof routes use their named proof mechanism.

### Plan secret integration

The controller opener is exercised through C1 plan acceptance with the shared
C3 validator and C4 settings resolver. A valid encrypted-secret plan persists.
Property checks place a decrypted stored secret in job, step, group and
concurrency identifiers. Every check returns `secret_in_identifier`, omits
the value from its error and commits no plan or jobs. Missing or failed
decryption still refuses acceptance. The former validator-stub gap is closed
for this controller/store path. Native host and generated-expansion evidence
remain separate obligations.

### Member credential issuance

`POST /api/v1/tokens` uses `PolicyRouter` and accepts `grants` plus optional
absolute `expires_at`. A session creates its own member root through
`Store.ProvisionCapability`. A member bearer uses `Store.DelegateCapability`.
Both wrappers commit through the sole `Store.Issue` path. The request carries
no issuer, subject, team, registration, pools, metering or binding.

Creation uses the member `runs.read` rule with `BindNone`. The prior
`ownToken` declaration required an existing token selector and could not
represent creation. Token-specific operations retain `ownToken`. Creation's
retry policy is `never` because the issuer returns a new one-time bearer and
has no idempotent secret replay contract. These are local C2 corrections.
The independent T1 creation expectation follows this correction.

`CredentialIssuer` preserves C1's existing provision and delegation signatures.
`Server.New` uses its store. `WithCredentialIssuer` injects that same contract
for embedded controllers and tests. The default session digest, CSRF and account
renewal paths are integrated with C1. Roleless sessions and console identity still
need their final adapters. Machine and service provisioning use the transport
below. Bootstrap provisioning and production capability-manager transactions
remain open.

The old global token mint, member-family mint and legacy team-token path are
removed. The dashboard's member
caller and response use the new route, grants and timestamp representation.

### Machine and deployment service provisioning

POST `/api/v1/runners` uses PolicyRouter with member `team.admin` and
BindNone. It accepts `name`, `repos`, optional `reserved_pools`, optional
`grants` and optional absolute `expires_at`. The existing machine-name grammar
and 32-pattern bound remain. Repository patterns use sourceurl's shared parser.
Omitted grants select nodes.claim and triggers.claim. Explicit grants narrow
to a nonempty unique subset of those two. A root has no reporting or sensitive
release grants. Pool names are unique and nonempty; the origin policy checks
that the registering team admin may assign them.

The authenticated team selects the machine's team. Its subject is machine/name
and its trusted registration selector is `runner:<team>:<name>`. The request
cannot select these origin fields, ancestry, metering or data authority. It
cannot create a service or operator root. The server calls the existing
CredentialIssuer.ProvisionCapability with ProvisionMachine. There is no
legacy CreateMachineToken fallback. POST `/api/v1/team/tokens`, its handler,
request/response types and server-built shell command are removed.

POST `/api/v1/operator/tokens` uses PolicyRouter with operator.tokens or
console authority. It accepts `name`, a nonempty unique `grants` array and
optional absolute `expires_at`. Its subject is service/name, team is empty,
and its registration selector is `service:<name>`. Grants are limited to
claims.launch, operator.limits, operator.object_store and operator.tokens.
It cannot issue console, member, work, source or data grants. It calls the same
issuer with ProvisionService and carries the authenticated AuthorityRef.

Both endpoints return credentialResponse: capability, prefix, team, grants and
optional RFC3339 expires_at. An omitted root expiry remains nil in the trusted
request and is subject to the origin policy. Explicit null or invalid expiry
is refused. Both use RetryNever because raw bearers are delivered once.
Session and console mutations require their selected session's CSRF proof.
Responses use no-store and canonical errors. Failed origin verification exposes
no bearer. Header and query team hints supply no authority.

These are local C2 transport declarations over C1's existing ProvisionSpec,
ProvisionPolicy and sole Store.Issue path. No persistence interface or second
registry is introduced. W1's production origin policy must atomically enforce
registration identity, current team administration, permitted pools/repos,
issuance limits and service enrollment. It must persist origin/registration
facts needed by claim identity and later root rotation. The controller cannot
supply those transactions by adding SQL. Missing origin policy returns
unavailable. Console service provisioning still needs W1 to recognize its
verified console session origin; the existing service check only accepts
operator/service subjects carrying operator.tokens.

Real SQLite HTTP tests prove machine and service root round trips and
refusal rollback through Store.Issue with a fake origin policy. They do not
prove production registration, duplicate-name races, issuance limits, console
session representation or ClaimProfileResolver integration. The Go client
uses separate member, machine and service requests and never replays issuance.
CLI/dashboard callers consume the same final response. The dashboard constructs
its runner setup from the one-time capability and returned grant set. Its
shell tests prove replacement of readable credential files, stop-on-failure,
repository arguments and grant-selected claim loops.

### GitHub Actions root exchange

`POST /api/v1/runners/github/exchange` uses `PolicyRouter` with
`AuthGitHubActions`. The proof verifier checks the signed JWT's issuer,
audience, time window and required repository IDs. The allowed event and branch
rules remain unchanged. A commit, workflow ref and workflow run are required.
The body accepts only `id_token` and `team`. Other headers and query selectors
supply no authority. Failed proofs expose no token or provider diagnostic.

The binding reader resolves the selected team's persisted numeric repository
and owner IDs. All three selectors must match before provisioning. Renames do
not change repository identity. `WithGitHubRunnerBindingLookup` supplies that
same read contract to embedded controllers and tests. The default reads through
`Store.ForTeam` and `Tenant.GitHubRunnerBinding`.

The controller sends a trusted `ProvisionActions` to the existing sole issuer.
It contains exactly `nodes.claim` and `triggers.claim`, the canonical `gh:<id>`
repository, and a push binding with the verified ref, SHA and workflow run ID.
Its registration ID is `github:<repository-id>:<owner-id>`. Its machine subject
appends `:<workflow-run-id>`. The existing one-hour root lifetime remains.
No caller selects pools, metering, trust, grants or expiry. The response retains
`token`, `team`, `repository`, `expires_at` and `labels` for the runner consumer.

This is a local C2 declaration of the trusted registration selectors, not a
second registry. C1's `ProvisionPolicy` must check that registration inside the
issuance transaction, preserve any applicable issuance limits, and connect
binding removal to the new capability roots. Missing policy returns unavailable.
The real-store HTTP test uses a fake policy and proves the committed capability
round trip and refusal rollback. It does not prove the registry race or native
Actions placement.

The old exchange mint handler is removed. The route has `RetryNever` because
issuance returns a one-time bearer with no replay contract. This local C2
correction follows the existing claim and member issuance policy. The public
reference generator records the named external proof instead of an empty scope.
Binding administration, legacy runner fences and their old combined tests remain
until their final routes and transactional writers are ported.

ProvisionSpec is ratified as trusted root-only Delegate input, with the origin
checks in issuance.go. It is never decoded from a delegation body. Root authority
is derived from the bootstrap gate, current membership, machine/service registration
or verified Actions evidence. Its authorizer is checked but is not a revocation
parent. Ordinary delegation and here Award ancestry remain unchanged. Deployment
credentials cannot acquire console trust/grant/refund/freeze powers through this seam.

`controller.DataController` binds C2 authority to blob.DataSigner and blob.RecordStore.
It resolves Authority.Upload and scope from authenticated, persisted facts, then
reauthorizes each operation. Signing follows W1 reservation. Commit physically
verifies bytes and repeats W1's transaction checks. S3 and filesystem backends use
blob.ObjectBackend. Filesystem transfer endpoints require exact signed-object proof.
DataRemoteCache always forces verified-default-only reads and never grants writes.

The bound signer and record adapters are implemented. Their trusted persistence
dependencies are explicit on DataController: ResolveAuthority rechecks ancestry,
membership and liveness before deriving sealed facts, ReadBlob supplies live
existence and size, and Access uses C1's DataAccessChecker. ResolveTenant exposes
only C1's existing upload and record method signatures. Missing dependencies fail
closed. These are local C2 contract additions, not implementations of W1's reads.
Download, presence, upload, commit and record HTTP routes use these bindings.
Upload takes `digest` and an explicit nonnegative `size`. Its response carries
`upload_id`, `url`, `headers`, `expires_at`, `sha256` and `size`. Commit takes
`upload_id` and returns only `digest` and `created_at`. These local C2 wire
declarations replace the cache-grant body and object-key selectors.

Record writes take `kind`, `pipeline`, `job`, `key`, `name`, `digest`, optional
`manifest_digest`, `members` and optional `expires_at`. Members carry `digest`,
`path`, `size` and `mode`. C2 derives team, repository, ref, uploader and each
member's manifest identity. Record reads select `kind`, `ref`, `pipeline`,
`job`, `key`, `name`, optional RFC3339 `deadline` and optional
`verified_default_only`. A remote-cache credential forces the latter true.
Responses expose public record/member metadata, excluding authority, uploader,
source and read-set snapshots. These routes cover generic RecordStore lookup
and commit.

### Capability inventory and rotation

GET `/api/v1/tokens` and GET `/api/v1/tokens/{token}` use PolicyRouter.
The collection uses member `runs.read` with BindNone because it has no token
selector. The manager filters the committed read to credentials owned by the
authenticated account, or the team's credentials for current team-admin
authority. A detail read retains ownToken. GET on the collection and resource
under `/api/v1/operator/tokens` requires operator.tokens or a console session
and selects only deployment service/operator credentials. Queries and headers
cannot change either scope. GET `/api/v1/team/tokens` is removed.

capability.Metadata is the explicit public projection. It contains prefix,
team, subject, audience, grants, created_by, issued_at, optional expires_at,
revoked_at and last_used_at, and git_credentials and metered. Times use RFC3339. It contains
no bearer, digest, binding, ancestry, pools or billing provenance. Collections return
`{tokens:[]}` when empty. Optional kind selects account, machine, service or
operator. Optional include_revoked is 0 or 1. Invalid or duplicate filters
return bad_request after authentication. Scope-invalid adapter output fails
without returning partial metadata.

POST `/api/v1/tokens/{token}/rotate` and its operator counterpart use the
existing resource ownership policies and RetryNever. The raw replacement is
returned once and has no secret replay contract. The body accepts optional
grace_secs and expires_at. Omitted grace retains the existing 24-hour default.
An explicit grace is an integer from zero through the existing seven-day
ceiling. Null, negative and overflowing values fail. Omitted expiry preserves
the original expiry. An explicit expiry passes the original parent's ceilings
for a delegated credential or the current origin policy for a provisioned root.
The response carries capability, new public metadata, old_prefix and the
RFC3339 old_revoked_at. Session and console writes require CSRF. All inventory
and rotation responses use no-store and canonical errors.

Invariant: credential metadata and replacements remain within authenticated
ownership and scope, and a failed transaction exposes no new bearer or partial
result. CapabilityManager is W2's local transaction interface. Team reports
the selected tenant, or empty for deployment operations. ListCapabilities
takes context, Authorized and capability.InventoryFilter. ReadCapability takes context
and Authorized with the exact token selector. RotateCapability takes context,
Authorized, grace duration and optional absolute expiry. It returns
capability.Rotation containing Issued, the pre-rotation Previous metadata and
the store-derived RevokesAt. WithCapabilityManager injects the same interface
for embedded controllers and tests. Defaults select Tenant or Operator and
refuse missing implementations with policy_missing.

W1 must implement these operations with its store clock and transaction-local
ancestry, membership, owner and route checks. Inventories select controller
root/delegated credentials, never unit awards or node-host credentials. The
store clock decides whether a scheduled revocation has ended a row. The HTTP
projection preserves that committed selection and its grace deadline.
Deployment inventories select unbound service/operator roots. Rotation issues
through Store.Issue, preserves the original origin/parent, registration, owner,
repository/data scope, pools, metering and source consent, and cannot widen
grants or escape its current issuance expiry ceilings. Its replacement must not parent to the old credential that
the rotation ends. The replacement and bounded old revocation deadline commit
together, advancing the old authority generation. Repeating rotation on an
already scheduled old credential fails rather than issuing a second replacement.
An explicit revocation cuts
grace short. A failure returns no Raw. No legacy token-table read or RotateToken
fallback is permitted. The controller cannot implement this transaction or
create a new persistence representation. Its fakes prove transport and
projection only. Real rotation, revocation propagation, registration and
source-consent transfer remain W1 integration work.

These are local C2 declarations. No C1 signature or implementation changes.
The collection binding and rotation retry corrections follow the existing
member-creation correction. Native route-matrix cells remain pending.

### Secret and git credential inventory reads

GET `/api/v1/secrets` and GET `/api/v1/team/git-credentials` use PolicyRouter
and the existing member rules. Both select the authenticated team and return
`Cache-Control: no-store`. Caller headers and query selectors cannot select a
different tenant. Machine roots, claims and bootstraps cannot read inventories.

Secret lists omit masked values without opening their envelopes. Unmasked
config values use the stored team's full row binding. Missing decryption returns
`unavailable`. A failed open returns a private `internal` error with no partial
list. Git lists expose the existing public metadata projection, without stored
secrets, row IDs or team fields. Empty collections are arrays.

CredentialInventoryReader is a local C2 subset of Tenant's existing Team,
ListSecrets and GitCredentials methods. The default resolver uses Store.ForTeam.
WithCredentialInventoryReader supplies that same contract to embedded
controllers and tests. A nil or foreign-team reader fails closed. No C1
signature changes. The replaced legacy list handlers and registrations are
removed. Credential writes and secret-by-name reads use the contracts below.

### Secret-by-name reads

GET `/api/v1/secrets/{name}` uses PolicyRouter and the existing member
`secrets.read`/`team.admin` or live `secrets.release` claim alternatives.
The authenticated team selects the reader. `run` and `pipeline` are selectors.
Duplicate selectors, malformed queries and invalid names return `bad_request`
after authentication. HEAD is refused. All responses use no-store.

Invariant: a masked value leaves only for a live, declaring claim whose sealed
pipeline/shared scope and reserved pool permit the row. Members and verified
local peers read unmasked config within their authenticated tenant. Neither a
machine root nor a deployment-admin token substitutes for a claim or member.

SecretReader is a local C2 interface with Team and
ReadSecret(ctx, Authorized) (*store.Secret, error). WithSecretReader supplies it.
Its transaction uses the store clock to recheck ancestry, current membership,
route grant, claim liveness and cancellation. Claims resolve against the sealed
run, require the declared name, prefer its pipeline row and fall back only to a
shared unscoped row. They enforce only_on against reserved and allowed pools
and refuse here access to restricted secrets. Caller selectors cannot change
that authority. Member run selectors resolve within the same tenant. Masked
member reads return forbidden. Selection and the claim's secret_released event
commit together before returning an envelope. A failed audit commits no release.

The controller requires BoundCipher and binds decryption to the authenticated
team and every stored row access field. It rejects wrong names and masked
member results before opening. Invalid envelopes and cipher diagnostics return
a private internal error without values or partial output. A missing cipher
returns unavailable. Missing or foreign-team readers return policy_missing.
Tenant's old ReleaseClaimSecret takes a retired ClaimToken and caller clock.
It cannot satisfy this interface and is never used as a fallback. W1 must supply
the authority-bearing transaction. Component tests use committed-fact and reader
fakes with the real envelope cipher. They do not prove SQL fencing or audits.

Startup no longer calls ResealStoredSecrets. The method and pre-team/plaintext
migration implementation are deleted, along with LegacyCipher. Row-secret helpers
require BoundCipher and never fall back to unbound Seal/Open methods. Generated
tests assert those methods are never called. Reads refuse legacy stored forms. Explicit
bound key rotation remains the credential-maintenance operation above.

### Credential mutations

POST `/api/v1/secrets`, DELETE `/api/v1/secrets/{name}`, POST
`/api/v1/team/git-credentials`, POST
`/api/v1/team/git-credentials/{host}/confirm` and DELETE
`/api/v1/team/git-credentials/{host}` use PolicyRouter's existing member
`team.admin` rules. Session writes require CSRF. All responses use no-store.
The authenticated team selects the writer. Request headers, query selectors
and bodies supply no team or actor authority. Secret writes derive Principal
and git writes derive CreatedBy from the authenticated subject.

Invariant: only live team-admin authority can commit a credential mutation
within its authenticated tenant, and stored envelopes bind to that tenant and
the complete row. CredentialWriter owns the transactional authority recheck,
store clock, row mutation and actor attribution. It exposes Team plus
WriteSecret, RemoveSecret, WriteGitCredential, ConfirmGitCredentialFor and
RemoveGitCredential, each taking context and Authorized. Secret writes carry
store.Secret. Git writes carry and return store.GitCredential. Confirmation
takes host/fingerprint and returns the persisted row. Deletes take the exact
name/pipeline or host selector.

WithCredentialWriter supplies this local C2 contract for embedded controllers
and tests. The default tenant must implement it. Nil, foreign-team and missing
writers fail with policy_missing. Existing Tenant credential writes take no
authority and cannot satisfy this interface. C1 must supply the transaction
adapter. No controller SQL or unfenced writer fallback is permitted. Fakes
prove the HTTP boundary and envelope/storage round trip, not the production
revocation race. The production integration remains open.

Bodies accept one typed JSON object and refuse unknown authority fields and
trailing values without echoing request data. Secret names always use the
shared grammar, including replacements. Masking defaults to true. Missing
encryption returns unavailable. Key-location refusal returns conflict with
fixed configuration guidance. Other cipher/store diagnostics remain private.
SSH writes pin the scanned host key and wait for owner confirmation. An omitted
SSH port uses 22. An explicit zero is invalid. Confirmation preserves the
pinned fingerprint check and returns only public metadata. Secret values and
git credential bytes never enter mutation responses.

Secret-by-name reads use SecretReader below. Tenant key rotation and
source-release audit reads use the maintenance contracts below.

### Credential maintenance

POST `/api/v1/secrets/rotate` and GET
`/api/v1/team/git-credentials/releases` use PolicyRouter's existing member
`team.admin` rules. Both return no-store. Session rotation requires CSRF.
Query parameters and headers cannot select another tenant or actor.

Invariant: a request can rotate or inspect credential rows only within its
authenticated tenant, and private values never enter maintenance responses.
`CredentialRotator` exposes Team and RotateCredentials(ctx, Authorized,
secretCallback, gitCallback) (secretCount, gitCount, error). The callbacks
take store.Secret and store.GitCredential respectively and return replacement
envelopes. One transaction rechecks ancestry, membership/team.admin or verified
local authority, selects only that tenant's rows, calls each callback once
serially and commits both tables together. Counts include all visited rows.
Only values change. Row ownership, access fields, actors and timestamps stay
unchanged. Any callback or authority error rolls back both tables.

The controller requires BoundCipher. It opens the current or previous key's
bound envelope and seals under the current key with the same complete binding.
An unreadable secret keeps its bytes and appears by name/pipeline in skipped.
Plaintext and unbound envelopes are unreadable and never gain trusted binding.
An unreadable git credential aborts the transaction. Key-location refusal and
any sealing failure also abort. Cipher diagnostics remain private, including
errors that happen to implement the public API error type. Rotation takes no
body. Its response retains rotated, skipped and git_credentials.

`SourceAuditReader` exposes Team and SourceReleaseAudits(ctx, limit), returning
[]capability.SourceReleaseAudit, newest first. The existing 200-row bound stays.
Its reader decodes the committed capability prefix and nanosecond store time.
The HTTP projection returns host, repository, run_id, job_id, generation,
runner, capability_prefix and released_at in Unix seconds. It excludes audit
IDs, credential IDs, owner account IDs and team fields. Empty lists are arrays.
A foreign-team row fails the whole response without a partial list.

WithCredentialRotator and WithSourceAuditReader supply these local C2 contracts
to embedded controllers and tests. Defaults require the tenant to implement
the same interfaces. Missing, nil and foreign-team adapters return
policy_missing. The old Store rotation and GitCredentialReleases reader are
not adapters. C1 must implement the final tenant transaction and audit
projection. Generated tests exercise the real verifier, rotation cipher and
HTTP projection with fakes. They do not prove C1's transaction or native
transport. The production integrations remain open.

### Cache record transport

POST `/api/v1/data/memo/lookup` takes `pipeline`, `job`, `hash` and optional
RFC3339 `deadline`. POST `/api/v1/data/depcache/lookup` takes `name`, `key` and
optional `deadline`. Both use the same read alternatives as generic records.
POST `/api/v1/data/memo/copy` takes the memo selectors and requires both
`data.read` and `data.write` on one live claim alternative. Copy has
RetryExactBody. Lookups have RetrySafe.

These flat request objects accept unique, known string fields. Repository,
ref and authority come from DataBinding. Copy destination is its current
attempt. Query parameters supply none of these facts. Memo responses contain
`found`, optional `output` and `manifest` digests, and a `members` array.
Depcache responses contain `found` and, on a hit, public `record` metadata
and the winning restore `source`. Misses omit stale hit fields. No response
contains the private ReadSet, uploader or source identity.

The handlers call C1's existing LookupMemo, LookupDepcache and CopyMemo
interfaces. C1 owns fallback order, verified-default restrictions, corruption
and budget misses, and atomic destination edges. Remote-cache authority stays
read-only. Bound CopyMemo checks both read and write policy before its transaction.

These are local C2 route/wire declarations. W9's NewDataClient still needs
these transports. C1 must complete the DataAccessChecker selection semantics:
DataLookup reaches it before a digest is selected, and CopyMemo's DataRecord
check has no digest before the transactional hit. The merged checker treats
lookup as a digest-presence check and refuses empty digests. No SQL or policy
fallback is added in W2. Production scope/metadata/charging adapters and W7's
physical storage remain separate integrations.



This contract covers frozen architecture sections 3.6, 5, 6.5, 8 and 9.5,
including A58, A59, A66, A68, A72 and A73. The Go files declare the target
interfaces. The policy evaluator, authentication verifier, HTTP policy router,
delegation validator, data/source checks, billing calculation, payment verifier
and error codec are implemented. Filesystem and S3 object signers implement the
signing boundary. Authentication reads use the store snapshot implementation. Root-origin
provisioning still needs its remaining trusted adapters. The Stripe callback, migrated engine and operator endpoints use this table in the existing
controller. Four credential/membership mutations, the member directory, three
credential discovery routes and both public OIDC metadata routes also use this
table. Five pipeline sizing routes also use this policy. Stored source release and machine-owner consent also use this table. The remaining routes have not switched to it.

Job list/detail status reads also use this policy and the declared JobDetail
projection. They read C1's snapshot/node interfaces and exclude execution
declarations, environment and claim identities. The old status-read handlers
and paths are removed. Native transport and persistence coverage remain open.

### Amendment integration handoff

The merge keeps the amended contract declarations and the existing W2 runtime.
Duplicate authentication and router declarations are removed. Claim selection
now checks every requested kind before relational binding. Job cancellation and
recovery failure reads use the shared HTTP policy and C1 transaction signatures.
Snapshot and expansion replay responses retain the amended execution fields.

[The amendment handoff](../W2-amendments-1-handoff.md) records every W2 action
row, remaining contract and runtime work, shared files and verification limits.

### Claim HTTP integration

The controller registers mixed runner claims, launcher claims, named trigger
claims, bootstrap exchange, own-unit renewal and job reconciliation through
PolicyRouter. Their decoders preserve kind selectors. Enrollment and software
policy stay trusted dependencies. The store repeats authority and grant checks
when awarding. No claim response is replayed to recover a lost bearer.

Endpoint.ResolveTarget is a local C2 contract addition. It derives implicit
selectors from the already authenticated principal before authorization.
Exchange and own-unit heartbeat use it because their paths have no run selector.
The heartbeat's claim ID stays caller-selected and must match the unit ID.

LauncherSyncReader is a local C2 adapter contract for a transactional launcher
snapshot. It receives the authenticated authority reference and observations.
The adapter must recheck ancestry and current award ownership. Sync copies the
response and clears every Capability before returning it. Missing wiring returns
policy_missing. C1 still needs the final snapshot operation. Its old
SyncLaunchJobs accepts retired claim identities and cannot serve this adapter.

Storage settings and reports use Operator. Quota and allowance writes resolve a
Tenant for the policy-authorized team. They retain the operator.object_store
grant and final operator routes. These calls still need C1's authority-bearing
mutation signatures to repeat the operator recheck under the write lock.

### Stored source release and plan-secret integration

`SourceController` is the local C2 adapter for C1's existing
`Tenant.ReleaseSourceCredential` and `Tenant.SetSourceMachineConsent` signatures.
Its `SourceTransactions` interface preserves those signatures. `Store.ForTeam`
is the default resolver. Embedded controllers and tests may supply
`ResolveTenant` through `WithSourceController`. The configured BoundCipher opens
only the returned team-and-host-bound envelope after the release audit commits.
The controller compares the audit's team, credential, host, repository, run, job,
generation and capability prefix with the authorized projection before opening.

`POST /api/v1/runs/{run}/source-credential` uses PolicyRouter and the existing
SourceRead/BindSource/LiveSensitive rule. It takes no body. Its local C2
`SourceCredentialResponse` carries kind, host, secret, optional username and
optional known_hosts. Checkout identity and policy remain in the sealed request.
This cuts the legacy claim-token handler, its separate eligibility checks and
its per-process limiter. Source release has RetryNever.

`PUT /api/v1/tokens/{token}/git-credentials` takes an explicit enabled boolean.
The policy requires an account session and BindMachineOwner. It passes the
authenticated AuthorityRef and token selector unchanged to C1's consent
transaction. Team administration does not supply owner consent. The former
team-token consent route and handler are removed.

`WithSecretsCipher` installs a PlanSecretOpener on its store. The adapter uses
the stored tenant, name, pipeline, shared and masked fields through the same
secret-envelope opener as controller reads. Clearing the cipher removes the
opener. Missing or failed decryption refuses acceptance. The controller/store
tests use the shared validator to refuse decrypted secret identifiers before
insertion. Native host and generated-expansion evidence remain separate.

C1's audited release method returns confirmed stored SSH/HTTPS credentials.
GitHub App minting and extra-repository release still need an audited C1
contract before their runtime can be restored. W2 retains no unaudited App
mint path. The source Access consumer in W3 and client projection in W9 must
consume the new credential response with their existing sealed source facts.

### Identity linking and local HTTP composition

POST `/auth/{provider}/link` and GET `/auth/{provider}/link/complete` use
PolicyRouter's AuthBrowserFlow proof. Both select exactly one live session
cookie without renewal. Authorization headers are refused. Only a direct
provider session can link identities. Capability-parented sessions cannot.
The existing ten-minute sign-in window and ten attempts per account per minute
remain. Start requires one bounded form containing only csrf_token, matching
the sole CSRF cookie and the selected session, with same-origin proof.

State is MAC-bound to the selected account, session, provider, nonce, expiry
and PKCE verifier digest. Completion checks the flow and state before entering
the writer or provider. It consumes the nonce before redeeming the provider
code, then commits only that provider's verified profile to the selected account.
Success clears the flow and redirects to the sign-in settings page. Refusals
return the canonical envelope. Provider diagnostics and private flow values
enter neither responses nor logs.

C2 declares capability.IdentityLink with Authority, AccountID, Provider, Nonce
and ExpiresAt. Authority is the committed session digest, never its presentation.
controller.IdentityLinkWriter has these methods:

```go
ConsumeIdentityLink(context.Context, capability.IdentityLink) error
LinkAccountIdentity(context.Context, capability.IdentityLink, store.SignInProfile) (store.Identity, error)
```

WithIdentityLinkWriter supplies them. The default requires Store to implement
the interface. Missing adapters return policy_missing. C1 must use its store
clock and recheck direct session ancestry, account, provider, recent sign-in, expiry and authority inside both transactions.
Consumption atomically records the unique nonce and its complete binding.
Attachment locks that consumed binding, rejects completed or mismatched claims,
requires the matching verified provider profile and preserves account email.
It commits identity uniqueness checks and nonce completion together. A session
revoked during provider exchange cannot attach an identity. Reopening and
multiple replicas must preserve both consumption and completion. The old
ConsumeIdentityLinkState and LinkIdentity calls are not production adapters.
No C1 signature, schema or implementation changes in this cut.

Server.HTTP() supplies wingd.LocalHTTP's API and dashboard handlers. It builds
the final controller registrations once and excludes the legacy router fallback.
The API's transport adapter reads only wingd.LocalPeer's verified connection
identity, derives a local subject with LocalSubject and passes it through the
shared authentication and binding interfaces. Explicit credential headers and
session cookies retain precedence. Browser requests do not inherit socket
identity. Nonlocal dashboard pages require their own live session independently
of legacy bearer-auth configuration. Dashboard.Local selects the local pages.

The runtime still owns election, store recovery, hosting, worker shutdown and
request drain. C1 peer authentication snapshots and authority-bearing mutations
remain required. Net-pipe HTTP tests use the real Wingd serving/drain path and
fake committed peer facts. They establish handler composition and identity
precedence, not native UID/SID or full local-runtime acceptance.

## Ownership

`internal/capability` owns the credential vocabulary, issuance rules, route
policy, liveness classes, billing provenance and release/signing boundaries.
`internal/apierr` owns the error object and code registry. `store.Issue` is
the only persistent issuer and participates in the caller's transaction.

C1 owns persistence, the store clock, claims, sealed source facts, blob
receipts/read sets/edges, quotas and financial ledger writes. C3 owns the
protocol and host lifecycle. C4 owns limits, configuration and credential
file delivery. No contract here defines a second blob model, state machine,
environment builder or deadline helper.

The route table includes node-host, public and external-proof routes.
`Routes`, `Grants` and `apierr.Codes` are the complete target inventories.

## Exported API

All capability names below are in `internal/capability`.

| Type or function | Purpose |
| --- | --- |
| `SubjectKind`, `Subject` | Identify the accountable account, machine, service, operator, local peer or anonymous caller. |
| `Audience` | Separate controller and node-host authority. |
| `BindingKind`, `Binding` | Carry the server-derived run/job/push fence and deadline. |
| `LeaseRef` | Name the owner or eval lease authorizing an expansion of a terminal source. |
| `PushScope` | Confine Actions authority to its repository, ref, SHA and workflow run. |
| `AuthorityKind`, `AuthorityRef` | Represent capability or session revocation ancestry. |
| `Capability` | Persist identity, grants, binding, scope, provenance and lifetime without raw credentials. |
| `CredentialClass` | Distinguish bearer, session, console, verified peer and anonymous presentations. |
| `Principal` | Normalize credential presentations for the common policy engine. |
| `Issued` | Return a generated secret once, after the transaction commits. |
| `Grant` | Name one exact permission without wildcard implication. |
| `IssuanceKind` | Select Delegate, Award or Exchange. |
| `UnitKind` | Identify persisted trigger, plan, eval or node units. |
| `AwardPurpose`, `DeliveryPurpose` | Separate fixed grant policy from direct or bootstrap delivery. |
| `AwardPolicy`, `ParentRule` | Map grant purpose to operational unit, grants, bindings and revocation parent. |
| `DelegateSpec` | Request a subset of existing authority. |
| `AwardSpec` | Select a locked unit whose row supplies privileged authority. |
| `ExchangeSpec` | Consume a bootstrap and recover its original unit policy. |
| `ProvisionKind`, `ProvisionSpec` | Supply trusted root-origin authority to the same issuer, as the trusted root-only Delegate case. |
| `IssueRequest` | Carry exactly one issuance variant. |
| `ValidateDelegation` | Refuse widening of team, grants, binding, repo/data scope or expiry. |
| `Owner` | Select the lookup that establishes resource tenancy. |
| `BindingRule` | Select the relation between principal and requested resource. |
| `Liveness`, `LivenessPolicy` | Define live authority and narrow replay/tail exceptions. |
| `CredentialRule` | Restrict which presentation or unit can use an access alternative. |
| `AccessRule` | Combine credential rule, required grants, binding and liveness. |
| `Authentication` | Distinguish capabilities from public, webhook, OIDC, URL and browser proofs. |
| `RetryPolicy` | Give the shared HTTP client its safe retry behavior. |
| `RoutePolicy` | Attach all policy metadata to one method/path registration. |
| `Target` | Carry decoded selectors and resolved resource ownership to binding checks. |
| `Presentation` | Supply private bearer/session or verified peer authentication input. |
| `RequestEvidence` | Carry decoded target selectors and the exact result-body digest into authentication. |
| `CredentialLookup` | Hash the complete presentation without sending its reusable secret to persistence. |
| `AuthorityState`, `AuthenticationSnapshot` | Read complete ancestry, current membership, binding facts and the store clock in one snapshot. |
| `BindingState`, `ExpansionState`, `RecordedResult` | Supply current unit fences, independent expansion leases and committed result identity. |
| `AuthenticationReader.ReadAuthentication` | Load committed facts for the verifier, including the awarded team's membership and cross-team ancestors. |
| `NewAuthenticator`, `ValidateAuthority` | Verify fresh snapshots and share ancestry checks with transactional authority resolution. |
| `AuthorityResolver.ResolveAuthority` | Resolve tagged capability/session authority and recheck ancestry and membership inside the store transaction. |
| `AuthorityGeneration`, `AuthGenerations`, `AuthCacheEntry` | Bind cached credential facts to every ancestor and the awarded team's membership generation. |
| `AuthCacheEntry.Reusable` | Require a fresh complete generation vector, an unexpired credential and age strictly below the shared cache TTL. |
| `Authenticator.Authenticate` | Validate identity and ancestry using route-specific liveness. |
| `LiveAccess`, `Authenticated` | Preserve which alternatives passed liveness for the matched route. |
| `BindingChecker.CheckBinding` | Resolve relational authority in the store or sealed node-host plan. |
| `Authorized` | Carry the selected authority and fence to the handler and store transaction. |
| `Authorize` | Select a complete live access alternative or return a typed refusal. |
| `LookupRoute` | Resolve an exact registered method and router pattern. |
| `ValidateRoutes` | Refuse duplicate, missing or extra registrations and incomplete policy rows. |
| `BillingStart`, `BillingUnit` | Separate charging start from compute kind. |
| `BillingProvenance` | Freeze payer, metering, executor class, reservation, start and unit at award. |
| `UnitBilling`, `UnitBillingMappings` | Map trigger to host, node to job, plan to plan and eval to eval billing. |
| `BillingRule` | Define unit minimums and nonbillable intervals. |
| `BillableInterval` | Record actual compute under an award. |
| `BillingSettlement` | Return billable seconds and unstarted-refund status for the ledger owner. |
| `SettleBilling` | Calculate settlement from immutable provenance and persisted intervals. |
| `PaymentMode`, `PaymentConfig` | Select test/live credentials for the same provider verifier and settlement code. |
| `PaymentEventKind`, `PaymentEventPolicy`, `PaymentEventPolicies` | Enumerate automatic committed-checkout outcomes and their provider event types. |
| `PaymentEvent`, `VerifiedPaymentEvent`, `VerifiedPaymentEvent.Event` | Carry authenticated payment facts to C1 without a caller-selected team. |
| `PaymentPresentation`, `PaymentAuthenticator`, `NewPaymentAuthenticator` | Verify the exact webhook body, signature, timestamp, event type and configured mode. |
| `DataUse`, `DataScope` | Distinguish executable claims, remote reads, local publication and expansion reads. |
| `DataOperation`, `DataPolicy` | Pin the grant and edge checks for each data operation. |
| `DataTarget` | Select a digest/upload and its uploader attempt without asserting authority. |
| `DataAccessChecker.CheckDataAccess` | Call C1's receipt, read-set and edge authorization owner. |
| `AuthorizeData` | Combine route authority, sealed scope and C1's checks before signing or committing. |
| `ObjectToSign` | Supply an authorized object key, method, size, digest and expiry to storage. |
| `SignedObject` | Return one external signed URL and its required headers. |
| `DataSigner.Sign` | Give S3/CloudFront and the filesystem signer the same boundary. |
| `SourceExecutor` | Supply verified runner ownership and machine-owner consent. |
| `SourceReleaseRequest` | Combine sensitive authorization with sealed repo and executor facts. |
| `SourceReleaseAudit` | Record a credential release without its secret material. |
| `AuthorizeSourceRelease` | Refuse team credential release without repo authority and cloud/owner consent. |

| Other package API | Purpose |
| --- | --- |
| `store.(*Store).Issue` | Mint in the locked intake, claim, provision or exchange transaction. |
| `apierr.Code`, `apierr.Definition` | Define one wire discriminator and its status/documentation metadata. |
| `apierr.Reason` | Name stable forbidden details instead of adding auth envelopes. |
| `apierr.Error`, `(*Error).Error` | Carry safe code, message, hint and details through Go error paths. |
| `apierr.Envelope` | Encode the shared outer `error` object. |
| `apierr.DecodeInner` | Decode one inner error object, rejecting malformed or nested envelopes while preserving unknown additive codes. |
| `nodeproto.ErrorUse`, `nodeproto.ErrorUses` | Describe consumers with typed `apierr.Code` and `apierr.Reason` without another code registry. |
| `apierr.Status` | Select the registry's HTTP status. |
| `apierr.WriteHTTP` | Write the error envelope and Retry-After without plain-text fallbacks. |
| `apierr.WriteJSON` | Write one JSON error line for CLI stderr. |
| `blob.NewFilesystem`, `blob.NewS3` | Physical backends own object validation, transfer signing and byte verification. |
| `controller.NewCloudFrontDataBackend` | Wrap S3 downloads with CloudFront for public-ingress requests. Direct requests and uploads retain the backend signer. Physical operations and log deletion remain on S3. |
| `Server.WithS3Logs` | Configure the S3 bucket and namespace for retained log readers. |
| `controller.PaymentSettler`, `Server.WithPayments` | Supply C1 settlement to the signed Stripe callback. The server registers `/webhooks/stripe` through `PolicyRouter` and passes only verified events to the store. |
| `controller.Server.WithAuthorization` | Configure the shared verifier, bindings, transport identity and CSRF adapters before constructing the server handler. Missing adapters fail closed. |
| `controller.RouteAuthorization`, `controller.Endpoint`, `controller.PolicyRouter` | Wire identity, decoding, CSRF, external proof and authorized handlers into the matched route. |
| `controller.NewPolicyRouter`, `PolicyRouter.Register`, `PolicyRouter.Validate`, `PolicyRouter.Registrations` | Register handlers independently of the policy table and check their proof wiring. Combine controller and node-host registrations for the complete inventory check. |
| `controller.AuthorityRevoker`, `EngineController.ResolveOperator` | Reuse C1's existing RevokeAuthority transaction through the tenant or deployment scope. The optional resolver supports embedded controllers and fakes without another issuer or persistence owner. |
| `controller.RunTransactions`, `EngineController.ResolveTenant`, `EngineController.ResolveRunTeam`, `Server.WithEngineController` | Supply final intake/sealing transactions and resource ownership. Production uses the existing store methods. Embedded controllers and tests may supply the same contracts without SQL or alternate authorization. |
| `controller.SizingReader`, `controller.SizingStore`, `Server.WithSizingStore` | Supply tenant sizing reads and authority-bearing measurement transactions. Default reads use `Tenant.GetPipelineProfile`. Writes require the explicit adapter and fail closed when it is absent. |
| `controller.CredentialInventoryReader`, `Server.WithCredentialInventoryReader` | Supply the existing tenant secret/git inventory reads with an authenticated-team check. |
| `controller.CredentialRotator`, `Server.WithCredentialRotator` | Supply one tenant's authority-bearing atomic secret/git rotation. |
| `capability.OIDCFacts`, `controller.OIDCReader`, `Server.WithOIDCReader` | Supply authority-rechecked sealed OIDC identity and its complete ancestry expiry ceiling. |
| `controller.SourceAuditReader`, `Server.WithSourceAuditReader` | Supply one tenant's final committed source-release audit projection. |
| `controller.CredentialWriter`, `Server.WithCredentialWriter` | Supply tenant credential mutations with committing authority rechecks and actor attribution. Missing adapters refuse writes. |
| `controller.RunStatusFacts`, `EngineController.ReadRunStatus` | Supply one committed snapshot, complete attempt history and validated log subjects for the authorized team/run. Verified engine policy supplies notices. The default uses Tenant.ReadRunView. No C1 signature changes. |

The exported data tables are `Grants`, `AwardPolicies`, `LivenessPolicies`,
`Routes`, `BootstrapGrants`, `BootstrapUnits`, `UnitBillingMappings`,
`BillingRules`, `PaymentEventPolicies`, `DataPolicies` and `apierr.Codes`. Constants in
their files define the closed vocabulary. Implementers consume these tables
and generate references from them; they do not maintain copies in handlers,
clients, SDKs or documentation generators.

## Issuance invariants

- Tokens contain 256 random bits encoded as 43 unpadded base64url characters
  after the audience prefix, `swk_` or `swn_`.
  The lookup key is SHA-256 of the complete reference token. Raw values
  travel only in memory, a read-once private file or the protocol pipe.
- Delegate narrows its parent's team, grants, binding, repository scope,
  data scope and lifetime. Only controller-to-nodehost audience narrowing
  is allowed. The node-host memory adapter uses the same validation and
  revokes the bearer when its process exits.
- A claimed trigger owner may delegate controller expansion authority for one
  terminal source. That child carries only `expansion.submit` and/or `data.read`.
  Its binding names the source generation while `Binding.Expansion` retains
  the parent's exact trigger unit, holder and owner generation. Optional data
  scope narrows run-wide `DataClaim` to source-bound, read-only `DataExpansion`,
  preserving repository, read refs and verified-default restrictions. Here
  reservations, claim windows, deadlines and ancestry stay bounded. Persistence
  derives the selected source facts and rechecks the owner's live lease.
- Award runs inside the transaction that owns the claim. `ClaimGrants` is
  any-of. Request fields select a unit; they cannot assert its team, holder,
  generation, pools, billing, repo or ref. Only `claims.launch` may award
  across teams. Work grants are exactly the frozen work table.
- Claim awards parent to the claimant root. `ParentClaimantRoot` stops at
  the nearest here Award when present, otherwise the registered root. It
  never skips the here Award to reach the requester's token or session.
  A session parent is checked in the sessions table, including membership
  and current `runs.write`; it is not converted to a new bearer root.
- Here is an Award during intake. It reserves `here:<run>`, allows the
  first claim within ten minutes, and expires at the owner deadline plus
  the claim window, capped by the parent. Reclaim after a first claim uses
  the existing reservation and expiry, not a new ten-minute deadline.
  Sensitive access before the trigger is claimed is refused. Pool-restricted
  secrets remain refused on here machines.
- Operational kinds are only trigger, plan, eval and node. `PurposeWork`
  selects the node grants. `PurposeHere` selects the trigger intake Award.
  `DeliveryBootstrap` keeps the underlying `Unit`, `UnitID`, `Purpose`,
  binding and billing. Its grants are only `source.read` and `claim.exchange`.
  Only launcher authority may bootstrap a plan, eval or node unit. A plan
  bootstrap has a run binding. Node and eval keep their unit bindings.
- Exchange atomically revokes the bootstrap and issues the original unit's
  fixed grant set. The new parent is the bootstrap's parent. A concurrent or
  repeated exchange cannot produce another live credential. A lost exchange
  response is handled by claim recovery, not a reusable bootstrap.
- `limits.Child` bounds every derived expiry by parent and binding deadline.
  Local runs introduce no execution deadline. Store timestamps use nanoseconds.

## Route and liveness invariants

The order is router match, authentication with route liveness, authorization,
then handler. `Authenticated` is tied to that method and pattern. `Authorize`
may select only an alternative listed in `Authenticated.Live`. It cannot
borrow replay permission from one alternative and grants from another.

Unknown, expired, revoked or ended credentials get `401 unauthenticated`.
A live credential lacking audience, grant or binding gets `403 forbidden`
with `details.reason`. A transaction that loses a previously valid fence
gets `409 lock_held` or `409 illegal_transition`. Cancellation refuses
sensitive release while preserving reporting. A missing policy is a 500
and a registration-test failure. A genuinely absent route is a 404.

All classes check parent revocation, expiry and membership. Result liveness
admits live reporting and, after termination, only replay of a recorded result
with identical actor, generation and digest. Log-tail liveness admits live
uploads and, after termination, only the final attempt within the existing
`limits.LogSealGrace` measured from persisted attempt finish, with no newer
attempt or claim generation. The store repeats this check before appending or sealing. Expansion liveness checks its
owner/eval lease and controller ownership where applicable, the immutable
source generation and uncancelled run. The transaction checks expected state
for a first expansion and permits an identical accepted-body replay while
authority remains live. The grace duration
and deadline arithmetic remain C4's responsibility.

| Credential rule | Required meaning |
| --- | --- |
| `CredentialAny` | A live principal still subject to every grant and binding check. |
| `CredentialMember` | An unbound member token/session, verified local peer or configured loopback anonymous principal with explicit local grants. A here award is excluded. |
| `CredentialAccount` | A signed-in account session; token possession alone cannot consent for an owner. |
| `CredentialClaim` | An awarded or narrowed unit capability with its applicable unit lease. |
| `CredentialEval` | A directly delivered or exchanged eval-unit capability, including narrowed descendants, with a live own eval lease. Bootstrap and owner-delegated expansion authority do not match. |
| `CredentialHere` | The here Award for this run, with the requester's live `runs.write`. |
| `CredentialLauncher` | A service root with `claims.launch`, checked across teams only for claim selection. |
| `CredentialDeployment` | An operational deployment-admin bearer; licensed self-hosters cannot use this path. |
| `CredentialOperator` | An operator-console session with CSRF checks on mutations. Deployment tokens cannot satisfy it. |
| `CredentialNodeProcess` | A nodehost-audience bearer bound to this process and attempt. |
| `CredentialPublisher` | User authority with `DataPublish` bound to this published local run. Executable claims cannot match it even if they have `data.write`. |
| `CredentialRepoReader` | User authority with `DataRemoteCache` and an existing repo-bound `data.read` grant. |
| `CredentialDiscovery` | A live ordinary root, session, local peer, Actions credential, here, trigger, plan or work credential. Bootstrap and eval grants do not open general discovery. |

| Binding | Required meaning |
| --- | --- |
| `none` | No extra resource relation; audience, credential rule, tenancy and liveness still apply. |
| `run` | The resolved resource belongs to the capability's run. |
| `ownNode` | Path run and job equal the bound run and job; attempt/generation come from the capability. |
| `runOfTrigger` | A trigger owner reaches its run's jobs; store holder rights still reject another holder's row. |
| `bodyNamesRun` | Decode and bind the body's run/parent lineage before handler execution. Member intake may omit a parent. A bound job must name its own parent run and job. `Target.NodeID` carries the body parent job on intake. |
| `pipelineOfBoundRun` | The requested pipeline equals the sealed bound run's pipeline. A bare machine or Actions root cannot read sizing through claim authority. |
| `childOfBoundNode` | The child was created by this bound job; cross-run reads follow the declared child edge. |
| `slotOfBoundNode` | Body/query holder identifies the authorized run/job/generation and declared concurrency key. Plan holders require the run owner. No admin bypass exists. |
| `readSet` | Dependencies, declared refs, a memo hit or expansion source grant establish the edge. Same-team or same-run membership alone grants nothing to a claim. |
| `declaredSecret` | The sealed plan declares the secret; `only_on` is in awarded pools, and here restrictions apply. |
| `source` | The live claim owns this sealed repo and may release source before execution under existing mint budgets. |
| `data` | The capability's use, repo/ref/run/job/attempt scope and C1's read-set or receipt checks permit this operation. |
| `expansion` | This owner/eval authority targets the terminal source job and generation. |
| `claimSelection` | Match the selected row against repo/push/pool authority. Here can select only its reserved run; a trigger's node selection stays inside its run. |
| `ownToken` | The account owns the token or has team-admin authority; new roots cannot exceed the minting authority. |
| `machineOwner` | The authenticated machine owner is the account consenting to credential release. |
| `selfOrTeamAdmin` | A member removes only itself unless it has team-admin authority. |
| `ownCleanup` | The cleanup belongs to this node-host process and attempt. |
| `planOwner` | A plan claim or live trigger owner submits its run's plan. A work claim cannot. |
| `ownClaim` | The path claim ID equals the awarded unit ID, including its generation and holder. |
| `ownEval` | The path claim ID and resolved log subject name this eval unit, its run, holder and claim generation. UnitSourceNodeID and UnitSourceGeneration match the immutable expansion source. The source job is never a job-attempt log subject. |

`BindingChecker` performs these relational checks using C1's store data or
C3's sealed local plan. Handlers perform no authorization. Store mutations
repeat the relevant checks under the transaction lock. Local peers and
anonymous loopback mode must self-claim execution like every other host.
Windows peers need a verified SID; proximity to a local socket is not proof.

Job state updates use one `/jobs/{job}/transition` route. Plan insertion uses
`/runs/{run}/plan`; generation uses `/jobs/{job}/expansion`. Published local
runs may submit the plan through the publish-only alternative, then results,
segments and finish. They acquire no executable owner claim. C1 applies the
same plan and blob checks and records local provenance.

Owner, plan and eval logs use C1/C3's shared `blob.LogSubject` with the
unit branch, distinct from job-attempt logs. The controller resolves that
subject into `Target` unit fields because capability cannot import blob
without cycling through runspec. Plan claims use `log.tail` on
`/claims/{claim}/logs`, `log-segments` and `log-manifest`. Eval uses a separate
`CredentialEval` alternative requiring `expansion.submit`, `ownEval` and
`LiveExpansion` on those same routes and heartbeat. Eval keeps exactly
`expansion.submit` and `data.read`. Neither a sibling eval nor a run owner's
expansion delegate can use this log alternative. Log submission precedes
lease-ending completion. A structured eval failure goes through the single
`AcceptExpansion` transaction and `/jobs/{job}/expansion`, like success.

Remote owners read `GET /runs/{run}/snapshot` and submit
`POST /runs/{run}/jobs/{job}/reconcile`. Both use trigger-run bindings.
The reconciliation source is a proposal. C1 checks the source read edge and
persisted outcome in its transaction. The wire carries no actor or grants.

Public and external-proof rows remain explicit. GitHub webhooks require HMAC
and replay-key deduplication. Actions OIDC establishes push scope, not verified
default-branch trust. Browser-flow authentication includes the existing
session, origin, CSRF, nonce and account-link rules. Signed object routes
check the URL's exact key, method, expiry, size and digest as applicable.
They do not accept a capability as a substitute for the signature.

Authentication caches have a five-second maximum age, owned by
`limits.AuthCacheTTL`. Every reuse reads C1's current `AuthGenerations` and
requires equality for the full capability/session ancestry and the awarded
team's membership generation. The ancestry includes cross-team launcher
roots. Missing ancestors, changed generations or a failed generation read
refuse cache reuse. Revocation and membership changes advance the affected
generations in the same transaction. Expiry, route liveness and binding fences
are rechecked on each request. TTL never substitutes for generation checks.

### Authentication read boundary

`AuthenticationReader` is the persistence seam. It returns one committed
snapshot with the store clock, leaf-to-root authorities, current membership
verdicts and the leaf's binding facts. Lookup uses the SHA-256 of the complete
bearer or session value. The reader never receives that value. Peer lookup
requires an identity verified by the transport. Anonymous authority requires
explicit local configuration in the reader.

Each authority carries its current permitted grants and membership in its own
team. A cross-team launcher stays in the ancestry, and the awarded team's
membership is checked separately. A missing authority, incomplete chain,
cycle, expiry, effective revocation or membership loss refuses authentication.
The original here Award also requires its immediate requester's current
`runs.write`. Delegated here descendants preserve that check through the chain.
`ValidateAuthority` supplies these checks to the transactional resolver too.

The verifier reads a fresh snapshot for each request. It does not cache
authority or liveness. An adapter that later caches facts must still satisfy
`AuthCacheEntry.Reusable` and read fresh liveness on every request.

`Presentation.Evidence` supplies decoded selectors and the exact request-body
SHA-256. The verifier supplies method and pattern from the matched route.
Replay requires the committed actor, operation, resource, generation, attempt
and digest. Final job log flush also requires the unchanged fence, unsealed
stream and finish-based grace. An eval capability, including its bootstrap,
uses its own lease and controller ownership lease independently of its terminal
source. Cancellation refuses sensitive operations while live reporting drains.
An unclaimed here Award can discover and claim during its first-claim window.
Once claimed, its reservation permits reclaim without a new window.

`PolicyRouter` applies the boundary after matching a real handler registration.
It returns decoder errors only after authentication because resource lookup
failures must not disclose existence to dead credentials. Authorization headers
take precedence over session or peer fallback. Session and console mutations
require a CSRF verifier. External routes require their declared proof verifier.
Missing policy, wrong proof wiring and missing dependencies refuse the request.
Unknown routes and unsupported methods use the shared JSON error object.

`PolicyRouter.Validate` checks each registered endpoint. The complete inventory
check combines the controller and node-host `Registrations` and calls
`capability.ValidateRoutes`. A production adapter must supply both checks.

These additions are local C2 contract extensions. C1 must implement the reader
against its transaction and sealed records. They introduce no storage tables,
second issuer, credential cache or compatibility adapter.

## Data and source release

`/api/v1/data/{download,upload,commit,has-many,records}` uses controller
capabilities. No cache audience, `swcg` acceptance, `cache.grant` or
`storage.count` grant remains in the target.

Before a download or HasMany answer, C1 proves every digest is in the read
set. `GET /data/records` uses `DataLookup`, selecting an authorized record
kind/scope/name with `data.read`. A commit verifies bytes, records the uploader's job/attempt/generation
receipt, and rechecks authority. Record creation checks every reference edge.
A receipt owned by job A cannot authorize unrelated job B's manifest.

Scope derives from sealed repo/ref facts. Typed repositories grant nothing.
Vouched writes use `manual:<ref>`. Trusted fallback entries require verified
App provenance. Remote-cache users read only authorized default-branch memo,
artifacts and depcache, never write through that opt-in, and never fetch
pipeline binaries. Published data stays local provenance under
`trust_local_builds`. Only trusted cloud builders assert cloud provenance.

Generic record HTTP selectors and public response metadata preserve that namespace:
`manual:<ref>` decodes to RefScope{Ref: <ref>, Verified: false}, and a plain ref
decodes to Verified: true. Neither selector inherits the current write's trust.
The existing read-grant check still precedes record lookup.

Signing follows authorization and atomic quota reservation. PUT signing binds
size and SHA-256; commit verifies them before the immutable key becomes live.
Data GET URLs last 60 seconds. Logs use batches of sixteen segment PUT URLs
and one manifest URL, valid fifteen minutes. Log reads stay controller-proxied,
reauthorize every read, meter exact egress and remain exempt from the download
cap. There is no `kind=log` data download. C4 supplies these limits.

Team git release consumes an `Authorized` sensitive decision, then requires
the same team's cloud runner or explicit consent from the machine's owner.
The sealed repo must be authorized; SSH host confirmation is mandatory for
SSH credentials. C1 rechecks the live claim and consent and commits
`SourceReleaseAudit` before releasing bytes. A failed audit write releases
nothing. App-token issuance still requires a live authorized claim.

Revocation stops new controller operations. It cannot recall existing GitHub
tokens, OIDC/provider sessions or signed URLs. Root plus privileged Docker on
a team-dedicated node and same-uid access to runner credentials remain accepted
residuals. This contract makes no process-isolation claim.

## Billing

Award freezes payer team, metered status, executor class, reservation, billing
start and billing unit. `UnitBillingMappings` is the only operational-to-billing
classification table. Here retains trigger-to-host billing and bootstrap
exchange retains the underlying unit's classification. Launcher awards are
metered. Other awards copy the root's
operator-controlled setting. A claimant already executing starts at award.
A pod reservation starts on the pod's first qualifying renewal/start.
Exchange preserves provenance; settlement never looks at the current token.

Exclude customer-owned runner wait intervals, including a metered run host
waiting for a customer pool. Plan and eval compute bill per second with no
minimum. A plan-cache hit is free. Executing jobs keep their sixty-second
minimum. An unstarted award refunds in full. Intervals must not overlap or
charge the same compute as both host and job. C4 owns
`limits.WorkBillingMinimum` and the separate
`limits.HostBillingMinimum`. Host orchestration keeps its existing minimum
after wait exclusion. Plan/eval stay separate, zero-minimum compute. C1 applies
the calculated result to its durable ledger and
keeps charges when operational runs are deleted. Stripe test/live selection
uses `PaymentConfig.Mode`, `APIKeyFile` and `WebhookSecretFile`. The private
files are loaded through C4's credential delivery boundary. Both modes use
the same implementation, callback path, event inventory and C1 settlement.

Controller startup selects `PaymentConfig.Mode` with `--payment-mode` and loads
`stripe-api-key` and `stripe-webhook-secret` from its credentials directory.
A mode without both private files, or files without an explicit mode, refuses
startup. Unconfigured callbacks return `unavailable`. The callback uses the
existing JSON body limit and acknowledges only after settlement returns.
Repeated deliveries reach C1 so its durable deduplication owns the verdict.
The caller-selected paid-grant HTTP route and its request type are deleted.

The sole automatic settlement callback is `POST /webhooks/stripe`, classified
as `AuthPaymentWebhook`. The verifier authenticates the exact body and the
`Stripe-Signature` header, checks the signature timestamp and configured mode,
and normalizes only `PaymentEventPolicies`. A paid
`checkout.session.completed` or `checkout.session.async_payment_succeeded`
may settle credit. An unpaid completed session stays pending. Async payment
failure and expiry close unpaid checkouts without granting credit. Unsupported
authenticated events produce no settlement event.

C1 consumes `VerifiedPaymentEvent`, looks up its provider session ID against a
committed checkout, and derives the team and credit amount from that row.
It checks mode, currency, amount and payment identity before one atomic ledger
write. Event-ID deduplication and checkout/payment identity prevent repeated
or distinct delivery events from crediting twice. Zero or unknown proof is
refused. A browser return URL, metadata team, or arbitrary service bearer
cannot settle credit. Human grants, refunds and freezes retain console-only
routes. C1 retains these financial facts independently of run retention.

## Error object

HTTP, `/node/v1`, protocol errors and CLI JSON stderr use
`{"error":{"code":"forbidden","message":"...","details":{"reason":"..."}}}`.
Protocol replies add `reply` and `ok` beside that same error member. Optional
`hint` and `details` are omitted when absent. Messages and details are masked
before encoding. Identifier refusals name a field/path rather than echoing
an identifier containing a secret. Error text uses C4's eight-KiB masked
head/tail limit. C3 imports `apierr.Code` and `apierr.Error`; it must not add
another codes registry. `unknown_job` replaces the customer-visible node name.
All four protocol error fields use `*apierr.Error`. `ParseRequest`,
`ParseFrame` and `Reply.Decode` use `apierr.DecodeInner(data []byte)` for the
inner object. Malformed and double envelopes fail decoding. Unknown additive
codes remain readable by older engines.

`ErrorUses` carries typed codes and reasons. Undeclared secrets, missing
dependency edges, undeclared references and unauthorized child repositories
use `forbidden` with their respective reason. Missing resources use
`not_found`. Unavailable secrets, upstreams and OIDC providers use
`unavailable`. Unready output uses `conflict` with `not_ready`. Rate limiting
uses `rate_limited`. A runtime child cycle is `bad_request` with `child_cycle`.
None of these conditions introduces a second wire discriminator.

## Acceptance evidence

The local tests prove policy-table consistency, console-only privileged rows,
publish isolation, own-job input, owner-only plan submission, delegation
narrowing, cache-generation comparison, source consent, billing arithmetic,
payment proof, error encoding and signed-object constraints.

`TestCredentialPolicyAlternatives` exercises 324 complete alternatives across
209 capability-authenticated route declarations against T1's independent
literals. It covers positive selection, missing grants, wrong audience, absent
live evidence and propagated binding refusal. Console alternatives also refuse
deployment bearers and local peers. Its binding checker is a fixture, so this
test establishes no store relations, HTTP/socket behavior or actual router
registration coverage. The cross-placement scenario skeletons remain skipped.

`TestCredentialHTTPAuthenticationBoundary`, `TestCredentialHTTPTerminalGrace`
and `TestCredentialHTTPCancelDrainsReporting` run the real verifier, router and
policy evaluator against a fake committed-fact reader. They assert successful
handler entry, the 401/403/409 split, ancestry and membership refusal, path and
audience isolation, result-body matching, log grace and cancellation draining.
Their fake transaction refusal proves HTTP propagation of 409, not a database
race. The fake binding checker establishes no persisted resource relation.

Package tests cover here first claim/reclaim and requester grant loss, eval
bootstrap source/exchange under its own lease, node-host process exit,
malformed references, CSRF, external-proof wiring and safe internal errors.
An independent review found the eval bootstrap liveness defect and preclaim
here discovery refusal. Both have regression coverage.

`TestDataRefScope` covers the data authority boundary, with edge decisions
supplied by a fixture. Manual-ref delegation cannot widen to the verified ref.
The full old cache-ref and credential-matrix ports still require store and
controller integration. None of those parked findings is declared closed by
these boundary tests.

The filesystem transfer URL signs method, key, size, SHA-256 and expiry with
HMAC-SHA256. Its upload path names the server-issued ticket. W7 owns the signer and transfer verifier. Controller-local duplicate signer types are removed.
The blob owner retains the separate upload/receipt identity and commits only
after checking the bytes. S3 uses the same constraints with SigV4. Data GETs
are bounded by `limits.DataDownloadURLTTL`, and PUTs by
`limits.DataUploadURLTTL`. Log authorization and proxying remain separate.

The payment verifier reads owner-only files through `fssecure`, checks key mode,
and verifies the exact body using Stripe's documented v1 HMAC and five-minute
timestamp tolerance. Both test and live configurations use the same verifier.
The tests use synthetic signed events and make no Stripe API requests.
[Stripe's signature specification](https://docs.stripe.com/webhooks#verify-manually)
defines the provider algorithm. Durable payment deduplication remains in C1.

T1 should implement these behavioral scenarios against the running contracts:

| Scenario | Required proof |
| --- | --- |
| Credential matrix | Every route with every principal; own job, sibling job, other run/team/pipeline; HTTP and api.sock share verdicts. |
| Liveness matrix | Cancel, expiry, revocation, membership loss, owner lapse, result replay, log-tail grace and stale transaction race return the specified codes. |
| Issuance | Delegate cannot widen; launcher awards cross-team from the row; normal claimants cannot; pools and billing ignore caller assertions. |
| Here lifecycle | Award at intake, first-claim window, reclaim, requester revocation and loss of runs.write; restricted secrets remain refused. |
| Bootstrap | Source-only init access; concurrent exchange yields one winner; revoked bootstrap cannot issue again; plan/work/eval ancestry and bindings survive. |
| Protocol bearer | Both SDKs receive only nodehost authority through hello; process exit revokes it; controller calls with it fail. |
| A68 regressions | Trigger can report its own unheld job; a foreign holder blocks it; body parent, sizing, slot and claim/input paths cannot escape their bindings. |
| Expansion | Owner/eval reads its terminal source and submits once; stale source generation, cancel and owner lapse refuse. |
| Blob authority | Foreign-team, foreign-run and unrelated same-run digests fail downloads, HasMany and manifest attachment. |
| Cache and publish | Manual refs cannot poison verified refs; default-only local reads remain read-only; publication remains local provenance and does not fail the local run. |
| Source release | Same-team cloud or machine-owner opt-in only; repo allow-list and SSH confirmation apply; audit failure releases no secret. |
| Operator split | Deployment tokens cannot trust, grant, freeze, refund or delete accounts, including by delegating or using a local peer. |
| Billing | Immutable operational-to-billing mapping through here/bootstrap, unstarted refund, plan/eval per-second compute, free plan-cache hits, customer wait exclusion and no double charge. |
| Payment callback | Signed test/live events use one verifier, bind to the committed checkout, derive team and grant once across retries and distinct duplicate events. Invalid proof, wrong mode/amount/currency, unknown checkout and human console powers remain refused. |
| Auth cache | Both TTL and fresh full-generation equality are required. Ancestor/session revocation, cross-team launcher revocation and membership loss invalidate reuse. |
| Windows | SID-verified local subjects, private-file ACLs and equivalent refusal semantics. |

`TestOperatorHTTPRefusesNonConsoleAuthority` exercises thirteen actual Server
registrations with the real verifier and fake committed facts. It refuses deployment
and delegated bearers, ordinary sessions, local peers, dead sessions, wrong audiences,
missing grants and missing CSRF proof. `TestOperatorHTTPCommitsAuthorizedActions`
checks grant replay, trust, freeze/unfreeze and their authenticated actor IDs against
SQLite, with and without the dashboard wrapper. Console handlers receive `Authorized`
and use its subject ID for attribution. They no longer read the legacy principal.

Five deployment-admin entry points are removed: account/team deletion, GitHub App
installation unbinding and credit-settings writes. Their
replacements live under `/api/v1/operator`. No aliases remain. The handler-policy
lint checks every function that takes `capability.Authorized`. Legacy handlers still
need migration before that lint can cover the complete server.

`WithAuthorization` is the controller wiring boundary. W1 must supply the committed
reader and bindings. The application must supply verified session/peer identity and
session-bound CSRF checks. The old `WithOperatorAccounts` list still gates dashboard
pages, but it does not replace those API dependencies. Authentication fixtures for
legacy handler tests are test-only adapters and establish no C1 snapshot guarantee.
The existing store credit-summary/settings and session methods still query the removed
`sparkwing_meta` table. Their real integration remains open. Console refunds and runner
metering also remain outside this cutover.

Ten deployment endpoints now use the same router: limits and signups require
`operator.limits`; object-store settings, quotas, usage and breaker controls
require `operator.object_store`. Both groups also admit a console session.
The ordinary storage view no longer includes deployment totals. Usage-metrics
and repo-dispatch routes, handlers and obsolete response types are deleted.
The deployment paths have no legacy aliases.

`TestDeploymentHTTPAuthorization` covers 86 refusal cells on these actual
registrations. The breaker positive cases use both credential alternatives.
`TestOperatorSettingsHTTPRoundTrip` uses the real verifier with fake settings
persistence to check successful reads/writes, quota and allowance preservation,
path-derived team selection and authenticated signup attribution. Invalid bodies
leave the fixture unchanged. Store failures remain private; a typed transaction
refusal reaches HTTP unchanged. These fakes do not establish transaction safety.
Seven breaker tests and two request-budget projection tests now exercise the
policy router without sockets, preserving budget, ceiling, reset and thaw
behavior. No additional full T1 port or
cross-placement scenario is claimed complete.

The private `operatorSettingsStore` interface lists existing store methods;
production uses `*store.Store`, while tests supply fakes. This is the added
mechanism: it isolates HTTP behavior from unfinished settings persistence
without adding a second persistence implementation or changing C1 signatures.
W1 still needs to replace `sparkwing_meta` queries, old compute-usage columns
and principal-keyed storage SQL with the final schema. Quota route selectors
now pass the team slug. W1 must also supply authority-bearing atomic settings
mutations, including a single signup update for mode and limits, and derive
audit identity inside that transaction. Those C1 contract changes remain open.

This cut also updates shared callers in `pkg/controller/client/admin.go`,
`pkg/controller/client/objectstore.go`, `cmd/sparkwing/help_registry.go` and
`cmd/sparkwing-controller/main.go`. The old launcher test seeds its dispatch
input through the store because the HTTP mutation is removed. API generators,
OpenAPI, source docs and their embedded mirrors follow the actual endpoint
registrations. The generator reads grants from the policy table after finding
a handler registration; it does not generate registrations from that table.

The legacy signup integration fixture fails before handler entry with
`no such table: tokens`; it still creates the deleted credential type.
It is unverified integration, not evidence against the fake-backed HTTP tests.
The deployment cut's extra shared test edits are
`internal/runners/launcher/launcher_test.go` and
`internal/apiroutes/apiroutes_test.go`. Source documentation edits are
`docs/api-reference.md`, `docs/auth.md`, `docs/cli-cluster.md`,
`docs/observability.md`, `docs/self-hosting.md`,
`docs/migrations/_unreleased.md`, `CHANGELOG.md` and `api/openapi.yaml`, with
the matching `pkg/docs` copies. No persistent store contract was changed.

`EngineController.CreateRun`, `AcceptPlan`, `AcceptExpansion`, `IntakeForStore`
and `ExpansionForStore` have implementations. Intake preserves the tagged
authority and idempotency key, derives actor and team, and removes caller App
proof. Sessions supply no capability-prefix fence. Plan acceptance reads the
persisted first seal and passes both author declaration layers unchanged.
The store supplies the acceptance clock and revalidates settings. API intake
and plan registrations still use their legacy handlers until the wider cutover.

`POST /api/v1/runs/{run}/jobs/{job}/expansion` is registered on the actual
server through `PolicyRouter`. Its decoder resolves team and source generation
from store reads. The adapter compares that generation with the capability and
body, derives the owner/eval lease actor, and sends success or structured
failure to `Tenant.AcceptExpansion`. Generated job responses use committed
declarations, hashes and settings. Empty generated lists encode as `[]`.
Bad persisted declaration bytes fail rather than yielding an empty job.

Expansion actor authority remains the capability reference. Its
`ActorRunHost` carries the live expansion lease holder/generation and an
`OwnerTrigger` or `OwnerEval` reference, including the separate source
generation. Plan claims use `ActorHolder`. Trigger owners use `ActorRunHost`
with their trigger owner reference. Publishers carry their tagged authority
without an executable owner. C1 resolves these references again in its
transaction. No persisted contract or actor kind was added.

The expansion binding now checks the exact source job as well as the run.
The HTTP refusal test reproduced a sibling-source acceptance when the
relational checker returned success. The policy shape check closes that gap.
Migrated route groups also send unsupported methods through `PolicyRouter`,
which returns the shared JSON 405 and `Allow`, instead of legacy fallback.

This is also a local C2 contract repair. Generic binding delegation rejected
the owner's trigger-to-source narrowing and treated the source generation as
an owner-generation replacement. `ValidateDelegation` now admits the precise
owner expansion case described above. All other binding/data changes keep
the ordinary subset checks. Unit tests refuse foreign leases, sources, refs,
repositories, writes, reporting grants and widened time/reservation bounds.
The HTTP owner case validates that narrowing and retains its full ancestry.
No C1 signature changes are needed. C1 must apply this same validator in
`Store.Issue` and derive the selected source facts from its records.

`TestEngineExpansionHTTP` covers 27 real-server cases with fake committed
authentication, resource reads and transaction results. Its successful cases
include owner/eval authority, empty success, structured failure and replay
projection. Refusals cover revoked ancestry, leases, cancellation, source
selectors, stale generations, malformed errors, caller actor fields, trailing
JSON, hidden lookup errors and corrupt persisted declarations. Four additional
method checks assert JSON 405 and `Allow: POST`. Adapter tests cover tagged
session/capability intake, one-time bearer projection, first-seal/declaration
preservation and session-backed publication. A fake transaction refusal proves
HTTP propagation, not a database race. No additional full T1 port or placement
scenario is claimed complete.

The added mechanism is `RunTransactions` with tenant and ownership resolver
callbacks, plus `WithEngineController`. Direct calls to the unfinished
`*store.Tenant` methods panic, so this boundary lets tests exercise the real
adapter against those exact method signatures. The production default uses
`Store.ForTeam`, `Operator.RunTeam`, `Tenant.ReadRun`, `ReadNode`, `AdmitRun`,
`AcceptPlan` and `AcceptExpansion`. These persistence implementations, the
committed authentication/binding adapters and W9's fetched-engine client
remain real-integration points. No second issuer, store or credential cache
was introduced.

The expansion cut touches shared `internal/apiroutes` code/tests so generated
references discover the actual engine endpoint declaration. It updates
`api/openapi.yaml`, `docs/api-reference.md`, `docs/auth.md`, `CHANGELOG.md`
and their embedded copies. The five replaced adapter stub bodies and their
comments remove 31 lines from `engine.go`. No legacy credential
subsystem was deleted in this slice.

Expansion/delegation verification passes the whole-repository build, scoped
vet, all capability/API-error/acceptance/API-generator tests, and the focused
controller policy-router, settings, signing and handler-policy tests. The
comment policy check passes. Commands use `GOWORK=off`,
`GOFLAGS=-buildvcs=false`, and at most four test-package workers. Tests use
fake committed facts and transactions where C1 bodies are stubs. Native
transport, real database mutation and placement scenarios remain unverified.
The full legacy controller suite and Sparkwing pipelines were not run.

The registered-route comparison must use actual rewritten router registrations,
not a list generated from `Routes`. A separate handler-policy lint must reject
authorization calls inside handlers. T1 owns runtime scenarios; C2's tests
exercise only available contract data and functions.

### Engine intake and lifecycle cutover

The running server registers intake, plan, finish, cancel, retry, run renewal,
job transition and job renewal through `PolicyRouter`, alongside expansion.
Each uses the shared controller wire. Intake preserves the idempotency key and
returns its committed seal, number and one-time here bearer. Plan acceptance
preserves the first seal even when a retry's embedded run ID names its source.
Plan publication uses `ActorOperator` and acquires no executable owner.

Lifecycle adapters derive tagged authority, holder, generation and owner
references from `Authorized`. Job results preserve authorized record keys.
Owner finish calls `FinishRunAndUnit`; publication calls `TransitionRun` with
`RunPublish`. Cancellation records member authority on `RunCancel`. Retry
names its source through `AdmitRun`, which owns source-seal loading. Renewal
uses `RenewOwner` or `RenewNodeClaim` and returns the committed expiry,
cancellation and watched jobs. The store owns all clocks, events and replay.
Typed stale-generation/holder refusals map to `lock_held`; illegal/terminal
edges map to `illegal_transition`. Private store failures remain private.

Result-route decoders hash the exact body before authentication. This supplies
`LiveResult` with actor, route, resource, fence and digest evidence before a
terminal replay can reach its transaction. Every body uses JSON, rejects
unknown fields and trailing objects, and cannot replace its actor or fence.
`apierr.Error.UnmarshalJSON` shares `DecodeInner`, including duplicate-key and
nested-envelope refusal. Unknown additive error codes remain readable.

This cut also repairs the local C2 `bodyNamesRun` shape check. A node-bound
credential creating a child must match the parent job as well as the parent
run. A real-server test reproduced sibling-parent intake when the relational
checker returned success. `Target.NodeID` already carries that decoded parent
selector, so no C1 signature was added.

`RunTransactions` now includes the five existing lifecycle/renewal methods
used by these adapters. A single fake supplies their exact C1 signatures.
The collection's existing GET list keeps its separate registration; intake
registers only POST there. Other migrated paths route unsupported methods to
the shared JSON 405. The old run upsert, repository-binding helper, plan
snapshot/acceptance handlers, inline finish follow-ups, cancellation, retry
and run heartbeat handlers are removed. `DaemonHandler` is removed and its
sole orchestrator caller uses `Handler`.

The removed legacy handler/route files contain 360 deleted source lines.
Five replaced lifecycle adapter stubs remove 22 more lines. The removed
inline-finish tests contain 192 lines. Their completion-effect responsibilities
belong to C1's durable effect transaction and reconciler; the new finish test
proves the joint transaction boundary, not delivery after a disconnect.
No full T1 port or placement scenario is claimed complete.

New HTTP proofs cover 16 intake cases, 14 plan cases, 56 lifecycle cases,
five malformed inner errors, 12 terminal replay cases, four typed transition
refusals and 28 method refusals. Additional adapter assertions cover publisher
actor classification and inherited retry seals. These use real server routers
with fake committed authentication and C1 transactions. They do not establish
native transport, SQLite/Postgres races, completion effects or runtime parity.

Contract/integration work still needed:

- C1 must make HTTP result digests reproducible in persisted replay evidence.
  `RequestEvidence.ResultDigest` hashes exact HTTP bytes, but `NodeTransition`,
  `PlanAcceptance` and `FinishRunRequest` cannot carry those bytes or their
  digest to the transaction. C1/C2 need one agreed digest codec or an explicit
  transaction field before the fake replay proofs become persistence proofs.
- Plan/eval units need a final renewal transaction and claim ownership lookup
  for `/claims/{claim}/heartbeat`. C2 implements the declared trigger/job
  renewal methods. `RenewalRequest.Executing` has no C1 parameter; those
  methods derive execution from stored state. The wire/store owners must
  decide whether the observation is needed.
- C1 must implement the transaction bodies and committed authentication,
  bindings and source-provenance adapters. W9 must connect the engine client
  and remaining old lifecycle callers to the shared wire. Browser/session,
  verified-peer and CSRF adapters still need production wiring.
- Claims, reconciliation, run detail, snapshots, compatibility and the
  remaining controller routes still need their cutover. Legacy credential
  auth and node-write routes remain. Full T1 scenarios stay skipped.

Shared changes are the one-line `internal/orchestrator/wingd_api.go` caller
migration, `.apidiff/pkg_controller.txt`, OpenAPI, API/auth documentation,
changelog and generated mirrors. API snapshots outside the controller already
drift from their committed baselines; their generated edits were restored
because this cut owns only the controller snapshot. No new configuration,
issuer, store implementation or cache was added. The lead owns independent
review, native integration, Sparkwing pipelines and launch checks.

Verification passes the repository build, scoped vet, capability/API-error
tests, focused controller tests, all executable acceptance proofs, API generator
tests, OpenAPI drift, comment policy, documentation symbols and embedded mirrors.
The full `pkg/docs` run cannot bind its web-client test sockets in the sandbox.
Its symbol, changelog and mirror checks pass separately. The full legacy
controller suite, native transports and Sparkwing pipelines were not run.

### Job reporting cutover

Seven reporting endpoints use `PolicyRouter`: job and step annotations and
summaries, plus step start, finish and skip. Their paths use `/jobs/{job}`.
The old `/nodes/{nodeID}` registrations and seven handler bodies are deleted.
Annotation and summary bodies retain `step_id`, `message` and `markdown`.
Annotations also accept the C1 level, file and line fields. Step lifecycle
uses the shared StepReportRequest and StepRecorded contracts described below.

The adapter passes tagged authority and holder/generation fences to C1's
existing `WriteAnnotation`, `WriteSummary` and `RecordStep` signatures through
`RunTransactions`. Job claims supply their bound attempt. Trigger owners and
publishers load the persisted job attempt without replacing their own actor
or owner fence. The captured source_attempt must match that attempt before
RecordStep runs. An unstarted job returns illegal_transition. Optional reported
timestamps retain nanoseconds. The store owns timestamp defaults, replay and
the final recheck.
Annotations and summaries enforce `limits.AnnotationBytes` without truncation.

The HTTP tests cover all three allowed authority alternatives, cancellation,
revoked ancestry, missing grants, sibling jobs, caller actor fields, typed
transaction refusals, malformed requests, exact text bounds, method refusals
and removed paths. A request-body checkpoint changes the fixture generation
after authentication. The original fence reaches the transaction and its
refusal reaches HTTP. This is a fake-backed boundary proof, not a database
race proof. Annotation/summary clients and the typed engine step writer reach
these handlers over an in-memory HTTP transport. The old standalone cancellation test is replaced
by the new step-finish case. No additional full T1 scenario or native placement
cell is claimed complete.

Real integration still requires C1's transaction bodies, committed auth and
binding readers, and production session/peer/CSRF adapters. C1 must verify
annotation and summary attribution across application retries: their request
contracts carry actor authority/generation but no expected attempt, unlike
`StepReport`. The store/wire owners must confirm whether the persisted
capability binding is sufficient or add that fence. No C1 signature is changed.

Shared edits are the seven path strings in `pkg/controller/client/client.go`,
the classification strings in `internal/orchestrator/wingd_api_routes_test.go`,
and removal of the retired breaker read path from `internal/orchestrator/wingd_api.go`.
The classification check also exposed stale entries from the earlier operator
and lifecycle cutovers. They now match the registered paths. API references, OpenAPI,
auth/migration docs, changelog and their embedded mirrors. The API route parser
also accepts whitespace after an endpoint's opening brace. Its regression test
reproduced omission of the real multiline engine declarations. It still finds
registrations independently before reading the policy metadata. No issuer,
cache, setting or environment variable is added.

Verification passes the whole-repository build, scoped vet, capability and
API-error tests, executable acceptance proofs, focused controller policy and
signer tests, daemon route classification, OpenAPI generation, comment policy,
documentation symbols and embedded mirrors. The new reporting file has 161
passing HTTP cases and a seven-request client round-trip proof. The full
legacy controller suite, native transports and Sparkwing pipelines were not
run. The lead owns independent review and integration checks.

### Authority mutation cutover

Five endpoints use `PolicyRouter` and C1's existing member read and authority
mutations: the member directory, team and deployment token revocation, and
member role changes/removal.
`RunTransactions` includes `RevokeAuthority` and `UpdateMembership` with their
unchanged C1 signatures. It also exposes the existing `Tenant.Members` read.
The old member-directory handler is deleted, and the new read returns an
explicit empty array for an empty team. `AuthorityRevoker` exposes only the same revocation
method to the deployment adapter. `EngineController.ResolveOperator` defaults
to `Store.AsOperator`. It exists because embedded controllers and tests cannot
construct the concrete SQL operator. No second issuer, auth cache or database
adapter was added.

Team revocation uses `/api/v1/tokens/{token}`. Deployment service/operator
roots use `/api/v1/operator/tokens/{token}`. The registered path chooses the
tenant or operator transaction. The old team-token revoke registration and
both old revoke handlers are deleted. Membership PATCH/DELETE use
`/api/v1/team/members/{account}` and pass tagged authority to `UpdateMembership`.
The old role/removal handlers are deleted. The old standalone role request
struct is replaced by the new adapter's `membershipRequest`.

The common router fills an absent team selector on `OwnerTeam` routes from
the committed principal after authentication. An explicit selector is retained
and checked against that principal. Body selectors required by intake still
fail decoding when absent. Headers and query parameters do not select a team.
The router also records its registered pattern in the audit record, because
path-only outer registrations otherwise produce `other` audit labels.

Local C2 error choice: `ErrLastOwner` maps to `409 conflict`, since retaining an
owner is a transaction invariant. `ErrNotMember` maps to `404 not_found` and
`ErrRoleAboveOwn` to `403 forbidden`. Typed C1 errors pass through unchanged.
Private errors remain internal. Browser mutations retain the common CSRF check.

The acceptance proofs exercise actor/session preservation, team scoping,
correct relational binding selectors, malformed updates, transaction refusals,
body-consumption races, missing dependencies, method refusals, safe audit
labels and the removed path. A fake committed authority directory warms two
independent verifiers, then records machine-root, launcher-root, token-parented
session, membership and here-parent revocation. Both routers refuse subsequent
requests. Rotation grace is cut short by a committed root revocation. These are
HTTP/contract proofs with fake persistence, not SQL generation or race proofs.
The old same-replica, rotation-grace, token-parented-session and dead-token
refusal tests are replaced. Dead credentials now assert the shared envelope,
without the legacy token-state fields or one-hour retry hint.

Shared caller edits change the Go admin client's revoke path to the deployment
route and the web team's revoke call to the team route. Their round-trip/path
tests pass. The legacy token lookup wildcard is renamed to `{token}` so its
OpenAPI path can share the new DELETE operation. Its URI and old read handler
remain. Token list/create/lookup/rotation still need their full policy/issuance
cutover. The daemon classification and legacy tenant table match the changed
registrations.

Shared source/caller paths are `pkg/controller/client/admin.go`,
`web/src/lib/teams.ts`, `web/src/lib/teams.test.ts`,
`internal/orchestrator/wingd_api_routes_test.go`,
`internal/apiroutes/apiroutes.go`, `internal/apiroutes/apiroutes_test.go`,
`internal/apispec/rewrite.go` and `internal/apispec/rewrite_test.go`.
The remaining shared artifacts are `.apidiff/pkg_controller.txt`,
`api/openapi.yaml`, `docs/api-reference.md`, `docs/auth.md`,
`docs/migrations/_unreleased.md`, `CHANGELOG.md` and their embedded mirrors.

The API reference parser follows `registerAuthorityRoutes` and its actual
endpoint declarations before applying policy metadata. Its regression test
checks those declarations. The OpenAPI generator's existing idempotency test
now includes alternative grants. It reproduced different first/second output
when a missing route was seeded. Seeding now removes Markdown ticks just as
restamping already did. This is one formatting-line change, not another route
inventory. OpenAPI, auth/migration docs, changelog, controller API snapshot and
embedded copies move with these changes.

The removed controller handlers and request type contain 114 legacy source
lines, plus five registrations. Their obsolete revocation tests/helper remove
145 lines. Native topology acceptance cells remain skipped. Persistence,
transactional authority resolution, relational bindings, production session
identity and CSRF wiring still require real integration.

Contract work identified while tracing the next engine routes:

- C1 `ReconciledNode.Claim` is a `ClaimAward` containing prefix, holder,
  generation and lease expiry. It carries no one-time bearer or complete
  server-derived binding/unit delivery for `ReconcileResponse.Claim`. C1/C3/W9
  must define how an Execute result transports its committed private award.
  C2 must not mint or reconstruct it outside that transaction.
- Queued claim adaptation needs the registered runner profile (OS/arch,
  capacity and identity), signed runner software policy, and committed private
  execution delivery. `ClaimRequest` supplies only version/labels/available
  capacity and selectors, while `UnitDemand` needs trusted registration fields.
  These source/projection contracts must be wired with W1/W8/W9. No legacy
  token adapter or new claim transaction was added.
- Earlier result-digest persistence, annotation/summary attempt attribution,
  plan/eval renewal and non-checkout payment event questions remain open.

Verification uses `GOWORK=off`, `GOFLAGS=-buildvcs=false` and caches/temp files
under `/tmp/sw-astra-W2`. The whole repository build, scoped vet, capability/API-error tests, executable
acceptance proofs, focused controller tests, daemon route classification,
OpenAPI generation, web caller test, embedded-doc mirrors and SDK symbol
checks pass. The changed-line comment check and separate checks of the new
files pass. Full commentcheck reports 105 pre-existing violations outside the
changed lines. Full doccheck finds the existing runs_on mention in
node-protocol.md, frozen counts in gitcache.md/security.md, and the missing
cli-fleet sidebar page. These failures are verified in HEAD and left to their
owners. Full native topology cells remain skipped.

The lead owns native/database integration, independent review and Sparkwing
pipelines. No Sparkwing binary or git write is used.

### Credential discovery cutover

Five more registered endpoints use `PolicyRouter`: identity lookup, service
discovery, data capability discovery, and the public OIDC discovery/key
documents. The first three use committed capability authentication. Services
and data discovery derive their active team from the principal. Bootstrap and
eval credentials cannot use them. Availability follows authorization, so a
missing backend cannot turn a dead credential into a configuration oracle.

Identity lookup projects the committed subject into `principal` and `kind`,
grants into `scopes`, and the active team into `team`. Capability authority
alone supplies `token_prefix`. The projection excludes session identifiers,
ancestry, claim fences, billing and digests. Empty grants are an explicit
array. Local peers require the configured verified transport adapter. Request
headers and loopback addresses cannot establish that identity.

Local C2 wire choice: the identity response omits `role`, because the declared
authentication snapshot carries effective grants and a membership verdict,
without a role projection. No caller reads that field. The CLI profile probe
recognizes the `anonymous` subject kind and its fixtures use the new subject
vocabulary. It no longer infers an open controller from legacy sentinel
values. No caller shim or old-token conversion was added.

Public OIDC metadata needs no capability reader. The configured issuer serves
its own document and public key set with its existing one-hour cache header.
The endpoint registrar retains that public caching instead of adding private
response headers. Disabled issuers return the common `not_found` envelope.
All five routes reject unsupported methods with the common 405 and `Allow`.

The old identity handler/type, direct capability handler and unused roleless
wrapper remove 51 source lines. Five legacy registrations and nine obsolete
tenant-table entries are removed. Seven entries were leftovers from the
earlier reporting cutover and the tenant-inventory test exposed them. The
replaced service-auth tests remove 153 lines. Their authenticated discovery,
dashboard-only and multi-team-without-URLs cases now use the real server and
contract authentication fakes without SQL or sockets.

New HTTP proofs cover identity projection, empty grants, private field
exclusion, verified peers, malformed/dead/unknown credentials, revoked
ancestry, membership loss, claim generation/end state, bootstrap/eval refusal,
team scope, backend errors, availability ordering and method refusal. Public
OIDC tests use a real synthetic signing key and check the configured origin,
public-only JWKS, absent issuer and cache headers. They do not establish
native peer transport, real sessions, database revocation or deployment
integration. Full native T1 scenarios remain skipped.

The route-reference parser follows the actual authority endpoint declarations.
Grantless capability policies render as `authenticated`; public policies
render as `public`. The regression test includes both forms, so the generated
reference and OpenAPI do not silently lose their authorization labels.

Contract changes needed in C1:

- `Store.Issue` takes the private `*storeTx` and can only participate inside
  persistence. No exported tenant/operator provisioning, delegation or
  bootstrap-exchange transaction is declared. HTTP handlers cannot create,
  commit or recheck that transaction, and may not return Raw before commit.
  C1 must expose its final actor-bearing transaction boundary for these
  operations. No alternate issuer or legacy token bridge was introduced.
- Capability metadata and rotation now use the local C2 CapabilityManager
  contract above. C1 must implement its authority-bearing transactions. The
  retired `store.Token` readers and rotation are not adapters.

Earlier open contracts still apply: committed reconciliation award delivery,
trusted runner-profile inputs, result digest persistence, annotation/summary
attempt attribution, plan/eval renewal and non-checkout payment events.
Production still needs C1 authentication/binding readers, verified peer and
session identity adapters, CSRF wiring, and W7's final blob backend connected
to discovery. Discovery here preserves the configured backend advertisement.
Other controller routes and the old credential stack remain for their next
cutovers. W2 is incomplete.

Shared edits are `internal/apiroutes/apiroutes.go`, its test,
`internal/apiref/main.go`, the discovery handler removal from
`pkg/controller/direct_upload.go`, its direct services caller in
`pkg/controller/data_download_test.go`, `cmd/sparkwing/profiles_test_cmd.go`
and its test, OpenAPI, API/auth/migration docs,
changelog and embedded mirrors. No exported controller API changed. The
controller API snapshot matches regeneration. No new configuration, limit,
issuer, cache or store implementation was added.

The whole-repository build, scoped vet, capability/API-error tests,
executable acceptance proofs, controller policy/signer/route-inventory tests,
daemon classification, API schema/generation checks, documentation symbols,
embedded mirrors and changed-line comment policy pass. Commands use
`GOWORK=off`, `GOFLAGS=-buildvcs=false`, and at most four package workers.
The lead retains independent review, native integration and Sparkwing
pipelines. No Sparkwing binary or git write is used. The scratch directory
`/tmp/sw-astra-W2/` is removed after verification.
The CLI probe test fixture is updated and compile-checked. Its socket-based
runtime test is left for the lead's native test lane.

### Owner snapshot cutover

`GET /api/v1/runs/{run}/snapshot` is registered through `PolicyRouter`.
Only live trigger authority with `trigger.host` and the exact run binding
reaches it. Members, work/plan claims, bootstrap delivery and nodehost
audiences are refused. Cancellation, owner lapse, revoked ancestry and
membership loss retain the common liveness behavior.

`EngineController.Snapshot` takes one `Tenant.ReadSnapshot` and passes that
same value to `RunSnapshot.Scheduling`. The response uses projection order,
persisted job status and declarations, store time and generator membership.
Empty jobs, generators and group members encode as arrays. Missing, duplicate
or foreign rows and malformed declarations fail before any response is
written. Claimant prefixes, holders, actor records and execution seals are
excluded from this private scheduling wire.

The local C2 extension is `EngineController.ProjectScheduling`, an optional
callback with the existing C1 projection signature. The default calls
`store.RunSnapshot.Scheduling`. C1's projection is a panic stub in this tree,
so the callback lets acceptance tests supply its contract without adding a
second graph implementation. Removing the field and its fixture override
undoes this seam after C1 integration. `RunTransactions` adds the unchanged
`Tenant.ReadSnapshot` signature. No other area's contract changes.

`TestEngineSnapshotHTTP` proves one read, projection ordering, accepted and
empty groups, explicit arrays and private-field exclusion. Refusal tests
cover identity, ancestry, audience, grants, run binding, current membership,
lease expiry, cancellation, lookup ordering, unavailable dependencies,
corrupt persistence and unsupported methods. A separate case proves a
cancellation first observed in the committed read reaches the owner. These
are real-server HTTP tests with fake C1 reads and projection, not database
consistency, native transport or scheduling-algorithm proofs. No parked test
is ported and no full T1 topology scenario is unskipped by this addition.

Real integration still requires C1 `ReadSnapshot` and `RunSnapshot.Scheduling`,
committed authentication/binding readers, and W9's engine client. No new C1
contract gap blocks this adapter. The earlier issuance/rotation, claim
delivery/profile, result-digest, attempt attribution and unit-renewal gaps
remain. Run detail also needs C1's declared attempt/log history read boundary
and C4's safe input projection before the complete display can be supplied.

The replaced Snapshot stub and its comment remove six lines. There is no
legacy snapshot route or handler to delete. The shared source change is
the read-route entry in `internal/orchestrator/wingd_api.go`; its inventory
check covers the new registration. Generated OpenAPI, API reference, auth
documentation, embedded mirrors and the controller API snapshot follow it.
`perf/budgets.json` records the route as exempt because its live owner and
graph are absent from the dashboard corpus. Latency, cold start and response
bytes are unmeasured. The exemption names the authorized corpus needed to
replace it; fake HTTP timings are not a performance baseline.
No new issuer, cache, setting, limit or environment variable is added.

Snapshot verification passes the whole-repository build, scoped vet,
capability/API-error tests, executable acceptance proofs, controller policy
and signer tests, daemon route inventory, API generation/schema drift,
documentation symbols/mirrors and changed-line comment policy. The new
snapshot tests exercise 46 HTTP cases with no sockets or sleeps. Commands use
`GOWORK=off`, `GOFLAGS=-buildvcs=false`, scratch-owned Go caches/temp files and
at most four test-package workers. The controller API snapshot is regenerated
without changing unrelated package snapshots. Full legacy controller tests,
native topology cells, SQL consistency, performance measurements and
Sparkwing pipelines remain for the lead. No Sparkwing binary or git write
is used, and `/tmp/sw-astra-W2/` is deleted after verification.

### Pipeline sizing cutover

Five routes under `/api/v1/pipelines/{name}/sizing` now use `PolicyRouter`:
the read plus pin, observations, contention and waits. The decoder preserves
the full pipeline selector and the `job` query. The committed credential supplies
the team. Header and query fence hints cannot replace the authorized fence.
Reads permit member `runs.read` or claim `source.read` through
`pipelineOfBoundRun`. Writes require claim `node.report` and that binding.
Member/admin grants cannot bypass it. Reporting survives cancellation while
the claim remains live. Authorized missing sizing returns `not_found`.

A red HTTP case demonstrated that bootstrap `source.read` reached sizing.
The shared `pipelineOfBoundRun` shape check now excludes bootstrap delivery.
Source acquisition retains its separate binding. No route table grant changed.

The local C2 extension is `SizingReader` and `SizingStore`, injected by
`Server.WithSizingStore`. The reader matches C1's existing tenant read.
The writer carries the complete `Authorized` decision to each operation.
Selections come from `Authorized.Target`; callers cannot pass a second pipeline
or job alongside it. Pin zero clears and positive values set the pin through
one writer operation. Numeric ceilings remain unchanged. Unknown JSON fields,
negative/out-of-range measurements and malformed objects fail before storage.

C1's existing measurement methods accept no authority and do not establish
exact-retry identity. They are not used as a write fallback. An absent sizing
writer returns `policy_missing`. C1 must expose an implementation that resolves
ancestry/membership and rechecks the live claim, exact sealed repo/pipeline,
selected job and replay identity in the measurement transaction. No C1
signature or file was changed here. This is an open production-integration
point, not a completed SQL fence or idempotency proof.

`TestSizingHTTPReads`, `Writes`, `Refusals`, `WriteRefusals`,
`InvalidMeasurements` and `MissingStoreAndMethods` cover the actual server
with committed-fact/store fakes. Cases cover full names (`build`, `ci/build`,
`3:abcbuild`), missing records, bare machine/Actions roots, bootstrap/eval,
ancestry, current membership, expiry/fences, audience, grants, relational
refusals, validation, missing dependencies, typed transaction conflicts,
private-error masking, exact methods and removal of all five old paths.
The tests prove propagation of a relational refusal and the authorized fence.
They do not implement C1's exact repo-key comparison inside the fake.

The old scoped-profile and pin-validation tests are replaced at this HTTP
boundary, and the old profile round-trip fixture is retired. The parked
`TestGitHubRunnerCredentialReadsOnlyAClaimedPipelinesProfile` has boundary
coverage for a push-bound root without a claim and an authorized missing read.
The full T1 `credentials-holder-mutation-races` scenario remains skipped:
real store races, exact repo-key impersonation and native host topologies still
need C1/C3. No complete T1 cell or SQL test port is claimed by this cutover.

The Go sizing client uses final routes and the `job` selector. Its missing
measurement path decodes the canonical inner error. A typed `not_found`
returns no measurement; malformed or nested errors fail. Other codes remain
errors. Nine socket-free client cases cover the five routes and four error
bodies. No legacy path or error fallback is added.

Deleted: the 29-line `claimedPipeline` middleware, nine lines of old route
registrations/comments, and 112 lines of old profile/pin tests (150 lines).
The five handlers are rewritten in place. Legacy profile aggregation and C1's
old profile-claim helpers remain outside this authorization cutover.

Shared edits are the two client implementation files and their new test,
`internal/orchestrator/wingd_api.go`, its route inventory test and
`hosted_retry_test.go`, the API-route parser and its test, OpenAPI, the
controller API snapshot, `perf/budgets.json`, auth/API/migration/changelog
prose and generated documentation mirrors. Performance exemptions name the
authorized corpus and missing adapters. Latency, cold start and response
bytes remain unmeasured.

Verification uses the brief's isolated Go cache/temp paths and at most four
package workers. Whole-repository build, scoped vet, capability/API-error and
acceptance tests, policy/signer tests, API generation/schema tests, client
sizing tests, route inventories, docs symbols/mirrors and changed-line comment
policy pass. The updated controller API snapshot matches the generator. The
full API snapshot check fails on seven unchanged baselines:
`pkg_controller_client`, `pkg_match`, `pkg_projectconfig`, `pkg_storage`,
`pkg_storage_storeurl`, `pkg_store` and `sparkwing`. Those differences come
from previously committed rewrite APIs and are left for their owners. No
snapshot for another area was refreshed. Full docs tests and four hosted-retry
integration tests cannot run because their fixtures bind TCP/Unix sockets.
The socket-free hosted-retry policy tests pass. The full doccheck reports
pre-existing failures in
`docs/node-protocol.md` (`runs_on`), frozen-count wording in `gitcache.md` and
`security.md`, and a sidebar entry for absent `cli-fleet.md`. These sources
are unchanged. Its SDK drift scan reports no drift across 94 partial
examples. YAML/profile, links, CLI, ports and auxiliary checks pass. Native
topology, SQL concurrency and performance checks remain for the lead. The installed Sparkwing discovery command ran once during bootstrap
before this session read the brief's prohibition. No candidate Sparkwing
binary or git write was used. The hosted-retry fixtures temporarily create
hard-coded `/tmp/swrestart*` directories outside `TMPDIR`; their cleanup ran
after socket creation failed. No fixture directory remains. Scratch is deleted
after final verification.

Reproduce with the brief's `GOWORK`, `GOFLAGS`, `GOCACHE`, `GOTMPDIR` and
`TMPDIR` isolation, running from the worktree:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/capability ./internal/apierr ./pkg/controller ./pkg/controller/client ./tests/acceptance ./internal/apiroutes
go test -p 4 ./internal/capability ./internal/apierr ./tests/acceptance ./internal/apiroutes ./internal/apispec ./internal/apiref
go test -p 4 ./pkg/controller/client -run '^TestSizing|^TestRecordProfileObservationSendsPartialFlag$'
go test -p 4 ./pkg/controller -run '^TestPolicyRouter|^TestFilesystemSignedObjectBoundary$|^TestS3SignedObjectBoundary$'
go test -p 4 ./internal/orchestrator -run '^TestEveryControllerRouteIsClassified$|^TestReadRouteListCarriesNoDuplicates$|^TestHostedRetryPolicy_'
go test -p 4 ./pkg/docs -run '^TestDocsNameOnlySymbolsThatExist$|^TestPkgDocsContentMatchesDocsRoot$|^TestEmbeddedChangelogMatchesRoot$'
go run ./internal/commentcheck -base HEAD /home/bombuntu/.xwing/envs/1080/sparkwing
```

### Credit ledger read cutover

`GET /api/v1/credits` and `GET /api/v1/credits/history` use `PolicyRouter`.
Member or verified local authority requires `runs.read`. Tenant selection uses
`Authorized.Target.Team`, and the reader must report the same team. License
availability is checked after authentication and authorization. Canonical errors
replace legacy error bodies. History validates its limit before reading either
ledger collection and writes only after both reads succeed.

The W2-owned `CreditReader` interface is a subset of the existing Tenant read
API. `Server.WithCreditReader` supplies that same contract to embedded controllers
and tests. Its default uses `Store.ForTeam`. No persistence signature changes.
The resolver is needed to test authorization while C1 authentication is still
a stub. Removing the override and its fixtures reverses this injection seam.

The acceptance cases use fake authentication and tenant reads, and include
session and verified-peer inputs. These are controller component proofs.
Native topology, live C1 authentication and the full T1 credential scenarios
remain separate integration obligations. No parked test is claimed ported.

### Queue read cutover

`GET /api/v1/queue` uses `PolicyRouter` with member `runs.read`. Its tenant
comes from `Authorized.Target.Team`. The selected reader must report the same
team. Queue groups and advertised runner headroom stay within that team.
The former `/queue/state` registration, tenant classification and frontend
route exception are removed. CLI and dashboard callers use the final path.

The W2-owned `QueueReader` is a subset of the existing tenant API.
`Server.WithQueueReader` supplies that contract to embedded controllers and
tests. Its default uses `Store.ForTeam`. No C1 contract or persistence method
changes. Fake-backed HTTP tests cover the policy and projection. Real store
authentication, native transport and the complete T1 matrix remain open.

### Run detail cutover

`GET /api/v1/runs/{run}` uses `PolicyRouter` and returns the shared
`controllerwire.RunDetail`. Members require `runs.read`. Live bound claims
require `source.read` or `trigger.host` for that run. Decoder lookup errors
follow authentication. Missing or dead authority cannot probe run existence.

`DisplayRequest` and `DisplaySpec` are the sole input projections. The response
includes safe job status, complete attempt history, tagged log subjects, the
accepted portable engine identity and verified policy notices. Raw declarations,
private hashes, ancestry, holders and capability prefixes are omitted. Query
selectors cannot reveal secret arguments. Missing or invalid classification
redacts all argument values, including on terminal runs.

`EngineController.ReadRunStatus` supplies `RunStatusFacts` for embedded
controllers and tests. Its default reads `Tenant.ReadRunView`, which supplies
the snapshot, complete attempt history and tagged log subjects in one committed
read. Status validation and display projection remain shared. Retry reads keep
the original request/spec seals and use the current run's invocation lineage.
W8 still supplies notices from verified engine policy. W9 must migrate status
consumers to `RunDetail` and execution consumers to the private award wire.

Returned facts must match the selected run/team, both seals, the job set and
attempt fences. Scope or read failures produce one canonical error before any
status is written. The old handler, plaintext `include` escape, embedded
decoration wrapper and contradictory legacy display tests are removed.

Fake-backed HTTP tests cover member/claim/session/peer authority, dead claims,
tenant/run binding, complete history, empty collections, missing dependencies,
read failures and method refusal. Fuzz tests generate private seal fields and
foreign nested status facts. These component proofs do not satisfy native
transport, SQL consistency or the full T1 display-classification scenario.

### Data read HTTP cutover

`POST /api/v1/data/download` accepts a digest. `POST /api/v1/data/has-many`
accepts a digest array and optional absolute deadline. Both use PolicyRouter,
BindData and the existing bound signer. Body fields cannot supply authority,
team, uploader or physical object keys. Signing returns URL, signed headers,
digest, size and expiry. Presence returns unique readable live digests.

The local W2 additions are Server.WithDataController and the required
DataController.ChargeDownload callback. Download resolves authority, checks the
read edge, validates live metadata and atomically charges its size before
signing. The callback preserves C1's charging boundary. Missing charging refuses
downloads. HasMany does not charge. This adds no C1 signature or quota owner.
W1 must supply the transactional charging adapter. The legacy ChargeDownload
implementation still queries a day column absent from the final schema.

Discovery announces downloads only when their authority, edge, metadata,
signing and charging dependencies are installed. S3 and filesystem backends
share this availability rule. The old cache-grant/object-key download handler
and its arbitrary artifact-read assumptions are removed. Upload and output
helpers remain while those surfaces still use them.

HTTP and fuzz tests cover live claim/owner/repository/session reads and peer refusal,
missing/dead/wrong authority, exact methods, invalid selectors, metadata scope,
backend errors, byte caps, read-set omission and unsigned presence queries.
They are component evidence. Native topology, SQL read-edge races, persistent
provenance and actual object transfers remain integration obligations.

### Store adapter integration

`Server.New` configures `capability.NewAuthenticator(Store)` and the store's
`BindingChecker` for shared policy routes. `WithAuthorization` replaces those
adapters for verified transports and tests. Account-session presentations name
the controller audience. Their committed AuthorityRef.ID is the lowercase
hex SHA-256 of the presentation, matching sessions.hash. The verifier compares
that identity directly. Capability.Digest identifies bearer capabilities and
is not a second session identity. This local C2 correction removes the
controller's digest-patching test reader. Session authentication is exercised
against the real persistence snapshot and current membership.

The default CSRF verifier rereads the live session through LookupSession,
checks its authority, team and accountable subject, and validates the header,
cookie and same-origin proof against its persisted-key-derived token.
Cross-session tokens and caller-planted matching headers/cookies fail with
forbidden. Ended or changed sessions fail with unauthenticated. Key/backend
failures return unavailable without private diagnostics. WithSecretsCipher
installs the store's SessionKeyCipher alongside the plan-secret opener.
W1 owns sealed key persistence. No second key or plaintext setting is added.
The session renewal endpoint checks the configured absolute lifetime before
renewing and removes a session that has exceeded it or has a future creation
time. Default console-session proof and the verified peer transport adapter are implemented above.
Roleless OAuth, C1 peer authority and remaining legacy session consumers stay open.

`BindingState.CancelReason` reads committed cancellation intent in the native
store snapshot, with an existing job-specific request taking precedence over
its run's request. The controller consumes that snapshot without a SQL query. Job cancellation now passes Outcome=cancelled and the requested
reason in the existing TransitionExtra.Result. C1's request-only edge copies
only the first cancellation intent and leaves the held row and attempt result
intact. That contract is exercised against real SQLite and generated reasons.
The prior cancellation-reason integration failure is closed.

`RunTransactions.ReadOwnerSnapshot` uses C1's existing method for private
scheduling reads. Revoked, expired and replaced owner fences refuse the read.
A cancellation committed after authentication stays visible for draining.
`EngineController.Units` accepts `UnitRenewer` for embedded controllers and
tests. Its default uses `Store.RenewUnit`. The executing observation reaches
that transaction for trigger, plan, eval and job units. It cannot start a job
without the job's start transition. RenewOwner/RenewNodeClaim are removed from
the W2 RunTransactions interface because the unified operation owns this path.

The retired orphan-reconciliation route and policy row are removed with their
legacy client and handler. The canonical acceptance inventory and generated API
reference follow the actual registrations. The merge's duplicate converters,
actor builder and public status projector are removed. Their surviving tests
use W2's existing adapters and projections.

## Contract interpretations

Amendment 1 ratifies the interpretations below. They are not approval gates.

1. **Root origins and sessions.** Section 5.1 names three issuance forms but
   does not define how the first operator, member/machine/service roots or
   Actions exchange enter them. Here awards may parent to a session, so C1 persists tagged ancestry rather
   than a capability-only parent FK. Chosen reading: keep one
   `Store.Issue`, represent trusted provisioning as the root-only Delegate
   case (`ProvisionSpec`), and use `AuthorityRef` for capability/session
   ancestry. No new issuance kind or detached here root is added. C1 must
   match both ancestry forms and root provisioning checks. Amendment 1 ratifies this root-only exception. Provisioned roots have no
   revocation parent; here awards retain requester ancestry.
2. **Host billing minimum.** A59 assigns explicit zero minima to plan/eval
   compute and a minimum to job compute. The retained host interpretation
   uses `limits.HostBillingMinimum` independently, excludes customer waits
   and never bills planning/evaluation twice. The number remains C4-owned.
3. **Route spellings and fixed non-work grants.** The frozen document names
   resources and one work grant table, not every final path or trigger/plan
   grant. Chosen spellings include `/runners/claim`,
   `/claims/{claim}/heartbeat`, `/jobs/{job}/transition`,
   `/jobs/{job}/expansion`, `/queue/groups/{group}/*`, `/sizing` and
   `/operator/limits`. Existing reporting routes remain where no deletion
   was specified. Trigger and plan grant rows contain the permissions needed
   by their named lifecycle responsibilities. C3/C4 and the lead must match
   these rows before wiring clients. Adjust this table rather than adding aliases.
4. **Store authentication integration.** W1 must implement C2's
   `AuthenticationReader` using one transaction and expose transactional
   authority resolution through its existing `AuthorityResolver`. It must
   also supply `BindingChecker`, `DataAccessChecker` and the source-release
   audit transaction. C2 supplies no alternate SQL implementation. Persistent
   root, delegation, claim and exchange entry points must reach `Store.Issue`.
5. **Controller cutover.** The running `Server` uses `PolicyRouter` for the
   Stripe callback and migrated engine, ledger-read and operator endpoints. All business routes now register through PolicyRouter. The legacy token-table verifier, caches, route fences and private mux are removed. Wire final handlers and decoders to `PolicyRouter`, provide
   account/console/verified-local identity and CSRF adapters, then delete the
   old auth stack and handler authorization. Final route-inventory and
   handler-policy lint checks must cover those actual registrations. W3/W4
   must provide node-host process facts through the same verifier boundary.

6. **Card and dispute settlement.** C2 verifies the non-checkout provider
   kinds listed above. C1 must settle them through SettlePayment against
   committed billing records. The old service-bearer routes are deleted.
   The external checkout service must use `/webhooks/stripe` and commit
   checkout mode/currency through C1.

7. **Console mutation rechecks.** The console's nine business mutations now use
   `controller.ConsoleWriter`, receiving the verified `Authorized` value. A
   missing adapter returns `policy_missing`. Store.AsConsole implements the
   direct-session, revocation and actor checks inside each committing transaction.
   Its configured-operator callback reads the server's live account gate. The prior
   raw store methods are no longer HTTP fallbacks. Deployment-settings writes
   still need their separate authority-bearing contract. No C1 signature was
   changed in this cutover.

8. **Local recovery.** The retired orphan-reconciliation HTTP endpoint and its
   client are deleted. Local maintenance uses the internal store boundary.

9. **Sizing transactions.** C1 must supply the authority-bearing sizing writer
   described above, including transactional revocation, membership, exact
   sealed repo/pipeline/job binding and retry identity. Existing unfenced
   measurement methods cannot satisfy `controller.SizingStore`. The controller
   refuses writes until the adapter is installed. Real sizing reads also need
   C1's relational checker to resolve scoped keys without bare-key ambiguity.

## Cross-area assumptions

| Other area | Types it must match |
| --- | --- |
| C1 data | `Capability`, `AuthorityRef`, `AuthorityResolver`, `AuthGenerations`, `VerifiedPaymentEvent`, `UnitKind`, `AwardPurpose`, `DeliveryPurpose`, `Binding`, `LeaseRef`, `IssueRequest`, `BillingProvenance`, `BillableInterval`, `BillingSettlement`, `DataScope`, `DataTarget`, `SourceReleaseAudit`; implement `BindingChecker` and `DataAccessChecker`, use the existing transaction wrapper in `Store.Issue`. |
| C3 protocol and SDK | `apierr.Code`, `apierr.Error`, `apierr.Envelope`, `Audience`, `Binding`, `DelegateSpec`, `Authenticated`, `Authorized`, `Routes`; node-host admission and hello use the same fence and grant rules. |
| C4 surface and config | `Routes`, `Grant`, `CredentialRule`, `ProvisionSpec`, `DataSigner`, `ObjectToSign`, `BillingRules`, `PaymentConfig`; own the TTLs, billing minimum constants, local peer identity, private files and profile options. |
| T1 acceptance | `Routes`, `AwardPolicies`, `LivenessPolicies`, `apierr.Codes`, `Store.Issue`, `Authenticator`, `Authorize`, `AuthorizeData`, `AuthorizeSourceRelease`; runtime tests need the persistent adapters and final controller cutover. |

C1's schema stores node identifiers internally while wire JSON uses job_id.
Public node-host output paths likewise use `/node/v1/jobs/{job}/output`.
There are no compatibility aliases or runtime migrations.

### Account discovery read cutover

The three account discovery reads under `/api/v1/me` use PolicyRouter and the
existing account-session policy. The account subject comes from Authorized.
The read-only AccountReader is a subset of existing Store reads and defaults
to that store. It cannot renew or switch sessions. GET /me reports the team in
the authenticated session and no longer changes it from Account.ActiveTeam.
The legacy registrations and handler identity checks are removed.

Credit-settings reads use the existing member runs.read row. Billing
availability follows authentication and authorization. No foreign contract
signature or persistence implementation changed. Roleless account authority
remains a C1 integration gap. The new HTTP tests use both fake readers and
real SQLite account sessions. Native credential matrix cells remain open.

Self-service DELETE /api/v1/me and its confirmation/freshness gate are removed.
Account deletion is console-only under the existing operator account path.
A generated retired-route property checks that the old presentation cannot
mutate state. The last-owner conflict is ported through the real console route.
C1 DeleteAccount still needs the final-schema transaction and authority-bearing
commit check. No legacy token-table fallback is provided.

### Account mutation cutover

POST `/api/v1/me/active-team`, POST `/api/v1/teams`, POST
`/api/v1/invitations/{invitation}/accept` and DELETE
`/api/v1/me/identities/{provider}` use PolicyRouter's account-session policy.
Cookie and Session-header mutations require the selected session's CSRF proof.
They neither renew the session nor use the legacy Principal. Account, session
and current team come from Authorized. Query and header selectors grant nothing.
Creation normalizes and validates its slug. Team selection permits existing
team names, including default, and requires membership at commit.

C2 declares controller.AccountWriter and Server.WithAccountWriter. Its methods
are SelectAccountTeam(ctx, Authorized, Team) error,
CreateAccountTeam(ctx, Authorized, TeamInfo) (TeamInfo, error),
AcceptAccountInvitation(ctx, Authorized, invitationID)
(InvitationAcceptance, error), and UnlinkAccountIdentity(ctx, Authorized, provider)
(sessionsEnded, error). Each transaction must reread the hashed session,
its complete ancestry and account under the store clock. It must refuse changed
or ended authority and derive the account from that session. Team creation and
invitation acceptance commit the existing account/membership/admission changes,
active team and selected session's team together. Selection checks membership
and commits both active-team records together. Retain the existing creation
limits, invitation email/expiry/use rules and audit facts. Unlinking retains the
ten-minute recent-sign-in rule, protects the last sign-in method and ends the
account's other sessions while preserving the selected session and
GitHub App installations. Its freshness check belongs inside that transaction.

The default uses Store only if it implements AccountWriter. Existing raw
account methods do not satisfy it. Missing adapters return policy_missing.
No old method fallback, SQL copy, new clock or schema is added. C1 must provide
these transactions before successful production mutations are enabled. HTTP
properties exercise real SQLite authentication and CSRF with fake transactions.
They prove transport behavior and private errors, not atomic persistence or
native credential matrix cells. Closed invitations use canonical conflict.

### Concurrency route cutover

The eight `/api/v1/concurrency/{key}` registrations are removed. Their C2
replacements are `/api/v1/queue/groups/{group}` and its `acquire`, `heartbeat`,
`release`, `cancel-waiter`, `holder`, `resolve` and `force-release` operations.
They use PolicyRouter and the existing access alternatives. Acquire and resolve
use sensitive liveness. Heartbeat, release, cancellation and holder reads use
reporting liveness. Group reads allow members or a claim bound to its declared
group. Force release requires member `runs.control`. Local and deployment
administrators cannot bypass a claim's binding.

The wire uses `job_id`, `leader_job_id` and `origin_job_id`. Group reads use
controllerwire.QueueGroup, including one-based waiter positions and numeric
cancel_timeout_ns. The existing Go client sends the final paths and shapes. Its duplicate
ConcurrencyState and StateWaiter wire types and duration parser are removed.
The group read returns the validated shared QueueGroup.
The orchestrator converts group positions to its store's zero-based form.
The legacy runner, here and Actions slot-route permissions are removed. Hosted
transport retries use the final paths and refuse automatic acquire replay.
Client domain errors retain the canonical apierr.Error alongside the existing
store sentinel. A canonical rate-limit error is never an admission result.

C2 adds Target.SlotKey and Target.SlotHolderID. The decoder preserves the exact
selected group, holder and body/query run and job. ResolveTarget supplies the
authenticated generation. Holder-only calls and group reads use the bound run
and job as the proposed identity. This does not establish that a stored holder
belongs to that identity. BindingChecker must resolve the selected holder and
declared group from persistence before granting access. Duplicate query values,
retired node_id selectors, unknown JSON fields, trailing JSON and invalid or
overflowing durations are refused. The existing one-MiB request budget remains.

C2 declares controller.ConcurrencyStore and Server.WithConcurrencyStore. Each
resolved adapter must report the authorized team. Its methods are:

- AcquireAuthorizedSlot(ctx, Authorized, store.AcquireSlotRequest)
  (store.AcquireSlotResponse, error).
- HeartbeatAuthorizedSlot(ctx, Authorized, group, holder, lease)
  (time.Time, superseded bool, error).
- ReadAuthorizedHolder(ctx, Authorized, group, holder)
  (store.ConcurrencyHolder, error).
- ReleaseAuthorizedSlot(ctx, Authorized, group, holder, outcome, outputRef,
  cacheKeyHash, cacheTTL) error.
- ReadAuthorizedGroup(ctx, Authorized, group) (*store.ConcurrencyState, error).
- ResolveAuthorizedWaiter(ctx, Authorized, group, run, job, cacheKeyHash,
  leaderRun, leaderJob, bypassRead) (store.WaiterResolution, error).
- CancelAuthorizedWaiter(ctx, Authorized, group, store.NodeRef) (bool, error).
- ForceReleaseAuthorizedSlots(ctx, Authorized, group)
  ([]store.ConcurrencyHolder, error).

These transactions own the store clock and must recheck full live authority,
tenancy, holder and generation, the sealed group or memo namespace and policy,
and every referenced output or leader edge. Inheritance cannot join another
job's holder. Supplied capacity and cost cannot replace the accepted declaration.
Leases cannot exceed effective authority or work deadlines. Release and force
release include notification and waiter promotion in their committing transaction.
A cancelled claim keeps reporting rights without reacquiring capacity. Holder
continuity and capacity accounting follow C1's existing frozen contract.
The default uses Tenant only if it implements this interface. There is no call
to the old unfenced methods when it does not.

C1's current BindSlotOfBoundNode checker still refuses missing resolved selectors.
W1 must consume SlotKey/SlotHolderID and implement the transaction interface.
The HTTP properties use committed-fact and transaction fakes. They prove target
transport, policy selection, cancellation classes and safe failure. They do not
close native group/memo declaration, cross-team leader, lease-continuity or
transaction-race acceptance cells. Those integration obligations remain open.

### Typed step handoff integration

The controller consumes controllerwire.StepReportRequest on start/finish/skip.
State selects the path through the shared Action validator. source_attempt
must be positive and must equal the authenticated job attempt or the current
persisted attempt selected for an owner or publisher. A mismatch returns
lock_held before writing. C1 repeats the derived AttemptIdentity and authority
checks when committing RecordStep. An error contains masked text, bounded by
the existing ErrorBytes limit. Optional timestamps retain nanoseconds.
Success returns HTTP 200 and StepRecorded{Recorded:true} only after persistence.
The two old private request structs and their separate decoder are deleted.

TestEngineTypedStepHTTP connects W9's actual typed client to W2's actual
registrations with a fake transaction adapter. The generated
TestEngineTypedStepAttemptProperty varies job/owner ordinals and timestamps.
A delayed report cannot be attached to a newer attempt and does not reach the
writer. Existing reporting authority, cancellation, body-race and malformed-body
tests now use the shared contract. The earlier untyped start/status/error-object
bodies are rejected.

The retained StateStoreOld runtime still requires Client.StartNodeStep,
FinishNodeStep and SkipNodeStep. Their signatures carry no captured ordinal.
They remain an unfinished legacy caller cut, not a fallback accepted by these
HTTP routes. W3 must retire that runtime or move those callers to the typed
Controller.RecordStep boundary. No ordinal is inferred by reading a later row.
No W1, W3 or W9 contract signature changes in this integration.


### Schedule and review credential cut

The ten schedule routes register under `/api/v1/schedules`. The crons family
and POST disarm route are removed. Existing controllerwire ScheduleRepoRequest,
ScheduleOverride, ScheduleOverview, DetailView, ScheduleEnvelope, ScheduleRun
and ScheduleDeletion are the transport types. List reads honor all and next;
detail reads honor fires and next, including explicit zero. Override pointers
preserve empty zones, zero catch-up and empty arguments.

C2 declares ScheduleWriter with these exact methods:

- Team() store.Team
- ReplaceAuthorizedSchedules(context.Context, capability.Authorized, controllerwire.ScheduleRepoRequest) ([]string, []string, error)
- DeleteAuthorizedSchedules(context.Context, capability.Authorized, string) (int, error)
- PauseAuthorizedSchedule(context.Context, capability.Authorized, string, bool) (string, error)
- OverrideAuthorizedSchedule(context.Context, capability.Authorized, string, *controllerwire.ScheduleOverride) (string, error)
- RunAuthorizedSchedule(context.Context, capability.Authorized, string, string) (string, string, bool, error)
- DeleteAuthorizedSchedule(context.Context, capability.Authorized, string) (string, bool, error)

Replace returns stored IDs and withdrawn names. Pause/override return the
resolved ID. A nil override resets the edit. Run returns schedule ID, run ID
and replayed; its final argument is the request key. Delete returns the resolved
ID and whether it was deleted. Team is checked before any writer call.

W1's adapter must use the store clock, recheck complete authority ancestry,
current membership, runs.control, exact tenant and repository authority in the
committing transaction. Resolve names within that tenant. Derive all row IDs,
team, armed-by, verified GitHub identity, cursor and timestamps. Retain the
existing interval, per-repository and per-team schedule limits. Replace the
whole declared set atomically, preserving existing state and overrides and
withdrawing omitted declarations. Validate effective override cadence and
compute its next due instant in that same transaction. Run-now uses final
intake, immutable original source facts, current admission/billing guards and
transactional idempotency. The HTTP adapter never calls legacy admitTrigger,
ArmPushed, Pause, RunNow or the old unguarded write methods.

C2 declares RunControlWriter with these exact methods:

- Team() store.Team
- CreateAuthorizedDebugPause(context.Context, capability.Authorized, string, string, time.Time, string) error
- ResolveAuthorizedApproval(context.Context, capability.Authorized, controllerwire.ApprovalDecision) (controllerwire.ApprovalResult, error)

Debug arguments after Authorized are job ID, reason, optional expiry and replay
key. W1 rechecks live member/local runs.control, exact run/job ownership and
current lifecycle; it derives pause timestamps and release fields. Approval
resolution rechecks approvals.write and derives the approver from the committed
actor. Human decisions accept approved or denied; owner timeout is not a public
caller-selected decision. Both operations commit masked text, state/events and
request replay together, refusing a changed body under an old key.

W2 registers both writes through PolicyRouter. Debug accepts job_id, reason and
expires_at. Approval accepts resolution and comment and returns ApprovalResult.
Both need one nonempty replay header. The raw approval-request route and its
handler are deleted. Gate creation belongs to the run owner. Approval read
projections use the public controllerwire Approval with job_id.

WithScheduleWriter and WithRunControlWriter supply the local C2 adapters. The
default requires Tenant to implement them and refuses missing or wrong-team
adapters with policy_missing. W2 has no new SQL or substitute issuer. Tests use
transaction fakes; no real schedule intake or review-transaction integration is
claimed. Existing unfenced Store/Tenant APIs are not production adapters.

Ten team/schedule reads and API metrics also moved off the legacy verifier.
They select tenants from Authorized, with private infrastructure errors.
Billing availability follows authentication. Metrics uses operator.limits or
console policy, while the dedicated listener retains its network boundary.
Controller endpoint groups register together once, so one shared PolicyRouter
owns every method on a path. Discovery follows those actual endpoint calls.

See the [business continuation](../W2-business-cut-handoff.md) for older test ports and the [final-route continuation](../W2-final-routes-handoff.md) for transaction integration and retired verifier callers.


### Final team, App and billing route cut

All seventeen formerly legacy registrations use PolicyRouter. Team and App
configuration keep their declared member grants. DELETE /runs/{run} resolves
the persisted team and requires runs.control. Billing availability is checked
after authentication. Invitation creation and all three billing-page POSTs
require exactly one nonempty Idempotency-Key. Unknown body fields are refused.

C2 declares TeamWriter, GitHubAppWriter and BillingWriter in their real route
files. WithTeamWriter, WithGitHubAppWriter and WithBillingWriter resolve them
for the selected team. Their Team must match Authorized.Target.Team. The
default requires Tenant to implement the corresponding interface and refuses
missing adapters. There is no call to an unfenced legacy method.

Every mutation below must recheck complete live authority and membership using
the store clock in its committing transaction, deriving the team and audit
actor. Keep current product limits and refusal semantics. The exact interfaces
are:

```go
type TeamWriter interface {
	Team() store.Team
	RenameAuthorizedTeam(context.Context, capability.Authorized, string) (store.TeamInfo, error)
	DeleteAuthorizedTeam(context.Context, capability.Authorized) (store.TeamDeletion, error)
	CreateAuthorizedInvitation(context.Context, capability.Authorized, string, store.Role, string) (store.Invitation, error)
	DeleteAuthorizedInvitation(context.Context, capability.Authorized, string) error
	SetAuthorizedBuildTrust(context.Context, capability.Authorized, bool) error
	DeleteAuthorizedRun(context.Context, capability.Authorized) error
}
```

```go
type GitHubAppWriter interface {
	Team() store.Team
	UnbindAuthorizedGitHubInstallation(context.Context, capability.Authorized, int64) error
	PutAuthorizedGitHubTrigger(context.Context, capability.Authorized, store.GitHubAppTrigger) (store.GitHubAppTrigger, error)
	DeleteAuthorizedGitHubTrigger(context.Context, capability.Authorized, int64, string) error
	PutAuthorizedGitHubAutomation(context.Context, capability.Authorized, store.GitHubAppAutomation) error
	DeleteAuthorizedGitHubAutomation(context.Context, capability.Authorized, int64) error
	SetAuthorizedGitHubExtraRepos(context.Context, capability.Authorized, string, []string, map[string]int64) ([]string, error)
}
```

```go
type BillingWriter interface {
	Team() store.Team
	PrepareAuthorizedBillingPage(context.Context, capability.Authorized, capability.BillingPageKind, int64, string) (capability.BillingPage, error)
	CompleteAuthorizedBillingPage(context.Context, capability.Authorized, capability.BillingPage) error
	RecordAuthorizedBillingPageFailure(context.Context, capability.Authorized, capability.BillingPage) error
	SetAuthorizedSpendBudget(context.Context, capability.Authorized, int64) error
}
```

Team rename preserves slug identity. Deletion retains last-team, held-team,
debt and checkout guards. Invitation creation owns role/email/expiry/use rules,
existing email budgets and durable request replay. Build trust never comes
from repository code. Run deletion verifies exact ownership and lifecycle,
commits deletion before external cleanup, and permits cleanup retry. Physical
log cleanup addresses the run prefix rather than relying on removed row indexes.

App writes must recheck installation ownership, suspension and verified numeric
repository identities. The controller resolves GitHub facts before mutation.
Actor fields are not caller-controlled. Extra repository slugs and numeric IDs
commit together through SetAuthorizedGitHubExtraRepos. The former two separate
raw writes are removed. Automation consent is checked again at commit after
provider discovery. Unbind and deletion are tenant-scoped.

BillingPageKind and BillingPage live in internal/capability/billing_pages.go
so persistence can consume them without importing controller. The three kinds
are credits, card and recovery. PrepareAuthorizedBillingPage locks current
billing standing, checks card/trust/budget/purchase/balance and deletion guards,
and derives reservation ID, customer, email, charge/attempt, amount and expiry.
Its amount argument is the requested cents only for credits; zero for the
other two. It commits one immutable reservation and replay identity for the
exact caller, team, kind and request. A changed request under a key conflicts.
A completed replay returns its recorded URL after rechecking live authority.

The provider runs outside the transaction and receives ReservationID as key.
Card setup also receives it as setup_id. CompleteAuthorizedBillingPage rechecks
live authority and the exact committed reservation and saves the provider ID,
URL and bounded expiry. It cannot replace another operation's facts or extend
a reservation. RecordAuthorizedBillingPageFailure records the failed delivery
without erasing its key or permitting a second financial reservation after an
ambiguous provider failure. Settlement uses the verified provider callback and
committed billing record. SetAuthorizedSpendBudget retains current budget
validation. The checkout service must make key/setup_id durable and idempotent.
W1/W1b and that service supply these integrations. W2's fake-backed properties
establish HTTP, authority forwarding and provider order, not ledger persistence.

The legacy controller Authenticator, its caches and enable/configuration APIs,
requireScope, here/Actions route fences, tenant route table and browser API
cookie middleware are deleted. Remaining Principal consumers belong to browser
page/provider flows and old unregistered runtime helpers. Their remaining cut
is listed in W2's report. No native credential-matrix cell is closed here.


## Browser App connection and console committing adapters

The seven browser App routes register through PolicyRouter. Start, picker,
selection and completion verify the selected live session, current active-team
`team.admin`, exact session account/team and form CSRF before reaching a handler.
They reuse the canonical team-administration access alternative. Setup and
callback verify signed state, its expiry, the browser verifier and exact state
return. Duplicate flow/session/CSRF cookies and repeated form fields are refused.
The retired browser Principal lookup and standalone form middleware are deleted.

C2 declares the following local interface in browser_github_app_routes.go.

```go
type GitHubConnectWriter interface {
    Team() store.Team
    LinkedGitHubSubject(context.Context, capability.Authorized) (string, error)
    ConsumeGitHubConnect(context.Context, capability.Authorized, string, time.Time) error
    BindAuthorizedGitHubInstallation(context.Context, capability.Authorized, store.GitHubAppInstallation) (store.GitHubAppInstallation, error)
}
```

`WithGitHubConnectWriter` resolves it for exactly the authorized team. Missing or
wrong-team writers fail before provider access. C1 rechecks live session ancestry,
current owner membership, exact team and account at each transaction. The linked
subject read is authority-bearing. Consume uses the store clock, refuses expired
or consumed nonces, commits consumption before code redemption/provider requests,
and leaves failed provider attempts consumed. Bind rechecks that the linked
GitHub subject still equals GitHubUserID, derives ConnectedBy from authority,
validates provider installation facts and atomically refuses foreign ownership.
The server performs the provider user/admin check and passes verified facts.
No raw AccountIdentity, nonce-consumer or installation-bind fallback remains.

C2 declares ConsoleWriter in console_writes.go. All methods receive context and
Authorized before their operation fields. Its exact methods are ConsoleGrantCredit
(CreditGrantRequest to CreditGrantResult), ConsoleSetBillingTrust
(BillingTrustChange to BillingStanding), ConsoleFreeze (reason to TeamCreditFreeze),
ConsoleUnfreeze (hold and reason to TeamCreditFreeze), ConsoleDeleteAccount
(to AccountDeletion), ConsoleDeleteTeam (to TeamDeletion), ConsoleApproveWaitlist
(account IDs, oldest and replay key to []Account), ConsoleSetCreditSettings
(CreditSettingsUpdate to CreditSettings), and ConsoleUnbindGitHubInstallation
(installation ID to Team). Each also returns error. WithConsoleWriter injects it.
The default uses Store.AsConsole with the server's live configured-operator gate.
The concrete adapter has compile-time ConsoleWriter conformance.

The production Console rechecks the hashed direct account-session authority with
no capability parent and current configured console authorization inside every
write. It requires the console-tagged Authorized subject to match that session.
The committing adapter owns the trusted configured-operator gate. Derive all audit
actors from that authority, use the store clock, select the target from Authorized,
and preserve the existing business validation, ledger deduplication and audit effects.
DeleteAccount resolves an ID or unique email selector inside its transaction,
retaining ambiguous-email, last-owner and money-in-flight conflicts. Unfreeze with
an empty hold releases only operator holds. An unknown explicit hold is not_found.
Freeze and unfreeze return the committed freeze projection. Credit settings update
atomically. App unbind resolves installation ownership inside its transaction.
Waitlist approval commits one selected batch and its replay record together,
rejects a changed body under the same key, and returns only newly admitted accounts
for notification. A repeated completed request never selects the next oldest batch.
Errors and completed replays produce no approval notification. The approval
marker stores only the request digest under a hash of operator account and key
in settings. A changed body conflicts. The marker and selected batch commit
together. Native HTTP tests exercise all nine writes through the default
composition. A generated property refuses derived, expired, revoked, missing
and substituted sessions before any write. Delisting before commit rolls back
the operation and its audit.

TestGitHubBrowserConnectionAuthorityProperty and StateProperty generate hostile
selectors, authority, cookie, form and signed-state cases. ProviderAndReplay runs
start, setup, callback, picker, selection and completion through actual Server HTTP
with a fake provider and transaction writer. CommitRefusal and RequiresTransaction
cover authority races, safe errors and missing adapters. ConsoleWriteHTTPAuthorityProperty
covers all nine actual writes, forwarding the authority and target, refusal before
a writer, committing conflicts, private failures and missing transactions. Existing
console executable acceptance now injects an explicit store-backed transaction fake.
The old TestSettleFinishedNodeClearsTheWindowForAnUnmeteredFinisher is retired with
its dead helper. Store's TestMeteredUnitNodeLifecycleUsesImmutableAward,
SettlementFailureRollsBackResult and StaleReportCannotSettleReplacementProperty
retain the immutable-billing, atomic-finalization and stale-holder invariants.

## Final credential helper and reporting cut

The legacy controller Principal, context accessor, HasScope and Scope constants
are deleted. No production handler reads the old context. Tenant, egress and
runner-budget wrappers that depended on it are removed. Policy-authorized
handlers receive Authorized, and internal lifecycle reads select their team
explicitly. Live-log append/read HTTP handlers and LiveLogRead are deleted.
Dashboard log streaming retains its in-memory reader.

The final metrics GET and POST routes and usage POST route are registered.
MetricWriter declares Team, WriteMetric(ctx, Authorized, MetricSample) and
WriteUsage(ctx, Authorized, NodeUsage). W1 implements it on Tenant. Each write
rechecks ancestry, current membership, exact tenant/job and the selected route
alternative inside its transaction. Claim and owner reports must retain their
current holder/generation and attempt fences. Publication cannot write to another
run. Metric kinds, nonnegative resource values, interval CPU-time constraints,
timestamps and bounded usage values are validated before calling the writer.
Metric-limit errors return the canonical rate_limited code. Missing or foreign
writer adapters return policy_missing. The raw metric and usage writers are not
HTTP fallbacks. The handler and client tests use explicit transaction/transport
fakes. The production transaction and replay integration remain W1 work.

The usage report contract still needs W9/W1 coordination: the retained client
sends one additive observation per completed process, but the body has no process
report identity. Two legitimate identical observations cannot be distinguished
from exact-body retries by the WriteUsage inputs. W2 has not changed that foreign
wire shape or claimed durable replay safety. W9 and W1 must settle the identity
contract before connecting a production usage writer. Omitted metric timestamps
also need a persisted receipt-time/replay rule. The default path refuses missing
writers rather than using the legacy additive writes.

OperatorSettingsWriter declares WriteComputeLimits, WriteSignUpSettings,
WriteStorageSettings, WriteStorageQuota and WriteStorageAllowance. Every method
receives Authorized. W1 supplies these methods on Operator. Recheck current
ancestry, deployment grants or the configured direct-console-session gate inside
the committing transaction. Never accept an actor from the body. Derive target
teams from Authorized and resolve team existence inside quota transactions.
WriteSignUpSettings takes store.SignUpSettingsChange, preserving omitted patch fields.
It validates and commits all supplied mode/limit changes and their audit together,
using the store clock. Quota writes preserve allowance and return the committed
projection. Allowance writes preserve other quota fields. Any failed validation,
authority check or write rolls back the whole request. Missing adapters return
policy_missing. The raw settings methods are no longer HTTP fallbacks.

Request budgets wrap actual policy handlers after authorization. Their keys use
the verified team/authority and holder. Only the initial root request may supply
a runner identity header. A bound holder cannot multiply its heartbeat budget by
changing headers. Empty final claim responses spend the polling budget, and an
award clears idle advice without spending it. No new limit or default was added.
Egress concurrency failures use the canonical rate_limited envelope and a named
reason in error.details. Audit identifiers use the final run/job path names, and
successful empty claims are skipped without the retired offer header.

TestServerPolicyConsoleUsesDirectSessionAndConfiguredGate exercises the default
console adapter with a real hashed SQLite account session, encrypted CSRF key,
current reader membership, configured operator account and a fake committing
writer. Its property varies selectors, operator configuration, CSRF, explicit
presentation and origin. Roleless console sessions and the production commit gate
remain W1 integrations. Existing peer transport properties remain fake-backed.
ReadAuthentication still refuses CredentialPeer, and C1 has no trusted local
mutation-authority representation. W2 has not fabricated a persisted credential
or a permissive local fallback.

The implemented declarations now live in capability/authentication_types.go,
capability/management.go and controller/policy.go, engine.go and data.go. Their
former *_contract.go files are deleted. W9's client declarations are unchanged.

## Output reads and retired claim input

`GET /api/v1/runs/{run}/jobs/{job}/output` returns `JobOutput` with verified
JSON bytes selected by `Tenant.ReadJobOutput`. The store reloads the complete
live authority chain, membership, binding and selected record in one snapshot.
Member reads require current runs.read. Claims require the declared dependency,
recovery, generated-group, expansion-source or pipeline-reference relation in
C1. Request hints cannot select a different relation. Child output uses the
same record reader after checking its creating-job relation. Missing, expired,
corrupt and unreadable records fail. Neither route returns a signed legacy
output grant.

C1 retired ResolveClaimInput and its wire types. The stale controller
`POST /api/v1/runs/{run}/jobs/{job}/claim/input` policy is removed with them.
The local node-credential probe now uses the existing
`GET /api/v1/runs/{run}/local-host/nodes/{job}/data-authority` policy. It retains
only data.read, ownNode and sensitive liveness. No transport alias remains.

## Console refund records

`POST /api/v1/operator/teams/{team}/refunds` records a completed provider refund.
It accepts `OperatorRefund` with payment_id, refund_id, positive amount_cents
and reason, plus an Idempotency-Key header. It requires a configured direct
operator-console session and CSRF. It makes no provider request.

`ConsoleWriter.ConsoleRefund` is implemented by `store.Console`. The committing
transaction rechecks the direct session and configured account, resolves the
payment's current-mode persisted paid grant and refuses a different path team.
The existing reversePaymentTx owns prior reversals and the paid-total bound.
Its reference is `refund:<mode>:<refund_id>`, shared with verified provider
settlement, so later delivery cannot debit the same refund twice.

The existing business_events table retains operator.refund_recorded with the
operator, reason, normalized request digest and result. Its subject hashes the
operator and request key, scoped by path team. Exact replay returns the receipt.
Changed request or mode conflicts. Authority is rechecked even on replay.
The ledger reversal, its existing audit and the request receipt commit together.
OperatorRefundRecorded returns team, optional grant_id, paid_micro,
reversed_micro, balance_micro and created. No schema change is needed.

<!-- design:end -->

## Signing implementation ownership

The physical ObjectBackend owns filesystem/S3 validation and signing. The
controller's CloudFront adapter embeds S3 and delegates to its Sign method before
replacing an ingress GET URL. It uses the same validated key, digest, size and
expiry. PUTs and direct requests retain the S3 signature. Ingress selection travels
in a private request context and grants no object authority. No foreign interface
changed. W1 data-authority, metadata and charging composition remains required.


## Runner observation cutover

`Server.WithRunnerInventory` supplies the tenant-scoped `RunnerInventoryReader`:
`Team() store.Team` and `ReadRunners(context.Context) (capability.RunnerSnapshot, error)`.
The snapshot types live in `internal/capability/runners.go` so persistence can
implement the reader without importing the controller.
`RunnerSnapshot.Now` is the store clock for the same snapshot as its `Runners`.
Each `RunnerObservation` carries its `Team`, granted `nodeproto.RunnerIdentity`,
location, last accepted observation time, active job references, enrolled maximum
concurrency and optional `RunnerHeadroom`. Headroom carries cores, memory bytes,
queue depth and the store's accepted `ObservedAt` time.

W1 implements this read on Tenant. Resolve live enrollment and complete ancestry,
then join accepted polls, renewals and current awards by their stored authority.
Use the award's immutable runner identity for cloud/here activity. A display name,
holder string or caller label cannot select or merge authority. Keep distinct
roots distinct even if their names match. Return only this tenant's rows and
current same-team job references. Exclude expired, revoked or replaced authority.
Neither a lease deadline nor a caller-supplied clock is an observation time.
An accepted renewal updates liveness without advertising free capacity or changing
headroom. LastSeen and headroom observation times remain separate. Unknown
headroom stays absent. The shared claim wire exposes available cores/memory but
has no queue-depth observation field. W8/W9 must supply that contract before
real queue-depth ingestion can be connected.

GET /api/v1/runners and GET /api/v1/queue use this snapshot. The former retains
its `agents` response field, with granted name/type/location and copied labels,
activity and capacity. Duplicate names remain separate rows. No live jobs and
an observation older than the existing two-minute window yields `offline`.
Headroom older than the existing one-hour window is omitted. Foreign readers,
foreign rows, absent snapshot clocks and corrupt numeric/timestamp facts refuse
before any partial response. The default uses Store.ForTeam only when Tenant
implements RunnerInventoryReader. Missing adapters return `policy_missing`.
There is no ListAgentClaims or process-local observation fallback.

Operational runner gauges use the same tenant snapshots through the existing
Operator.ListTeams enumeration. A failed read preserves the previous sample and
logs the failure. Claim-attention uses Tenant.ListRegisteredAgents and its
committed profile/LastSeen. It does not overwrite enrollment from an in-memory
poll. Successful authenticated machine claim polls call C1 RecordAgentLabels,
which rechecks live authority and persists filtered labels with the store clock.
Here and launcher credentials do not enter that machine-only transaction.

The old holder parser, claim-token-prefix projection caller, runner presence,
headroom and heartbeat registries, and their obsolete claim-body structs are
deleted. The port inventory records the test mappings. W2's snapshot tests use
real member credential issuance/authentication and a fake reader. Claim attention
and accepted-poll observations use real SQLite. No native cell or production
ReadRunners adapter is claimed by these component checks.

## Account-session origin and effective lifetime

`Principal.DirectAccountSession()` identifies an authenticated account session
whose committed ancestry has no capability parent. `CredentialAccount` uses
this predicate for owner consent and account-authentication operations. Changing
a bearer into a session preserves its capability parent. That session may
continue using its grants but cannot establish independent account consent.

`POST /api/v1/tokens` provisions a member root only for a direct account
session. Bearers and capability-derived sessions delegate from their current
tagged authority. `ValidateAuthority` returns the shortest expiry, revocation
grace deadline or binding deadline in the complete live ancestry as the leaf's
effective expiry. Omitted delegation expiry inherits that ceiling.

The store uses the same origin predicate in `checkMemberProvision`, machine
provisioning and `SetSourceMachineConsent` after resolving authority inside each
committing transaction. Machine provisioning also accepts deployment authority
and restricts grants to `nodes.claim` and `triggers.claim`. The native
committing-origin property covers detached member and machine roots and both
consent directions for generated capability-derived sessions. The session
creation transaction and browser verifier refuse credentials with a data scope.


## Canonical dashboard source and pause resume

Dashboard triggers send `source.location.remote_url`, the `.sparkwing` pipeline
directory and the selected source ref. The controller derives repository identity
from that acquisition location before invoking `AdmitRun`. GitHub identity comes
from the selected team's live App installation and its current repository API
response. A display name or persisted runner-enrollment slug cannot supply a
numeric identity because repositories can move or names can be reused. Missing
GitHub resolution returns `unavailable`. A supplied key that differs from the
resolved identity returns `forbidden`. Other providers use C4's canonical remote
identity. Credential-bearing remotes are refused. This lookup attaches no App
provenance and grants no pool or source authority.

The first-seal policy remains `IntakePolicy.ResolveIntake`. Its C1/C4 composition
must supply resolved options, operator ceilings, verified source location,
repository authority and checkout SHA. The controller source test uses a fake C1
transaction behind that existing interface. GitHub metadata transport is also
fake, while installation ownership is stored in SQLite.

`POST /api/v1/runs/{run}/jobs/{job}/resume` requires one JSON object with no actor
fields. `DebugPauseResumer` declares `Team()` and
`ResumeAuthorizedDebugPause(context.Context, capability.Authorized) error`.
`Tenant` implements it. The committing transaction reloads current ancestry and
membership, repeats native route authorization, locks the run and verifies the
job. It closes open pauses with the current subject and store clock, clears only
`status_detail=paused` and appends one `debug.resumed` event. It preserves job
status, attempt, generation and claim authority. A repeated resume writes no
second event. Missing jobs return `not_found`. Foreign or changed authority
cannot close a pause. The legacy release HTTP route and its anonymous fallback
have no handler.

`TestResumeDebugPauseCurrentAuthorityProperty` generates changed membership and
foreign selectors against the concrete transaction. The HTTP port verifies
principal attribution, repeat delivery, missing jobs, strict body decoding and
revoked credentials. `TestIntakeSourceNumericIdentityProperty` varies display
text and a forged numeric key through the real router and source resolver.
Existing `CreateAuthorizedDebugPause` and `ResolveAuthorizedApproval` contracts
still need their C1 committing adapters and public diagnostic masking.


## Native bootstrap, data and controller attempt snapshots

Store.EnsureBootstrapCapability sends the configured bootstrap seed through
Store.Issue in a locked transaction. The settings marker commits with the root
and survives revocation. Only the fixed bootstrap subject and administration
grants accept a seed. Store.HasDeploymentAdministrator resolves complete live
ancestry. A future rotation deadline stays live until that deadline in both
authentication and native reads.

WithDataObjects supplies concrete store authority, access, metadata and download
charging adapters. Tenant.ResolveDataAuthority derives sealed repository and
exact uploader identity. ChargeDataDownload rechecks the read edge and object
size in the charging transaction. Cancellation closes sensitive blob access
without ending holder result rights. Logical-record selection checks live scope
before selecting a digest. Its record transaction checks the selected record
and every reference edge. Caller presentation fields do not select another
persisted authority.

A controller work capability with attempt zero covers its holder generation.
Authentication projects its current attempt and claim data scope from the same
committed BindingState.Current after checking run, job, generation and holder.
Node-host credentials and controller delegates with a nonzero attempt retain
exact attempt fencing. The projection copies the scope and never changes the
persisted capability. Native HTTP claim, start and incremental log commits
exercise this boundary.

ReadAuthentication projects separate cancellation intent from the committed run
and job rows. Job intent takes precedence, including an empty job reason. It
does not substitute terminal result reasons.

The native launcher snapshot checks the exact observed award against current
unit ownership and launcher ancestry. It returns immutable execution inputs
without bearers, or the exact supplied Kubernetes instance for cleanup. Sync
preserves holder result rights. The legacy SyncLaunchJobs implementation is
deleted. W8 owns real Kubernetes correlation and teardown evidence.


## Native console and queue-priority adapters

Console finance operations share the existing ledger transaction helpers.
Account selection resolves an exact ID or unique email in the deletion
transaction. Native deletion revokes capability envelopes and advances their
revocation generations. Team deletion uses the shared run and job transitions
to record cancellation intent. Its internal deletion actor exists only while
that transaction holds the selected team's deletion scope. A wire actor cannot
create that scope. Live jobs retain their holder and attempt while draining.
Global account audit events use nullable team ownership from the final schema,
with a deterministic primary key preserving kind/subject deduplication.

PUT /api/v1/queue/groups/{group}/priority is registered under its existing policy.
It validates controllerwire.QueuePriorityWrite, preserves explicit zero and
passes the Idempotency-Key through controller.QueuePriorityWriter. That local
interface declares Team() and SetAuthorizedGroupPriority(context.Context,
capability.Authorized, string, controllerwire.QueuePriorityWrite)
(controllerwire.QueuePriorityResult, error). WithQueuePriorityWriter supplies a
tenant resolver. A missing or foreign tenant writer returns policy_missing.
The transaction owns current authority, stable run/number resolution, rank and
position selection, exact retry identity and unchanged holder leases. C1 has
not declared persisted integer priority or an ordering transaction. The HTTP
property uses a fake for that boundary and claims no persisted ranking evidence.

## Native data binding and persisted result proofs

Every data endpoint decodes its operation and selectors before authorization.
The authenticated data scope supplies the run, job, attempt and generation.
Commit binding checks the selected upload ticket against its current uploader.
Has-many binding checks live read authority before the handler filters each
requested digest through the read set. Account data opt-ins retain only the
read or publication grant allowed by their current membership role.

`Authorized.Result` carries the exact HTTP-body SHA-256, authenticated actor,
route, resource and fence into `Actor.Result`. Node and run transitions persist
that proof in their existing terminal `state_actor` value. Plan acceptance
persists its body digest in the issuing capability's existing `result_digest`
column in the acceptance transaction. These records have separate owners, so
finishing a run retains its accepted plan proof. Authentication reads the proof
in the same snapshot as current ancestry and binding state. A terminal replay
requires the same actor, route, resource, generation, attempt and body digest.

The native controller tests cover all nine data endpoints, including a signed
filesystem upload, committed receipt, result record and download. Native result
tests cover node completion, plan acceptance and run finish through the unchanged
server constructor. Generated cases reject changed result bytes, actor and fence
proofs, request authority hints and reads after membership removal.


## Shared credential authority owners

The bootstrap setup transaction locks the seed gate before resolving its actor.
Issuing the first non-bootstrap deployment administrator revokes the seed and
ends all descendant authority through its ancestry generation in that transaction.
A still-live identical seed can survive an unfinished setup restart. A consumed
or different seed is refused as already used.

Deployment authority uses the same predicate in root issuance, readiness and
route policy. Console management carries `ConsoleProof` naming its direct
session. Native transactions resolve it again and check the configured account
and session lifetime. Inventory, rotation and revocation share `OwnsToken`.
Native ProvisionPolicy, CapabilityManager and OIDCReader implementations are
composed by the production store and controller constructors and checked at compile time.

`Repositories == nil` is unrestricted. A nonnil empty list authorizes nothing.
RepositoryPattern and RepositoryIdentity distinguish path patterns from canonical
keys. RepositoryAllowed and RepositoryScopeNarrows own matching and narrowing.
The award boundary and claim admission both intersect every ancestor's scope.
Delegation and owner expansion preserve that distinction.

Pod bootstraps require the bootstrap credential class before any ordinary route
alternative. Only source acquisition and the one-time exchange declare that
class. Signed filesystem errors use the canonical API envelope. An unsupported
method returns method_not_allowed and Allow before signature validation.


## Decision 112 billing composition amendment

C1's `Tenant` now implements `BillingWriter`. `Server.New` reaches it through
its native tenant resolver. The controller asserts conformance at compile time.
`WithBillingWriter` remains the contract test seam. No extra runtime resolver
configuration is needed for checkout, card, pay-now or owner budgets.

`BillingPage.IntentID` distinguishes the callback's setup/payment intent from
the hosted-session `ProviderID`. Card setup responses contain `id`, `url`,
`customer` and `setup_intent`. Recovery responses contain `id`, `url` and
`payment_intent`, with an optional customer that must match the reservation.
Credit responses contain `id` and `url`. Optional `expires_at` can shorten the
reserved expiry. Provider service idempotency uses the persisted reservation ID
as `key`, and also as `setup_id` for card setup. These facts commit before any
signed callback may change the ledger. Webhook delivery before completion must
retry after correlation has committed.

`PaymentConfig.HTTPTransport` optionally supplies the external provider
transport. The verifier keeps its provider URL, credential files, request
validation, deadline and redirect policy. Tests use this seam for provider GETs
while keeping signature verification and store settlement real.

Decision 112 supersedes purchase-window and purchase-cap gates in the earlier
billing-page paragraph. Current member authority, amount validation and deletion
guards govern funding. Cloud admissions own standing and budget enforcement.
`TestBillingModelProductionPagesAndBudget` and
`TestBillingModelProductionCardPrepayAndRecoverySettlement` exercise construction,
real sessions and CSRF, page commits, provider delivery and signed settlement.

Controller startup commits its validated `--payment-mode` through
`Operator.SetPaymentMode` before serving requests. Verifier configuration and
ledger configuration therefore share the operator's mode selection. Constructor
proofs configure the ledger through that operator method instead of SQL seeding.

## Runner integration amendment: source metadata and process auxiliaries

`BindSourceReport` names a source job and generation under its independently
live expansion owner or eval lease. Job annotation and summary routes accept
`expansion.submit` with that relation. They use the same committing metadata
writers as work reports. Step metadata and step lifecycle routes retain their
work-attempt fence. Trigger and plan reporting continues to use `node.report`.
The fixed Award grant tables are unchanged.

The private local-host snapshot, authentication and binding routes accept plan
`data.read` with `BindPlanOwner` and eval `expansion.submit` within its bound
run. The store repeats eval source and lease authorization before returning the
snapshot. These reads expose accepted state for reconstruction and do not grant
node mutation. Root data-authority reads use `data.read` with the plan-owner or
source-report relation.

Node-host mask, annotation, summary and cleanup routes accept an eval process's
`expansion.submit` under `BindSourceReport`. The process registry checks the exact
registered process, captured source job and owned cleanup registry before checking
its parent relation. Trigger and plan processes retain their own owner bindings.
`FuzzOwnerProcessCannotOutliveItsAuthority` covers both output reads and auxiliary
mask authorization after lease loss, cancellation, revocation and membership loss.

Trigger-owner expansion submissions also use the existing expansion route under
`expansion.submit` and `BindRunOfTrigger`. The controller narrows that persisted
owner to the requested source generation before calling `AcceptExpansion`.
The delegated capability retains the original owner lease and grants only
`expansion.submit`. Its bearer stays inside the controller. The committing
expansion transaction rechecks the owner chain and source identity, including
on an identical replay.

## Runner composition amendment: planning failure

The private `POST /api/v1/runs/{run}/local-host/finish` route also accepts a
plan award's existing `node.result` grant with `plan_owner` and result liveness.
The controller and committing transaction restrict that authority to failure or
cancellation before plan acceptance. Success and run scheduling remain outside
the plan award. The store records a fixed masked stage and closes the plan lease
with the run verdict. No execution grant is added to the frozen plan table.

## W9 launcher preparation authority

The existing launcher service `claims.launch` route may request preparation
renewal for exact awarded lease fences through launcher sync. The controller
revalidates root ancestry and the original bootstrap award in the store writer.
It validates all Job observations before changing any lease. An invalid or
ambiguous observation therefore cannot cause a renewal before its refusal.

Bootstrap grants remain exactly `source.read` and `claim.exchange`. No bearer is
returned by renewal, persisted in a canary or copied from the launcher into a Job.
After exchange the launcher cannot renew the SDK-owned lease. Its preparation
request receives no acknowledgement for that fence. Normal reconciliation still
reports the exchanged live Job, while blocking publication treats a missing
acknowledgement as `lock_held` and cancels its preparation context.

## Integrated source submission authority

`POST /api/v1/data/source` and `POST /api/v1/data/source/{upload}/commit` use
controller audience, team ownership, authenticated capability/session delivery,
and unbound member/local `runs.write` authority. Neither operation is retried
transparently. The store rechecks tagged requester ancestry, live team grants
and repository scope before reserve, commit and intake binding. The caller
cannot select provenance or create a data execution grant.

The `sealed_source` read edge admits only the `source_bundle` digest in the
current run's persisted seal and its source-submission record. It remains under
ordinary live awarded data authority. It grants no cross-run, cross-tenant,
cache, binary or App trust. HTTPS and HTTP signed transfers follow the same
configured endpoint contract as other shared data objects.

GitHub identity is verified before admission using the configured App, a
confirmed team HTTPS source credential, or the public repository API. Bound
credential decryption remains server-side and registers the token with masking.
A private package context proof carries the resolved location and repository
through the intake transaction; policy refuses missing or changed proofs and
does not open a second SQLite connection inside the transaction. The proof is
absent from the wire and seal. CLI environment tokens stay at the GitHub API.

## Credential inventory and debug-view reconciliation

Credential inventory, rotation and revocation revalidate a presented console
proof with the same transaction-local direct-session and configured-operator
predicate as issuance. A cached console projection never grants authority after
configuration, ancestry or session liveness changes.

Job detail returns `controllerwire.DebugJob`. Embedded controllers and tests
supply its committed snapshot and attempts through the existing
`EngineController.ReadRunStatus` callback. The default still reads
`Tenant.ReadRunView`. The projection validates the selected run/team, nonempty
job identities and current attempt fences before returning public fields.
No route grants, credential classes or wire fields change.


## Operator settings composition reconciliation

Operator implements the five OperatorSettingsWriter methods. Each mutation resolves
live ancestry and the exact deployment grant, or the current configured direct
console proof, inside its transaction and rechecks before commit. Existing compute
and storage setters share their transaction helpers with these authorized writers.
SignUpSettingsChange lives in store, which owns its atomic patch and provenance.

BindNone permits an unbound deployment credential to select a target team for
operator quota policy. The shared route policy still requires the exact operator
grant. Run and job selectors keep their bindings, and tenant credentials cannot
select a foreign team. Relational authorization resolves console proof through the
same transaction-local owner as issuance and settings writes.

## GitHub completion identity

Completion reads the sealed run request's verified App facts, never trigger
environment strings. The numeric repository ID must agree with its repository
key, execution SHA and ref. Delivery needs the installation still bound to the
same tenant and unsuspended. It mints only the exact repository ID with
checks:write or statuses:write. `githubapp.Client.RepositoryByID` resolves the
current name under that restricted token, so a rename or reused old name cannot
redirect a report. Installation ownership is rechecked after that lookup.
An invalid or unavailable identity keeps the revision due.

The shared completion worker calls the provider synchronously under the
existing per-run report lock and acknowledges only a successful revision.
Stale pending and running deliveries cannot regress the persisted phase.
Timeout conclusions derive from the committed terminal event reason. Summary
payloads contain aggregate outcomes and the authenticated dashboard link,
without job identifiers, arguments or private errors. The old queue-only
Server.Shutdown API and its callers are removed because no reporter goroutine
remains to drain.


## Trusted App origin integration

The verified webhook adapter now calls C1 `AdmitGitHubAppRun` with the selected
installation and consent generation. C1 supplies `VerifiedAppIntake` only to that
admission transaction. `Server.ResolveIntake` keeps the normal settings, ceilings
and retention resolution and uses those trusted delivery/source facts.

The transaction-local `github_app` authority reference is an accountability tag.
It is not an accepted credential class, token spelling, authentication route or
new issuance operation. Customer intake cannot supply its proof. Revocation,
installation moves, consent replacement and partial-delivery replay are checked
again in the committing transaction. Source credential release remains a separate
claim-authorized operation.
