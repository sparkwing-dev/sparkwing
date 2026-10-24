# Store data persistence

## Schema and audit invariants

The baseline uses SQLite STRICT tables with INT, REAL, TEXT and BLOB declarations.
PostgreSQL uses BIGINT, DOUBLE PRECISION, TEXT and BYTEA. Both dialects enforce
claim and terminal tuples, one open attempt per node, nonnegative resource and
counter domains, boolean flags, and nonempty concurrency keys. Installations
are referenced with their tenant. Job receipts reference the exact attempt and
generation. Sessions, memberships and identities reference an existing account.
The schema counterexample suites exercise these rules against either dialect.

Step lifecycle timestamps use the store clock. Runner-reported start and finish
times remain in `source_started_at` and `source_finished_at` for display, with
nanosecond precision. They are observations and do not participate in lifecycle
CHECKs or recovery. A runner clock ahead of the controller cannot block closure.

Secret releases, source credential releases and privileged mutations append to
`audit_events` in the transaction that authorizes them. Audit rows retain plain
team and run identifiers because deleting a run, team or account must preserve
the facts. Audit rows are append-only against application code. Triggers reject
every UPDATE and permit DELETE only for rows older than the operator-set horizon.
PostgreSQL also rejects TRUNCATE. The same role owns the PostgreSQL tables and runs
the application, so a holder of the database credentials can disable these guards.
Separate owner and runtime roles are post-launch hardening. Local SQLite
append-only holds only against SQL the app issues. SQLite audit rows use
STRICT, WITHOUT ROWID so replacement cannot bypass their guarded primary key.
Every run deletion appends a `run.deleted` tombstone with a fresh identity,
including when a later run reuses the ID.

Human actors and account subjects use account IDs. Console authorization uses
the credential class and preserves the verified account subject through the
committing transaction. Non-account claimants are
identified by capability and holder digests, with no caller-selected actor name.
Source credential history retains its owner account ID separately from the actor.
System actors use fixed enums permitted by each payload kind. Invitation
subjects use invitation IDs. One audit writer accepts a closed, typed payload
for each kind. Payloads contain identifiers, enums, digests and generated
receipt IDs. Retained caller names and grant keys use digests. Free-form reasons
and messages are excluded.
Readers render a missing account as "deleted account" without rewriting history.
The account and operational records own personal details and deletion of them.

The operator storage settings API exposes `audit_retention_days`, stored as
`operator.audit_retention_days`. The default is 400 days. A zero or omitted write
restores that default. Positive values set the horizon. The existing leased
controller maintenance pass prunes expired audit rows in bounded batches,
including facts for deleted teams. It uses the same database-clock cutoff as the
SQL guards. The database stamps `recorded_at` at insertion, and that timestamp
controls retention. A caller cannot set it. The separate `ts` occurrence time
remains available for display, so a backdated occurrence cannot shorten retention.
All UPDATEs remain refused, including changes to expired rows.

`team_first_success` records the team's first successful run in the
same transaction as its completion and audit fact. It has no run foreign key,
so run deletion and audit expiry cannot make a later success the first.

`console_refund_receipts` retains each operator refund's request digest and full
original result with the financial state. Replaying a key returns that result,
including its balances and `created` value, after audit retention. A changed
request under the same key conflicts. The separate expiring audit projection
contains identifiers, digests and financial facts, with no free-form reason.

Editors may delete only runs they submitted. Owners may delete any run in their team.

`Tenant.BusinessEvents` reads bounded pages in descending timestamp and ID order.
Its cursor is the last event ID from the preceding page and stays within the
selected team and kind. The operator team view returns 50 events and a
`next_cursor` when older events remain. Passing it as `cursor` continues the view.


Record writes, reads and memo copies call `blob.RecordNamespace` and
`blob.AuthorizeEdge` directly. The store's rule adapter and permissive test
implementation are removed. Store tests use the shared rules with valid sealed
refs and memo hashes. The tests keep a manifest-reader fake for byte I/O.

`WithPolicies` supplies verified manifests through `Policies.ResultBlobs` to tenant record commits,
lookups, memo copies and terminal result attachment. `BlobRecords` uses that
configured reader when its caller omits one. The controller's `WithDataObjects`
installs it before sharing the store. Physical manifest corruption refuses a
required record read and a terminal attachment. Memo and depcache distinguish cache absence from authorization or fence refusal.
A refusal stays an error even when joined with missing or corrupt cache data.
Memo publication stages a producer-owned record. Only successful settlement makes
it reusable in that transaction. Unsuccessful settlement removes staged records.
A rolled-back report cannot publish a reusable result.

`Tenant.StorageStanding` reads policy, retained events, unique blobs, tagged log
bytes and live upload holds in one snapshot. It shares the blob/log inventory
reader with storage billing. Committed and aborted tickets contribute no holds.
Expired holds disappear from the report without a maintenance write. Historical
usage, raw storage counters and other tenants cannot change retained byte totals.
Corrupt inventory or an overflowing total returns an error and no partial report.
The controller's team storage report consumes this snapshot.

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

Download accounting uses `Tenant.ChargeDownload`. The raw Store method is
removed. The request accepts a trusted replica ID and the existing cap policy,
with no team or caller clock. The tenant owns the team and the store owns time.
The shared download quota row serializes admission with reservations. Admission
and additive replica reports commit together. Unexpired holds consume capacity.
Expired holds release before charging. Midnight resets admission but retains
the earlier day's report. The download that crosses a cap finishes, and bytes
already delivered can still be recorded. A zero-byte check records no bytes.
Overflow and write failures roll the entire charge back.

