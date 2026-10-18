# Store data persistence

Record writes, reads and memo copies call `blob.RecordNamespace` and
`blob.AuthorizeEdge` directly. The store's rule adapter and permissive test
implementation are removed. Store tests use the shared rules with valid sealed
refs and memo hashes. The tests keep a manifest-reader fake for byte I/O.

`SetResultBlobRecords` supplies verified manifests to tenant record commits,
lookups, memo copies and terminal result attachment. `BlobRecords` uses that
configured reader when its caller omits one. The controller's `WithDataObjects`
installs it before sharing the store. Physical manifest corruption refuses a
required record read and a terminal attachment. Memo and depcache retain their
existing cache-miss rules.

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
amount from the committed checkout. Actionable fraud warnings refuse that
transaction before a grant or event receipt commits. C2's verified-event
implementation and controller adapter remain integration dependencies.

The service-token storage and download counter routes are removed.
`Store.RenewStorage` and its rolling block replay helpers are removed with them.
Final upload tickets retain atomic reserve, verified commit, abort and replay.
Legacy direct uploads and node logs still use the internal raw reservation code.
Their consumer cutovers and old cache RPC client removal remain open.

`SetFreeTeamSlots` persists checked JSON in the final settings table. Negative
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

`SetResultBlobRecords(manifestReader)` configures result attachment before the
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
It then calls `BlobDeleter` outside any transaction and finally deletes the blob
row under the blob lock. A new upload, source upload or record clears a plain
tombstone through `clearBlobTombstoneTx`, which refuses a collecting one with a
conflict, so no reference is published to bytes that may be gone. A deletion
failure or crash leaves the collecting tombstone, and the next leased pass
repeats the idempotent deletion. A collecting digest that has gained a reference
fails the pass loudly.

`RecordSegments` and `ReadLogSegments` accept tagged job or owner-unit subjects.
`Segment.Size` and `Offset` count decoded bytes. `StoredBytes` counts the
compressed object whose hash is `Digest`. Retained storage and billing count
stored bytes once. Cursors continue to count decoded bytes. Both lengths are
immutable on replay, and the proxied reader verifies both before returning bytes.
`LogSegmentObjectKey` is shared with signing so a commit cannot substitute an
object from another stream. Segments are contiguous and immutable. Final commits
seal even empty streams, and only identical final bodies replay. Job tail grace
uses the persisted finish time and original writer. Trigger, plan and eval logs
retain their own live fences. A reader with run-read authority can read historical
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
its exact boundary. Trigger, plan and eval streams close when their persisted
unit fence is gone. Eval also checks the source generation. The bounded batch
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
Legacy direct-upload and node-log reservation writers are removed. The pod log
sink consumes `logseg.Data` through `SubjectUploader`. The old segment-signing
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


`payment_provider_settlement.go` extends verified-event dispatch to card setup,
collection, refunds, warnings and disputes. Its receipt and provider-correlation
schema contract is recorded in `design/rewrite/contracts/c1-data.md`. Tests use
a schema fake until W1 installs those tables. Production callbacks also need
the trusted billing-page setup writer. Failed receipt writes cannot leave
financial mutations behind.

`Operator.RecordAttemptIntent` binds collection payments and their provider
correlations under one ledger lock. The persisted attempt and charge supply the
payer. The trusted worker supplies the customer selected for its provider request,
so replacing the saved card while that call runs cannot change the association.
The configured mode and USD currency complete the binding. Immutable replay
writes nothing. A conflicting association, changed customer or payment, or a
payment already associated with a checkout fails without changing the attempt.
Association failure rolls back both writes. This writer requires the pending
correlation schema and does not fall back to the old unassociated update.

The unused `ErrMeteredInProcessNodes`, `LiveLogOffsetHeader`,
`LiveLogAttemptHeader` and `LogWatchHeader` exports are removed. Tagged log
subjects and the unit-renewal wire own those concerns.

Global payer discovery uses the existing reviewed `paymentWarningTeamTx` owner.
Verified events require its mode-scoped checkout or collection association to
agree with any retained grant or attempt. Collection reads the exact attempt
under the payer derived from its committed correlation. Generated tests replace
the persisted correlation's team or mode and require financial state to stay
unchanged across collection, refund, warning and dispute events.

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
Abandonment creates no receipt or successful-upload usage. Live tickets, receipts
and record graphs still protect a shared digest.

Every non-final metered checkpoint checks headroom after charging. Exhaustion
records the team's existing timestamp. Renewals request `credits_exhausted`
cancellation at `DefaultCreditGraceSeconds`. Its ten-minute value is a placeholder
pending Korey's grace decision. Restored headroom clears the timestamp. M4's
plan/eval execution-start rule remains unchanged.

The schema enforces one billing award per capability and per unit generation.
Card settlement scopes both updates to the attempt's team and requires one row
from each. Every new job attempt clears its job summary and annotations.

Upload tickets retain their run and job identifiers without cascading foreign
keys. They survive run deletion because leased expiry still needs the digest to
collect possible copied bytes and release every quota dimension.

## Lock order

Every store transaction acquires locks in one global order and skips tiers it
does not use. A transaction never takes an earlier-tier lock after a later one,
never reads a row shared and later writes it, and performs no object-store,
GitHub or git I/O while it holds locks. Sets such as ancestry, batches and
migration destinations are discovered without locks, locked complete in this
order, then reread and validated.

1. Global advisories, in fixed order: credit ledger, settings, compute and free
   tier, bootstrap and service enrollment, capacity admission.
2. Singleton lease rows (maintenance, controller owner and reaper).
3. Settings and signup gate rows.
4. Teams, sorted.
5. Accounts, sorted.
6. Memberships.
7. The complete authority set: capabilities, then sessions, deepest first and
   then by kind and key. Authority resolution reads leaf to root, so writers
   lock leaf to root too. `lockAuthorityTx` takes registered claimants and
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
| Launcher preparation | controller lease KS, launcher and award authority, all owners, all runs, all nodes |
| Release and reporting | authority, owner, run, node, attempt and metadata rows |
| GitHub source release | installation, then subscription |
| Blob and source upload | authority, ticket, digest advisory, quota |
| Blob collection | lease KS, digest advisory; deletion runs between two transactions |

Justified exceptions:

- A concurrency-maintenance entry lock followed by an implicit run key-share is
  compatible with renewal's run `NO KEY UPDATE`.
- A team key-share from a foreign key followed by a team update during billing
  is compatible with other key-share holders.
- GitHub source release locks its installation after work rows. No transaction
  holds an installation and then waits on work.
- Work billing still takes the ledger advisory after work locks, opposite team
  and account deletion. Rare admin paths that remain out of order (findings 9-15,
  20, 21 of the 2026-10 lock-order review, and the remaining parts of 22) rely on
  the retry below.

`inTx` reruns its whole function in a fresh transaction when PostgreSQL reports
a deadlock (`40P01`) or serialization failure (`40001`): at most five attempts,
with full-jitter backoff starting at 10 ms and capped at 250 ms. A function
passed to `inTx` resets any state it shares with its caller on each attempt and
performs no external I/O, and callbacks it invokes must tolerate a rerun.
Direct `beginTx` transactions are not retried. Object reads that a transaction
needs run between attempts through `inBlobTx`/`withBlobIO`, which repeats the
transaction with the stored result. The GitHub check reporter holds a session
advisory on a connection with no transaction open.
