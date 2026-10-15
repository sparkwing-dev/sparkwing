# C1 data contract

<!-- design:begin -->

## Committed result copies

`NodeRow.OutputRecord` and `ManifestRecord` persist the exact selectors committed
with terminal state. Materialized copies use `blob.MaterializedRecordKey` with
the destination result sequence. The destination has no synthetic attempt.
`SetResultBlobRecords` configures both attachment and materialization. Its reader
also supplies `VerifyBlob(context.Context, blob.Blob)` for copies. The filesystem
and S3 backends verify every selected object and manifest member before mutation.

`CopyMaterializedRecords` retains source provenance and reassigns record lifetime
to the destination run. It uses the shared record mutation owner, leaving commit
and rollback to reconciliation. A materialization savepoint discards all copies
on a missing, expired or corrupt source. A materialize-only miss returns Dispatch.
An ordinary miss continues to execution admission. Authority errors stay errors.
Memo sources name their exact record and need no source attempt or surviving run.

The private execution wire carries both selectors unchanged. Native host storage
reads select them directly. Trigger and plan owners can read selected terminal
records in their run. Dependencies and pinned references require the exact selected
record even when the producing result has no attempt. Digest equality grants no
record metadata authority.

`Tenant.ResolveBlobAuthority` exposes the transaction-local scope and uploader
resolver to the data controller. It accepts only a data read or write grant and
refuses caller hints that differ from persisted authority. Failed resolution
returns no authority. `DataController` uses this resolver for capability records
when no override is supplied. The materialization proof needs no custom data
authority callback and checks identity substitution and revocation after binding.

## Payment mutation transactions

Card setup, collection, payment reversal, dispute hold/release, warning and
refund-queue mutations have transaction-local owners. Their existing Tenant and
Operator entry points call those owners in a store transaction. Payment receipt
settlement can use the same owners without nesting transactions or committing
financial state before its receipt. No new unverified payment entry point exists.

Card setup ordering uses the supplied completion timestamp at nanosecond
precision, or receipt time when completion time is absent. Older timestamps
cannot replace the saved card. The subsecond migration marker is removed.
Collection amounts must fit the credit ledger's integer representation.
Refund retry state is read and updated under one transaction and ledger lock.

`TestPaymentMutationsRollBackWithReceipt` exercises eight mutations against a
failing receipt boundary and checks their financial state after reopening.
Generated properties cover refund conservation, payer isolation, immutable
replay, setup ordering and unrepresentable collection amounts. Verified event
dispatch now consumes C2's expanded proof in `payment_provider_settlement.go`.
The schema and billing-page writers below are pending W1 integration. The tests
substitute those tables and do not establish production payment readiness.

### Non-checkout payment persistence contract

`SettlePayment` dispatches verified setup, collection, refund, warning and dispute
facts through the existing transaction-local owners. One locked transaction
checks configured mode, immutable provider-object association and event replay,
then commits the financial result and receipt. A receipt failure rolls back the
financial mutation. C2 owns provider payload/status verification. C1 owns payer
association, supported ledger currency, integer arithmetic and settlement.

W1 must extend its final `schema.sql` with these local C1 requirements before
these callbacks can settle in production. No worker schema fragment or migration
is installed by W1b.

- `payment_events` keeps its existing columns and `(payment_mode,event_id)` key.
  Its kind check admits all `capability.PaymentEventKind` values. `checkout_id`
  becomes nullable for non-checkout events, and `team` becomes nullable only for
  a warning with no committed payer yet. Checkout events retain their composite
  checkout foreign key and required team/checkout association.
- Add `object_id TEXT NOT NULL DEFAULT ''`, `event_json TEXT` and
  `settlement_json TEXT` to that receipt. Non-checkout events require all three.
  The JSON stores the verified facts and `PaymentSettlement`, respectively.
  Checkout writes retain their existing shape. Index `(payment_mode,object_id)`.
  Several event IDs can refer to one provider object. They cannot change its
  payer association or a settled object's payment, amount, currency, method and
  card. A payment-intent failure is an observation before settlement. Its amount,
  method and latest charge can change before success while the persisted payer
  and attempt remain fixed. Closing a dispute preserves its
  terminal outcome and prevents a late open from restoring its hold.
- Add `payment_correlations` with `payment_mode`, `provider_id`, `kind`, `team`,
  `customer_id`, `currency`, `attempt_id` and `charge_id`, all non-null TEXT.
  The primary key is `(payment_mode,provider_id)`. Mode is test/live, kind is
  setup/collection, and team references `teams(name)`. Customer is nonempty.
  Currency, attempt and charge default to empty for setup. Collection requires
  its currency and exact persisted attempt/charge. These associations are
  immutable and survive run retention.
- The trusted billing-page completion transaction records a setup correlation
  from its committed reservation and confirmed provider setup ID. The trusted
  collection adapter records a collection correlation from its exact attempt,
  internal charge, customer and confirmed payment-intent ID. It also binds
  `card_attempts.payment_intent` before settlement. Provider metadata, callback
  parameters and caller-selected teams cannot create these associations.

`providerPaymentFixture` supplies a schema fake for this contract. It drives all
seven non-checkout outcomes, event/object replay, rollback, reopening, setup
ordering, partial refunds, warning escalation and payer isolation through the
real transaction owners. A signed refund also enters through C2's authenticator
and the public opaque-proof `SettlePayment` entry point. No provider network or
billing-page persistence integration is claimed.

Refund and dispute owners resolve the mode-scoped checkout or collection payer
before touching legacy financial rows. Unknown warnings may persist before a
payment is associated, so later settlement can refuse the warned payment.
Warnings escalate monotonically. Returning to non-actionable never removes
recorded protection. Mode isolation of retained card fingerprints and legacy
warning/collection rows still needs W1's final schema and writer cutover. This
contract does not treat the raw legacy rows as a new provider association.

## Persisted log readers

`Store.LatestNodeLog`, `RunNodeLogs`, `NodeLogSettled` and the legacy `NodeLog`
read model are removed. Controller log reads select tagged subjects through
`Tenant.ReadRunView`, then use `logseg.CommittedReader` and `Tenant.ReadLogSegments`.
Object listings and physical manifests grant no read authority. The reader binds
its actor to the authenticated capability prefix or session digest. Each segment
read and live-ring delivery rechecks current persisted authority. Log routes keep
the existing response-byte egress meter and log download-cap exemption.

Job reads select the newest attempt by ordinal and generation. Streams resume in
that order. Whole-run reads include committed job, trigger, plan and eval subjects.
Missing or corrupt committed bytes fail the read. Persisted incomplete seals add
a loss line or a `log_incomplete` stream event. Completeness verifies bytes before
reporting the persisted seal, and never changes the job result.

`Segment`, `SegmentCommit`, `LogSegments`, `RecordSegments` and `ReadLogSegments`
live in `pkg/store/result_logs.go`. Log subject validation uses
`blob.LogSubject.Validate`. The remaining declarations in `result_contract.go`
cover steps, annotations and summaries.

The controller tests drive the final SQLite claim, start, segment commit and
terminal transition through the reader. Revoking its capability refuses the next
read before physical I/O. Generated fake-boundary tests cover arbitrary payloads,
foreign actors and authority ending between inventory selection and segment read.
Authenticated `LogTransport` signing/verification and host subject construction
remain separate integrations.

Remote log follow opens the stream without probing legacy segment capabilities.
Stream refusals remain errors. The old segment-unavailable classification is removed.

## Host log uploads

The pod log sink consumes `logseg.Data` and uses `logseg.SubjectUploader` for
signed uploads and transactional segment commits. Its subject comes from the
authenticated binding. It never infers a team or attempt from an object prefix.
An absent binding leaves the pod's stdout log intact and reports the missing
binding. Job sinks keep live watch delivery under that exact attempt fence.

`Client.LogSegment`, `SealLog`, `LogSegmentUploader`, `store.LogSegmentRequest`
and `LogSegmentGrant` are removed with the unused `EngineNodeLog` wrapper.
The generated sink property covers job, trigger, plan and eval subjects and
mutated input identities. The watch test uses an in-process transport.
W2 still supplies authenticated `LogTransport` and `SegmentRecorder` adapters.
W3 still supplies the production `logseg.Data` binding to the pod host.

## Log byte accounting

`Segment.Offset` and `Segment.Size` describe decoded log bytes. `StoredBytes`
and `Digest` describe the immutable compressed payload. Both sizes are positive
and participate in replay identity. `log_segments.stored_bytes` and
`log_streams.stored_bytes` count retained physical bytes. Segment start/end
offsets and `uploaded_through` count decoded bytes. Reads return both sizes.
The schema needs no new column because decoded size is `end_offset-start_offset`.
The subject uploader supplies both sizes on append and final sealing. The
committed reader verifies stored length and digest before emitting decoded bytes.

## Controller result verification

`Server.WithDataObjects` installs `storage.BlobManifests{Objects: objects}` through
`Store.SetResultBlobRecords` before serving or sharing the store. Terminal
output and manifest attachments use that backend inside the live tenant
transaction. `Tenant.CommitRecord`, `LookupRecord`, `LookupMemo`, `LookupDepcache`
and `CopyMemo` use the configured verified manifest reader too. An explicit
`BlobRecords` reader overrides it. Without either reader, manifest operations
fail. The controller property test drives real claim/start, upload
reservation/settlement, record commit and terminal transitions. Corrupt physical
manifest bytes refuse record reads and completion. Restoring them allows
completion and exact replay. Authenticated data binding remains C2's separate
contract.

The legacy `AttemptReport.OutputFrom`, `Store.FinishNodeCopyingOutput` and
their `node_outputs` copy branches are removed. Host memo materialization
uses the existing transactional `blob.RecordStore.CopyMemo` contract. The
unused backend output-read forwarding methods are removed too.

`Store.SetResultBlobRecords` also binds the real `CopyMaterializedRecords`
implementation to reconciliation's transaction. Retry and memo copies retain
exact selectors, verify physical bytes and commit with terminal state. Coalesced
copies still require a declared persisted read edge. A source ref and matching
spec hash grant no authority. The final waiter schema excludes the legacy
coalesce policy, so that table cannot establish a coalesced read. This path
refuses until its owner declares the missing authority.

`Tenant.StorageStanding` reports the selected team's policy, retained event,
blob and tagged-log bytes, and live upload reservation bytes from one snapshot.
The controller and retained-storage billing share the same blob/log inventory
owner. Historical usage counters never determine retained bytes.
The raw `Store.StorageStandingFor` and `Store.TeamStorage` reads, their
`TeamStorageUsage` type, and `Store.ReconcileFreeEventBytes` are removed.
`free_slots` records only slot ownership and `taken_at`. Event bytes come from
retained `runs.event_bytes`, including during event storage admission.
`Store.ReleaseExpiredStorage` is removed. Upload expiry uses quota admission
and the leased blob-maintenance pass.

## Amendment 1: authority and execution seams

`Store.ReserveStorage`, `Store.CommitStorage`, `StorageReserve`,
`StorageReservation`, `StorageCommit` and `DefaultStorageReservationTTL` are
removed. Public uploads use tenant blob tickets with exact identity, size,
digest, expiry and verified settlement. Generic byte deltas cannot mint a blob
or receipt. The controller's retention probe reads `Tenant.StorageStanding`
and slot availability without writing reservations. The legacy `Store.ReserveUpload`, `Store.CommitUpload`,
`Store.UploadForTeam`, `Store.ReleaseStorage` and `UploadRequest` are removed.
Their output-upload and output-commit HTTP writers are removed too. Generic
`storage_reservations` and `storage_commit_receipts` no longer have writers.
The legacy `Store.OpenNodeLog`, `SignNodeLogSegment`, `NodeLogSegments`,
`MarkNodeLogSealed`, `GetNodeLog` and `CurrentNodeAttempt` APIs are removed.
Their node `log-segments` and `log-manifest` HTTP writers and S3 signing code
are removed too. No Go writer references `node_logs` or `node_log_segments`,
and reads no longer seal abandoned logs. `Tenant.RecordSegments` and leased
`Operator.SealAbandonedLogStreams` own tagged persistence and sealing.
Legacy capabilities no longer advertise segment upload support. Old output
reads and host/client interfaces still need their final record cutover.
`Store.NodeIsAncestor` is removed. Its controller caller uses
`Tenant.NodeIsAncestor`, which shares the blob read-set dependency resolver over
sealed job specs, including optional, artifact and generated-group dependencies.
The duplicate `MaxOutputBytes` and `MaxRunOutputBytes` constants are removed.
Remaining output callers use `limits.NodeOutputBytes` (16 MiB) and
`limits.RunOutputBytes` (1 GiB). File and signed-URL reads refuse bytes above
the shared per-job bound even when their recorded size and digest match.
`SetNodeStatus` and `RevokeNodeReady` on Store, Tenant and Client are removed
with their legacy HTTP handlers. Debug pauses use `status_detail` through
activity writes and leave lifecycle state intact. Queue withdrawal uses
`Tenant.TransitionNode` with `CauseRevoke` under the current owner fence.
Claimed, running and terminal jobs cannot be withdrawn. Refused transitions
change neither state nor events. The SQLite
`lifecycle-transition-replay-and-fencing/store-sqlite` proof covers withdrawal,
reopening, holder exclusion, cleared terminal claims and exact replay.
The raw readiness publisher remains pending its caller migrations.

`Store.TouchRunHeartbeat`, its Tenant twin, `Client.TouchRunHeartbeat` and
`Run.LastHeartbeatAt` are removed with the independent scheduler ticker,
legacy HTTP handler and Doctor's heartbeat-age run finalizer. `TouchNodeHeartbeat`
on Store, Tenant and Client is also removed with the node-touch route and
Kubernetes dispatcher's advisory heartbeat loop. Job claim renewal supplies
the job's liveness timestamp. Run liveness is
the current owner lease. `RenewUnit` and `RenewOwner` renew that exact fence.
Only the elected `ReapUnits` pass recovers an expired owner. Job renewal never
extends its run owner's lease. The final C2 owner-heartbeat route remains a
declaration for W2 to bind to the implemented `EngineController.Renew` adapter.

`Tenant.AcceptPlan` is the sole plan-acceptance API. The raw `Store.AcceptPlan`,
its separate decoder/validator/inserter and `Client.SubmitPlan` are removed.
The controller's claim-token acceptance branch and old launched planner are
removed with them. Legacy plan-node bootstrap, snapshots, claims and lifecycle
writers remain pending their caller migrations. They are not v2 acceptance paths.

Required-secret preflight returns canonical `plan_invalid`. After acceptance
rolls back, its diagnostic transaction rechecks the current owner, serializes
on the tenant's run and records the detail once using the store clock. It
rechecks the owner before commit. A failed or stale diagnostic write leaves
no diagnostic. The original refusal is retained if recording fails.

The SQLite `lifecycle-request-and-plan-seals/store-sqlite` cell exercises real
admission, owner claim, strict validation, settings resolution and sealing. It
checks invalid-input refusal, rollback on the second job insertion, immutable
replay and drift after reopening. Intake policy and enrollment use contract
fakes. Native execution and HTTP transport remain separate evidence.

Schedule reads, edits, deletion and fire history belong to `Tenant`. The thirteen
implicit-default-team `Store` schedule methods are removed. Local CLI and
dashboard constructors select the registered DefaultTeam. Controller requests
retain their authenticated tenant. A deployment tick enumerates teams through
`Operator.ListCronSchedulesAcrossTeams`, then selects each tenant before resolving
its schedules. `Service.Schedules` is required for tenant operations. A nil value
selects deployment enumeration only for Tick.

`Operator.RecordCronTick` and `Operator.GetCronTick` persist deployment tick
metadata atomically in `settings`. The old Store methods and their
`sparkwing_meta` writes are removed. The cadence guard reads the same `settings`
rows its existing writer commits. `Tenant.ArmCronSchedule` writes the required
`repo_key` through `runspec.CanonicalRepository`. GitHub identities require the
resolved numeric repository ID. A non-run history entry stores SQL NULL for
run_id. A fired history entry retains the final same-team run foreign key.

The SQLite `surface-schedules-and-managed-timers/store-sqlite` proof checks two
teams sharing a repository path, foreign read/edit/history refusal, atomic rollback
when history insertion fails, nanosecond timestamps, reopening, tick metadata and
tenant deletion. The generated package property varies 32 pairs of schedules
and mutation order. Native timers, schedule launch admission and HTTP remain
integration obligations.

[Amendments 1](amendments-1.md) records the worker requests and assignments.
These declarations leave transaction implementation with W1.

- `plan_contracts.go` declares `PlanSecretOpener`. The controller installs
  the trusted decryptor before serving. Acceptance selects tenant/pipeline secrets
  in its transaction and passes plaintext only to validation. Neither intake facts
  nor secret values arrive as trusted planner JSON. `Resolved.SecretArgs` retains
  every classification sealed at intake. Describe may add classifications.
- `enrollment_contract.go` declares `ClaimProfileResolver`. It resolves persisted
  enrollment from authenticated root/subject identity. Granted runner name/type,
  pools, repository authority and metering never come from labels, environment or
  a caller-selected runner name. Enrollment configuration and claim observation
  remain separate. No new registration table is required by this seam.
  `Store.ReadClaimProfile` gives controller adapters that same enrollment owner.
  Transactional matching re-reads registered capacity before awarding work, so a
  stale controller observation cannot widen it. Unknown capacity retains the
  matcher's existing meaning.