The controller's signed downloads and delivered-byte middleware use the tenant
method and their existing lease-holder identity for replica accounting. Log and
live-log reads stay exempt. The SQLite replica acceptance proof races real
charges through independent clients and checks the exhausted cap after reopening.
The signed-data capability adapter and native PostgreSQL path remain integration
work.

Egress writes use `Tenant.AddUsage` with the authenticated team, trusted replica
identity and UTC day. The controller records actual successful-response bytes
when the request ends, including partial writes and requests whose context was
cancelled. An absent in-memory meter still records tenant usage. Logs consume
egress bytes while remaining exempt from download caps. A failed write is logged
as an error. Process termination before a request ends can lose its egress sample.

`Tenant.EgressUsage` sums replicas for one UTC day or month.
`Operator.EgressUsageAcrossTeams` supplies team aggregates for alarm restoration.
Neither report exposes principal identities. `Operator.PruneEgressUsage` preserves
thirteen months under the existing maintenance lease and store clock. The raw
Store high-water-mark writer, reader, unleased prune and controller timer flush
are removed. Deleting an account leaves tenant egress counters unchanged.

`Store.StorageStandingFor`, `Store.TeamStorage`, `TeamStorageUsage` and
`Store.ReconcileFreeEventBytes` are removed. `free_slots` retains slot ownership
and its timestamp. Its duplicate event-byte column and writer are removed.
Event storage admission reads retained run counters under its existing free-tier
lock. Run deletion releases retained event bytes without a repair pass. The
financial storage usage counters survive deletion and remain separate.
`Store.ReleaseExpiredStorage` and its unleased cross-team scan are removed.
Final upload expiry uses quota admission and leased blob maintenance. Empty
uploads reserve object capacity too, and expired tickets cannot commit.

The raw paid-credit grant path is removed. `Tenant.GrantCredits` is removed.
`Tenant.RecordCreditGrant` accepts free grants only. Payment reversals use verified
provider settlement or the authorized console refund transaction.
Paid checkout credit uses `Store.SettlePayment`, which derives the team and
amount from the committed checkout. Actionable fraud warnings refuse
unprotected payments before a grant commits. A protected same-payment warning
preserves the credit under its hold. The verified event receipt commits with
settlement.

The service-token storage and download counter routes are removed.
`Store.RenewStorage` and its rolling block replay helpers are removed with them.
Final upload tickets retain atomic reserve, verified commit, abort and replay.
Legacy direct uploads and node logs still use the internal raw reservation code.
Their consumer cutovers and old cache RPC client removal remain open.

`Operator.ConfigureDeployment` persists checked JSON in the final settings table. Negative
values and sealed rows refuse updates. The policy survives reopening, and an
unreadable slot source cannot open the controller's signup gate.

Build trust uses `Tenant.TrustLocalBuilds` and `Tenant.SetTrustLocalBuilds`.
Their raw Store methods are removed. Missing policy defaults to false, invalid
flags return an error, and writes cannot change another tenant's opt-in.
Policy survives reopening and cannot change persisted upload provenance.
Execution still needs W7's binary check and W3's adapter.

Financial writes use the same transaction as the state change that caused them.
Unpriced work is refused by the claim transaction without changing the job,
attempts, events or ledger. The unused `Tenant.FailNodeForUnpricedClass` writer,
its unfenced cancellation helper and its separate event kind are removed.
Terminal reports use `Tenant.TransitionNode` and its current claim fence.
Balances use `Tenant.CreditBalanceMicro`. Billing state and grant history use
`Tenant.CreditState` and `Tenant.ListCreditGrants`. Their raw Store twins are removed. Burn windows use
the store clock and include charges at the window's lower boundary.
`Tenant.ListCreditCharges` reads the final `capability_prefix` column into
`CreditCharge.TokenPrefix`. Both history methods return the selected tenant
on every row and preserve ledger metadata after run retention. Equal timestamps
sort by descending row ID. Default and maximum history sizes are unchanged.
`Tenant.CreditUsageByRun` nets reservation, usage and refund rows by run. Storage
and runless charges affect the balance but stay outside this report. Per-run
usage survives operational run retention and reopening.
`Operator.CreditLedgerTotals` reads deployment-wide grants and charges in one
snapshot. Open metered awards exclude their refundable seconds using C2's
calculator and immutable intervals. It uses the same calculator as
unit renewal and settlement. The raw Store method is removed. A report cannot
advance charged checkpoints,
close intervals or write settlement. It counts no storage seconds and consults
no node, trigger or current credential rows. Settled and unmetered awards need
no calculator call. C2's production calculator is the default.
Open intervals stop at `charged_through` in reports. Closed intervals use their
persisted finish time. This keeps a late disruption from refunding time that a
report counted only because the reaper had not run yet.
Grant requests use `Tenant.RecordCreditGrant`. Its raw Store twin is removed.
Deployment price and policy reads use `Operator.CreditSettings`,
`Operator.CreditRateTable` and `Operator.WarmCPUClassCores`.
Billing-policy writes use `Operator.SetCreditSettings`. Once a table is stored,
change prices through `CreditSettingsUpdate.RateTable`. A scalar update is
refused before any sibling settings change. An absent table derives the default
ladder from the scalar rate. The separate `SetOperatorCreditSettings` writer
and the reader override for older binaries are removed.
`Store.GrantCredits`, `Store.ListCreditCharges` and `Store.AppendEventOnce` are
removed too. Each caller must select a Tenant for its grants, charge history
and idempotent events.
The separate `SetCreditRateTable` wrapper is removed.
`recordBillingAwardTx` stores immutable provenance and a reservation charge. The
charge snapshots the CPU class, rate and card fingerprint. Its caller must pass
server-derived authority, pricing and reservation size after taking claim locks.
A capability prefix belongs to one reservation. Identical award replay writes
nothing, and conflicting provenance or pricing fails.

