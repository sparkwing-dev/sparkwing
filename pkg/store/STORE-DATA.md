# Store data persistence

Financial writes use the same transaction as the state change that caused them.
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
once. A reaper passes the persisted lease lapse. Both call C2's `SettleBilling`
for per-unit minimums and elapsed seconds. Settlement reads the award and ledger,
so deleting a run or revoking its claimant cannot change its payer or rate.
Refunds retain the reservation's card and spend day. Callers keep renewal,
transition and accounting in one transaction. They apply the existing spend
policy to the resulting balance. A pause cannot precede the interval's last
charged checkpoint.

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

The record naming conventions are:

- `ResultBlobKey` names output and manifest records for one destination attempt.
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

`MarkBlobTombstones` and `CollectBlobTombstones` require the maintenance lease.
Records and live receipts root the graph, including transitive manifest members.
Collection waits `limits.BlobGCGrace`, takes the same blob lock as writers,
rechecks reachability and calls `BlobDeleter`. A deletion failure preserves the
row and tombstone. A new upload or reference clears its tombstone.

`RecordSegments` and `ReadLogSegments` accept tagged job or owner-unit subjects.
`LogSegmentObjectKey` is shared with signing so a commit cannot substitute an
object from another stream. Segments are contiguous and immutable. Final commits
seal even empty streams, and only identical final bodies replay. Job tail grace
uses the persisted finish time and original writer. Trigger, plan and eval logs
retain their own live fences. A reader with run-read authority can read historical
segments after the writer ends. Log incompleteness never changes the job result.