- `authentication_contract.go` implements the declaration of C2's
  `AuthenticationReader` on Store. A single committed snapshot includes store time,
  ancestry, current membership and route-specific unit/result/log facts. Binding,
  data-edge and source-release checks also run on the transaction owning the write.
  A source credential is released only after its authorization audit commits.
- Trusted root provisioning is the root-only Delegate variant. Roots have no
  revocation parent. `ProvisionSpec.Authority` is their authorizer, whose current
  rights constrain provisioning. Membership still governs account-root liveness.
  Here awards retain the requester's session/token ancestry and current runs.write.
  Member roots have their own optional expiry. The issuing session is checked at
  issuance and does not become their lifetime or revocation parent. Current
  account membership still constrains each subsequent use.
- `provision_contract.go` declares `ProvisionPolicy.AuthorizeProvision` for trusted
  bootstrap, machine/service enrollment and verified Actions evidence. W2 installs
  the policy before serving. It receives the issuing transaction's authority
  resolver and the complete trusted `ProvisionSpec`. Missing verification refuses
  issuance. The store rechecks current authorizer scope, expiry, root shape and
  registered team after verification. Only deployment token authority can set
  metering or authorize service issuance. Provisioning never grants console
  authority. Machine enrollment records the account authorizer in `created_by`,
  so only that account can change source-credential consent. Roots retain no
  revocation parent. The evidence providers remain W2's integration.
- Claim selection checks every requested kind's grant before one ordered scan.
  Runner selection accepts trigger/node, launcher selection plan/eval/node. The
  named trigger path retains the exact here reservation's trigger.host exception. Unknown,
  duplicate, empty or unauthorized kinds are refused. A new award is delivered once.
  A lost claim reply recovers through the held lease, not a second automatic award.
- `ReconcileRequest.MaterializeOnly` preserves terminal/holder decisions and may
  materialize an authorized source. A source miss returns `Dispatch` without
  self-claim or bearer. `ReconciledNode.Bearer` is private delivery for a fresh
  execution award, never persisted or placed in public status.
- `ReadFailure` returns a recorded `nodeproto.JobFailure` under a declared recovery
  edge and the immutable source generation. It preserves stage, code, masked
  message and details. It cannot manufacture a failure from a terminal status.
  `NodeResult.Failure` supplies that already masked structure in the result
  transaction. It must name the reported job and a failed outcome. A recovery
  holder can read only the source declared by its own accepted `OnFailureOf`.
- Run-host cancellation is scoped to its own live owner lease. Pending work can
  end immediately. Held work receives only cancel_requested_at and one request
  event, then its holder drains. It grants no right to overwrite holder results.
- Attempt starts at zero. First execution start establishes one. A started
  disruption's successor starts the next ordinal. An unstarted release consumes
  none. Application retry advances under the same generation. Start/retry return
  the authoritative ordinal in Applied. Owner/eval logs always use unit subjects.

`NodeTransition.SourceAttempt` is a required nonnegative ordinal pointer for
holder start, retry, report and unstarted release. It names the persisted ordinal
before the edge. Initial start supplies an explicit zero. A resumed claim supplies
the last ordinal it retained through disruption. Start and retry return the new
ordinal in `Applied.Attempt`. An unstarted report or release allocates none.
Other actors and causes must omit the pointer.

The source ordinal participates in the transition digest. An exact replay of the
last committed transition returns its recorded result, including after retry has
advanced the ordinal. Every other holder execution transition must match the
locked row's current ordinal. Missing or negative ordinals return bad_request.
A mismatched ordinal loses holder rights. Delayed reports cannot close a newer
attempt under the same claim generation. W9 must add the corresponding optional
wire field and preserve its presence through the adapter. The store never derives
an externally reported source ordinal from current authentication state.

`Store.ReapUnits` is the sole unit-expiry entry point. Every pass requires the
current `controller.reaper` lease and rechecks it in each unit transaction.
`Store.ReapExpiredNodeClaims`, `Maintenance.RecoverExpiredNodeClaims`,
`Maintenance.ReapExpiredTriggers` and `AgentLossRecovery` are removed. Recovery
returns tagged `ReapedUnit` results and never creates a retry run. A local owner
that has started fails in place after lease loss. An unstarted local owner
requeues without consuming a disruption. Terminal owner timestamps stay recorded.
The old dispatcher-expiry path and its application-retry accounting are removed.
Metered recovery uses the lifecycle settlement join. C2's production billing
calculator remains an integration point.
`Store.RequeueUnstartedClaim` and its Tenant twin are removed. Only the leased
pass recovers expired owners. Unstarted owner recovery preserves intake identity,
clears holder authority, consumes no disruption and fences every previous award.
C2's billing start rule still determines whether compute began at award or awaits
a qualifying pod renewal/start.

`Operator.MaintainConcurrency(ctx, lease, options)` runs under that same
`controller.reaper` fence. It locks affected capacity entries in tenant/key order,
removes expired or terminal holders and abandoned waiters, and promotes live queued work in
one transaction. The fence is checked before work and before commit. Any failure
returns zero counters and rolls back the whole pass. Expired waiter leases never
retain arrival order. Waiters bound to terminal or missing jobs cannot promote.
Live blocked waiters retain their arrival and lease. Options retain the existing
promotion lease and abandoned-waiter age defaults.

`Store.MaintainConcurrency`, the `Maintenance` function registry and its six
unfenced operations are removed. `OperatorStore` includes the leased pass. The
obsolete concurrency memo sweep, cache-count API and cache-cap fields are removed
because the final schema stores memo records through C1's blob-record contract.
Record expiry remains in `Operator.MaintainRetention`.
`Store.PurgeDeadLocalConcurrency` and `Store.CountDeadLocalConcurrency` are removed.
The elected pass cleans terminal work in every scope. Doctor no longer runs an
independent local-key repair or exposes its two repair counters.

Cancellation intent persists in `runs.cancel_reason` and `nodes.cancel_reason`,
separately from terminal result fields. The first request owns that intent.
`RunRow.CancelReason` and `NodeRow.CancelReason` retain it after draining and
reopening. Renewal carries it in `LeaseBeat.CancelReason` for job, owner and eval
units. A request-only edge never writes a holder's error, failure reason or exit
code. Run cancellation passes its reason to affected jobs without replacing an
earlier job request. C2's authentication snapshot still needs a reason field to
transport the same intent through `AuthenticationReader`.

Session creation and lookup use the store clock and Unix nanoseconds. The session
row holds a SHA-256 digest. Every authentication reads current membership and
complete capability ancestry. Account sessions follow current role grants.
Capability-backed sessions also retain their parent's team, account and grant
ceiling. The controller preserves those verified grants instead of replacing them
with the full role. The removed `triggers.read` permission is absent from the role
map because C2's final grant registry excludes it.

`SessionKeyCipher` and `Store.SetSessionKeyCipher` configure row-bound encryption
for `settings.session_csrf_key`. The row must be sealed and open under that cipher.
A missing key is minted once, atomically invalidating sessions signed by no
available key. Replicas open the same committed envelope. Wrong keys, malformed
envelopes and missing cipher configuration refuse session operations. The
controller's existing `WithSecretsCipher` installs its bound cipher on the store.
No new key file, environment variable or encryption implementation is introduced.

`CreateCapabilitySession(ctx, prefix, ttl)` replaces `CreateTokenSession`.
It derives identity from an unbound account capability and retains that capability
as its revocation parent. `Session.CapabilityPrefix` replaces `TokenPrefix`.
`CreateAccountSession`, `CreateIdentityAccountSession`, `LookupSession` and
`LookupSessionAndRenew` no longer accept caller clocks. Their callers select
session identity and duration. The store supplies timestamps, digest persistence,
current grants and the committing transaction.

The SQLite session proof `credentials-session-digest-csrf/store-sqlite` checks
creation, digest authentication, encrypted key storage, identical CSRF after store
reopening, membership demotion and session revocation. It does not exercise OAuth,
a native browser, production C2 middleware or a network listener. The controller
component property separately exercises the registered session handler with
sixteen generated grant subsets and a fake origin verifier.

`IntakePolicyFacts.SourceLocation` is the server-verified acquisition location for
the selected repository. Intake replaces the proposed location with that fact.
A nonempty proposed location without this fact is refused before either run or
owner insertion. The policy checks credential-free remote identity, local-store
authority and the checkout-relative pipeline directory before supplying it.
Retry carries the original location with the original SHA.

`Store.DelegateCapability` and `Store.ProvisionCapability` wrap `Store.Issue` in
the committing transaction. Ordinary delegation invokes C2's `ValidateDelegation`
on current parent authority and retains that parent. All issuance adapters return
the one-time bearer after commit and discard it on validation or storage errors.
`ExchangeCapability` uses that same commit boundary.

Controller claim adapters use the installed enrollment resolver and a
`RunnerAdmissionPolicy`. `SignedRunnerAdmission` delegates index verification and
runner eligibility to `internal/engine`. Request bodies supply requested kinds,
labels and available resources. Persisted authority supplies identity, total
capacity, repositories and pools. Cancellation and renewal repeat their store
fences. Expansion replay returns the persisted `ExecutionJob` in each `Current`.

`Store.CheckBinding` and the transaction's `CheckBinding` reload current authority
before checking resource relations. Caller-supplied principal fields cannot widen
the persisted binding or grants. The checker covers run, job, owner, plan,
pipeline, child-generation, own-unit, declared dependency/recovery, source and
secret relations. C2 still owns route grants and liveness classes. Named claim
checks share the matching-input reader without taking write locks. The awarding
transaction evaluates observed labels and available resources through the matcher.
Data selectors call the transaction's `DataAccessChecker`, supplied by the blob
owner. Receipt and read-edge changes in that transaction govern its verdict.
Concurrency binding returns unavailable until C2's resolved target can name the
declared key. Node-host process cleanup remains the host's binding check.

Snapshot transactions omit row locks because PostgreSQL refuses them in a
read-only transaction. Authorization and owner snapshots use the same repeatable
snapshot for ancestry and resource relations. Write transactions retain ancestry,
run and resource locks through commit. The public binding check grants no right
to skip the committing transaction's authority and fence checks.

`Tenant.ListRegisteredAgents` reads only its team's enrolled claimants.
`Operator.ListWaitingNodes`, `Operator.SetNodeAttention` and
`Operator.CountConcurrencyCache` own deployment inspection and advisory writes.
Their raw `Store` methods are removed. The unused raw `SetRetriedAs`,
`ActiveConcurrencyHolder` and `ListSpawnedChildrenByRun` wrappers are removed.
Their tenant methods remain while legacy callers migrate.

`Tenant.ResetPipelineProfile`, `Tenant.ResetAllProfiles` and
`Tenant.ProfileSamples` own capacity resets and sample reads. Their raw Store
twins are removed. Local stats reset selects DefaultTeam explicitly. The local
dashboard selects the request's tenant before reading profiles or samples.
Query parameters cannot select another team's profiles.

The remaining raw Store profile methods are removed: RecordProfileObservation,
RecordWaitObservation, RecordContention, SetProfilePin, UpsertProfilePin,
GetPipelineProfile, ListPipelineProfiles and CacheExcludedCounts. Tenant owns
the complete profile API. Local orchestration, capacity stats and profile
diagnostics select the registered DefaultTeam explicitly.

Tenant owns concurrency acquisition, holder reads and renewal, release,
follower resolution, promotion, waiter polling/cancellation and state reads.
The raw Store twins and unused deployment-wide ListConcurrencyStates are removed.
The local adapter selects the registered DefaultTeam and propagates handle errors.
Acquisition and promotion persist the work's current generation. Queued arrivals
also persist their lease. Expired waiters cannot keep arrival order or promote.
Admission, polling, renewal and capacity maintenance use the store clock. Promotion and
waiter maintenance use the owner lease or a live run job claim instead of the
removed run heartbeat. Run queries carry the selected tenant key.

The SQLite recovery proof `recovery-concurrency-continuous-adoption/store-sqlite`
holds node and plan capacity through owner loss, remote claim renewal and store
reopening. It reclaims the owner through ClaimUnit and adopts both holders.
Job completion releases node capacity. Run completion releases plan capacity.
Each release allows one queued competitor, and repeated promotion admits none.
Enrollment is a contract fake. Native process recovery remains separate evidence.

`Tenant.ReadOwnerSnapshot` reads the current owner authority, lease and scheduling
state in one snapshot. A replaced, expired or revoked owner receives no snapshot.
Cancellation remains readable while a held owner drains. The controller transports
the shared scheduling projection, exact expansion bodies and empty generators.
An uncancelled sealed guard refusal returns unavailable until the wire contract
can carry it. Dropping that refusal would permit refused work to execute.

Ordinary delegation retains its parent's immutable unit, purpose, delivery and
billing identity while narrowing authority. Persisting the child creates no new
billing award.

`Tenant.ReadRunView` returns a `RunView` containing the run snapshot, node-wide
attempt history and committed tagged log subjects from one read transaction.
Log subjects expose identity only, without object prefixes, segment keys or
signing authority. This reader does not write log persistence. The controller's
`RunDetail` adapter projects seals through `runspec.DisplayRequest` or
`DisplaySpec`, uses the destination row's retry lineage and omits private hashes,
bearers and execution declarations. `EngineController.VersionPolicy` supplies
read-time policy through the engine owner. `SignedEngineVersionPolicy` loads the
verified index, preserves fallback warnings, and evaluates the accepted engine
version. Active revocation/minimum errors become enforcement notices on stored
runs. Other policy failures discard the response instead of returning partial
status. A run without an accepted engine needs no version lookup.

`EngineController.Compatibility` looks up the exact release in verified metadata
and delegates calendar arithmetic to `controllerwire.CompatibilityFor`. Execution
eligibility does not change that release's API guarantee. Missing configuration,
failed index reads and unknown releases return errors. The engine index/evaluation
and wire calendar helper remain their owners' runtime integrations.

The controller calls `Operator.ReconcileControllerRuns` on startup and each
maintenance tick under `controller.owner`. It pages across all teams and renews
the exact fence between full pages. Election, renewal or reconciliation failure
stops the pass. Every Server has a random instance lease identity, separate from
the hostname displayed in schedule history. Another process on the same machine
cannot reuse that holder's live fence.

Final run pruning uses `Operator.MaintainRetention`. The unleased
`Store.PruneRunsOlderThan` API is removed. Finish time controls eligibility, and
a failed pass rolls back every removal and discards its result counters.

Identity expiry also uses `Operator.MaintainRetention`. The unleased
`Store.PruneSpentIdentity` and `Store.ExpireSessions` methods, `IdentityPrune` and
`SpentIdentityRetention` are removed. Invitations expire thirty days after
acceptance, withdrawal or expiry. Invitation and email-delivery timestamps use
nanoseconds. Each identity table observes the pass limit. Session expiry preserves
any row named by a capability parent. `RetentionResult.Invitations` and
`RetentionResult.Sessions` count only committed deletions. Controller maintenance
calls this fenced pass once instead of running a second identity sweep.

`Tenant.ReleaseSourceCredential` accepts C2's authorized authority reference and
route target. In one write transaction it reloads ancestry, locks the owner, run
and job, checks the live unstarted source binding and run deadline, and derives
the credential host from the sealed source location. It resolves executor
enrollment through `ClaimProfileResolver`. Repository permission requires the
award's repository scope and the current enrollment filter. A cloud executor
requires both the immutable cloud award class and enrolled cloud location.
Other executors require the same team's machine root and its owner's persisted
consent. Caller-supplied principal, binding and executor fields grant nothing.

The transaction calls C2's `AuthorizeSourceRelease` through a private contract
dependency, reads the confirmed team credential, and commits the complete
`SourceReleaseAudit` before returning the sealed credential. The controller owns
decryption. Audit or commit failure returns neither credential nor audit. A final
clock check refuses a lease or authority that expires during policy work.
`git_credential_releases` includes repository_key, node_id, generation, machine_id
and owner_account_id so the C2 audit survives without consulting mutable claims.
Release timestamps use store-clock nanoseconds.

`Tenant.SetSourceMachineConsent` reloads current account and machine authority,
requires the account to match the root's persisted created_by, and writes or
removes the consent under the same root lock used by release. Team administration
cannot replace machine ownership. Its timestamp uses the store clock.
The production C2 policy, route/decryption adapter, trusted machine-owner
enrollment and GitHub App issuance path remain separate integrations.

Offer-round finalization and the warm-dispatch fallback are removed.
`Store.FinalizeClaimRound`, `ClaimRoundResult` and `WithQueueRunners` no longer
exist. The controller endpoint and client finalizer are removed with their warm
dispatcher caller. `ClaimUnit` uses shared matching and persisted queue order.
A soft preference never requires an offer window. A mismatch changes no ready
row and emits no event. The final ordered-queue SQLite acceptance proof covers
push scope, eligible ordering, soft preferences and a later matching claimant.

Legacy node readiness writers remain until their host callers migrate. They no
longer open or emit offer rounds. The old offer columns remain because legacy
claim and credit code still references them. Their removal is part of that cut.