`startBillableIntervalTx` and `finishBillableIntervalTx` delimit compute. An
executing claimant opens at award. A launcher reservation opens when execution
starts. Pauses close the current interval, and resuming uses a fresh interval
key. Customer-runner waits therefore have no interval. Application retries keep
the reservation and use separate interval keys.

`chargeBillingAwardTx` charges new elapsed compute during renewal without
closing the interval. `settleBillingAwardTx` closes open intervals and settles
once. A reaper passes the persisted lease lapse. Metered settlement calls C2's `SettleBilling`
for per-unit minimums and elapsed seconds. Unmetered checkpoint and final
settlement return zero cost without invoking a calculator or writing a charge.
Final settlement still closes the immutable award, and exact replay returns
that zero result after claimant pruning. Settlement reads the award and ledger,
so deleting a run or revoking its claimant cannot change its payer or rate.
Refunds retain the reservation's card and spend day. Callers keep renewal,
transition and accounting in one transaction. They apply the existing spend
policy to the resulting balance. A pause cannot precede the interval's last
charged checkpoint.

The unused expired-claim settlement adapter is removed. Reapers must use the
immutable award and interval helpers in their state transaction. Generated
SQLite tests settle through an earlier persisted lapse after database reopening,
race two settlement connections, and replay the committed result. The original
rate, CPU class, card and refund day survive claimant pruning and pricing/card
changes. C2 calculation is a contract fake in these tests. Claim/start/reaper
wiring and native execution remain integration obligations.

`SettlePayment` resolves the team and credit amount from a committed checkout.
Provider mode, event identity, signed amount, currency and payment identity must
match. The ledger lock serializes payment grants with reservations. A provider
payment cannot fund another checkout, including one belonging to another team.
Checkout creation snapshots the configured payment mode, USD cents and credits.
Until the provider session is attached, the checkout ID occupies the required
session column. Settlement refuses that pending identity. Attachment is immutable,
and abort closes only an unattached checkout. Financial checkout rows survive
later checkout creation and abort. Settlement records each checkout transition
once and clears exhaustion only when the resulting balance is positive. Audit
failure rolls the financial result back.

`ReserveBlobUpload` binds the upload ticket, tagged uploader, size, digest,
provenance, sealed ref and quota reservation in one transaction. The adapter
chooses a ticket ID within its authenticated idempotency scope. That ID is one
ASCII letter/digit/underscore/hyphen path segment. Staging keys are
`teams/<team>/pending/<ticket>`. Immutable keys are `teams/<team>/cas/<digest>`.
Replays must retain every immutable fact, including expiry. A released ticket
cannot reopen. The internal quota reservation ID is `upload:<ticket>`.

`LookupBlobUpload` rechecks the exact uploader and live authority before physical
publication. The backend must verify size and SHA-256 and finish `CommitObject`
before passing server-observed bytes to `CommitBlobUpload`. The commit request's
replica ID comes from controller configuration. It never comes from client JSON.
An existing CAS object alone cannot establish upload authority. Commit checks
the ticket and authority again, then records existence, the exact receipt, ticket
completion, quota settlement and per-replica usage in one transaction. A job
receipt retains team, run, job, attempt, generation and capability. Owner and
publication identities remain tagged.

The owner-receipt SQLite acceptance proof uploads through trigger and plan
awards before jobs exist. It checks refused verification, exact receipt replay,
reopening and refusal to transfer the receipt or ticket to a job in the same run.
Its awards and physical verification are contract fixtures. Eval upload tests
also check the source attempt and unit generation independently, using explicit
fake write authority. Issued eval awards retain their read-only data grant.
Signed transfers and EOF verification remain physical-backend integration work.

Attempt-scoped publication requires an imported terminal attempt with a persisted
finish time. Reservation, lookup, commit, abort, data checks and log commits use
the same authority resolver. An unfinished row cannot authorize those operations,
even if the publisher holds a ticket reserved before that row changed. Run-scoped
publication uploads retain their separate identity.

Only a new blob counts bytes and an object. A readable duplicate reserves zero
additional bytes, so it can be uploaded at the quota ceiling. Unreadable bytes
still require a full reservation, which prevents a quota-based existence oracle.
Commit rechecks whether it inserted new content before consuming the reservation.
Deduplicated and empty uploads still commit their reservations. Exact replay
requires the original receipt and size and remains valid after ticket expiry.
`AbortBlobUpload` releases only an uncommitted reservation. Abort replay and an
abort following commit preserve the receipt. The leased tombstone-mark phase
releases expired ticket reservations, including those orphaned by run deletion.

