# W1b store-data review fixes

Scope: the assigned Opus findings B1, M1, M2, the billing part of M5, award
uniqueness, card settlement, attempt metadata, and the GC test. No Git writes.
M4 is unchanged. M3 and M6 remain W1-owned.

## Findings and invariants

- B1 complete. Every non-final metered checkpoint checks current headroom.
  An exhausted metered unit's renewal requests cancellation at the grace
  boundary. Real renewal tests cover jobs, plans, evals and hosts, restoration,
  and a grant that leaves a spend cap binding. They failed at the deadline before
  the fix. `DefaultCreditGraceSeconds = 10 * 60` is the single named placeholder.
  Korey still owns the final grace decision. Saved legacy grace settings do not
  override this placeholder.
- M1 complete. A pending upload protects its tenant's digest while object copying
  can precede the receipt commit. Reservation takes the digest lock and clears
  its tombstone. Live tickets root the reachability graph. The initial test
  failed on the uncleared tombstone. Shared-ticket generated cases also pass.
- M2 complete. Failed upload commits leave possible copied bytes discoverable
  by leased GC. Abort and expiry register a tombstoned blob before releasing
  quota. Tests first reproduced leaks after abort, expiry, prior admission
  releasing quota, and run deletion. Upload metadata survives run deletion so
  maintenance retains the digest. Repeated cleanup never recreates collected
  bytes. Conservative abandoned rows count as retained bytes but create neither
  receipt nor successful-upload usage.
- M5 billing complete. The award ledger is the sole owner of compute billing.
  Deleted FinalizeNodeCredits, legacy trigger/node reservation and settlement
  helpers, token metering reads, eight credit-window columns and two indexes.
  Schema tests first exposed the old columns and zero active-award usage.
  Node price and compute usage now project awards and reservation charges.
  W1 still owns the separate tokens.go APIs named in M5.
- Award uniqueness complete. A capability and a unit generation each identify
  at most one award. Both duplicate inserts succeeded before the unique indexes.
- Card settlement complete. A payment changes only its attempt team's charge,
  and success requires one changed attempt and charge. Foreign-team and ignored
  update tests failed before the fix. A generated foreign-debt property checks
  exact refusal and unchanged financial state.
- Attempt metadata complete. Each new attempt starts with empty job summary and
  annotations. The real retry test exposed carried metadata and now passes twice
  under the same claim. Reset occurs at attempt allocation, including resumed
  starts.
- GC test complete. Reachability and object metadata stay within one team.
  The generated root/member matrix varies protected teams and per-team sizes.
  Removing the recursive team join makes all six cases fail. The correct query
  is restored.

## Verification

The full store command is `go test -p 4 -json ./pkg/store/...` with GOWORK=off,
GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache and GOTMPDIR under
/tmp/sw-astra-W1b. Top-level failing test count fell from **15 to 4**. Failure
records including subtests fell from 19 to 8. No new failure remains. The four
inherited failures are:

- TestCollectionBindingCannotChangePayerProperty
- TestTenantWriters_EveryTenantTableHasAWriterThatSetsTheKey
- TestAWarnedCardRepaysTheDebtOnPayNow
- FuzzStorageStandingTenantInventory

The final broad store run took 230.96 seconds. Focused follow-ups cover the last
constant update, schema checks, generated card cases and dialect contention.
SQLite contention passes. PostgreSQL skips because SPARKWING_TEST_PG_URL is
unset. No database concurrency claim is made from SQLite evidence.

Repository-wide build and vet pass. Commentcheck passes. Focused controller
native-data and filesystem/result tests pass. The broader controller credit
selection retains five legacy fixture failures inserting into the deleted
tokens table. No controller production file changed. No new acceptance cell is
claimed. The pricing/refund behavior ports are recorded in t1-port-inventory.

## Contract changes and shared files

C1-local schema changes add nullable uploads.abandoned_at and remove upload
run/job cascading foreign keys. The marker makes abandonment idempotent because
an expiry sweep otherwise recreates blobs after collection. Retaining the ticket
preserves cleanup facts after run deletion. Removing those changes reopens M2.
No foreign-area interface change is needed.

Files outside pkg/store: `.apidiff/pkg_store.txt`,
`design/rewrite/contracts/c1-data.md` and
`design/rewrite/contracts/t1-port-inventory.md` only.

Shared W1 implementation files touched within pkg/store: schema.sql, store.go,
compute_limits.go, node_state.go, unit_billing.go, state_renewal.go and
unit_renewal.go. Shared schema/scope/standing/publication/crash fixtures were
updated for the enforced identities and conservative orphan accounting.
Lead-owned handoffs.md and amendments files are unchanged.

## Volume and limits

Session production Go delta: **114 added, 742 deleted, net -628 lines**.
The combined store delta against main is **-3,822**, which is **428 lines over**
the -4,250 area target using the lead's exclusions. This session removes 628
lines. Completing the whole-area budget needs further W1-owned legacy cuts.
The 114 added lines supply the required transactional cleanup, cancellation and
scoped settlement checks. There is no area-budget waiver or launch-readiness
claim in this scoped completion.

The final grace duration and treatment of the legacy writable grace setting
still need Korey's decision. PostgreSQL execution needs a database URL. Scratch
under /tmp/sw-astra-W1b is removed after checks; the shared cache is retained.

STATUS: COMPLETE