Actions claims and awards check every push binding in the capability ancestry.
The sealed repository, ref and SHA must match. The workflow run identifies the
verified credential and cannot be replaced by a claim request. Named and queued
claims use the same check, and direct issuance repeats it in the award transaction.
Push authority never creates verified-branch trust. Production Actions evidence
and enrollment remain C2's integration.

### Upload and record persistence

`Tenant.ReadBlob(ctx, authority, digest)` supplies C2's live metadata read hook.
It rechecks tenant authority and the digest's read edge in one transaction,
then returns only untombstoned committed existence and size. An unreadable,
missing or tombstoned digest returns no metadata. Reading creates no receipt
or record edge. C2 still owns selecting the authenticated authority and wiring
this tenant method into DataController.ReadBlob.

`Tenant.TrustLocalBuilds` and `Tenant.SetTrustLocalBuilds` own the team's
local binary execution opt-in. Their raw Store methods are removed. An absent
row means false. Read errors or invalid stored flags cannot enable trust.
Enabling trust does not change upload or record provenance. The controller's
membership checks own who may read or change this policy. W7's
`blob.CheckBinaryExecution` and W3's execution adapter must enforce the policy
against the committed binary and digest.

`blob.Authority.Upload` contains C2's resolved tagged uploader. W1 rechecks it
against the exact job/attempt/generation, owner lease or imported terminal
published attempt. Attempt-scoped publication requires a persisted finish time
through the shared authority resolver for upload admission, ticket lookup,
commit, abort, data access and log commits.
`RecordLookup.VerifiedDefaultOnly` selects only the authorized repository's
verified default-branch entries before required bytes or member metadata are read.
An excluded record is a miss. The evidence retains the App event key, SHA and
matching verified ref/default branch. A bound reader's sealed default branch
must match when present. The default ref comes from persisted verified
facts. Setting the flag false cannot weaken DataRemoteCache authority. Returned
`RecordLookupResult.Members` includes committed path/size/mode metadata after
read-edge checks. Internal member reads confer no client read authority.

`blob_upload.go` defines upload tickets and verified evidence.
`upload_runtime.go` implements ReserveBlobUpload, LookupBlobUpload, CommitBlobUpload
and AbortBlobUpload using the existing uploads table. Reservation binds tenant,
uploader, digest, size, expiry, provenance and ref facts before signing. The
physical backend verifies size/digest before W1 commits existence and the exact
receipt with quota settlement. Commit repeats live authority and fence checks.
`VerifiedUploadCommit.ReplicaID` is trusted controller configuration, never a
client-selected field. The ticket ID also binds the persisted quota reservation.
It is one ASCII letter/digit/underscore/hyphen path segment selected by the
trusted adapter within its tenant/principal idempotency scope.
Existing CAS bytes grant no receipt. Failed settlement grants no edge. Abort or
leased retention releases uncommitted reservations without deleting a live blob.

`blob.ObjectBackend` owns signed physical transfers, verified immutable publication
and deletion. Both filesystem and S3 implementations consume stored bytes through
verified EOF and successful close. S3 fences its copy to the verified source ETag,
creates the final key only if absent, and verifies final bytes before returning.
A physical commit grants no record, existence row or receipt. Those remain W1's
transactional responsibility after the controller repeats authorization.

`ObjectBackend.ReadManifest(ctx, team, digest)` derives the tenant CAS key and
returns file members only after verifying the full stored length, digest and
successful close. `storage.BlobManifests` implements W1's `BlobManifestReader`
with that physical reader. The record transaction authorizes the manifest before
reading it and independently authorizes every returned member. This adapter opens
no store transaction or authenticated data request, so it can run inside W1's
existing transaction. Controller binding must supply it to `Tenant.BlobRecords`.

`ObjectBackend.DeleteBlob` implements W1's existing `BlobDeleter` interface for
filesystem and S3 collection. It derives the tenant CAS key from the selected
blob. Missing bytes are an idempotent success. The leased collector owns the
reachability recheck and tombstone grace, including references added after
marking. The controller's age-based output pruner and the bucket measurement
expiry callback are removed. The maintenance coordinator must bind the final
physical backend before collecting objects.

`blob.LogDeleter.DeleteLogs(ctx, team, run)` removes physical logs in the selected
tenant across stored retention classes. An empty run selects that tenant's logs.
Both common backends implement it and return the removed byte count. Invalid
selectors fail before I/O. Filesystem traversal follows no symlink. S3 deletion
checks each listed key against the selected prefix and rejects stalled pages.
Neither backend removes CAS objects through this operation. Run deletion and
the team's leased purge/recheck call it through the configured data backend
before removing rows. Listings establish no record or read authority.
This is a W7-local contract addition. Authenticated log bindings remain with W2.

`internal/teamblob` retains scoped key construction and team validation for
legacy S3 output reads. Its cache byte/multipart APIs, prefix deletion,
directory listing, measurement and write breaker are removed. Deployment
measurement remains separate from authoritative tenant accounting.

The standalone filesystem backend uses the controller URL as its signing base.
It signs the C2 data upload, object download and log-segment paths. Method,
exact object path, declared byte checks and expiry remain bound to the signed
token. An external URL prefix belongs to the proxy, while verification uses
the registered controller route.
`Filesystem.VerifyTransfer` supplies C2's external endpoint proof callback.
The common controller mounts these signed transfers without bearer middleware.
Authenticated binding and new local-controller construction remain separate.
Legacy filesystem output and raw log paths have been removed.
The sharded filesystem ArtifactStore, generic raw storage.ArtifactStore contract
and portable conformance package are also removed. Filesystem deployment
measurement uses fs.NewUsageReporter. It exposes totals without an object-read
or write API. S3's remaining raw log reader still needs its authenticated
cutover. Physical CAS reads and writes stay with the common object backend.

`CommitObject` requires verified bytes at the exact ticket's staging key even
when the final CAS object already exists. An absent staging key returns
`ErrNotFound`; a digest and another uploader's final object cannot establish an
own-upload receipt. Retries before settlement retain staging. After settlement,
the authenticated adapter replays the persisted receipt under current authority
without repeating physical publication. Staging cleanup follows settlement.

The `storage-cas-integrity/store-data-sqlite` proof refuses mismatched verified
size, digest and tenant evidence. It reopens an expired interrupted upload,
reserves a replacement under the same capacity, rolls back a receipt failure,
then commits and replays the replacement. After reopening again, only the new
uploader can read the digest and usage counts one blob. Authority and physical
verification are contract fixtures. Native transfer integrity and EOF checks
remain separate integration proofs.

`TestBlobOwnerReceiptCannotTransferToAJob` and
`TestBlobUploadOwnerTicketsRetainUnitIdentity` check tagged trigger/plan uploads
before any job exists, reopening and refusal to transfer an owner's receipt or
ticket to a job.
`TestBlobUploadCommitRechecksTicketAndAuthority` checks failed verification.
Generated eval tests check both source and unit fences under explicit fake write
authority. C2's issued eval grant table remains read-only. The native cell still
requires signed transfer and stream EOF verification.

Upload tickets reserve monthly bytes, lifetime run bytes and lifetime run objects
in the existing quota tables. Each dimension has its own durable reservation.
The aggregate keys are `storage`, `storage.run.bytes:<run>` and
`storage.run.objects:<run>`. All paths acquire them in that order. Run dimensions
have no calendar reset. Commit charges only newly inserted tenant blobs, with
one object even when its size is zero. Authorized existing bytes reserve zero
in every dimension. Abort and leased expiry release all three holds, including
holds whose upload row was removed by a run cascade. Effective default-tier
policy is read for every new upload without creating an override row.

Logical record pointers add no storage usage. Output and memo records may point
to the same authorized blob at the run's object ceiling. A new blob still needs
an object reservation. The legacy `SetNodeArtifactManifestCharged` methods are
removed with the legacy manifest-pointer endpoint. W7/W9 wire final publication
and member validation through the record and result contracts. No manifest
column or mutable pointer route is restored to the final schema.

CommitRecord and CopyMemo use one transaction-local record mutation owner.
They prepare every namespace and verified manifest before taking the sorted
blob locks. After checking existence, reference edges and immutable destinations,
the owner rechecks writer ancestry and holder authority against the store clock.
That check is the publication boundary. The owner writes the complete destination
batch and tombstone changes under the same transaction. Its caller owns commit
and rollback. An expired writer cannot publish even when it was live before
namespace or manifest preparation. Immutable replay and atomic output/manifest
copies retain their existing contracts.

Record commits normalize expiry instants to UTC and clear values in unset optional
expiry and manifest fields. Returned metadata matches persisted metadata across
replay and reopening. A different expiry instant still conflicts with an immutable
output or manifest record.

`TestBlobUploadCrashReplay` exercises admission, commit, abort and deduplication
through the store's shared upload fixture. Child processes exit without closing
the database before and after commit and abort. The parent reopens the database
and checks persisted tickets, quota holds, usage and replay capacity. Physical
verification evidence is a contract fake. Native controller checkpoints, signing
and transfer integrity remain integration obligations.

The `TestReplica` tests use `pkg/store/internal/storetest` targets to check usage
sums, quota races, reservation release, singleton election and download caps
across independent clients and reopening. Quota setup uses ReserveQuota and
SettleQuota. The suite's dialect selection applies to these tests.

LookupRecord, LookupMemo, LookupDepcache, CommitRecord and CopyMemo remain the
only record owners. W1 integrates receipt/read-set checks, members, unique-byte
accounting, newest-success pins and reachability retention. Artifacts require
read authority. The cloud-provenance execution restriction applies to binaries.
Claim/start/reap/finish transactions invoke the shared billing owner with immutable
award facts. Plan/eval have no minimum, job and host minima remain separate, and
customer-runner waiting is excluded. Verified payment events settle committed
checkouts idempotently. No caller-selected team or credit amount reaches settlement.

The unit lifecycle joins W1b's billing helpers in the claim, start, renewal,
release, plan acceptance, expansion acceptance, finish and reaper transactions.
Pricing comes from the sealed request or resolved job resources. The existing
minute reservation is refundable. C2's calculator applies the separate final
minimums, including zero for plan/eval. A here intake capability authorizes a
future claim and opens no compute reservation at generation zero.

Queued matching checks funding before ranking eligible work and repeats the
reservation under the ledger lock when awarding. An unfunded team cannot block
funded matches. Renewal starts Cloud job billing without consuming an execution
attempt. Owner Executing reports current compute. False closes an interval and
true resumes it with a fresh key. Application retries keep their reservation
and open another interval. Reaping settles at the persisted lease lapse.
The eval lease and billing generation come from persisted expansion authority.
C2's production calculator and W3's current-activity reporting remain integration
dependencies. The SQLite billing-start proof covers issuance, exchange, starts,
immutable pricing and reopening. Lifecycle tests substitute C2's calculator with
a contract fake. Awarding repeats the claim and eval-owner deadline checks after
accounting. Expiry rolls back the reservation and unit binding together.



This document specifies the rewrite target. Functions marked `unimplemented`
are signatures for the implementation swarm, not working runtime behavior.
The authority is the frozen architecture v4, especially sections 3, 4, 7.5 and
8, with decisions 87-104. Earlier design chapters supply evidence only where
they agree with that target.

## Ownership and package boundaries

C1 owns the final neutral schema, persisted run and job state, transition
rules, both run-input seals, attempt and repository identity, blob records and
reference authority, concurrency continuity, completion revisions and retention.
Internal Go and SQL identifiers use node; customer JSON uses job.

- `pkg/store/schema.sql` is the only baseline. `TranslateSchema` is a signature.
  Neutral types are TEXT, BIGINT, DOUBLE PRECISION and BLOB. PostgreSQL maps
  BLOB to BYTEA without changing checks, keys or predicates.
- `pkg/store` owns transactional enforcement. `Tenant` carries an explicit,
  registered team and cannot widen itself to the raw store. `OperatorStore`
  exposes named deployment operations. `Store` keeps opening, handle creation,
  capability-derived claims and cross-team reaping.
- `pkg/storage.StateStore` is the final host-facing interface implemented by
  `*store.Tenant`. The replaced interface and its old consumers have the `Old`
  suffix solely to compile the old implementation beside the contract. There
  is no forwarding alias or compatibility adapter. Retained account, billing,
  approval and schedule implementations are not ported by this contract step.
- `internal/runspec` owns immutable inputs and identity. It resolves no settings,
  authenticates no credential and validates no plan.
- `internal/blob` owns byte integrity and reference-edge types. The controller
  supplies its signed data transport; `Tenant` supplies transactional records.
- C2 owns capability issuance, route authentication, binding and API errors.
  C3 owns `ValidatePlan` and host interfaces. C1 owns `RunSnapshot.Scheduling`
  and the shared pure `Decide` in `pkg/store`. Run hosts and controller
  settlement use that same projection and decision before transition writes. C3's host expansion interface
  calls `Tenant.AcceptExpansion`; it is not a second persistence owner.
- C4 owns setting resolution and numeric limits. C1's retention table records
  the frozen policy, and implementations obtain timing and size values from
  C4's limits package. T1 owns cross-placement acceptance execution.

## State and transaction invariants

1. Intake creates the run and its executable owner unit together, except for
   publish intake, which has no executable owner. Run numbers allocate under
   the team lock and are unique per team; IDs remain globally unique.
   Verified App redelivery returns the original run for that delivery and
   pipeline, even without a client idempotency key. The unique
   `(team, webhook_replay_key)` index enforces this identity. The provider's
   unsigned delivery header is audit data and has no uniqueness constraint.
2. Intake seals the requested inputs. Acceptance validates the C3 plan,
   resolves C4 settings and ceilings, performs server-side secret preflight,
   seals the resolved spec and inserts nodes in one transaction. Different
   accepted hashes fail as `spec_drift`. Projections are written only in
   `run_spec.go`. Guard refusal may end a run before a plan is accepted.
3. Every node insertion uses `insertNodeTx` through plan or expansion acceptance
   and starts with `state_seq=0`. Only `node_state.go` and `run_state.go` own
   status changes. An absent edge is illegal; a zero-row write is not success.
4. `nodeState` derives ready and claimed from persisted status and claim fields.
   Lease expiry does not itself turn a row pending. The reaper commits that
   edge using the store clock. Paused is status detail, not a state.
5. Holder rights require the exact live capability binding, holder and generation.
   A trigger owner cannot overwrite another live holder. Generation never resets.
   Identical actor, generation and payload digest is a replay with no new event.
   Terminal is otherwise absorbing. State actor and digest survive claim clearing.
6. Attempt identity is `(team,run,job,attempt)`. Attempt is a positive node-wide
   ordinal and never resets across claims; generation is an attribute.
   Application retry closes the failed attempt and advances the ordinal under
   the same claim. Requeue consumes no application retry. Unstarted lapse is
   free; started lapse records `agent_lost`, increments disruptions and fails
   when the increment reaches three. OOM is an ordinary job failure.
7. Every pending or terminal transition clears claim columns. A cancellation
   of claimed/running work sets `cancel_requested_at` and emits
   `job_cancel_requested`; it does not emit a terminal event. `NodeEvents`
   selects by requested target plus `RequestOnly`. Cancellation wins when a
   lapsed claim is reaped. Claimed/running work drains before the run finishes.
8. State, attempt closure, output references, manifest authority, lifecycle
   event, concurrency release, expansion barrier and controller settlement
   commit together. The store owns lifecycle events and the per-run event
   sequence. Hosts never emit a second lifecycle event.
9. Reconciliation decides and self-claims atomically. `WaitHolder` follows
   events until terminal or requeued, then reconciles again. Resume, retry,
   memo and coalesced source policies keep distinct read authority. The only API
   is `ReconcileNode(ReconcileRequest) (ReconciledNode, error)`. Materialization
   reads the source outcome and result edges from persisted state. A request
   cannot supply an outcome.
10. Successful and memoized generator sources remain `expected` until expansion
    submission. Failed, skipped, cancelled or superseded sources resolve to
    `none` in the terminal transaction. Run cancellation resolves all expected
    expansions. Generator errors resolve to `failed`. Expected expansions
    prevent settlement and release of `needs_generated` dependents.
11. Expansion uses a live owner delegation or separately leased eval award,
    never the ended source claim. It checks source generation, expected state,
    cancellation and both applicable leases in the transaction. Exactly one success
    body or structured failure is required. An empty success contains an explicit
    empty jobs array. Both forms persist a canonical hash and replay unchanged.
    A changed body or failure conflicts. Persisted generated specs and hashes control
    topology. Generator replay reconstructs code only and must match those hashes.
    `PlanAcceptance.Seal.Declarations` carries restricted pipeline/default inputs.
    `settings.ResolvePlan` and `settings.ResolveExpansion` seal execution settings
    separately in `NodeRow.Execution` and `nodes.execution_json`. Hosts execute
    those winners and compare declarative hashes only for reconstruction.
12. Owner loss preserves remote work in the same run. Controller-owned runs
    use `controller.owner` after plan acceptance. Local started orphans fail.
    Controller ownership lapse does not fail its runs; the next elected replica
    resumes them, subject to their existing limits.