`BlobRecords(manifestReader)` provides the `blob.RecordStore` contract. The
manifest reader verifies bytes before decoding paths, modes and sizes. SQL stores
the member reachability graph. Every member still needs existence and reference
authority inside the record transaction. The shared blob package owns namespace
encoding and the pure edge rule. The direct `Tenant` record methods use those
same rules and have no manifest reader. Manifest operations through them fail
until the caller uses the configured record store.

Record commits return UTC expiry instants and zero values for unset optional
expiry and manifest fields. Equivalent metadata replays after reopening, including
an expiry expressed in another timezone. Changing the expiry instant still
conflicts with an immutable output or manifest record.

`WithPolicies` configures `Policies.ResultBlobs` during store construction before the
store is shared. The result transaction verifies each output or manifest key
against the current tenant, run, job, attempt and generation. It reads existing
records, verified manifest members, reference authority and existence through
that transaction. An absent, expired, corrupt or mismatched record prevents
attempt closure, terminal state and the result event from committing. The event
retains the verified record keys. Attachment requires `data.write` and rechecks
authority after manifest I/O. It leaves commit and rollback to the state owner.

The record naming conventions are:

- `blob.ResultRecordKey` names output and manifest records for one destination attempt.
  The namespace includes the run, attempt and generation. Its name is `result`.
- Memo records use the repository, verified or manual ref, pipeline, job and hash.
  The name is `output` when the primary digest is the typed output. An optional
  manifest belongs in that same record. A manifest-only memo uses the name
  `manifest` and sets both digest fields to the manifest digest.
- Depcache records use repository, ref and cache key in the scope, with the cache
  name in `RecordKey.Name`. Pipeline and job are empty because this namespace is
  shared within the authorized repository/ref.

Memo reads and writes require memoization in the accepted job declaration.
Record lookup checks authority over the named record before checking its bytes.
Read access to the same digest through another record grants no metadata access.
Memo copies preserve source provenance while assigning operational ownership to
the destination. Both result records commit together. Memo failures become
misses and depcache failures become cold restores for missing or corrupt bytes
and exhausted budgets. Required output reads fail. Default-branch fallback needs
verified source facts and a permitted ref in the capability's data scope. A typed
repository or ref cannot widen that scope.

`ReadableBlobs` and `CheckDataAccess` use the current receipt and read set. An
unrelated job in the same run cannot use another job's receipt. Dependencies,
generated groups and declared pinned pipeline references supply permitted edges.
Remote-cache capabilities expose only verified-default data and exclude binaries
and source bundles.

The owning store transaction also implements `CheckDataAccess`. Its checks see
that transaction's receipt, reference, revocation and holder changes and leave
commit or rollback to the caller. `Tenant` uses the same checks. Supplied
principal fields cannot widen persisted capability authority.

Record publication uses one transaction-local mutation owner for CommitRecord
and CopyMemo. Both prepare every namespace and verified manifest before the
owner takes the sorted blob locks. It checks existence, edge authority and
immutable destinations, then reloads current writer ancestry and holder authority
against the store clock. That final recheck is the mutation boundary. Only that
owner persists the complete record/member batch and clears its tombstones.
Commit or rollback remains with the caller. Failed commits return no record or
memo hit. Immutable replay retains its persisted metadata.

Lookups recheck live authority after verified manifest I/O. A read that crosses
the job lease, leaf capability or ancestor expiry returns no authorized result.
SQLite checks exercise both data binding rules through `Store.CheckBinding`
and through the transaction that owns receipt changes.

`ReadableBlobs` returns each readable digest once in request order, including
large requests. It checks the budget after its final read and returns an empty
presence result with `blob.ErrBudget` when the deadline has passed.

`MarkBlobTombstones` and `CollectBlobTombstones` require the maintenance lease.
Leased record expiry and reachability GC replace `ExpiredOutputRuns`,
`ReleaseRunOutputs` and `ForgetRunOutputs`. The controller's output-key sweep
and its separate `output_runs` and `output_tombstones` bookkeeping are removed.
Copied output records keep their bytes and manifest members after the source
run is deleted. Collection removes the graph after its last record disappears.
Collection checks its persisted holder, generation and expiry before selecting
candidates, including empty passes and passes with only grace-period tombstones.
Each deletion rechecks the fence in its own transaction. A supplied expiry cannot
extend an expired lease or revive a generation replaced by the same holder.
Records and live receipts root the graph, including transitive manifest members.
Collection waits `limits.BlobGCGrace`, takes the same blob lock as writers,
rechecks reachability and sets the tombstone's `collecting_at` before it commits.
Immediately before each physical delete, a fresh transaction rechecks the live
maintenance lease and the same collecting tombstone. It then calls `BlobDeleter`
outside any transaction and finally deletes the blob row under the blob lock,
matching the collection timestamp. A new upload, source upload or record clears
a plain tombstone through `clearBlobTombstoneTx`, which refuses a collecting one with a
conflict. A deletion failure or crash leaves the collecting tombstone, and the
next leased pass repeats the idempotent deletion. These checks narrow the window
for an overlapping collector. S3 uses the observed ETag as `If-Match` on delete.
Recoverable bytes can still be lost in a rare race, including identical S3
bytes with the same ETag. A referenced digest with missing bytes fails loudly.
A collecting digest that has gained a reference fails the pass loudly.

