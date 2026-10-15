# C2 credentials contract

<!-- design:begin -->

This contract covers frozen architecture sections 3.6, 5, 6.5, 8 and 9.5,
including A58, A59, A66, A68, A72 and A73. The Go files declare the target
interfaces. The policy evaluator, authentication verifier, HTTP policy router,
delegation validator, data/source checks, billing calculation, payment verifier
and error codec are implemented. Filesystem and S3 object signers implement the
signing boundary. Persistent issuance and authentication reads still need the
store implementation. The Stripe callback and thirteen console endpoints use this table in the existing
controller. The remaining routes have not switched to it.

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
| `ProvisionKind`, `ProvisionSpec` | Supply trusted root-origin authority to the same issuer, pending the root clarification below. |
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
| `controller.NewFilesystemDataSigner`, `FilesystemDataSigner.Sign`, `FilesystemDataSigner.Verify` | Sign and verify exact filesystem transfer constraints. The blob owner verifies uploaded bytes before recording a receipt. |
| `controller.NewS3DataSigner`, `S3DataSigner.Sign` | Sign one configured bucket's GET or checksum/size-bound PUT without making a storage request. |
| `controller.PaymentSettler`, `Server.WithPayments` | Supply C1 settlement to the signed Stripe callback. The server registers `/webhooks/stripe` through `PolicyRouter` and passes only verified events to the store. |
| `controller.Server.WithAuthorization` | Configure the shared verifier, bindings, transport identity and CSRF adapters before constructing the server handler. Missing adapters fail closed. |
| `controller.RouteAuthorization`, `controller.Endpoint`, `controller.PolicyRouter` | Wire identity, decoding, CSRF, external proof and authorized handlers into the matched route. |
| `controller.NewPolicyRouter`, `PolicyRouter.Register`, `PolicyRouter.Validate`, `PolicyRouter.Registrations` | Register handlers independently of the policy table and check their proof wiring. Combine controller and node-host registrations for the complete inventory check. |

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
| `bodyNamesRun` | Decode and bind the body's run/parent lineage before handler execution. Member intake may omit a parent; a claim may not invent one. |
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
HMAC-SHA256. Its upload path contains an opaque encoding of the staging key.
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
installation unbinding, orphan reconciliation and credit-settings writes. Their
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

The registered-route comparison must use actual rewritten router registrations,
not a list generated from `Routes`. A separate handler-policy lint must reject
authorization calls inside handlers. T1 owns runtime scenarios; C2's tests
exercise only available contract data and functions.

## Open questions for the lead

1. **Root origins and sessions.** Section 5.1 names three issuance forms but
   does not define how the first operator, member/machine/service roots or
   Actions exchange enter them. Here awards may parent to a session, so C1 persists tagged ancestry rather
   than a capability-only parent FK. Chosen reading: keep one
   `Store.Issue`, represent trusted provisioning as the root-only Delegate
   case (`ProvisionSpec`), and use `AuthorityRef` for capability/session
   ancestry. No new issuance kind or detached here root is added. C1 must
   match both ancestry forms and root provisioning checks. This needs lead
   ratification because literal parent-subset delegation has no parent here.
2. **Host billing minimum.** A59 assigns explicit zero minima to plan/eval
   compute and a minimum to job compute. The retained host interpretation
   uses `limits.HostBillingMinimum` independently, excludes customer waits
   and never bills planning/evaluation twice. The number remains C4-owned.
3. **Route spellings and fixed non-work grants.** The frozen document names
   resources and one work grant table, not every final path or trigger/plan
   grant. Chosen spellings include `/runners/{jobs,runs}/claim`,
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
   Stripe callback and thirteen console-only endpoints. Other routes retain legacy auth and
   registrations. Wire final handlers and decoders to `PolicyRouter`, provide
   account/console/verified-local identity and CSRF adapters, then delete the
   old auth stack and handler authorization. Final route-inventory and
   handler-policy lint checks must cover those actual registrations. W3/W4
   must provide node-host process facts through the same verifier boundary.

6. **Card and dispute events.** The existing card setup, off-session charge,
   refund, fraud warning and dispute callback routes are outside the four
   checkout-only `PaymentEventPolicies`. C1 has no verified-event contract for
   those outcomes. Extend that contract before replacing those remaining
   service-bearer routes. No new event kinds or settlement semantics were
   inferred here. The old external checkout service also needs to send paid
   callbacks to `/webhooks/stripe` and commit checkout mode/currency through C1.

7. **Console mutation rechecks.** Existing grant, trust, hold, deletion,
   waitlist and credit-settings store methods do not accept `AuthorityRef`.
   C1 must replace those signatures with an authority-bearing transaction
   boundary that rechecks revocation and derives the audit actor. The HTTP
   policy gate is implemented, but it cannot establish that transaction
   guarantee. No C1 signature was changed in this cutover.

8. **Local orphan reconciliation.** The shared HTTP client uses the console-only
   maintenance path. W3/W8's automatic local maintenance must use its internal
   store boundary rather than a deployment or local-peer credential on that path.

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

<!-- design:end -->