13. A concurrency holder's own lease proves continuously counted capacity.
    Node holders renew while their node is nonterminal and its claim or owner
    remains live; plan holders renew while their run is nonterminal and its
    owner or any node claim remains live. Expired capacity cannot revive.
    A still-live waiter keeps arrival order; an expired waiter is a new arrival.
14. `LockOrder` fixes acquisition order and lexical order within a class.
    Implementers must review claim, cancellation and financial paths together
    before adding a lock; they must not silently acquire an earlier class late.

## Scheduling, placement and log contracts

`ReadSnapshot` reads the run, ordered nodes and accepted expansion bytes in one
transaction. `RunSnapshot.Scheduling` uses the persisted plan and job specs for
edges, `ClosureSpec.Generate` for generator identities, and accepted generated
`JobSpec.Groups` for membership. It keeps an entry for an empty accepted group.
States and outcomes come from persisted rows. Invalid declaration/state mixtures
fail projection. Both owners pass this result to the same pure `Decide`.

C1 owns placement input construction. `matchingInputsTx` derives `match.Profile`
identity and `ReservedPools` from the live capability and its enrollment facts.
It derives `match.Demand.Selector`, `Prefers` and `Request` from resolved execution
settings and `AllowedPools` from intake. Every named, queued and self claim calls
`match.Evaluate` with those inputs. `UnitDemand.Target` selects a named unit;
absence selects the ordered eligible queue. Reservations require an explicitly
required granted pool that intake allowed. A self-asserted `pool=` label grants
nothing. Preferences rank only eligible work. `match.Fits` checks availability.

`ClaimProfileResolver` supplies enrolled repository acceptance and location,
plus the host observations needed by a self-claim. The store replaces its name,
class and reserved pools with authenticated capability facts. Self-claims
require this resolver because `ReconcileRequest` carries no machine observations.
Named and queued claims still carry their observed labels and capacity in
`UnitDemand`.

Advisory runner discovery reads live unbound machine capabilities and rechecks
their ancestry and current claim grants. `RecordAgentLabels` persists only
self-asserted labels and a nanosecond last-use time. `RegisteredAgent.CapabilityPrefix`
identifies that capability. Its profile combines enrollment with the same granted
identity and pools used for claims. Online presence replaces observed labels and
platform only. `ListWaitingNodes` pages ready host-owned jobs from their sealed
execution demand and request runner selection. `WaitingNode.AllowedPools` carries
the intake-authorized pools into the advisory matcher. Advisory results grant no
authority to claim or mutate a job.

`ReconciledNode.Bearer` carries the new self-claim credential after commit and
is excluded from JSON. `nodes.owner_capability_prefix` and the matching
`NodeRow.OwnerCapabilityPrefix` identify which live trigger award may renew
a self-claimed job. Dispatched claims leave this field empty. Claim clearing
also clears this field. This fence is separate from capability ancestry because
C2 awards parent to the claimant root.

`MaterializeSource.Result` names a committed terminal result by job and state
sequence. It is exclusive with the positive execution-attempt identity in
`Source`. This lets a later retry reuse a previously materialized result without
inventing an execution attempt. Both variants read the outcome from persisted
state and require the same policy-specific record authority.

`RunRow.CreatedAt` is the new invocation's creation time. Queue deadlines use
it because retries preserve the original request seal's admission time.
`nodes.queue_started_at` and `NodeRow.QueueStartedAt` retain a job's first
release time across unstarted claim losses and releases. Execution start or
terminal completion clears it. A started disruption therefore receives a new
queue interval on its next release. An unclaimed pending job with an elapsed
queue interval can fail as `queue_timeout` before its owner releases it again.
The accepted spec supplies claim wait after seal point two. Before acceptance,
the request seal supplies it. Named, queued and self claims enforce that same
deadline before awarding work, even when reaping has not run yet.
`ScheduleSnapshot.GuardRefusal` carries a sealed guard refusal to the shared
decider. Refused jobs become skipped and the run fails with the recorded reason.
Cancellation still takes precedence. A refused seal cannot release or claim work.

`Store.RenewUnit` is the controller adapter for a unit capability and the wire's
`executing` flag. It derives team, unit, holder and generations from the live
capability. Trigger and plan renewal share `Tenant.RenewOwner`'s transaction
path. Job renewal shares `Tenant.RenewNodeClaim`'s path. Eval renewal checks its
own claim, terminal source generation and elected controller generation.
Bootstrap credentials cannot renew. The first executing renewal records an
owner unit's start, which distinguishes free loss from a started disruption.
Job starts still use `TransitionNode`. Cancelled units renew for draining,
return cancellation, and record no new execution start. Billing settlement
must join the renewal transaction through the accounting implementation.

Owner requeue clears the claim and appends one `run_requeued` event in the
same transaction. It advances the run state sequence without changing its
pending or running status. Repeated sweeps cannot duplicate that event.

`Operator.ReconcileControllerRuns` resumes accepted controller-owned runs
under the exact live `controller.owner` lease. It visits a bounded page across
teams, ordered by global run ID. Callers pass the last visited ID to the next
page and start the next sweep with an empty cursor. Each run settles in its
own transaction. Errors return the committed prefix. The sweep renews only
continuously live concurrency holders and waiters to the persisted owner
lease expiry. It cannot revive expired capacity.

Controller ownership begins at a positive `plan_accepted_generation`, after
the current invocation's planner accepts its plan. A retry carries its source
spec before that handoff, so a spec alone cannot establish controller ownership.
Read projections, owner authorization, settlement and owner reaping use the
accepted generation. A lost retry planner still requeues or fails at its
disruption limit. The carried request, spec and plan remain unchanged.

Published plans, job results and completion share one publisher check:
controller audience, `data.write`, a run binding, publish data scope, matching
repository and live ancestry. Completion requires an accepted plan and a
verdict that agrees with persisted jobs. Published history has no executable
owner, including when its dispatch field names the controller. Result-record
attachment remains transactional and must supply its own blob authority proof.

Materialization uses the store-local `materializationRecords` interface inside
the state transaction. Its implementation must authorize the policy-specific
source and copy verified output and manifest edges atomically. Memo authority
comes from the retained record, so pruning its source run does not remove a hit.
Retry and coalesced outcomes come from persisted terminal job and attempt rows.
The unconfigured path refuses materialization. Tests may fake the record boundary,
but those tests do not establish blob persistence or byte-integrity coverage.
The matching `nodeResultRecords` interface attaches reported or published output
and manifest records in the terminal transition transaction. It must authorize
the exact persisted attempt or publication binding. A refused attachment rolls
back state, attempt closure and events. An exact result replay does not reattach
records.

`Store.SetResultBlobRecords` configures the blob implementation of
`nodeResultRecords` before the store is shared. Attachment verifies exact
destination result keys and all required bytes in the state transaction through
the existing record reader. It requires `data.write`, rechecks live authority
after manifest I/O, and preserves caller-owned commit and rollback. Missing,
expired or corrupt records prevent the result and its event from committing.
No synthetic attempt represents an unexecuted job or a published import.

`IntakePolicy` resolves request options through C4 and supplies server ceilings,
repository authority, verified App delivery facts and log retention. The store
requires it before admission and rechecks the caller in the intake transaction.
It does not accept client origin labels as repository or branch authority.

`blob.LogSubject` is the shared identity for signing, commits, live pushes and
reads. It contains either a positive job attempt identity or a trigger/plan/eval
unit. Trigger and plan use the run and unit generation. Eval also names its
source job and immutable source generation, separate from its own lease fence.
`Validate` rejects mixed or missing variants. No synthetic jobs or zero attempts
represent pre-job logs.

`log_streams` persists that tagged identity, its generation, retention class and
final commit digest. `log_segments` references the stream with the same generation.
The writer rechecks its exact capability binding and persisted generation.
An unsealed final job attempt may flush after its result commits until the
persisted finish time plus `limits.LogSealGrace`. C1 rechecks the recorded writer,
attempt, generation, current credential ancestry/expiry/membership and absence
of a newer claim. After sealing, only an identical final commit may replay.
Owner-unit liveness stays separate; eval gains no job-attempt grace exception.
Published job logs use live run-bound `data.write` publisher authority and the
exact imported terminal attempt in a publish-mode run. Publication has no
execution lease, so execution tail grace does not govern its uploads. Normal
segment integrity, retention and seal rules still apply. Owner-unit publication
needs an imported unit identity contract before it can bypass a live unit lease.
`LogTransport.SignSegments` derives object keys and retention from the authorized
subject and run. Each segment URL includes its required `Headers`; the final
manifest uses `ManifestURL` and `ManifestHeaders`. Storage transports must send
those headers, including S3's signed create-only condition. HTTP 412 proves only
that the key exists and cannot authorize a commit or an identical replay.
`LogTransport.VerifyUpload` recovers an uncertain PUT by reauthorizing the exact
writer and verifying the stored bytes. `LogUploadCheck` carries the subject,
index, stored size and SHA-256. Index zero selects the final manifest. Positive
indexes select segments. The controller derives the key from the subject and
the run's persisted retention class. `logseg.UploadVerifier.Verify` consumes and
closes that object before confirming its exact size and digest. Verification
returns no bytes or read URL and creates no record edge. The subsequent
`RecordSegments` transaction repeats writer authorization. Cancellation remains
an error. A changed payload, missing object or failed close cannot recover.
`ReadLogSegments` and `LogTransport.ReadLog` reauthorize the same
subject, proxy bytes through the controller, meter egress and apply the log
exception to download caps. Persisted segments fill gaps or stalls in the ring.

`blob.LogObjectKey(subject, class, index)` owns the physical key for every signed
log slot. Index zero names the final manifest. Physical segment slots start at
one in signing and verification.
`LogSubjectPrefix`, `LogSegmentKey` and `LogManifestKey` are its shared key builders.
The physical layout is `logs/<class>/teams/<team>/runs/<run>/`, followed by the
tagged job or unit fence. Persisted references must name those physical objects.

The `storage-hasmany-read-set/store-data-sqlite` proof uses exact imported job
attempts under a preseeded publication capability. It checks hidden unrelated and
foreign existence, budget expiry, reservation and failed-commit refusal, independent
verified receipts, deduplicated replica charging and persistence after reopening.
Both `BindData` and `BindReadSet` dispatch through `Store.CheckBinding` to the
transactional data checker in this SQLite proof. Generated store tests also
check uncommitted receipt changes and caller-supplied principal forgeries.
Presence metadata consumes neither download nor egress bytes. Physical verification,
job-holder transports and native placements remain integration obligations.

The `storage-hasmany-large-budget/store-data-sqlite` proof checks 1,025 distinct
digests and their duplicates across reopening. All 257 readable digests appear
once in request order. Unrelated and foreign bytes remain hidden. Presence reads
leave upload, download and egress accounting unchanged. Generated store tests
advance the injected clock across the final read and require an empty budget
error once the deadline is reached. Trigger/plan owner receipt properties also
check forged identities and refusal to transfer the receipt to a job in the same
run. Physical stream verification and native placements remain separate proofs.

Record commits and lookups recheck live authority after verified manifest I/O.
Generated tests cross the job lease, leaf capability and ancestor expiry during
that read through CommitRecord, LookupRecord, LookupMemo, LookupDepcache and
CopyMemo. Refusals return no read set and persist no output or manifest record.

The `TestLogPersistence` tests own immutable segments, gapped-batch rollback,
offset reads, incomplete seals, replay, retained-byte accounting, subject
isolation, revocation and abandoned-stream maintenance. Job, publication and
maintenance-seal metadata survive reopening. Publication adds no operational or
financial units. A later node finish cannot extend an attempt's read grace.
Generated tests check reads, segment commits, final seals and maintenance before,
at and after the grace boundary. Historical reads and stored bytes survive
sealing. Log loss leaves the successful job result unchanged. Claim authority
and segment digests are contract fixtures. Physical bytes, owner-unit transport,
ring fallback, signing, lifecycle expiry and proxy egress remain integration
obligations.

`Operator.SealAbandonedLogStreams(ctx, maintenanceLease, limit)` closes existing
unsealed execution streams with `writer_lost`. Job attempts wait until strictly
after their persisted finish plus `limits.LogSealGrace`. Trigger and plan streams
need no matching live unit generation. Eval checks its own generation and its
immutable source generation separately. The operation locks each run, rechecks
eligibility and the maintenance fence, and commits a bounded batch atomically.
It preserves segments, stored-byte accounting, final seals and execution results.
Publish-mode streams retain their publisher's final-commit contract because
publication has no executable lease. The controller invokes this operation from
its leased retention pass. The old `Store.AbandonedNodeLogs` query and unfenced
controller sweep are removed. Physical manifests and ring fallback remain with
the log transport.

Persisted reads recheck a trigger's current owner lease even when its award has
`runs.read`. Job-scoped readers retain their exact writer, attempt and generation
fence and cannot reuse sealed-write replay to read past `limits.LogSealGrace`.
Unbound run/log readers can still read historical subjects. Identical final
write replay keeps its existing ancestry checks and separate replay exception.

The `storage-reference-edge-laundering/store-data-sqlite` proof reopens a seeded
output/manifest/member graph and checks presence plus download/reference
authorization. Direct, optional, transitive, artifact-input, generated-group and
failure-handler dependencies grant edges. Unrelated, foreign-team and missing
digests stay hidden. These metadata checks consume no egress or download budget.
Accepted-plan intake, manifest byte verification, record commits and native
routes remain separate integration obligations.

The `storage-publish-one-way/store-data-sqlite` proof commits published job logs
after local completion, reopens their segments and incomplete seal, and checks
retained bytes, immutable replay, credential revocation and unchanged job results.
It creates no executable unit or billable interval. Publisher authority and
terminal imported attempts are preseeded. Run/result import, owner-unit logs,
local publication transport and physical bytes remain integration obligations.

| Export | Purpose |
|---|---|
| `AcceptedExpansionRow` | Keeps accepted expansion bytes, source fence and body hash in a consistent snapshot. |
| `RunSnapshot.Scheduling` | Projects declarations and persisted state into the scheduling graph. |
| `ScheduleNode`, `ScheduleExpansion`, `ScheduleSnapshot` | Carry the accepted graph, including empty generator groups. |
| `DecisionKind`, `NodeDecision`, `ScheduleDecision`, `Decide` | Define one pure graph decision consumed by both owners. |
| `UnitRef`, `UnitDemand` | Select named or queued work with observed capacity and labels. |
| `match.Profile`, `match.Demand`, `match.Evaluate` | Match granted reservations against sealed requirements and authorized pools. |
| `blob.LogSubjectKind`, `blob.LogUnitKind` | Select attempt versus unit logs and the persisted owner-unit kind. |
| `blob.LogSubject`, `blob.LogUnit`, `blob.LogSource`, `blob.LogSubject.Validate` | Carry and validate one fenced log identity. |
| `blob.LogSigningRequest`, `blob.LogUploadURL`, `blob.LogUploadBatch` | Request and return bounded segment PUT slots for that identity. |
| `blob.LogUploadCheck`, `blob.LogTransport.VerifyUpload` | Verify the exact stored payload after an uncertain PUT under current writer authority. |
| `logseg.UploadVerifier` | Derive one authorized object's key and check its stored size and digest after successful close. |
| `blob.LogReadRequest`, `blob.LogTransport` | Sign uploads, verify uncertain writes and proxy authenticated reads through the controller. |
| `SegmentCommit`, `LogSegments`, `Tenant.ReadLogSegments` | Commit and read persisted segments under the same subject fence. |
| `Operator.SealAbandonedLogStreams` | Seal abandoned execution streams under the maintenance lease without changing execution results. |

## Capability persistence, authority and payment settlement

`Actor.Authority`, `IntakeRequest.Authority` and the sealed intake authority use
C2's tagged `AuthorityRef`. A session remains a session through transaction
rechecks. `CapabilityPrefix` remains a separate claim fence. `storeTx` implements
C2's `AuthorityResolver`, so current ancestry, membership and grants are checked
inside the write transaction.

`Store.ResolveAuthority` and `Store.LookupCapability` expose consistent ancestry
and membership reads to controller adapters. Lookup accepts a bearer digest.
These methods do not authorize a route or establish a unit's live binding.
`Store.ExchangeCapability` owns the bootstrap exchange transaction and returns
the replacement bearer only after commit.

`CapabilityRecord` stores version 1 and the full C2 capability in `authority_json`.
The bearer digest stays in its indexed column and no raw bearer is stored. The
envelope preserves attempt, deadlines, reservation, Actions workflow identity,
expansion lease, UnitID, delivery purpose, data scope and repository scopes.
Indexed columns are exact projections, validated on load and written with the
envelope. They cannot reconstruct authority from newer mutable rows. Tagged
`parent_kind` and `parent_id` support capability and session ancestry. C2 validates
the referenced store and parent liveness transactionally. Retention and revocation
must preserve that ancestry check, including descendants of here sessions.

`capability.UnitKind` owns operational values `trigger`, `plan`, `eval`, `node`.
Grant purpose maps work to node and here to trigger. Bootstrap is a delivery
purpose that retains the underlying plan/eval/node unit. The schema stores purpose,
delivery and unit separately. `billing_unit` stores `host`, `plan`, `eval`, `job`
with checks for trigger-to-host and node-to-job. Both ledger tables preserve the
operational kind and financial classification after run or capability pruning.