Attempt-start and owner-unit claim transactions create their log streams. Publication
imports create their streams atomically too. `RecordSegments` appends and seals
existing streams, refusing a missing registration. `RecordSegments` and
`ReadLogSegments` accept tagged job or owner-unit subjects.
`Segment.Size` and `Offset` count decoded bytes. `StoredBytes` counts the
compressed object whose hash is `Digest`. Retained storage and billing count
stored bytes once. Cursors continue to count decoded bytes. Both lengths are
immutable on replay, and the proxied reader verifies both before returning bytes.
`LogSegmentObjectKey` is shared with signing so a commit cannot substitute an
object from another stream. Segments are contiguous and immutable. Final commits
seal even empty streams, and only identical final bodies replay. Final-tail grace
uses the persisted finish time and original writer. Completed trigger, plan and
eval units accept only their awarded credential's logs before `LogSealGrace`
ends. The current unit generation and eval source generation must still match.
Credential ancestry stays live, and the allowance cannot renew or execute work.
Maintenance waits for that grace before sealing an unfinished owner stream as
incomplete. A reader with run-read authority can read historical
segments after the writer ends. Log incompleteness never changes the job result.
Segment replay counts retained bytes once. Refused content and byte-limit
violations preserve the committed inventory. Run deletion cascades streams and
segments. A later job finish cannot extend an attempt's persisted tail grace.
Generated tests check reads, segment commits, final seals and leased maintenance
on both sides of that grace boundary. A historical reader keeps the same bytes,
and maintenance preserves the job's successful result and finish time.
The unused raw `Store.RunFinished` pruning query is removed. Leased retention
and tagged-stream maintenance own log lifetime.

`SealAbandonedLogStreams` closes existing execution streams as incomplete under
the maintenance lease. Finished job attempts keep their final tail grace through
its exact boundary. Completed trigger, plan and eval streams keep that final
tail window. Expired or replaced unit fences seal incomplete immediately.
Eval also checks the source generation. The bounded batch
locks runs and rechecks the maintenance fence before writing. Any error rolls
the batch back. Existing final seals remain immutable. Publication streams keep
their publisher's final-commit contract because publication has no lease.

The independent bucket-listing storage pass is removed with its mark/reconcile
APIs, raw download-day pruning, legacy receipt pruning and free-share log row
selection. The controller's maintenance timer uses the existing singleton lease,
final retention, tagged-stream sealing and committed-inventory billing. Physical
bucket bytes cannot overwrite replica counters or become retained billing facts.
Allowance-driven physical eviction still requires the final record/log adapter.

`Store.ReserveStorage`, `Store.CommitStorage`, their exported request/response
models and `DefaultStorageReservationTTL` are removed. Tenant blob tickets own
public reserve, verified commit and abort. The controller's log-retention probe
uses `StorageStanding` and slot availability without mutating storage rows.
Legacy direct-upload and node-log reservation writers are removed. The old segment-signing
client, upload wire types and unused engine-log wrapper are removed. Missing
authenticated bindings report an error while pod stdout remains available.
Production signing, verification and recording adapters remain with the controller,
and the host must supply the persisted subject binding. The remaining legacy scheduler and controller output readers still use the
output-reference model. Native host evaluation reads the committed result selector
through its storage binding.

The raw provider callback methods `Tenant.SaveCard`, `Tenant.HoldForDispute`,
`Operator.PaymentTeam`, `Operator.ReversePayment`, `Operator.RecordPaymentWarning`
and `Operator.ReportCardRefund` are removed. Their private transaction owners
retain the existing setup, refund, warning and dispute rules. Store tests supply
trusted fixture facts through test-only entry points. Those tests do not prove
provider verification. `SettlePayment` is the public verified-event boundary.
Its expanded event contract is implemented against the schema contract below.
Console hold and release operations keep their authenticated tenant handles.

`Store.SetTokenMetered`, `Store.TokenMetered` and `Store.FinalizeNodeCredits`
are removed. Awards and billable intervals own reservation, checkpoint and final
settlement. Nodes and triggers carry no legacy credit-window columns. Node price
and active compute usage project the immutable award ledger. Runner metering
operates on enrolled root capabilities.


Materialized results persist their exact output and manifest keys on the node.
`CopyMaterializedRecords` uses those selectors for retry and resume
sources, or the exact scoped memo record for a memo source. It verifies every
selected object and manifest member before copying. The records retain uploader,
provenance and ref facts while operational ownership moves to the destination run.
Their keys name the destination result sequence. They create no execution attempt.

Copies, selectors, terminal state and the event share the reconciliation transaction.
A missing, expired or corrupt source rolls back to the materialization savepoint
and leaves the job available for execution. Authority refusals remain errors.
The final writer check runs after physical verification and before record mutation.
The configured manifest adapter must also verify selected blobs through
`VerifyBlob`. Missing verification fails closed.

`ExecutionJob` carries the committed selectors through the private controller API.
The native host consumes them for generator evaluation and reconstruction when a
storage binding is present. Trigger and plan owners read only selected terminal
records in their own run. Dependency and pinned-reference readers also require
the exact selector, including results that have no execution attempt. Another
record pointing to the same digest grants no metadata access.

The materialization proof uses the bound data controller for record lookup,
authorization, blob metadata, download charging and filesystem signing. Restoring
its copied output, manifest and independent artifact records the exact signed
byte total. It still uses an in-process controller adapter and transfer transport.
Data authority uses the controller's
default persisted resolver through `Tenant.ResolveBlobAuthority`. This resolver
checks tenant scope, live ancestry and the current uploader in its transaction.
Caller hints can only narrow the result. Failed resolution returns no authority.
The bound reader rechecks authority on each call, including after revocation.