`Store.AuthGenerations` returns one consistent vector covering the leaf, every
capability/session ancestor and the awarded team's membership generation. Launcher
roots remain in that vector across team boundaries. Missing ancestors refuse cache
reuse. C2 additionally checks the five-second `limits.AuthCacheTTL`, expiry and
route liveness. `RevokeAuthority` advances the affected capability/session generation
in its revocation transaction. `UpdateMembership` advances the team generation in
its membership transaction. The internal advancement helper cannot commit separately.

`Store.SettlePayment` accepts C2's opaque verified event and looks up the committed
checkout by provider session ID and configured test/live mode. The checkout supplies
the team and credit amount. Verified amount, currency and payment identity must
match. An unset payment ID binds once from the signed event for that checkout.
`payment_events` deduplicates mode/event ID and its canonical payload. The locked
checkout records at most one credit grant, even when multiple paid event IDs arrive.
Failures and expiry cannot undo paid credit. Financial rows have no run cascade.
Human grants, refunds and freezes remain console-only operations.

`payment_settlement.go` owns settlement and receipt deduplication. The
`settings.payment_mode` value is a required JSON string, `"test"` or `"live"`.

Payment lookups and console refunds follow the checkout's persisted `grant_id`.
The grant's ledger reference is not a provider payment ID. Refunds retain that
ledger reference in `reverses`, count prior reversals and cap the total at the
paid grant. Checkout lookups select the configured test/live mode. Distinct card
charge grants retain their provider payment reference. Ambiguous payment/grant
associations fail instead of choosing a team. Caller-directed checkout closure
is removed. Verified failure and expiry events own those outcomes.
`CreditGrantRequest` carries no checkout session. Only verified payment
settlement writes the checkout's paid timestamp and grant association.

`Operator` owns deployment card collection and refund workers: `DueCardCharges`,
`RecordAttemptIntent`, `SettleCardPayment`,
`DueCardRefunds`, `MarkCardRefundMade`, `FailCardAttempt`
and `DropCardAttempt`. Their raw Store methods are removed. Provider workers
select Operator before operating on persisted attempt and payment identities.
Tenant spend policy and recovery checkout keep their tenant handles.
Raw card setup, warning and provider refund callback APIs are removed. Their
transaction-local owners remain for verified payment-event settlement.
The collection worker and webhook settlement retain their ledger lock,
persisted payer attribution, replay rules and refund queue. This handle cut
adds no provider verification contract. C2 still owns verified event delivery.

Warning persistence and its payer hold share one transaction-local mutation owner.
A warning ID retains its first payment and card fingerprint. A conflicting
replay fails before applying a hold. Payer lookup compares every matching paid
grant and card attempt. Conflicting tenant evidence rolls back the warning and
hold. Shared-card warnings still hold each team when its own payment settles.
Holds use the tenant-scoped warning identity. The legacy provider-only hold
identity is removed. Replaying an exact warning after release cannot restore
its hold. A failed audit rolls back both warning persistence and the hold.

`Operator.ReversePayment`, `Operator.PaymentTeam`, `Tenant.SaveCard`,
`Tenant.HoldForDispute`, `Operator.RecordPaymentWarning` and
`Operator.ReportCardRefund` are removed. Verified event settlement owns provider
mutations. Private transaction owners retain payment/grant resolution, refund
conservation, setup ordering and immutable dispute attribution. A dispute stays
bound to its first team and payment after release. Replay cannot restore a hold.
`Tenant.ReleaseCreditFreezes` still requires a named hold, actor and reason.
`Tenant.HoldByOperator` and `Tenant.ReleaseOperatorHolds` use those same owners.
Console HTTP and break-glass callers retain their authenticated tenant selection.

The raw `paid` grant path and `POST /api/v1/credits/grants` are removed.
`Tenant.GrantCredits` is removed. `Tenant.RecordCreditGrant` owns grant requests
and refuses caller-selected paid credit before opening a ledger transaction.
The existing free grant and payment-linked reversal paths remain. Checkout
settlement checks actionable fraud warnings in the same transaction as the
grant and event receipt. A refusal leaves the checkout and ledger unchanged.
The verified-event implementation and HTTP adapter remain W2 integrations.

`Operator.CreditLedgerTotals` owns deployment-wide ledger reports. Its raw Store
method is removed. Reports use the same billing calculator as unit renewal and
settlement and never advance financial checkpoints. The real claim/exchange/release
regression checks an unstarted reservation before and after its refund.

Credit pricing and storage-rate settings use unsealed JSON values in `settings`.
Only missing keys select defaults. Malformed values, invalid prices and sealed
rows return errors. A settings batch and its returned snapshot commit together.
Rate-table reads use one snapshot so a concurrent update cannot mix its scalar
price with another table. Operator table authority is checked under the shared
advisory lock before any settings write.
`Store.CreditRateMicroPerSecond` and `Store.CreditGraceSeconds` are removed.
Their callers read the existing `CreditSettings` snapshot fields.

`CreditSettings` supplies max-charge seconds, the storage price, the free storage
allowance and rate-table presence. Their unused Store getters are removed.

| Export | Purpose |
|---|---|
| `CapabilityRecord`, `CapabilityRecord.Validate` | Preserve and validate complete versioned authority alongside indexed projections. |
| `Store.AuthGenerations` | Read the complete ancestor and membership cache fence. |
| `Tenant.RevokeAuthority`, `Operator.RevokeAuthority` | Revoke authorized tenant or deployment authority and advance its generation atomically. |
| `MembershipUpdate`, `Tenant.UpdateMembership` | Change or remove membership with the corresponding generation increment. |
| `PaymentSettlement`, `Store.SettlePayment` | Deduplicate verified checkout events and commit their derived-team financial result. |

`TestRewriteSchemaCapabilitiesPreserveAuthority` persists full capability envelopes
and indexed projections, including session parents, here purpose and bootstrap
delivery. It checks invalid parent, operational and financial classifications.
The tests use JSON and the final SQLite schema because transaction methods are stubs.

## Completion, retention and accounting

Run transitions update one desired-state row per effect in their transaction.
Desired rank advances pending, running, terminal and never regresses. Each
advance increments `desired_rev`. Delivery acknowledges only the sent revision,
so a pending acknowledgement cannot consume a concurrently written terminal
revision. GitHub deliveries retain their report lock and stale-phase check.
Metrics and profile folding act once per terminal run. Parent notification is
also terminal-only in this contract.

The leased retention pass keeps each pipeline's newest successful run and its
output records. Runs otherwise expire after the configured window (Cloud 90
days; self-hosted disabled). Outputs expire after 30 days except pinned outputs.
Metrics use their own setting. Log classes retain funded logs for 90 days and
free logs for 30, or an operator-defined class. Standing changes affect new runs
only; signing refuses a class without its backend lifecycle rule. Unpinned blob
records expire by their own time; reachability GC uses a 24-hour tombstone grace.
Capabilities expire seven days after expiry or revocation. Fully delivered
terminal effects expire seven days after delivery; undelivered effects remain due.

Credit grants, charges, checkouts, freezes, cards, refunds, Stripe evidence,
storage usage, immutable billing awards and billable intervals never cascade
from a run. Removing a run must not restore spent credit. Award snapshots survive
capability pruning. Unmetered checkpoint and final settlement need no billing
calculator and write no charge or spend row. They retain award closure and
exact replay. Plan/eval compute has no minimum; job compute retains its
60-second minimum; customer-runner waits produce no billable interval. Billing
keys are configuration, so test-key and live-key deployments use the same schema.

The cache service's egress-total endpoint, its high-water-mark store methods,
client bridge and fake route are removed. Tenant usage has no service-selected
counter owner.

The legacy `PruneExpiredCacheObjects` row sweep is removed with its controller
call. Record expiry and reachability GC govern final blob lifetime. Old creation
time alone cannot delete a live record's bytes, including the default tenant's.
`ClaimExpiredSourceBundles`, `DeleteSourceBundleRows`, `PruneExpiredUploads`,
`DirectCacheMaxAge` and the controller's `CacheObjectMaxAge` are removed.
The controller's cache listing has no age-expiry callback. Source records retain
bytes independently of their originating run. Upload-ticket expiry releases
quota through the leased final retention pass. Physical GC wiring remains with
the storage adapter.

`Store.ExpiredOutputRuns`, `Store.ReleaseRunOutputs`, `Store.ForgetRunOutputs`,
`ExpiredOutputRun` and the store's duplicate `OutputRetention` constant are removed
with the controller's output-key sweep. Leased record expiry and reachability GC
own output lifetime. A copied output and its transitive manifest members survive
source-run deletion and reopening. Their bytes become collectible after the last
copy disappears. Copies add no upload usage. The SQLite retention proof checks
both deletion stages, and generated store tests race memo copying with collection.

Usage rows are additive per team, period and replica. Reporting sums rows.
Admission takes a shared team/quota lock and records each reservation durably;
settlement is idempotent by reservation ID. Log reads meter egress exactly and
are exempt from download quota. Deferred plan counters add no enforcement here.
The unused service-wide `egress_day` table is removed. Tenant egress remains in
`egress_usage` with period and replica identity.

`Tenant.AddUsage` owns egress writes under tenant, UTC day and trusted replica
identity. The controller records delivered successful-response bytes once at
request completion, including partial writes and cancelled request contexts.
An absent in-memory meter still records durable tenant usage. Logs count as
egress without consuming download quota. Accounting errors are logged. A process
termination before request completion can lose that request's egress sample.
`Tenant.EgressUsage` sums replicas for a UTC day or month.
`Operator.EgressUsageAcrossTeams` supplies team totals for startup alarm reload.
`Operator.PruneEgressUsage` retains thirteen months under the maintenance lease
and store clock. `Store.RecordEgressUsage`, `Store.ListEgressUsage` and the raw
`Store.PruneEgressUsage` are removed with the controller's principal flush timer.
Account deletion leaves the tenant counters unchanged because they have no user
identity. No schema or policy limit changes.

`Tenant.AppendEventCharged` replaces the raw Store event writer. Its trusted
replica identity comes from the controller. The method accepts no principal,
team or clock. It locks the tenant's run and commits the event, retained run
counters, shared storage admission and additive replica usage together. Event
bytes include the kind and payload. Events consume monthly and lifetime run
bytes alongside pending uploads, without consuming object capacity. Monthly
admission resets with the store clock. Run capacity survives month changes.
The existing event caps, free share, funding and claim fences remain in force.
Usage is recorded even when no quota is configured. Failed writes and overflow
return no sequence and roll back every accounting row.

`Tenant.ChargeDownload` replaces `Store.ChargeDownload`. `DownloadCharge`
carries the controller's trusted `ReplicaID`, byte count, delivered-byte flag
and existing free/funded caps. It accepts no team or caller clock. The tenant
handle supplies the team. Store time after the shared `download` quota lock
selects the UTC day. Admission totals and additive `team_download_day` replica
rows commit together. Unexpired download reservations consume shared capacity.
Expiry releases their holds before another charge can use that capacity.
Already delivered bytes remain countable after the cap, and the download that
crosses the cap finishes. Zero-byte checks add no usage row. Midnight resets
admission while preserving earlier report rows. Overflow or a failed counter
write rolls back both sides. Controller adapters reuse their existing lease
holder identity as the replica ID. Logs retain their download-cap exemption.

`Operator.ChargeRetainedStorage(ctx, maintenanceLease)` bills unique `blobs`
and committed `log_streams.stored_bytes`. Historical replica usage and reference
counts cannot multiply the charge. The team's cache and log rows share their
existing `storage_charged_through` watermark because the free allowance applies
once to the combined bytes. The tenant's retained allowance caps that total
before the free allowance comes off. An absent quota row adds no ceiling.
Each watermark and ledger debit commit under the current maintenance fence and
credit lock. Initial samples establish billing time. Idle gaps are clamped to
the stored bytes' creation time. A zero price resets existing watermarks so
resuming billing starts a new interval. Storage charges remain runless.

The `billing-ledger-survives-retention/store-data-sqlite` acceptance proof
checks a real storage debit and preserves every financial row across leased
retention and reopening. Tenant billing state, grant history and charge history
exclude a neighboring team's ledger and survive the same retention and restart.
Both history methods retain tenant attribution. Charge history reads the final
capability column into CreditCharge.TokenPrefix and preserves the original
run, job and awarded capability after those operational rows expire.
`Tenant.CreditUsageByRun` nets reservations, usage and refunds, excluding storage
and runless charges. Its per-run rows survive operational run retention and
reopening alongside the tenant balance and history.
`Store.CreditLedgerTotals` reads deployment-wide financial totals in one snapshot.
For each open metered award it uses C2's billing calculator and the immutable
intervals to subtract refundable seconds from the ledger's net compute seconds.
Consumption beyond already charged seconds waits for a billing checkpoint.
Open intervals stop at their persisted charged checkpoint for reporting. Closed
intervals use their finish time. A delayed reaper cannot add unconfirmed time.
Reporting cannot advance charged checkpoints or close intervals. Operational
run, node, trigger and credential rows do not control these totals. Settled and
unmetered awards need no calculator call. Generated store tests can use a contract
fake. The SQLite billing-start boundary uses C2's production calculator through
final settlement and replay after reopening. The retention proof
also checks exact deployment totals across pruning and reopening, with storage
seconds excluded. The unused `Store.RunFinished` pruning query is removed.
`Operator.CreditSettings`, `Operator.CreditRateTable` and
`Operator.WarmCPUClassCores` own deployment price and policy reads.
`Operator.SetCreditSettings` owns every billing-policy update. A stored rate
table refuses a scalar price update before any setting changes. The separate
`SetOperatorCreditSettings` writer and the reader override for older binaries
are removed. Explicit tables supply their own prices. An absent table derives
the default ladder from the scalar rate. Validation, ledger locking, settings
serialization and rollback stay in their existing transaction owners.
The raw Store methods are removed with every caller.
`Store.GrantCredits`, `Store.ListCreditCharges` and `Store.AppendEventOnce` are
also removed. Callers select a Tenant explicitly before grants, charge history
or idempotent events. The tenant methods keep their existing behavior.

`Tenant.CreditState` and `Tenant.ListCreditGrants` own these reads. Their raw
Store twins are removed. Burn windows use the store clock. A credit-state read
uses one snapshot for grants, charges, storage spend, burn, exhaustion and prices.
The read-only SQLite property commits a concurrent ledger and price update
between the aggregate and burn reads. The first report retains its original
snapshot, and the next report sees the complete update.
Balances use `Tenant.CreditBalanceMicro`. Its raw Store twin is removed.
`Tenant.RecordCreditGrant` owns grant requests. Its raw Store twin is removed.
The proof does not exercise provider verification or native placements.
The controller's billing caller uses this operator method. W1's
maintenance coordinator owns the shared retention, quota and GC ordering.

The `billing-start-award-versus-start/store-sqlite` proof now lives in
`pkg/store/store_unit_billing_boundary_test.go` as
`TestStoreBillingStartAwardVersusStart`. It uses the store's existing metered
claim fixtures with the production C2 calculator and reopens SQLite after execution
starts. Finish and replay retain the award's original price after settings change.
The ordinary-package
`internal/acceptance/store_unit_billing.go` harness and its acceptance wrapper
are removed. No native placement proof is implied.

### Storage policy and reports

The service-token `/internal/storage/reserve`, `/internal/storage/commit`,
`/internal/storage/release` and `/internal/downloads/charge` routes are removed.
`Store.RenewStorage`, `storageRenewRequest`, `recordStorageRenewTx` and
`replayStorageRenewTx` are removed with that transport. Final upload tickets own
reservation, verified commit, abort and immutable replay. Old cache RPC clients
still need W7's hard cut. Legacy direct-upload and node-log writers and their
internal reservation code are removed. Their old host/client and read surfaces
remain pending final data and tagged-log bindings.

`SetFreeTeamSlots` writes the final settings table through the existing checked
writer. Negative or sealed updates fail without replacing the prior value.
The persisted setting survives reopening. The controller reads slot availability
and passes full or unreadable conditions to its signup gate without a counter RPC.

`Tenant.SetStorageQuota`, `StorageQuota`, `SetStorageAllowance`,
`StorageUsageFor` and `StorageRetainedBytes` replace raw principal-selected
storage methods. Quota, usage and ranking rows carry `Team`. Write limits and
retained allowances have independent writers. Allowance creation preserves the
effective default tier. Usage reports sum replica rows and reject corrupt or
overflowing totals. Retained-byte reports share the billing inventory.

`Operator.StorageSettings`, `SetStorageSettings`, `SeedRunRetention`,
`DatabaseSize`, `ListStorageQuotas` and `TopStorageTeams` own deployment policy
and reports. Run and metric windows use `operator.run_retention_days` and
`operator.node_metric_retention_days`. Alarm and default-tier keys retain their
existing names in the final settings table. Policy writes commit as one batch.
Malformed, sealed, negative and overflowing values fail loudly.

The controller's storage maintenance calls leased retention and billing with the same
maintenance fence. The independent bucket-listing reconciliation pass and its
StorageMarks, ReconcileStorage, TeamsOverFreeLogShare and
PrunableNodeLogs APIs are removed. Retained-byte reports and billing read unique
blobs and committed tagged log streams. Bucket listings cannot replace replica
usage counters. PruneStorageCommitReceipts and PruneDownloadDays are removed with
the pass. Final upload tickets and quota reservations retain their atomic replay
state through leased maintenance. Cloud seeds 90-day run retention only when absent. Metric
retention stays independent. The old event-only and principal allowance sweeps
are removed. Allowance-driven eviction, physical GC and full elected-worker
coordination remain integration obligations.

The local C1 correction removes the raw methods and principal quota types.
Controller callers use team quota paths and `run_retention_days`. No C2, C3 or
C4 contract declaration changes here.

## Input and blob invariants

Environment snapshots persist names and a digest, not values. Raw argument
storage retains the execution/retry contract; secret argument classification
controls display. Before describe has classified arguments, display redacts all
requested values. The second seal must retain every secret name classified at
intake. Display and reproduction fail closed if those classifications disagree.
Reproduction and display omit secret values and public hashes
of secret-bearing input. App trust comes only from verified, deduplicated webhook
facts. Intake copies an available App default branch into the request and refuses
a conflicting supplied branch. Origin HEAD can fill a missing default branch
without manufacturing trust.

Repository identity is distinct from access. GitHub uses its numeric ID;
other hosts lowercase the host and preserve ports and path case, except
GitLab.com and Bitbucket.org path folding. Credentials and trailing .git are
stripped. Filesystem identities hash the cleaned absolute path and remain local
to that machine. Controller access rules and memo writes reject local keys.

A verified upload produces a receipt only after size and digest verification
and immutable-key commit. Object existence, integrity and reference authority
are separate checks. Every record/member/copy edge checks the current read set
or an exact permitted uploader receipt in the transaction. HasMany answers only
inside the read set; absent and unauthorized are indistinguishable. A download
must reach a verified EOF; early Close does not establish success.

Repo-bound user cache readers have no job attempt or upload identity. Their
server-derived `memo_scope` read edges use an empty reader attempt and carry the
authorized repository and verified record ref. `AuthorizeEdge` accepts these
only for output, manifest, memo and depcache. The transaction checks the user's
live repo grant and verified-default evidence before constructing those edges.

`blob.OutputData` applies the shared output cap to writes and reads using
`Store` and `RecordStore`. `blob.ResultRecordKey` supplies W1's persisted
output and manifest layout. `Name` is `result`. `Scope.Key` is
`<run>/<attempt>/<generation>`, alongside the authorized repo/ref/pipeline/job.
The exact uploader also stays in `Record.Source`. `ResolveWriteKey` derives this
layout from the bound attempt and refuses another run/job or a supplied scope
key. Empty output has an explicit blob and record. Required reads verify EOF and
successful close. Memo copies use the same destination layout.

`blob.MemoRecordName` is `output` for memo writers and exact-key readers.
A manifest-only memo retains W1's separate `manifest` name.
Read-only remote memo hits verify bounded output, manifest/member agreement
and member bytes before granting artifact access. Failed lookups or verification
produce a miss. Caller cancellation remains an error.

Job receipts retain team, run, job, attempt and generation. Owner-unit and
non-job publication receipts authorize only binary/source bundles. Imported
publication attempts may attach output/manifests only for the bound published
job. Owning another receipt in the same run grants no access. Source provenance
persists separately from operational run ownership, so memo, depcache and binary
records may survive the source run without losing uploader/ref facts.

Memo namespace contains repo, ref, pipeline, job and hash. Submitter-vouched
refs use manual scope and never overwrite a verified ref. Depcache restores own
ref, verified PR base, then verified default branch. A memo hit adds authorized
edges and copies output/manifest records atomically. Missing or corrupt memo is
a miss; depcache runs cold; staging and required output reads fail. Logs use a
separate signed upload path and controller-proxied reads. Cloud binary execution
checks committed digest and cloud provenance unless trust_local_builds is set.
Local remote-cache opt-in uses the user's repo-bound read grant and returns only
verified default-branch memo, artifacts and depcache, with no writes or binaries.

`depcacheblob.NewRemoteReader` adapts that `blob.RemoteCache` to `depcache.Reader`
with an explicit repository and lookup budget. `depcache.WithRemoteReader`
keeps local archives first and saves locally. The remote interface has no write
method. Both dependency-archive backends use the same verified-download and staged
extraction path. A copied memo without an explicit output digest is a miss,
including for followers. Explicit empty output remains a valid hit.

Memo and depcache default-branch fallback apply `VerifiedDefaultOnly` before
reading bytes or member metadata. A verified ref without default-branch evidence
does not qualify. Memo uses own scope before default fallback and has no PR-base
fallback. Depcache preserves its own-ref, PR-base, default order.

The same fallback restrictions govern persisted read edges, including HasMany,
download authorization and new references. A PR-base grant permits depcache
reads, not memo reads. A default fallback needs recorded default-branch evidence
matching the reader's sealed default branch. The
`storage-memo-and-depcache-scope/store-data-sqlite` proof checks those read-set
decisions after reopening. It seeds records and publication authority; native
App intake, namespace rules and byte verification remain integration work.

`CheckDataAccess` carries a complete target run/job/attempt/generation tuple into
the authority recheck. Published job receipts require that exact imported
attempt; omitting the selectors cannot borrow a receipt from another job.

Both `Tenant` and the owning store transaction implement C2's `DataAccessChecker`.
They share the persisted authority, receipt and read-edge checks. The transaction
implementation opens no second transaction and leaves commit or rollback to its
caller. Uncommitted receipt removal, reference changes, revocation and holder
fences therefore govern authorization in that write. Caller-supplied principal
fields cannot widen the persisted capability. Final upload and record writes
still repeat their locking checks before persisting a receipt or reference.

The `storage-reachability-and-retention-tails/store-data-sqlite` proof exercises
leased record expiry, newest-success output pins, receipt roots, transitive
members, persisted tombstones, grace and failed physical deletion. Collection
requires the persisted maintenance fence even when no tombstones are ready.
The proof refuses forged fences before and after reopening, then refuses an
expired or superseded generation on an empty inventory. A same-holder takeover
must acquire a new generation before collection resumes. Collection
rechecks references added after marking, then removes tails after their last
root disappears. An old source bundle survives its originating run and becomes
collectible only after its record expires and the tombstone grace passes.
Graphs and the deletion backend are contract fixtures. Native
placement, manifest verification and physical object deletion remain integration
obligations.

The 16 MiB output cap applies to memo records named `output` at commit and lookup.
An oversized persisted memo is corrupt cache data: memo lookup and copying miss,
while a required record lookup fails. Manifest-only memo records retain their
separate artifact contract.

## Publication and retry interpretation

Publication uses the same validators and transitions under a live publisher
capability bound to the imported run. Its accepted plan creates importable jobs
without awarding an executable claim. A publish-only node edge materializes
terminal results and exact imported attempts under local provenance. That edge
is refused for executable runs. Publication never changes the local outcome.

Retry intake reads the source named by `Request.Lineage.RetryOf` from the same
Tenant. It carries source request/spec/plan bytes and hashes unchanged. The new
run ID and retrier lineage live on the destination row, outside the immutable
source input envelope; the envelope's embedded RunID remains the source identity.
`RunRow.ID` is the execution identity. `RunRow.Lineage` reads the destination
row's versioned `lineage_json`, populated by intake; `retry_of` and
`created_principal` are indexed/source-attribution projections. Inputs are never reconstructed from a new
branch head, current project settings or caller-supplied copies of App facts.

## Acceptance scenarios

These are implementation requirements for T1, not claims that stubbed methods
already pass. Each should run on SQLite and PostgreSQL where a store is involved.

| ID | Scenario and required result | Frozen section |
|---|---|---|
| C1-01 | Cross-team child/receipt/event writes fail; omitted team fails; no silent default team. | 4.1 |
| C1-02 | Concurrent intake allocates distinct team numbers and atomically creates the executable owner. | 3.1, 4.2 |
| C1-03 | Every placement seals equal inputs equally; explicit zero survives; over-ceiling input is refused. | 3.1, 7.5 |
| C1-04 | Different second seal is spec_drift; identical accept replays without inserting duplicate jobs. | 3.1 |
| C1-05 | Foreign/stale/lapsed holder cannot write; exact terminal replay writes no event. | 3.3 |
| C1-06 | Application retry retains generation/lease and advances ordinal; disruption requeues pending in same run. | 3.3-3.4 |
| C1-07 | Third started disruption fails; unstarted lapse is free; cancel wins; OOM uses ordinary retry. | 3.4, 7.6 |
| C1-08 | Claimed cancellation emits request only, then one terminal event after drain; output refusal rolls back all writes. | 3.3 |
| C1-09 | Reconciliation waits for foreign live work, then reclaims only after committed requeue. | 3.3 |
| C1-10 | Success/memo source waits for expansion; failed/skipped/cancelled source clears barrier atomically. | 3.6 |
| C1-11 | Expansion accepts only live owner/eval and terminal source generation; replay matches body; code reconstruction matches persisted hash. | 3.6 |
| C1-12 | Owner loss while remote work runs retains capacity; expired holder cannot steal capacity back; expired waiter loses arrival priority. | 3.7 |
| C1-13 | Controller owner moves after lapse without failing runs; local started orphan still fails. | 3.8 |
| C1-14 | Pending delivery in flight cannot acknowledge a later terminal revision; metrics/profile fold occur once. | 3.9 |
| C1-15 | Run retention cascades operational rows but preserves all charges and newest-success pins. | 4.4 |
| C1-16 | Two replicas reserve against one quota row; crash/retry settlement releases exactly one persisted reservation. | 2.7 |
| C1-17 | Uploaded foreign-team, foreign-run and unrelated same-run digests cannot be attached by forged manifests. | 8.1 |
| C1-18 | Missing/corrupt required bytes fail; memo misses and depcache runs cold; HasMany reveals readable bytes only. | 8.1 |
| C1-19 | Manual refs cannot overwrite verified refs; memo cannot share across pipelines without a permitted edge. | 8.1 |
| C1-20 | Local binary provenance is refused by Cloud until trust_local_builds; exact committed digest is executed. | 8.1 |
| C1-21 | Opted-in local cache reads user-authorized verified-default data only, never binary fetch or shared writes. | 8.5 |
| C1-22 | Publish accepts plan/results/logs without owner claims; failed publication leaves local success intact. | 3.1, 9.3 |
| C1-23 | Retrying older default-branch SHA retains original App trust, actor, PR and seals, with distinct retrier lineage. | 3.1, 3.5 |
| C1-24 | Repository rename, case, host ports and filesystem paths preserve the canonical identity rules. | 4.1 |
| C1-25 | Final neutral schema and translated schema have identical shapes and classified tables. | 4.1, 4.5 |
| C1-26 | Pre-plan binary/source receipt works under owner authority but cannot authorize unrelated job outputs. | 3.1, 8.1 |
| C1-27 | Log gaps/stalls read flushed segments; incomplete tail cannot change job outcome; retention class is snapshotted. | 2.7, 8.1 |

## Cross-area assumptions

| C1 type or surface | Other area must match |
|---|---|
| `Actor`, `OwnerLeaseRef`, `capability.UnitKind`, `UnitClaim.Bearer`, `AdmissionResult.HereBearer`, schema capabilities | C2 resolves references in the transaction, matches actor/fence and fixed award kinds trigger/plan/eval/node; here is a trigger Award case. Bearers return in memory once through C2's transport, never in persisted snapshots or generic JSON. |
| `ErrIllegalTransition`, blob errors | C2 maps holder_rights/stale_generation to lock_held; illegal_edge/terminal to illegal_transition; unreadable digest to forbidden with digest_not_readable. |
| `PlanAcceptance`, `ExpansionSubmission`, `RunSnapshot` | C3 validates plan declarations. C1 projects a consistent snapshot and shares Decide between both owners. |
| `AcceptedExpansion`, `NodeRow.SpecHash`, `AttemptIdentity` | C3 preserves source vs eval generations, exact node-wide attempt ordinal and persisted generated topology. |
| `runspec.Setting`, `RunnerSelection`, `Ceilings`, `SealInput` | C4 applies the frozen precedence and refuses over-ceiling settings at the correct seal; the store supplies time, not the requester. |
| `RetentionRules`, `SegmentCommit` | C4 owns numeric limits and log class settings; C3 emits immutable attributed segments and terminal completeness. |
| `blob.Authority`, upload identity, publication edge | C2 derives run/repo/ref/publisher scope; job receipts never transfer through possession of a digest or same-run membership. |
| `billing_awards`, `billable_intervals` | C2 billing provenance and settlement use immutable award facts, exclude customer-runner wait and survive run/capability pruning. |
| `StateStore`, `OperatorStore`, `blob.RecordStore` | C3 host seams consume these final interfaces; old implementation consumers are removed when ported, not adapted. |
| C1-01 through C1-27 | T1 supplies executable cross-placement scenarios and aligns names with its independent catalog. |

## Intake replay

`IntakeRequest.IdempotencyKey` survives `IntakeForStore` and retry intake.
`AdmitRun` derives the principal/team/pipeline scope and canonical input digest
and commits them on the run, including published runs with no trigger row.
A matching key and payload returns the original result; changed input returns
`conflict`. An absent key requests a separate invocation. Trigger queue rows
hold no competing idempotency fields.

## Interpretations and implementation obligations

1. Scheduling and expansion ownership is settled: C1 owns `RunSnapshot.Scheduling`,
   `Decide`, `ReconcileNode` and `AcceptExpansion`. C3 consumes those APIs through
   the host interfaces. The controller's wire adapter combines a declaration
   body or structured failure with C2-derived `Actor` authority.
2. Capabilities are operator-owned with nullable team only for unbound service
   and operator roots. Awards and exchanges require the derived team. Tagged
   parents preserve session and cross-team launcher ancestry.
3. Eval uses `Binding.Generation` for its immutable source fence and
   `Binding.Expansion.Generation` for its own lease. The complete binding persists
   in the versioned capability envelope, with indexed source and eval projections.
4. The frozen state table omits publication despite requiring one-way published
   jobs. The added publication-only acceptance and materialization edge uses
   live run-bound publisher authority, local provenance and no executable claim.
   Likewise, pre-plan binary/source uploads need a tagged non-job uploader. That
   authority must never satisfy an unrelated job's output edge.
5. Retry keeps the original input-envelope identity, with new execution identity
   outside it, as specified above. This preserves literal sealed bytes; C3 must
   use RunRow.ID for execution and the carried envelope only for inputs.
6. Canonical JSON and scope serialization need shared golden vectors before
   implementation. Hashes exclude their own fields and seal timestamps; object
   keys sort, list order and explicit presence remain stable. Typed namespace
   components must encode without collisions, including manual ref scope.
7. Account audit may precede any team. `business_events` is operator-owned with
   nullable team; tenant queries still filter it. Intake idempotency belongs to `runs`, scoped by the server-derived principal
   and pipeline, so published runs receive the same replay guarantee.
8. Newest-success pins use a restrictive run FK because a cascading pin would
   permit deletion to erase the retention exception. Explicit deletion must
   resolve the pin first. The general operational-cascade rule has this exception.
9. Terminal replay uses C2 LiveResult with the persisted actor, generation and
   digest. Its narrow exception still checks token expiry, ancestry and membership.
   C1 rechecks the same identity transactionally; replay grants no sensitive reads.
10. The lock order is a target derived from the existing ledger order with new
    capability, ownership, capacity and blob locks. Implementation must exercise
    competing awards, revocation, cancellation, quota and billing before it is
    treated as proven deadlock-free.

## Schema inventory

The final baseline contains 83 tables and 101 explicit indexes. `SchemaTables`
classifies 66 tenant and 17 operator tables. It retains accounts, identity,
GitHub App/Actions, schedules, sizing, admission, approvals, logs, metrics,
credits, cards, git credential opt-ins/audits and build trust. Obsolete token,
output, memo, agent-loss, dispatch, bounce and duplicate-plan tables are removed.
`quota_admission` is the locked aggregate; `quota_reservations` persists each
hold and its settlement. `settings.session_csrf_key` requires an envelope and
its sealed flag. All persisted timestamps use Unix nanoseconds.

## Verification boundary

This branch is a contract artifact. The neutral schema replaces the old dialect
files, so the old runtime SQL is not a functioning implementation of this
baseline. No Sparkwing binary was run. Runtime startup, PostgreSQL translation,
state-machine execution and end-to-end acceptance remain implementation work.
Completed checks:

- `GOWORK=off GOFLAGS=-buildvcs=false go build ./...` passed.
- `go vet` passed for `pkg/store`, `pkg/storage/...`, `pkg/controller/client`,
  `internal/nodehost`, `internal/orchestrator`, `internal/blob` and `internal/runspec`,
  with the same workspace and VCS flags.
- `go test ./pkg/store -run '^TestRewriteSchema' -count=1` passed all four tests.
  They exercise raw in-memory SQLite against the final embedded schema.
- `commentcheck -base origin/main` passed for the changed Go files;
  `git diff --check` passed.
- An independent Astra review found publication, pre-plan uploads and durable
  quota identity gaps. Follow-up inspection confirmed those blockers closed.
  The remaining retry read-model field was added before the final checks.