`blob.RefScope.CapabilityScope` supplies the same trust-tagged ref for store and
controller checks. Manual refs compare as `manual:<ref>`. Verified refs compare
as the raw ref. Data bindings compare write grants, read grants and logical
lookup targets through that helper.

Coalesced source copies remain closed until their contract supplies persisted
read-edge authority. A source result and matching spec hash cannot grant that
read. The legacy waiter table cannot supply it because its final schema excludes
the coalesce policy.

The moved store boundary proofs now build their reference graphs through upload
and record commits. The retention proof materializes records through reconciliation,
finishes the destination to create its success pin, and keeps copied provenance
after source deletion. GC uses a controlled store clock across grace and takeover.
The ledger retention proof creates all financial-table rows through their mutation
owners, then verifies them unchanged after run retention and reopening. The signed
refund proof also joins C2 verification to settlement, using the schema fake below.
The unused legacy storage-pass lease API is removed; maintenance uses the elected
Operator pass.


`payment_provider_settlement.go` dispatches verified card setup, refill,
refund, warning and dispute outcomes. Provider correlation and the payment mode
derive the payer. Settlement, ledger mutations and the event receipt commit
in one transaction.

`payment_refunds` retains refund, lost-dispute and warning reversal obligations
by payment mode, kind and provider object. A pending payment has no verified
paid outcome yet, even if a failed outcome closed its reservation. A refused
payment has a verified paid outcome but no grant. Settlement consumes pending
obligations atomically with its grant. Warning obligations use the immutable
protection snapshot. Replays share the same reversal reference and all reversals
are capped by the paid grant. Only refund-kind rows count as money returned by
Stripe when closing the card-refund queue.

Refill reservations persist amount, customer, payment method and receipt email.
Unbound reservations may expire or be explicitly replaced. A bound payment
keeps the team's single refill slot until its payment outcome is known.
`CanConfirmRefill` rechecks current card eligibility, refill authorization,
holds and limits before the worker confirms an unconfirmed intent. The worker
reconciles processing and succeeded intents without confirming again. An
ineligible unconfirmed intent is canceled at Stripe; `RecordRefillCanceled`
retains its binding and closes the reservation only after Stripe reports
cancellation. Unresolved reservations and every confirmed payment count against
refill limits, including refusals and refunds; canceled payments do not.
Resolved outcomes enter their spending windows when recorded, so delayed
settlement cannot age a new charge out of its limits.
`credit_checkouts.refill_blocked` records an active failure independently of
its historical decline text. An accepted card save or successful Add funds
clears that marker in the same transaction. Settings changes leave it blocked.

`credit_checkouts.three_d_secure` stores the authentication result from the
first verified paid outcome, including a refused payment. The separate
`liability_shift` stores Sparkwing's protection decision: only `authenticated`
is protected. Missing, unknown and `attempt_acknowledged` results are
unprotected. Later deliveries cannot change either value. A fraud
warning keeps protected credit under a payment-specific team hold. Without
protection, it reverses the credit and queues a refund. Both paths block the
card deployment-wide. Held credit cannot fund storage growth or retention.

The unused `ErrMeteredInProcessNodes`, `LiveLogOffsetHeader`,
`LiveLogAttemptHeader` and `LogWatchHeader` exports are removed. Tagged log
subjects and the unit-renewal wire own those concerns.

Global payer discovery uses the payment's mode-scoped checkout correlation
and checks it against any retained grant. Conflicting payer or provider facts
refuse settlement without changing financial state.

`Store.CommittedObject`, `Store.NodeOutputObject` and their legacy `Upload` model
are removed because the controller now reads committed blob records. Source-key
validation and the old local output writers remain until their W1/W3 callers
are retired.

`Store.CancelNodeForExhaustedCredits` and `Store.CancelNodeForComputeLimit`
are removed. Terminal failures use `Tenant.TransitionNode` with the live holder,
source attempt and structured result. The transition releases the claim and
capacity, closes compute, settles its immutable award and emits its event in one
transaction. Exact replay adds no charge or event. Transition timestamps come
from the store clock, so a caller cannot supply a stale cancellation timestamp.
This result path does not implement renewal-time exhaustion or grace policy.
The unused raw `Store.AccountEmail` read is also removed.

Blob and source upload reservations take the digest lock and clear its
tombstone. Expiry abandons one ticket per transaction, and source expiry deletes
staged bytes before a per-ticket transaction releases the ticket. Pending tickets
protect that tenant's digest until commit or abandonment. Abort and leased expiry
register possible copied bytes as a tombstoned blob before releasing quota. The
nullable `uploads.abandoned_at` records that handoff once, including tickets whose
expired quota was released by another admission. GC tolerates absent object bytes.
`DeleteExpiredBlobStaging` deletes the staging object of every committed or
abandoned blob ticket once `limits.BlobGCGrace` has passed since its expiry,
which also catches a commit whose immediate staging delete failed. It deletes
outside any transaction, then sets `uploads.staging_deleted_at` in a leased
transaction. A failed delete leaves the column unset for the next pass.
Abandonment creates no receipt or successful-upload usage. Live tickets, receipts
and record graphs still protect a shared digest.