Build caches and temporary files stayed inside this worktree. No migration, compatibility shim or intermediate
schema version is added.

## File manifest

| Change | Path |
|---|---|
| Change | `internal/nodehost/approvals_test.go` |
| Change | `internal/nodehost/events_test.go` |
| Change | `internal/nodehost/expand.go` |
| Change | `internal/nodehost/rehydrate_test.go` |
| Change | `internal/nodehost/retry_eval_unix_test.go` |
| Change | `internal/nodehost/retry_test.go` |
| Change | `internal/nodehost/scheduler.go` |
| Change | `internal/nodehost/scheduler_test.go` |
| Change | `internal/orchestrator/backends.go` |
| Change | `internal/orchestrator/orchestrator.go` |
| Change | `internal/orchestrator/pod_node_host.go` |
| Change | `pkg/controller/client/client.go` |
| Change | `pkg/controller/client/state_assertion.go` |
| Change | `pkg/storage/doc.go` |
| Change | `pkg/storage/state.go` |
| Change | `pkg/storage/state_test.go` |
| Change | `pkg/storage/storeurl/spec.go` |
| Change | `pkg/store/doc.go` |
| Delete | `pkg/store/schema_postgres.sql` |
| Delete | `pkg/store/schema_sqlite.sql` |
| Change | `pkg/store/schema_test.go` |
| Change | `pkg/store/store.go` |
| Change | `pkg/store/tenant.go` |
| Create | `internal/blob/contracts.go` |
| Create | `internal/runspec/contracts.go` |
| Create | `pkg/storage/state_contract.go` |
| Create | `pkg/store/claim_contract.go` |
| Create | `pkg/store/completion_contract.go` |
| Create | `pkg/store/concurrency_contract.go` |
| Create | `pkg/store/maintenance_contract.go` |
| Create | `pkg/store/node_state.go` |
| Create | `pkg/store/result_contract.go` |
| Create | `pkg/store/rewrite_schema_test.go` |
| Create | `pkg/store/run_spec.go` |
| Create | `pkg/store/run_state.go` |
| Create | `pkg/store/schema.sql` |
| Create | `pkg/store/schema_contract.go` |
| Create | `pkg/store/source_release.go` |
| Create | `pkg/store/state_contract.go` |

The existing host, orchestrator, storage and client edits are mechanical
`StateStore` to `StateStoreOld` renames. Store startup changes only its schema
source and translator call. The existing dialect-shape test retains its purpose;
its comment now describes translation. `c1-data.md` is this contract document.

## Export inventory

Each row names a contract declaration and its purpose. Enum constants belong
to their adjacent type; registry functions and variables contain complete sets.
The existing `Store`, `Tenant`, `Operator`, `Open`, `ForTeam`, `AsOperator` and
`Team` handles retain their names. Their final authority boundaries are above.

### internal/runspec/contracts.go

| Export | Purpose |
|---|---|
| `Optional` | Optional preserves an explicitly selected zero separately from absence. |
| `Hash` | Hash is a lowercase SHA-256 of canonical, versioned JSON. |
| `RepoKey` | RepoKey identifies a repository without conferring authority. |
| `AttemptIdentity` | AttemptIdentity uses a node-wide positive Attempt that never resets. Generation is a fence attribute, not the attempt ordinal or its identity. |
| `RepoInput` | RepoInput supplies independently resolved identity facts, never authorization. |
| `Repository` | Repository retains a human spelling beside the canonical key. |
| `RepoRule` | RepoRule makes case and machine-scope rules available to conformance tests. |
| `RepoRules` | RepoRules enumerates every canonical identity form. |
| `CaseInsensitiveProviders` | CaseInsensitiveProviders is the complete approved path-folding set. |
| `CanonicalRepository` | CanonicalRepository strips URL credentials and trailing .git, preserves ports, and hashes cleaned absolute filesystem paths. Resolving a GitHub ID grants no access. |
| `RepoUse` | RepoUse separates machine-local storage from controller authorization scopes. |
| `ValidateRepoUse` | ValidateRepoUse refuses local keys in controller access rules and memo writes. |
| `IntakeMode` | IntakeMode prevents published history from obtaining executable owner claims. |
| `Dispatch` | Dispatch selects the owner of scheduling after acceptance. |
| `Origin` | Origin identifies the intake path without making it a trust assertion. |
| `Actor` | Actor preserves the original actor when a different person retries a run. |
| `PullRequest` | PullRequest keeps base and head facts so intake can refuse forks before award. |
| `AppFacts` | AppFacts is server-populated only after HMAC verification and replay-key dedup. No current-head lookup changes these facts, including on a retry of an older SHA. |
| `Git` | Git pins checkout to the sealed SHA, never the later head of Ref. |
| `EnvSnapshot` | EnvSnapshot persists only names and a digest; values remain in private host storage. |
| `Source` | Source records acquisition intent without embedding source credentials. |
| `Lineage` | Lineage records retriers separately from immutable original actor and git facts. |
| `Layer` | Layer names the source to report when an explicit setting is invalid. |
| `SettingOrigin` | SettingOrigin allows C4 errors to name source, file and key. |
| `Setting` | Setting retains presence and origin without resolving precedence in this package. |
| `Resources` | Resources preserves explicit sizing through both seal points. |
| `RunnerSelection` | Preserve the requested any/name/label/here route without granting identity or access. |
| `RunnerSelectionKind` | Select the closed route vocabulary; here requests C2's run-bound Award. |
| `Options` | Options carries request winners and resolved values without zero-as-default rules. |
| `Ceilings` | Ceilings snapshots operator bounds for C4 to enforce without silent clamping. |
| `Request` | Request is submitted to the one intake transaction on every placement. App facts, allowed pools and authority references are derived by intake, not trusted from JSON. Raw requested argument values are restricted execution inputs, not a display document. SecretArgs is unset before describe. Display must then redact every requested value. |
| `IntakeFacts` | IntakeFacts are server-derived, with opaque authority IDs resolved only by C2. |
| `SealedRequest` | SealedRequest is immutable at intake, even when later settings resolve from code. |
| `SealRequest` | SealRequest canonicalizes immutable intake data after the intake owner authorizes it. |
| `DefaultBranchSource` | DefaultBranchSource limits discovery to App facts or the acquired origin HEAD. |
| `Guards` | Guards preserves refusal evidence so a rejected run seals the same inputs as a plan. |
| `Resolved` | Resolved contains checkout-derived facts and C4-resolved settings for seal point two. SecretArgs must be present (an empty, non-nil list means none) after describe. Raw Args remain available for execution, retries and masking, never direct display. |
| `Spec` | Spec binds both seals and the accepted plan hash as the one host input. |
| `SealInput` | SealInput accepts raw plan bytes already validated by C3's ValidatePlan. The store performs that validation and C4 ceiling checks in its acceptance transaction. |
| `SealSpec` | SealSpec binds the canonical spec and plan without owning plan validation. |
| `CheckSeal` | CheckSeal permits an identical replay and refuses a different hash as spec_drift. |
| `Projections` | Projections supplies write-once indexed fields derived only from the sealed spec. |
| `Project` | Project prevents independently writable copies of sealed run inputs. |
| `RetryInput` | RetryInput carries sealed state unchanged and records the new requester as lineage. |
| `RetrySnapshot` | RetrySnapshot preserves source trust and seals while adding separate retry lineage. |
| `CarryRetry` | CarryRetry does not consult branch head or reconstruct inputs from current settings. |
| `Context` | Context provides trusted execution inputs on every placement. It may contain secret argument values and must not be rendered or logged. |
| `Spec.RunContext` | RunContext refuses unsealed input through the Spec-only method boundary. |
| `Spec.GuardContext` | GuardContext retains trusted default-branch provenance for fail-closed guards. |
| `Spec.Reproducer` | Reproducer excludes secret argument values and all credentials. |
| `CanonicalHash` | CanonicalHash excludes the hash field itself and nonsemantic seal timestamps. Sorted object keys, stable list ordering and explicit Optional presence prevent hash drift. |
| `EnvironmentDigest` | EnvironmentDigest returns only sorted names and a digest of their captured values. Callers retain values in private host storage and never persist them in the spec. |
| `Display` | Display is a separate safe projection, because serializing Spec exposes execution inputs. Hashes and env digests are omitted because deterministic hashes can reveal guessed secret values. |
| `DisplayRequest` | DisplayRequest redacts all requested values until trusted describe supplies classification. A malformed classification fails closed. This copy never changes inputs retained for execution. |
| `DisplaySpec` | DisplaySpec applies resolved secret classification to both requested and resolved args. Every dashboard, event, public API and CLI renderer uses this projection, never raw Spec JSON. |

### internal/blob/contracts.go

| Export | Purpose |
|---|---|
| `Digest` | Digest names immutable bytes by lowercase SHA-256. |
| `Kind` | Kind excludes logs because log reads must be controller-proxied. |
| `Kinds` | Kinds is the complete record-kind registry for schema and wire drift checks. |
| `Blob` | Blob is existence truth; object storage alone cannot establish a live blob. |
| `Provenance` | Provenance is assigned by the server, never copied from upload metadata. |
| `Principal` | Principal is an opaque server-resolved C2 reference, never a bearer token. |
| `RefScope` | RefScope distinguishes submitter-vouched refs from verified refs. |
| `RefFacts` | RefFacts carries server-sealed evidence rather than accepting client trust flags. |
| `Scope` | Scope bounds records by authorized repository, ref and pipeline. |
| `RecordKey` | RecordKey keeps the kind, server-derived namespace and logical name distinct. |
| `Record` | Record binds source facts and provenance to immutable committed bytes. |
| `Member` | Member makes every manifest edge independently subject to authority checks. |
| `Receipt` | Receipt retains its tagged uploader and cannot become run-wide job authority. Job and imported-publication receipts keep the exact job, attempt and generation. |
| `Tombstone` | Tombstone starts reachability GC's grace period, never an age-only object expiry. |
| `EdgeKind` | EdgeKind enumerates the only admitted ways to make bytes reachable. |
| `EdgeKinds` | EdgeKinds is complete so authorization tests cannot silently omit an edge. |
| `ReadEdge` | ReadEdge records a server-derived ancestry or scoped-cache entitlement. |
| `ReadSet` | ReadSet is a transaction snapshot and must never be decoded from caller JSON. |
| `Authority` | Authority carries opaque C2 references for a transaction-time authorization recheck. Possession of this struct is not proof of authority; all values must be resolved server-side. |
| `EdgeRequest` | EdgeRequest checks authority independently from integrity and existence. |
| `AuthorizeEdge` | AuthorizeEdge rejects unrelated same-run receipts as digest_not_readable. Own-upload job edges require the exact Attempt, including generation. Owner and non-job publication receipts authorize only binary/source_bundle, never another job's outputs. Imported publication output edges require the matching imported attempt in a publish run. |
| `CommitRecordRequest` | CommitRecordRequest leaves provenance to server derivation and checks every member. |
| `RecordStore` | RecordStore methods execute with live binding, existence and edge checks in one transaction. |
| `Budget` | Budget bounds a batched existence query by an explicit caller deadline. No package default silently adds a customer-facing limit; limits owns policy values. |
| `PutRequest` | PutRequest signs both expected size and digest before any transfer. |
| `GetRequest` | GetRequest names bytes only through a live server-computed read set. |
| `HasManyRequest` | HasManyRequest must never expose existence beyond the caller's read set. |
| `Presence` | Presence reports only readable, live blobs; absent and unauthorized are indistinguishable. |
| `VerifiedReader` | VerifiedReader reports digest mismatch at EOF; a caller must consume EOF for success. Close before EOF does not claim verification. |
| `Store` | Store is the sole byte backend: signed /data upload plus commit, and verified downloads. Put yields a receipt only after size/digest verification and immutable-key commit. |
| `SignedTransfer` | SignedTransfer carries a one-object external URL, not a Sparkwing credential. |
| `UploadIntent` | UploadIntent binds staging bytes to the uploader before issuing a PUT URL. |
| `UploadTicket` | UploadTicket is opaque and is checked again by the commit endpoint. |
| `CommitUpload` | CommitUpload cannot substitute a digest or uploader after signing. |
| `DataSigner` | DataSigner is the controller's signed-data backend contract for S3 or filesystem. Download expiry comes from limits (60 seconds), and logs use a separate signer. |
| `SignedStoreOptions` | SignedStoreOptions injects signed-data transport without adding another cache protocol. |
| `HTTPClient` | HTTPClient constrains transport injection to one signed transfer operation. |
| `NewSignedStore` | NewSignedStore verifies transferred bytes and never falls back to cache-service routes. |
| `ObjectKey` | ObjectKey produces teams/<team>/cas/<sha256> after validating both components. |
| `MemoKey` | MemoKey cannot share an unclaimed result across pipelines. |
| `MemoRecordName` | Names the persisted memo output consistently for writers and exact-key readers. |
| `ResultRecordKey` | Names output and manifest records for an exact persisted attempt. |
| `MemoNamespace` | MemoNamespace reserves manual:<ref> for submitter-vouched writes. |
| `MemoLookup` | MemoLookup uses the authority's server-derived scope and verified default fallback. |
| `CopyMemoRequest` | CopyMemoRequest creates output and manifest edges atomically with the hit. |
| `MemoHit` | MemoHit carries digests and authorized edges rather than relying on the source run's survival. |
| `DepcacheKey` | DepcacheKey shares repo/ref isolation with memo while naming a file cache. |
| `RestoreSource` | RestoreSource orders own-ref, PR-base and verified-default cache lookups. |
| `RestoreRule` | RestoreRule makes the trusted fallback requirement explicit. |
| `DepcacheRestoreOrder` | DepcacheRestoreOrder is the complete ordered restore policy. |
| `DepcacheLookup` | DepcacheLookup never accepts a caller-selected repo outside its data grant. |
| `FailureUse` | FailureUse distinguishes safe cold starts from missing required bytes. |
| `FailureAction` | FailureAction prevents missing bytes or exhausted budgets from looking successful. |
| `FailureRule` | FailureRule applies equally to missing, corrupt and over-budget reads. |
| `FailureRules` | FailureRules is the complete per-use failure policy. |
| `BuildTrust` | BuildTrust preserves the team-owned opt-in for local binary provenance. |
| `BinaryExecution` | BinaryExecution binds cloud execution to the committed digest and provenance. |
| `CheckBinaryExecution` | CheckBinaryExecution permits local provenance only with trust_local_builds. |
| `RemoteCacheOptions` | RemoteCacheOptions uses the user's existing repo-bound read authority. |
| `RemoteCache` | RemoteCache exposes reads only, with no binary fetch or shared-cache write method. Every returned record must originate in a verified default-branch run. |
| `OpenRemoteCache` | OpenRemoteCache is opt-in by profile and never borrows a runner credential. |
| `PublishPolicy` | PublishPolicy labels published results local and never creates an owner claim. Publication failure is reported separately from the completed local run's outcome. |
| `ErrBudget` | ErrBudget follows FailureRules instead of being treated as successful absence. |
| `ErrNotFound` | ErrNotFound makes missing committed bytes loud at required-read boundaries. |
| `ErrIntegrity` | ErrIntegrity prevents corrupt bytes from being accepted as a successful read. |
| `ErrDigestNotReadable` | ErrDigestNotReadable maps through C2 to forbidden with reason digest_not_readable. |
| `RecordLookup` | RecordLookup combines a logical key with authority instead of accepting a bare CAS digest. Local stores may use local repository keys. Controller lookups require repo-bound data authority. |
| `RecordLookupResult` | RecordLookupResult distinguishes a cache miss from a record with required missing bytes. All returned members have passed existence and edge checks before their digests enter ReadSet. |
| `DepcacheHit` | DepcacheHit records the winning fallback so provenance remains visible after a restore. No hit, corrupt bytes and exhausted budget mean a cold cache under FailureRules. |
| `WriteScopeIntent` | WriteScopeIntent omits repo, ref and trust because the authority supplies those facts. Pipeline/job choices still require membership checks against the sealed run at commit. |
| `ResolveWriteKey` | ResolveWriteKey derives repository and vouched/manual ref scope from live authority. It rejects client-selected real-ref writes without server-verified facts. |
| `RecordNamespace` | RecordNamespace encodes typed components without delimiter collisions. Memo namespaces preserve memo/<repo_key>/<ref_scope>/<pipeline>/<job>/<hash> semantics. |
| `DepcacheNamespace` | DepcacheNamespace shares the memo write-ref rule while preserving the cache name and key. |
| `UploadIdentityKind` | UploadIdentityKind covers executable jobs, preplan owners and non-executable publication. |
| `UploadUnitKind` | UploadUnitKind keeps owner uploads bound to the live awarded unit's fence. |
| `UploadUnit` | UploadUnit identifies the owner lease without inventing a job before AcceptPlan. Eval source identity binds the evaluation to its terminal generator source. |
| `UploadIdentity` | UploadIdentity is resolved by C2 from live authority, never accepted as caller proof. Job requires Attempt and forbids Unit. Owner requires Unit and forbids Attempt. Publication forbids Unit and requires a publish-mode run without an executable claim. Imported publication outputs require Attempt matching that run's imported job result. Nested attempt Team/RunID must equal this identity's Team/RunID. |
| `UploadRule` | UploadRule distinguishes non-job byte intake from job-owned output edges. |
| `UploadRules` | UploadRules is the complete identity/record-kind policy, rechecked at commit. Publication with an imported attempt follows the final row rather than non-job publication. |
| `AuthorizeUpload` | AuthorizeUpload checks the C2-resolved identity and target kind in the write transaction. It requires a live owner fence or publication authority, verifies imported job identity, and rejects identity/principal/capability mismatches with the authenticated authority. |