Every metered renewal charges usage and reserves the next slice under the team's
ledger lock. A refusal writes `billing_awards.stop_reason` and
`drain_deadline_at` (one slice out) and cancels the metered job in the same
transaction. A job stop leaves its owner orchestrating independent unmetered
jobs. An owner-unit stop cancels the whole run. Placing a dispute, warning or
operator hold, or revoking standing,
also writes the stop and cancellation for active metered awards before the hold
commits. Releasing a hold never clears that stop. These billing mutations use
the same whole-transaction retry as other admin/work lock-order exceptions.
`leaseBoundTx` in `lease_bound.go` is
the only lease bound: every node, owner, eval and launcher-preparation lease is
the earliest of the default lease, the capability expiry, revocation and binding deadline,
each metered award's funded horizon and its durable drain deadline. A stopped,
unstarted award retains its drain after refunding the reservation. A refunded
award without a stop cannot renew. Launcher preparation returns the committed
expiry to its keeper, including capability bounds shorter than a slice.
An owner renewing active self-claimed jobs uses those jobs' authority bounds
and funded horizons. A stopped child retains its own bounded lease and no
longer constrains the owner's renewal or cancellation.
Stopping a metered trigger or plan writes whole-run cancellation in the same
transaction because its orchestrator has stopped. The reaper drains that run
and settles its awards without taking ownership of its graph. Stopped units
cannot requeue after funding returns or a hold is released.

The schema enforces one billing award per capability and per unit generation.
Refill settlement scopes its checkout and grant to the same team. Every new job attempt clears its job summary and annotations.

Upload tickets retain their run and job identifiers without cascading foreign
keys. They survive run deletion because leased expiry still needs the digest to
collect possible copied bytes and release every quota dimension.

## Run-anywhere claim ranking

`SelectDispatch` seals controller dispatch for an executing run with no runner
selection or `pool=` requirement, on every controller. A
unit is ranked when its run is run anywhere and the unit's own requirements pin
no `pool=`, `name=` or `class=` term. Tiers come from the authenticated chain:
here (the personal machine sealed as `OriginMachine` from a verified proof),
personal, github-actions, self-hosted (including local queues) or cloud. An
empty ranking means first come, and an unset ranking means the default order.

`claimSQL` selects and rechecks every claimant through one SQL predicate: readiness,
grants, binding, repository scope, trust, selectors, reservation, exclusions,
placement and funding. Teams take turns by their last committed award ordinal;
arrival and stable unit identity break ties. Failed awards advance no ordinal.
There is no launcher scan frontier, demand hash or probing savepoint. Repository
routing identities are normalized by the source URL owner when intake writes the run.

`claim_contexts` groups work by immutable claim policy. Its hash covers the
request and provenance, resolved options and guards, job execution settings,
repository access, allowed pools and creator. It omits seal timestamps and
unrelated jobs. Intake, plan acceptance and expansion insertion write context
references with their source rows. The context table stores only identity,
requirement set and unit kind; run policy remains on the run.

Label lookups select matching requirement sets. Each context then seeks one
ready head per unit kind through an index on team, requirement set, context,
arrival and identity. A batched proof uses the same eligibility and ranking
predicate to identify contexts held for a better shared claimant. Those routes
seek past the ranking window through the arrival index. Bound, personal and
Actions claimants cannot supply this shared proof because their eligibility can
depend on a particular run. Every selected head still passes the full predicate
and atomic award recheck. Context and requirement references prevent pruning
policy used by live work. Retention removes unused contexts before requirement
sets; tenant purge uses the same dependency order.

Inside the 5-second `limits.RankingWindow`, placement holds reserve unnamed work
for the best-ranked tier with a live claimant on the same team, matching labels
and a free slot in its latest poll. A busy or offline better tier causes no wait.
After the window, any eligible claimant may take it. SQL applies holds before
`LIMIT 1`, so a held prefix cannot hide eligible work. Explicit placement, burst
and `Prefers` holds remain. There are no ask rows, ask retention or post-window
grace period. Preference scoring is removed; fair share and arrival order select
between eligible jobs. Polls report free slots; an omitted count offers one slot,
and an explicit zero means busy.
`explainUnitEligibilityTx` supplies diagnostics for notices; it never selects
or awards work.

A personal machine takes only its owner's manual runs. `automatedLineageTx`
follows a retry or child run back to its original request and refuses any whose
origin is an App delivery or a schedule.

## Database observations

Completion readers capture one database time before opening their result cursor.
That time selects due rows and supplies every row's `ObservedAt`. The reader
closes its cursor before committing and issues no clock query while it is open.
Runner inventory and placement also capture their validation time before
opening the observation cursor. Future observations remain invalid, and stale
polls cannot hold placement for a preferred runner.

## Lock order

Every store transaction acquires locks in one global order and skips tiers it
does not use. A transaction never takes an earlier-tier lock after a later one,
never reads a row shared and later writes it, and performs no object-store,
GitHub or git I/O while it holds locks. Sets such as ancestry, batches and
migration destinations are discovered without locks, locked complete in this
order, then reread and validated.

`lock_order.go` defines the canonical tiers and comparator. Authority and unit
batch locking use it. The acquisition-sequence test checks actual SQL against
that order with shuffled authority and unit inputs.

1. Global advisories, in fixed order: credit ledger, settings, compute and free
   tier, bootstrap and service enrollment, capacity admission.