### internal/blob/output.go

| Export | Purpose |
|---|---|
| `OutputData` | Binds output bytes and records to reader authority and an exact uploader. |
| `OutputData.Put` | Checks the output cap and uploader binding, obtains a verified receipt, then commits the output record. |
| `OutputData.Read` | Looks up the authorized output record and consumes bounded bytes through verified EOF and successful close. |

### pkg/storage/state_contract.go

| Export | Purpose |
|---|---|
| `StateStore` | StateStore is the tenant-scoped host contract; status writes use transitions and source inputs use the two seals. A host borrows this handle and does not close the shared store. Claims and deployment maintenance stay unscoped. |

### pkg/store/claim_contract.go

| Export | Purpose |
|---|---|
| `UnitDemand` | UnitDemand carries self-asserted labels only; granted identity, reservations and team come from C2's capability. |
| `UnitClaim` | UnitClaim records the store-derived team and award; source generation remains distinct from eval claim generation. |
| `Store.ClaimUnit` | ClaimUnit applies the shared matcher and C2 Award atomically; only launcher service scope can cross teams. |
| `Store.ReleaseUnit` | ReleaseUnit returns an unstarted unit without consuming an attempt; cancellation and started leases use transitions. |
| `Store.RenewUnit` | Renews the capability-derived unit, records owner execution starts and delivers cancellation without accepting caller-selected fences. |
| `OperatorStore` | OperatorStore restricts deployment work to explicit methods without embedding a tenant or raw Store. |

### pkg/store/controller_recovery.go

| Export | Purpose |
|---|---|
| `ControllerRun` | Carries the team and global run ID of a visited controller-owned run. |
| `Operator.ReconcileControllerRuns` | Resumes settlement and live capacity renewal in bounded pages under the elected controller fence. |

### pkg/store/completion_contract.go

| Export | Purpose |
|---|---|
| `Effect` | Effect identifies one revisioned desired-state row per team and run. |
| `EffectPhase` | EffectPhase is ranked independently of the terminal outcome so a late pending write cannot regress it. |
| `EffectPolicy` | EffectPolicy controls delivery phases and external idempotency keys. |
| `CompletionEffects` | CompletionEffects is the entire effect registry consumed by the elected reconciler. |
| `EffectPhaseRanks` | EffectPhaseRanks supplies the monotone desired-state order. |
| `DesiredRunEffect` | DesiredRunEffect is a committed snapshot for delivery; Payload comes from the run, never an executor. |
| `EffectDelivery` | EffectDelivery acknowledges only the revision actually sent, leaving a newer desired revision due. |
| `Operator.DueRunEffects` | DueRunEffects reads revisions under the completion singleton lease; expiry stops further delivery acknowledgements. |
| `Tenant.AcknowledgeRunEffect` | AcknowledgeRunEffect advances delivered_rev only to the delivered revision, never to a newly observed desired_rev. |
| `Tenant.RetryRunEffect` | RetryRunEffect records a failed delivery without overwriting a later desired state or its due work. |

### pkg/store/concurrency_contract.go

| Export | Purpose |
|---|---|
| `CapacityBinding` | CapacityBinding makes capacity follow work across owner loss instead of following a process. |
| `CapacityHolder` | CapacityHolder retains its own lease as proof that capacity has counted continuously. |
| `CapacityWaiter` | CapacityWaiter keeps its arrival order only while its own lease remains live. |
| `AdoptionKind` | AdoptionKind refuses to revive expired capacity; a new arrival goes through normal admission. |
| `CapacityAdoption` | CapacityAdoption reports the store's liveness decision, never a caller-supplied original arrival time. |
| `CapacityLivenessRule` | CapacityLivenessRule describes the conditions which renew the holder's continuity lease. |
| `CapacityLiveness` | CapacityLiveness retains existing admission policy while changing who keeps capacity alive. |
| `Tenant.AdoptCapacity` | AdoptCapacity checks continuity under the entry lock; expired holders and waiters are removed before a fresh request. |
| `Tenant.SupersedeCapacity` | SupersedeCapacity marks the holder superseded and requests cancellation on its bound work in the same transaction. |

### pkg/store/maintenance_contract.go

| Export | Purpose |
|---|---|
| `SingletonLease` | SingletonLease is fenced so a former replica cannot continue maintenance after another replica takes over. |
| `SingletonWork` | SingletonWork lists every correctness-critical elected worker, including the local schedule worker. |
| `Operator.AcquireSingletonLease` | AcquireSingletonLease refuses a live foreign holder and advances the fence after a lapse. |
| `Operator.RenewSingletonLease` | RenewSingletonLease refuses an expired fence; owner recovery acquires a new lease instead of reviving one. |
| `ReapedUnit` | ReapedUnit reports recovery in the same run; application retry budgets are never consumed by a lapse. |
| `Store.ReapUnits` | ReapUnits uses the store clock and cancellation precedence, settling billing and effects with each transition. |
| `RetentionRule` | RetentionRule separates operational expiration from pins and financial retention. |
| `RetentionRules` | RetentionRules is the full leased-pass policy; nil means operator-defined or disabled, never an implicit zero default. |
| `FinancialTables` | FinancialTables identifies records that never take a cascading run foreign key. |
| `RetentionRequest` | RetentionRequest is executed only while the maintenance lease is live; settings are resolved inside the store. |
| `RetentionResult` | RetentionResult reports committed removals and preserved pins; financial rows must always remain untouched. |
| `Operator.MaintainRetention` | MaintainRetention is the single deployment pass for run, metric, object and credential expiration. |
| `UsageDelta` | UsageDelta is additive per replica; summed counters are reports and never admission locks. |
| `QuotaReservation` | QuotaReservation is backed by the shared team/quota row, preventing two replicas from spending the same capacity. |
| `Tenant.AddUsage` | AddUsage records accounting under the Tenant with a replica identity, including exact log egress. |
| `Tenant.ReserveQuota` | ReserveQuota locks one shared team/quota row; log reads are exempt from download quota. |
| `Tenant.SettleQuota` | SettleQuota commits actual usage or releases the reservation in one transaction. |

### pkg/store/result_contract.go

| Export | Purpose |
|---|---|
| `StepReport` | StepReport fences step state to the attempt that produced it. |
| `AnnotationWrite` | AnnotationWrite carries already masked text; the store rechecks the shared annotation byte limit without truncation. |
| `SummaryWrite` | SummaryWrite carries masked job or step markdown under the writer's live binding. |
| `Tenant.RecordStep` | RecordStep refuses stale attempts instead of letting delayed reports replace a later attempt's steps. |
| `Tenant.WriteAnnotation` | WriteAnnotation refuses an oversized masked annotation so the store cannot split UTF-8 or reveal a raw suffix. |
| `Tenant.WriteSummary` | WriteSummary stores job or step markdown only after the claim binding is rechecked. |
| `Tenant.CommitRecord` | CommitRecord checks all member existence and reference authority before making any digest reachable. |
| `Tenant.CopyMemo` | CopyMemo creates destination output and manifest edges atomically from a server-scoped cache hit. |
| `Tenant.LookupRecord` | LookupRecord adds no authority merely because a key exists; all returned bytes must be in the caller's read set. |
| `Tenant.LookupMemo` | LookupMemo applies the claim's repo/ref/pipeline scope and the verified default-branch fallback. |
| `Tenant.LookupDepcache` | LookupDepcache follows the single restore-order table and runs cold on absent or invalid bytes. |

### pkg/store/result_logs.go

| Export | Purpose |
|---|---|
| `Segment` | Separates decoded ranges from stored bytes and digest. |
| `SegmentCommit` | Binds a contiguous batch and optional seal to its writer and tagged subject. |
| `LogSegments` | Returns authorized persisted ranges and seal state. |
| `Tenant.RecordSegments` | Commits immutable segments and seals under the current writer fence. |
| `Tenant.ReadLogSegments` | Rechecks authority for the exact subject before selecting byte ranges. |

### pkg/store/schema_contract.go

| Export | Purpose |
|---|---|
| `TranslateSchema` | TranslateSchema preserves the neutral schema's constraints on both engines. SQLite uses the input unchanged; PostgreSQL maps BLOB to BYTEA. Unsupported dialects or syntax return an error rather than dropping clauses. |
| `SchemaOwner` | SchemaOwner makes deployment authority explicit even when an operator table carries an optional tenant for audit or binding checks. |
| `SchemaTable` | SchemaTable classifies every baseline table so new state cannot evade the tenant-predicate guard by omission. |
| `SchemaTables` | SchemaTables is the complete ownership inventory for schema.sql. Implementations must compare the created tables with this registry on both engines. |

### pkg/store/source_release.go

| Export | Purpose |
|---|---|
| `Tenant.ReleaseSourceCredential` | Rechecks live source authority, sealed repository, enrollment, host confirmation and machine-owner consent, then commits C2's audit before returning the sealed team credential. |
| `Tenant.SetSourceMachineConsent` | Changes a machine root's source-credential consent only for its current account owner in the same tenant. |

### pkg/store/state_contract.go

| Export | Purpose |
|---|---|
| `RunRow` | RunRow is the immutable request/spec plus the mutable lifecycle read model used by hosts. |
| `RunSnapshot` | Reads persisted state and accepted expansion declarations for the shared scheduling projection. |
| `SourcePolicy` | SourcePolicy keeps resume, retry, memo and coalescing authority distinct at reconciliation. |
| `ReconcileKind` | ReconcileKind tells a host whether it may execute; only Execute carries a newly fenced claim. |
| `ReconcileRequest` | ReconcileRequest names a possible source; the store checks it rather than trusting the host's cache hit. |
| `ReconciledNode` | ReconciledNode is decided with any self-claim in the same transaction. |
| `LeaseBeat` | LeaseBeat delivers cancellation and watched jobs without changing the owner heartbeat cadence. |
| `StateEvent` | StateEvent uses a single per-run sequence and a unique node state sequence for lifecycle writes. |
| `Tenant.ReadRun` | ReadRun returns only the Tenant's run, including both seals and the stable team number. |
| `Tenant.ReadNode` | ReadNode returns persisted state without interpreting an elapsed lease as an implicit requeue. |
| `Tenant.ReadSnapshot` | ReadSnapshot prevents scheduling from a mix of states observed in different transactions. |
| `Tenant.ReadStateEvents` | ReadStateEvents resumes strictly after the caller's sequence; state transitions own lifecycle event writes. |
| `Tenant.ReconcileNode` | ReconcileNode decides and self-claims atomically; WaitHolder callers follow events and reconcile again after requeue. |
| `Tenant.RenewOwner` | RenewOwner also renews self-claimed jobs and continuously live plan and job concurrency holders atomically. |
| `Tenant.RenewNodeClaim` | RenewNodeClaim renews the exact live claim and its bound concurrency holders using the store clock. |
| `StateEventRule` | StateEventRule makes every lifecycle event name store-owned on customer surfaces. |
| `NodeEvents` | NodeEvents matches requested To and RequestOnly; an empty cause matches every cause for that terminal outcome. Cancellation requests emit job_cancel_requested while preserving claimed/running state until the holder drains. |
| `LockClass` | LockClass fixes transaction lock order across claims, state, capacity and the financial ledger. |
| `LockOrder` | LockOrder is acquired left to right; keys within one class use lexical order. Operations starting later in the list must not acquire an earlier class afterward. |

### pkg/store/node_state.go

| Export | Purpose |
|---|---|
| `NodeState` | NodeState separates offerability and claim ownership from the persisted status. |
| `NodeStatus` | NodeStatus is the persisted status; ready and claimed are derived by nodeState. |
| `ExpansionState` | ExpansionState prevents settlement before generated jobs have been accepted. |
| `ActorKind` | ActorKind identifies which store-verified authority may take an edge. |
| `OwnerLeaseKind` | OwnerLeaseKind selects the persisted liveness row; local hosts use a trigger lease too. |
| `OwnerLeaseRef` | OwnerLeaseRef identifies a fence to re-read under the store clock, never a caller's liveness assertion. |
| `Actor` | Actor carries references to authenticated authority; C2 resolves the capability inside each write transaction. |
| `NodeRef` | NodeRef omits team because a Tenant supplies it; a wire encoder uses job_id. |
| `NodeRow` | NodeRow contains the state facts consumed by the shared decision and reconciliation functions. |
| `NodeAttempt` | NodeAttempt preserves the node-wide ordinal across claims; generation is an attribute of that ordinal. |
| `TransitionCause` | TransitionCause selects edge-specific validation rather than accepting arbitrary column updates. |
| `EdgeGuard` | EdgeGuard names predicates that the transition re-checks after locking the row. |
| `NodeEdge` | NodeEdge is a complete legal edge family; absence from the table means refusal. |
| `NodeTransitions` | NodeTransitions returns fresh data so callers cannot mutate the store's legal transition table. Insertions start at pending with state_seq=0 through AcceptPlan or AcceptExpansion and are not edges. |
| `NodeResult` | NodeResult references authorized records; raw digests alone never authorize output attachment. |
| `MaterializeSource` | MaterializeSource preserves the read edge that permits copying another result. |
| `TransitionExtra` | TransitionExtra carries only the columns owned by a particular edge; mismatched fields are refused. |
| `ApprovalResolution` | ApprovalResolution records the resolver and policy result in the same transaction as the node edge. |
| `ClaimAward` | ClaimAward identifies an award already derived by the store; callers cannot choose its team or grants. |
| `NodeTransition` | NodeTransition has no caller-selected event kind or clock; the edge determines both. |
| `Applied` | Applied distinguishes an identical replay from a state change or a cooperative cancellation request. |
| `IllegalTransitionReason` | IllegalTransitionReason maps stale authority to lock_held and invalid edges to illegal_transition. |
| `ErrIllegalTransition` | ErrIllegalTransition refuses a failed write loudly; a zero-row mutation is never success. |
| `ErrIllegalTransition.Error` | Error supplies a stable reason without exposing a capability or result payload. |
| `Tenant.TransitionNode` | TransitionNode changes state, attempts, events, expansion barriers, holder release and settlement atomically. |

### pkg/store/run_state.go

| Export | Purpose |
|---|---|
| `RunState` | RunState is both the persisted status and the logical state; terminal states never reopen. |
| `RunCause` | RunCause distinguishes replay-safe lifecycle writes from trigger recovery that keeps the same run alive. |
| `RunEdge` | RunEdge records all legal run-state families; a cancel of live work is initially request-only. |
| `RunTransitions` | RunTransitions keeps publication, owner recovery and cooperative cancellation visible to implementers. |
| `RunTransition` | RunTransition never accepts a client clock or effect payload; those derive from committed state. |
| `FinishRunRequest` | FinishRunRequest binds the terminal verdict to the owner unit that must end in the same transaction. |
| `Tenant.TransitionRun` | TransitionRun applies the run edge, its event and revisioned completion intents together. |
| `Tenant.FinishRunAndUnit` | FinishRunAndUnit prevents a terminal run from leaving an executable owner claim behind. |

### pkg/store/run_spec.go

| Export | Purpose |
|---|---|
| `IntakeRequest` | IntakeRequest carries client input and the authenticated caller reference, never self-certified App facts. |
| `AdmissionResult` | AdmissionResult includes the atomically allocated team number and the first sealed request. |
| `PlanAcceptance` | PlanAcceptance binds the second seal and C3 plan to a live owner, or a live run-bound publisher for publish intake. |
| `AcceptedPlan` | AcceptedPlan is immutable after its first commit; an identical resubmission is replayed. |
| `ExpansionSubmission` | ExpansionSubmission uses owner or eval authority after the source job's claim has ended. |
| `ExpansionFailure` | ExpansionFailure resolves the barrier without inventing generated topology on a generator error. |
| `AcceptedExpansion` | AcceptedExpansion reports persisted topology; deterministic code reconstruction must reproduce each SpecHash. |
| `Tenant.AdmitRun` | AdmitRun is the only intake for local, remote, retry, child and published runs. It derives authority and allowed pools in the transaction; published runs receive no executable owner unit. Retry intake loads Lineage.RetryOf from this Tenant, carries its seals unchanged and records the new ID outside them. |
| `Tenant.AcceptPlan` | AcceptPlan validates the C3 plan, ceilings and declared secrets, then seals spec and inserts nodes atomically. Neither a request field nor an existing seal may change; a differing sealed hash returns spec_drift. Published plans require live publisher authority and create importable jobs without awarding an executable claim. |
| `Tenant.AcceptExpansion` | Atomically accepts exactly one success body or structured failure, rechecking authority and persisting its canonical hash. |

`Store.SetTokenMetered` is removed. Root capability metering uses C2's operator
route. Legacy claim selection shares one private metering query until those
claim APIs are retired.

<!-- design:end -->