2. Singleton lease rows (maintenance, controller owner and reaper).
3. Settings and signup gate rows.
4. Teams, sorted.
5. Accounts, sorted.
6. Memberships.
7. The complete authority set, deepest first, then by kind and key at equal
   depth. A delegated session precedes its capability parent. Authority
   resolution reads leaf to root, so writers lock leaf to root too.
   `lockAuthorityTx` takes registered claimants and
   observation targets `FOR NO KEY UPDATE` on first access and the rest shared.
8. GitHub installations.
9. Subscriptions, automation, bindings, pool rules, secrets and credentials.
10. Schedules, then the intake advisory.
11. Trigger and eval owner rows (triggers, then evals).
12. All runs, sorted, `FOR NO KEY UPDATE` on first access.
13. All nodes, sorted. `lockUnitsTx` takes 11 to 13 for every award load.
14. Concurrency entries sorted by `(team,key)`, then holders, then waiters.
15. Upload and source-upload tickets.
16. Blob digest advisories, sorted and deduplicated.
17. Quota dimensions: monthly bytes, run bytes, run objects; admission before
    reservation within each dimension.
18. Billing awards, intervals, payment rows and storage counters.
19. Records, manifests, logs, events, effects and invitations.

Hot transaction families:

| Family | Order |
|---|---|
| Claim, self-claim, reconcile | controller lease KS, authority set, owner, run, node, billing |
| Renewal | controller lease KS, authority set, owner, run, node, entries, billing |
| Exchange | bootstrap authority at write strength, owner, run, node |
| Launcher preparation | controller lease KS, launcher, bootstrap and current unit authority, all owners, all runs, all nodes |
| Release and reporting | authority, owner, run, node, attempt and metadata rows |
| GitHub source release | installation, then subscription |
| Blob and source upload | authority, ticket, digest advisory, quota |
| Blob collection | lease KS, digest advisory; deletion runs between transactions |

Launcher preparation and rejection discover bootstrap and current unit
capability prefixes before authority locking. After locking the units, they
reread that set. A changed set rolls back the transaction and restarts discovery.

Justified exceptions:

- A concurrency-maintenance entry lock followed by an implicit run key-share is
  compatible with renewal's run `NO KEY UPDATE`.
- A team key-share from a foreign key followed by a team update during billing
  is compatible with other key-share holders.
- GitHub source release locks its installation after work rows. No transaction
  holds an installation and then waits on work.
- Work billing takes the ledger advisory after work locks, opposite team and
  account deletion. Tenant and operator revocation, membership changes, team
  deletion requests, account deletion, purge finalization, leased retention,
  sign-in and console mutations run their complete transaction bodies through
  `inTx` because their admin lock order can conflict with live work.

`inTx` reruns its whole function in a fresh transaction when PostgreSQL reports
a deadlock (`40P01`) or serialization failure (`40001`): at most five attempts,
with full-jitter backoff starting at 10 ms and capped at 250 ms. A function
passed to `inTx` resets any state it shares with its caller on each attempt and
performs no external I/O, and callbacks it invokes must tolerate a rerun.
These admin bodies perform SQL mutations only. Each attempt rebuilds its result,
including returned revocations, deletion counts and console approval batches.
Console callbacks recheck live operator policy on each attempt. Transaction-local
secret cleanup runs after commit. Sign-in retains its separate retry for uniqueness,
slug and changed-identity conflicts. The controller acquires a fresh intake's
run-cap token after rolling back its discovery transaction, then re-enters with
a request-scoped grant. Conflict retries reuse that grant. Idempotent replays
return before policy resolution and spend no token.

Direct `beginTx` transactions are not retried. Object reads that a transaction
needs run between attempts through `inBlobTx`/`withBlobIO`, which repeats the
transaction with the stored result. Each publication pass batches every manifest
in its snapshot. Fallback readers also batch pending manifests across their
authorized candidates. They wait for earlier candidates before selecting a hit.
At most four transaction passes run. A stable fallback graph needs one fetch
round and one result-consuming pass, regardless of candidate count. Missing or
corrupt memo manifests return a miss after those results are consumed. The GitHub check
reporter holds a session advisory with no transaction open in a separate pool
limited to two connections. Its metadata and callbacks use the operational pool,
including when that pool permits only one connection.

Credential rotation returns counts and skipped secret identities as one
`CredentialRotation` result from the committed attempt. Its secret transform
returns an explicit skip flag. Skips leave the stored envelope unchanged. Each
retry resets all response metadata, and a failed transaction returns none.

Team deletion stores and reads request, completion, recheck and lease times in
Unix nanoseconds. Lease expiry and recheck predicates preserve subsecond
boundaries. Purge's account timestamp uses the same unit.

## Durable admission

Schema baseline 17 adds `admission_work`, keyed by tenant, run and job. The store
commits complete logical group grants and one durable arrival per level. Plan
admission commits with release permission. Job admission commits an award-bound
permit enforced by starts and process delegation, including groupless jobs.
Reservations retain their identity through owner handoff. Queue precedence,
grant and renewal use claim-or-owner liveness. Expired reservations do not revive.
Job and run terminal transitions release their records. Released work settles
while plan reacquisition withholds only new releases.

Approval predicates and generated expansions use store-owned evaluation release
permission. Approval callbacks hold no groups or compute. Controller-owned
approvals use source-fenced evaluation units. Typed outcomes settle the source
and evaluation through the existing state machine and derived verdict owner.
