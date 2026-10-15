# W1b handoff

Continues `9c5e341a8` after integration merge `5171ae77c`. W1b remains incomplete.
This continuation is uncommitted.
The lead owns review, commits, pipelines and native integration.

## Current continuation

Closed record metadata replay under A1-29/A1-35. The invariant is that equivalent
record metadata returns the same committed record across replay and reopening.
Record commits normalize expiry instants to UTC and clear values in unset optional
expiry and manifest fields before validation and persistence. The existing
immutable-record comparison then uses the same representation as the database.
A changed expiry still conflicts. No helper, API, schema or setting is added.

The minimal regression reproduced `job output record is immutable` when an output
commit replayed its expiry instant in another timezone. The property also
reproduced unset expiry values surviving the first response. It generates 48
cases each for output and manifest records, covering timezone offsets, nanosecond
precision, optional presence, ignored values, reopening and changed-expiry refusal.
After refusal, lookup must return the original record and the database must hold
one record. Namespace and edge rules are explicit contract fakes because W7's
implementations remain stubs. The tests use real SQLite persistence and no sleeps.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE, GOTMPDIR and TMPDIR are beneath /tmp/sw-astra-W1b/. Go commands use
`go -C /home/bombuntu/.xwing/envs/1088/sparkwing`.

- Final whole-repository `go build -p 4 ./...` passed.
- `go vet -p 4 ./pkg/store/... ./internal/acceptance/...
  ./tests/acceptance/...` passed.
- The expanded store selection from the prior continuation passed **197
  top-level tests / 487 cases**, without skips. Both new tests are selected by
  TestBlob. It is the Manual selection below plus TestGrantCredits,
  TestCreditGrant, TestCreditReversal, TestCreditHistory, TestCreditsBalanceIs,
  TestCreditsDefaultTenant, TestTenantSQLScope, TestTenantWriters and
  TestTenantTwins.
- `go test -json -p 4 ./internal/acceptance/... ./tests/acceptance/...
  -run '^(TestStorage|TestBilling)$' -count=1` passed **2 top-level tests /
  43 cases**, including all **12 existing SQLite data/billing proofs**.
  The selected native/platform skeleton retains **562 skips**. No new native
  cell or archived T1 inventory port is claimed complete.
- Required `commentcheck -base main` passed across 905 files. Session
  `sleepcheck -base HEAD`, focused embedded/changelog documentation tests,
  `sync-docs --check` and whitespace checks passed. The public API is unchanged.

Legacy deletion is **0 lines**. No W1 lifecycle file or other-package production
caller changed. Shared files are design/rewrite/contracts/c1-data.md,
CHANGELOG.md and generated pkg/docs/changelog.md. Owned files are
blob_record_runtime.go, new blob_record_replay_test.go, STORE-DATA.md and this
handoff. C1 documents an implementation correction without changing contract
shape. No foreign contract change or new human decision is introduced.

Amendment audit retains the previous boundaries. A1-24 has the transactional
data checker. A1-27 has immutable billing helpers but needs W1's lifecycle joins
and the existing unstarted at_award refund and explicit customer-wait contract
decisions. C2 SettleBilling, VerifiedPaymentEvent.Event and payment verification
remain stubs. A1-28 has committed-checkout persistence but still needs W2's
verified-provider route cutover. A1-29 through A1-35 have the owned persistence
implementation and this replay correction. W7 namespace/edge policy, verified
manifests, physical transfer/deletion, W2/W9 data/log transports, trusted replica
wiring and proxy egress remain real integrations. Result attachment needs its
verified manifest reader. Never-executed materialization destination identities
and published owner-unit log identities still need agreement. A1-53 still needs
native evidence. handoffs.md has no new W1b assignment.

Old claim-credit, paid-grant, raw-storage, output, upload and log paths retain
other owners' callers. Their retirement and the cross-owner contracts above
prevent area completion. The full legacy suite, native placements, PostgreSQL,
providers, physical backends, binary execution and race checks were not run.
Pipelines and independent review remain with the lead under the swarm brief.

Friction was cold compilation and an initial generated result-key fixture that
violated the exact destination contract. The fixture was corrected before the
timezone regression ran red. No Sparkwing binary, service, provider, deployment,
release or Git write ran. Scratch is deleted after final inspection.

## Previous continuation (committed in 9c5e341a8)

Closed the collection fence gap in A1-35. The invariant is that every
CollectBlobTombstones call requires the current persisted maintenance fence,
including an empty pass or an inventory whose tombstones are still in grace.
The candidate scan now checks that fence inside its transaction. Each physical
deletion retains its existing transaction and fence check. This uses the existing
transaction and singleton helpers. No API, schema, setting or mechanism is added.

The regression first failed for both empty and grace-period inventories. It
also reproduced success at the exact persisted expiry. The property generates
96 forged fences for each of empty, grace-period and ready inventories. A second
test checks exact expiry, a forged future ExpiresAt, same-holder takeover, stale
generation refusal and the replacement's successful empty pass. These tests
exercise Operator.CollectBlobTombstones through its public interface and use a
fake physical deleter. No test sleeps or starts a service.

Extended storage-reachability-and-retention-tails/store-data-sqlite to refuse
wrong holder, generation, worker and missing fences before marking and after
reopening. Its final empty inventory now checks persisted expiry, same-holder
takeover and the replacement fence. The existing graph, retention, grace and
physical-deletion assertions still run. No native cell is newly activated.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE, GOTMPDIR and TMPDIR are beneath /tmp/sw-astra-W1b/. Go commands use
`go -C /home/bombuntu/.xwing/envs/1088/sparkwing`.

- Final whole-repository `go build -p 4 ./...` passed.
- `go vet -p 4 ./pkg/store/... ./internal/acceptance/...
  ./tests/acceptance/...` passed.
- The expanded selection under Manual, including the additional grant,
  reversal, history and tenant guard names listed in the prior continuation,
  passed **195 top-level tests / 483 cases**, without skips.
- `go test -json -p 4 ./internal/acceptance/... ./tests/acceptance/...
  -run '^(TestStorage|TestBilling)$' -count=1` passed **2 top-level tests /
  43 cases**, including all **12 SQLite data/billing proofs**. The selected
  native/platform skeleton retains **562 skips**. Other acceptance families
  were outside this continuation's verification scope.
- `commentcheck -base main` passed across 902 files. Session sleepcheck,
  sync-docs --check and whitespace checks passed. No public API snapshot changed.
- The broader documentation selection failed TestDocsNameOnlySymbolsThatExist
  on 17 removed SDK symbols, including Await, CacheKey and Memoize. The SDK and
  every named source document are identical to HEAD. W9 owns that inherited
  documentation cutover. Changelog/mirror, privacy and migration checks are
  verified separately without the unrelated symbol-reference test.

Legacy deletion is **0 lines**. The replaced unguarded query is not counted as
retired legacy implementation. No archived T1 inventory row is newly ported.
No W1 lifecycle file or other-package production caller was changed.

A1-24 and A1-27 through A1-35 retain their prior implementation and integration
boundaries. This continuation closes collection entry fencing under A1-35.
A1-53 still needs native evidence. handoffs.md adds no W1b assignment. C1 now
documents the existing maintenance-fence requirement and its expanded proof.
No foreign contract shape was changed.

Real integrations still open: W1's issuance, start, wait, renewal, disruption
and finish transactions must join the immutable billing owner. Its current
persistBillingAwardTx still writes unmetered awards directly. C2 SettleBilling,
VerifiedPaymentEvent.Event and payment authentication remain panic stubs.
The unstarted at_award refund operation and explicit customer-runner wait input
still need an agreed cross-owner contract. W7's namespace, edge-policy,
physical-byte verification/deletion and binary-execution implementations remain
dependencies. Result attachment needs the verified manifest reader.
Never-executed materialization destinations and published owner-log identities
still need agreement. W2/W7/W9 must finish data and log transports, trusted
replica wiring and native acceptance. Legacy claim-credit, paid-grant,
raw-storage, output, direct-upload and log paths retain those owners' callers.
No new human decision or cross-area contract request is introduced here.

Shared edits are internal/acceptance/store_reachability.go, the new
internal/acceptance/store_gc_fence.go, design/rewrite/contracts/c1-data.md,
CHANGELOG.md and its generated pkg/docs/changelog.md mirror. Owned edits are
blob_gc.go, new blob_gc_fence_test.go, STORE-DATA.md and this handoff.

Friction was cold compilation and starting a build while creating the acceptance
helper. That initial build missed its new symbols. The final build ran after
source edits stopped and passed. The broader docs check found inherited W9
SDK-reference drift. The unsupported standards-match --type flag
was replaced by matching the actual diff. Bootstrap ran installed sparkwing
info before the swarm prohibition was read. No subsequent Sparkwing binary,
candidate, provider or pipeline ran. No Git writes occurred. HEAD and default
Git identity are unchanged. Scratch is deleted after final inspection.

## Previous continuation (committed in 7d6ce07bf)

Moved local binary trust policy to owned build_trust.go. The only public methods
are now Tenant.TrustLocalBuilds and Tenant.SetTrustLocalBuilds. Removed both raw
Store APIs, including their 26-line definitions and separating blank lines.
Their behavior lives on Tenant, with no forwarding API. The controller uses
its existing membership-selected tenant. Legacy direct-object readers obtain
their tenant before reading policy. An absent policy remains false. Invalid
stored flags and database errors return false with an error.

The invariant is that a build-trust read or write affects only the selected
Tenant, and policy changes cannot relabel persisted upload provenance.
The first public-API test failed to compile because Tenant had neither method.
It now covers defaults, repeated opt-in/out and the neighboring tenant.
A 64-case property covers 128 generated tenant keys with Unicode and SQL
metacharacters, independent policies, updates and reopening. A failed-write
case preserves default refusal. Invalid numeric and text flags fail loudly.
The text-flag case first reproduced boolean conversion accepting a corrupt
"true" value. The reader now accepts only the stored integer flags 0 and 1.
The generated fixture normalizes team keys before insertion, matching ForTeam.

Extended storage-owner-receipts-and-stream-verification/store-data-sqlite.
For both trigger and plan owners, its non-cloud upload starts local, the team
opts into local binary trust, and reopening preserves both that policy and the
unchanged local upload provenance. The proof still uses preseeded awards and
physical verification evidence. It does not run a binary or claim that W7's
binary execution check has landed. No additional native cell or archived T1
inventory port is claimed complete. No legacy test was deleted.

Verification commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
-p 4 and GOCACHE/GOTMPDIR/TMPDIR beneath /tmp/sw-astra-W1b/. Go commands use
`go -C /home/bombuntu/.xwing/envs/1088/sparkwing`.

- Final whole-repository `go build -p 4 ./...` passed.
- `go vet -p 4 ./pkg/store/... ./pkg/controller/...
  ./internal/acceptance/... ./tests/acceptance/...` passed.
- The expanded store selection under Manual below passed **193 top-level tests /
  478 cases**, with no skips. It also selects TestGrantCredits, TestCreditGrant,
  TestCreditReversal, TestCreditHistory, TestCreditsBalanceIs,
  TestCreditsDefaultTenant, TestTenantSQLScope, TestTenantWriters and
  TestTenantTwins. TestStoreData selects the three new tests.
- Both acceptance trees passed **20 top-level tests / 486 cases**. All **12
  SQLite data/billing proofs** passed. There are **2,992 skipped cases** in the
  platform/native skeleton. No native cell is newly activated.
- Required `commentcheck -base main` passed across 900 files. Session sleepcheck,
  focused documentation/changelog tests, sync-docs --check and whitespace checks
  passed.

The complete legacy store suite, native controller runtime suite, PostgreSQL,
providers, physical backends and binary execution were not exercised. Existing
legacy fixtures are caller-migrated and compile. They are not final-schema
execution evidence. No additional archived T1 test is claimed complete.

Amendment audit:

- A1-24 retains the merged transaction data checker and binding integration.
- A1-27 has immutable award/interval/settlement helpers. W1 must connect its
  lifecycle transactions. The unstarted at_award refund operation and explicit
  customer-runner wait operation still need agreement. C2 SettleBilling remains
  a panic stub. This continuation does not change those foreign contracts.
- A1-28 has committed-checkout settlement. W2 still owns provider verification,
  route cutover and retirement of caller-selected paid grant authority.
- A1-29 through A1-35 have upload/receipt/member/record/reference/tombstone/log
  persistence. Build trust is now Tenant-owned. W2/W7 transport, policy,
  physical verification/deletion and proxy egress remain real integrations.
  Result attachment still needs its manifest reader. Materialization needs a
  destination identity for a terminal job that never executed. Published owner
  log identities still need agreement. W7 CheckBinaryExecution and W3's binary
  execution adapter must consume persisted provenance, digest and tenant trust.
- A1-53 retains the SQLite proofs. Native acceptance remains open.
- handoffs.md has no new W1b assignment. W1's caller-migration clarification is
  respected. No W1 lifecycle file was edited and no removed API was restored.

C1 records the local API ownership correction. No foreign contract shape,
schema, rate, setting, limit, environment variable or runtime service changed.
No human decision is requested. The existing refund/wait/publication identity
questions and real-integration boundaries remain open.

Shared production caller: pkg/controller/build_trust.go. Shared test callers:
pkg/controller/direct_upload_test.go and
data_download_claim_provenance_test.go. The only changes in those files select
the tenant API. Other shared edits are
internal/acceptance/store_owner_receipts.go, design/rewrite/contracts/c1-data.md,
.apidiff/pkg_store.txt, CHANGELOG.md, docs/migrations/_unreleased.md and generated
mirrors. Owned changes are build_trust.go and its tests, direct_upload.go and
its single test caller, STORE-DATA.md and this handoff.

Friction was cold compilation and the generated team's normalization mismatch.
No Sparkwing binary, service, provider, Git write, deployment or release ran.
Git identity is unchanged. Scratch is deleted after final inspection.
The lead must run native HTTP/provider/backend/PostgreSQL evidence and retire
legacy consumers with their owners. Aggregate API snapshot drift remains the
lead's integration task. Only the two removed and two added policy signatures
were changed in this continuation's snapshot.

## Previous continuation (committed in 189094152)

Moved `Store.CreditLedgerTotals` into owned credit_totals.go and replaced its
legacy node/trigger refund queries with immutable billing awards and intervals.
The invariant is that deployment totals use durable financial facts, exclude
unconsumed reservations, and never change billing while being read. Open
intervals stop at their persisted charged checkpoint. Closed intervals use their
finish time. This prevents a delayed disruption settlement from refunding time
that a report counted only because the reaper had not run yet. Compute beyond
already charged seconds waits for a billing checkpoint. Storage seconds remain
outside compute totals. Errors return no partial financial report.

The old public implementation was probed through a compiler overlay. An
unstarted final-schema award reserved 60 seconds and the old report incorrectly
counted all 60 as settled. The replacement reports zero while preserving the
600-micro reservation and 999400 balance. The schema still has the legacy
operational billing columns. Their presence was verified after correcting an
initial mistaken assumption that they had been deleted.

The report uses the existing billingCalculator contract seam. The interval
reader and settlement-result validator are shared with the accounting writer.
The reader does not advance charged_through or close an interval. Six new tests
cover final-ledger sums, unstarted reservations, confirmed renewal/disruption
progress, calculator failures, and unmetered awards. A 64-case property covers
128 generated tenant keys, all four unit kinds, both start policies, colliding
run/job/capability/reservation names, customer wait gaps, and reopening. It
checks the complete totals and unchanged interval checkpoints and finish times.
C2's calculator is a contract fake for open metered awards. The public report
needs no calculator for settled or unmetered awards.

Removed unused `Store.RunFinished`: **13 production Go lines**, including its
comments and separating blank line. It had no caller. No other-package caller
changed. The old totals refund queries are replaced; moved code is not counted
as deleted. No additional archived T1 inventory row is claimed complete. W1's
mixed legacy queue/claim tests remain for W1's caller migration.

Extended `billing-ledger-survives-retention/store-data-sqlite` to compare exact
deployment totals before leased retention and after reopening. Both teams count
once. Storage affects balance but contributes no compute seconds. All 12 existing
SQLite data/billing cells remain active. No native cell is newly activated.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
and GOCACHE/GOTMPDIR/TMPDIR beneath /tmp/sw-astra-W1b/. Go commands use
`go -C /home/bombuntu/.xwing/envs/1088/sparkwing`.

- Whole-repository `go build -p 4 ./...` passed on the final production tree.
- Scoped `go vet -p 4 ./pkg/store/... ./pkg/controller/...
  ./internal/acceptance/... ./tests/acceptance/...` passed.
- The expanded store selection passed **190 top-level tests / 475 cases**,
  without skips. This is the Manual selection below plus TestGrantCredits,
  TestCreditGrant, TestCreditReversal, TestCreditHistory, TestCreditsBalanceIs,
  TestCreditsDefaultTenant, TestTenantSQLScope, TestTenantWriters and
  TestTenantTwins. The six new tests are selected by TestStoreData.
- Both acceptance trees passed **20 top-level tests / 486 cases**. All **12
  SQLite data/billing cells** pass. The native/platform skeleton retains
  **2,991 skips**. No new native cell is activated.
- Required `commentcheck -base main` passed across 896 files. Session
  sleepcheck, documentation/changelog checks, sync-docs --check and whitespace
  checks passed. No full documentation socket suite was run.
- The final scope-guard selection passed after renaming its existing exemption.
  The initial expanded run's only failure was that stale key.

The default report's no-open-award path runs through the public API in the
acceptance proof. Open-award tests use the existing private calculator seam,
because C2's production calculator remains a panic stub. The complete legacy
store suite and controller runtime suite were not run. Their W1-owned claim
fixtures are not final billing evidence.

Amendment disposition:

- A1-24 retains the transactional data checker and merged binding integration.
- A1-27 retains award/interval/settlement helpers. W1 must join its lifecycle
  transactions. The agreed refund operation for an unstarted at_award claim and
  explicit customer-runner wait operation remain missing. C2 SettleBilling is
  still a panic stub. Open-award reporting is now also an explicit consumer of
  that existing calculator contract. Its store tests use fakes.
- A1-28 retains committed-checkout settlement. W2 owns provider verification,
  verified routes, and removal of caller-selected paid grant authority.
- A1-29 through A1-35 retain upload, receipt, member, reference, record, tombstone
  and tagged-log persistence. W2/W7 transport, namespace/edge policy, physical
  verification/deletion and proxy egress remain real integrations. Result
  attachment needs its manifest reader. Materialization still lacks destination
  identity for a never-executed terminal job. Published owner-log identities
  still need a cross-owner contract.
- A1-53 has these SQLite proofs. Native acceptance remains open. handoffs.md has
  no new W1b assignment.

No new foreign contract change or human question is added. C1 documents the
local reporting interpretation and removed API. The shared API snapshot was
updated only for W1b's method removal and the totals declaration. Generating a
fresh snapshot under scratch found inherited W1 drift, including provision
policy, operator method moves and egress_day removal. The lead still owns
aggregate regeneration. No unrelated API snapshot change was copied back.

Shared files touched: pkg/store/tenant_sql_scope_guard_test.go (rename only the
existing deployment-totals exemption), internal/acceptance/store_billing.go,
design/rewrite/contracts/c1-data.md, .apidiff/pkg_store.txt, CHANGELOG.md,
docs/migrations/_unreleased.md and generated mirrors. Other edits are owned
accounting/storage files, new credit_totals.go and its tests, STORE-DATA.md and
this handoff. W1's admission, state, claim, reconciliation and reaping files are
unchanged. No new schema, interface, price, setting, limit, environment variable
or service is added.

Friction was cold compilation, the moved scope-guard key and inherited API
snapshot drift. The scope guard now retains the same reason under the moved
method name. Native providers, PostgreSQL, physical backends, backup/restore,
controller runtime tests, candidates and pipelines were not exercised. Migrated
legacy runtime behavior is not claimed as new evidence. No Git writes, service
starts or Sparkwing binaries ran. Git identity remains Korey's default with no
core.worktree override. Scratch is deleted after final inspection.

## Previous continuation (committed in 3e1232e53)

Removed `Store.CreditBalanceMicro`, **10 production Go lines**, including its
comments and separating blank line. Fourteen store-test calls and three
controller-test calls now select the existing Tenant method. No production
caller outside pkg/store used the removed API. The remaining
`Store.ListCreditCharges` wrapper has W1-owned claim/race test callers and stays
until W1 migrates them. W1 implementation and test files are unchanged.

Extended `billing-ledger-survives-retention/store-data-sqlite` to check the exact
`Tenant.CreditUsageByRun` row before leased retention and after reopening.
Storage charges and the neighboring team's ledger cannot enter that report.
Added a 64-case property spanning 128 tenant keys, shared adversarial run IDs,
reservation/usage/refund netting, storage and runless charges, tied ordering,
limits, balances and reopening without operational run rows. Its invariant is
that each Tenant exposes only its own immutable ledger and per-run totals.

Ported the three tests in node_logs_test.go to tagged persistence and deleted
that file, **173 legacy test lines**. New tests cover segment replay, conflicting
content, segment and attempt byte limits, exact retained-byte accounting and
run-deletion cascade. The existing final-schema tests retain immutable seals,
current-attempt fencing and historical reads. A new test verifies that a later
node finish cannot extend an attempt's tail grace. This reverses the legacy
node-end fallback under the final attempt-specific contract. A 96-case property
checks that team, run, job, attempt and generation forgeries cannot read or write
another subject. No new archived T1 inventory row is claimed complete.

Verification uses the environment and command prefix under Manual below.

- Whole-repository go build -p 4 ./... passed.
- go vet -p 4 over store, controller and both acceptance trees passed.
- The expanded store selection passed **183 top-level tests / 468 cases**, with
  no skips. This includes the existing Manual selection plus grant, reversal,
  history, balance and tenant-scope/writer/twin guards.
- Both acceptance trees passed **20 top-level tests / 486 cases**. All **12
  SQLite data/billing cells** pass. The native/platform skeleton retains
  **2,991 skips**. No new native cell is activated.
- Required commentcheck -base main passed across 890 files. Session sleepcheck,
  focused documentation/changelog tests, sync-docs --check and whitespace checks
  passed. The full pkg/docs suite reached TestWebClient_Versions_HappyPath and
  could not bind its socket in this sandbox. The original failure remains in
  the session transcript. Its pure mirror/changelog checks passed separately.

The full legacy store suite, controller runtime tests, PostgreSQL, providers,
physical storage, backup drill, native placements, candidates and pipelines
were not run. Migrated legacy consumers compile. Their old claim fixtures are
not new execution evidence. After correcting the tail fixture's start/finish
ordering, its final focused test and store vet passed again.

Amendment disposition:

- A1-24: transaction data checker and merged binding integration remain in place.
- A1-27: immutable award/interval/settlement helpers exist. W1 must join its
  lifecycle transactions. An agreed full refund for an unstarted at_award claim
  and an explicit customer-runner wait operation remain missing. C2's production
  calculator is still a panic stub. W1b does not change these foreign contracts.
- A1-28: committed-checkout settlement exists. W2 owns verified-provider routes
  and removal of caller-selected paid grants.
- A1-29 through A1-35: upload, receipt, member, reference, record, tombstone and
  tagged-log persistence exist. W2/W7 transport, policy and physical integration
  remain open. Result attachment still needs the real manifest reader. Memo
  materialization needs a destination identity for a never-executed terminal job.
  Published owner-log identities still need a cross-owner contract.
- A1-53: these SQLite proofs pass. Native acceptance stays open.
- handoffs.md contains no W1b assignment.

No new foreign contract change or human question is added. W1/W2/W7 integration
and legacy runtime retirement remain the limit on completion. The provider
verifier, blob namespace/edge policy and physical byte adapters still require
their real owners. Existing contract-fake proofs remain distinct from native
readiness.

Shared edits outside the owned store files: pkg/controller/credits_test.go,
pkg/controller/node_claim_named_test.go (only the three balance caller changes),
internal/acceptance/store_billing.go, design/rewrite/contracts/c1-data.md,
.apidiff/pkg_store.txt, CHANGELOG.md, docs/migrations/_unreleased.md and generated
mirrors. Other edited store files are owned credit/log tests, credits.go,
STORE-DATA.md and this handoff. No schema, interface, setting, limit, environment
variable or runtime mechanism was added.

Friction was cold compilation, the docs socket restriction and unavailable
foreign integrations. The log property was corrected to accept a missing-run
write refusal while still requiring read authorization before existence lookup.
No Git writes, service starts or Sparkwing runtime commands ran after bootstrap
discovery. Git identity is Korey's default with no core.worktree override.
Scratch is deleted after final inspection.

## Previous continuation (committed in 00ed1f202)

Fixed final-schema billing history. Tenant.ListCreditCharges queried the removed
credit_charges.token_prefix column. It now reads capability_prefix into the
existing CreditCharge.TokenPrefix field. Grant and charge history now retain the
selected tenant on every returned row. The two history methods moved unchanged
apart from these fixes and their GoDoc into owned credit_history.go. The shared
tenant_reads.go only loses those two W1b methods. W1's admission, state, claim,
reconciliation, reaping and authority code is unchanged.

The invariant is that history exposes only the selected tenant's immutable
ledger and awarded capability identity. The new property first failed with
"no such column: token_prefix". It passes 64 generated cases covering 128 tenant
keys with Unicode and SQL metacharacters, all grant/charge kinds, exact metadata,
equal-timestamp ordering, limits and reopening. The extended SQLite acceptance
proof independently failed because grant history dropped the tenant. It now
compares the complete compute and real storage-charge rows before leased run
retention and after reopening, beside a foreign ledger.

Removed Store.RecordCreditGrant, **13 legacy production Go lines**, counting its
GoDoc and separating blank line. Its eighteen direct test calls use the existing
tenant helper. The existing Store.GrantCredits consumer selects the default
Tenant's grant recorder. Its legacy API remains until its external and W1-owned
fixtures migrate. No new forwarding API was added. The surviving Tenant method
retains its behavior. There was no cross-package production caller to migrate.
The API snapshot, source migration guide, changelog and mirrors record the cut.

Ported TestCreditHistoryLimitIsClampedToItsMaximum from the old three-grant fixture
to final-schema tests of both histories with 1,005 rows each. The old **23 test
lines** are deleted. The test checks default, negative, small, maximum and
above-maximum requests and the exact newest/oldest returned rows. An existing
reversal test's debit fixture now uses capability_prefix and bound arguments.
No archived T1 inventory row is newly claimed complete.

Verification uses the environment and command prefix under Manual below.

- Whole-repository go build -p 4 ./... passed.
- Scoped go vet over store and both acceptance trees passed.
- Focused store checks passed **170 top-level tests / 434 cases**, with no skips.
  The Manual selection also includes TestGrantCredits, TestCreditGrant,
  TestCreditReversal and TestCreditHistory for this continuation.
- Both acceptance trees passed **20 top-level tests / 486 cases**. The same
  **12 SQLite data/billing cells** pass. The native/platform skeleton retains
  **2,991 skips**. This continuation activates no new native cell.
- The extended billing cell passed again after tightening the full-row comparison.
- Tenant SQL-scope, writer and twin guards passed.
- Required commentcheck -base main passed across 888 files. Session sleepcheck,
  documentation/changelog checks, sync-docs --check and whitespace checks passed.

The full legacy store suite, PostgreSQL, providers, physical storage, native
placements, backup drill, candidates and pipelines were not run. Unit persistence
proofs still use contract fakes for the unavailable calculator and storage policy.
Review and native verification remain with the lead.

Amendment disposition remains: A1-24 has the transactional data checker and merged
binding integration. A1-27 has accounting helpers but no production callers from
W1's award/start/wait/renew/finish/reap paths. A1-28 has committed-checkout settlement
but still needs W2's verified-provider route cutover. A1-29 through A1-35 have the
store persistence owners but need W2/W7 transport, policy and physical integration.
A1-53 has these SQLite proofs while native coverage remains open. handoffs.md has
no entry assigned to W1b. There is no new foreign contract shape or human question.

The previous publication/result integration gaps remain. AttachResultRecords is
implemented and must be configured with the real manifest reader. Materialization
still lacks a destination identity for a terminal job that never executed, and
the owner-publication identity contract remains unresolved. W1's continuation also
requests an agreed unstarted-release refund operation for at-award intervals and
an explicit customer-runner wait operation. Those need cross-owner agreement
before metered lifecycle integration. C2 SettleBilling, VerifyPaymentEvent and
W7's namespace/edge policy remain panic stubs in this checkout. Physical blob/log
transports, deletion, proxy egress and legacy consumers remain integration work.
No native readiness is claimed.

Shared edits: tenant_reads.go (only the moved billing methods),
internal/acceptance/store_billing.go, design/rewrite/contracts/c1-data.md,
.apidiff/pkg_store.txt, CHANGELOG.md, docs/migrations/_unreleased.md and generated
mirrors. Other edits are owned credit code/tests, STORE-DATA.md and this handoff.
No caller in another package changed because the removed API had none.

Friction was cold compilation, the removed-column fixture reproduced in the
reversal test, and foreign runtime/identity gaps. The prior commentcheck finding
has been fixed in the merged tree. No new schema, interface, setting, limit,
environment variable or runtime mechanism was added. No Git write, service start
or Sparkwing runtime command ran after bootstrap discovery. Git author identity
remains Korey's default with no core.worktree override. Scratch is deleted after
final inspection.

## Previous continuation (committed in 9c42f1022)

Removed `Store.CreditState` and `Store.ListCreditGrants`, **15 legacy production
Go lines**, including their comments and separating blank lines. Production
callers already used tenant handles. Ten remaining test calls now use the
existing tenant helper or their default-team handle. The default-tenant test
uses a committed final-schema debit instead of the removed machine-token claim
fixture. No W1 implementation file or other-package production caller changed.
The source migration guide, changelog, mirrors and the two API snapshot entries
record the hard cut.

The billing-read invariant is that a tenant's state and history contain only its
own ledger, and its burn window uses the store clock. The new generated property
first failed because CreditState used wall time. The fix changes that one call
to the existing store clock. All 64 generated cases pass, covering 128 tenant
keys with Unicode and SQL metacharacters, grant ordering, reversals, storage and
compute charges, refunds, exact window boundaries, zero windows, clock advance
and database reopening. No sleep, new setting, schema, interface, runtime
mechanism or environment variable was added.

Extended `billing-ledger-survives-retention/store-data-sqlite`. Its real storage
debit and financial snapshot now coexist with a separately seeded neighboring
team's ledger. Tenant.CreditState and Tenant.ListCreditGrants must return only
the selected team's rows, and their complete results must survive leased run
retention and reopening. No new native acceptance cell or archived T1 inventory
row is claimed complete.

Verification uses the environment and base selection under Manual below, plus
these migrated tests: TestCreditsBalanceIsGrantsMinusCharges,
TestCreditHistoryLimitIsClampedToItsMaximum,
TestCreditReversalNetsOutOfTheBalanceAndTheHistory,
TestCreditGrantAmountIsBounded, TestCreditsDefaultTenantReadsItsLedger and
TestGrantCreditsIsIdempotentByReference. The new property is already included by
TestStoreData.

- Whole-repository `go build -p 4 ./...` passed on the final Go tree.
- `go vet -p 4 ./pkg/store/... ./internal/acceptance/... ./tests/acceptance/...`
  passed.
- The focused store selection passed **163 top-level tests / 427 cases**,
  without skips.
- Both acceptance trees passed **20 top-level tests / 208 cases**. All eleven
  storage SQLite cells and the extended billing SQLite cell pass. The separate
  native/platform skeleton retains **2,995 skips**.
- Tenant SQL-scope, writer and twin guards passed.
- Embedded documentation/changelog tests, sync-docs --check, whitespace,
  session commentcheck and session sleepcheck passed.
- Required `commentcheck -base main` reports the inherited W9 finding at
  internal/controllerwire/contracts.go:47. It reports no W1b finding.

The full legacy store suite, backup/restore drill, PostgreSQL, providers,
physical backends, native placements, candidates and pipelines were not run.
Migrated legacy claim-fixture tests compile, but no new runtime claim evidence
is asserted for them. Review, aggregate API snapshot regeneration, native
checks and pipelines remain with the lead.

Amendments A1-24, A1-27 through A1-35 and A1-53 retain the implementation and
integration disposition in the previous continuation below. Accounting helpers
still have no production caller outside their owner file. W1 owns joining them
to award/start/wait/renew/finish/reap transactions. C2 SettleBilling and W7's
namespace/edge policies remain panic stubs, so their persistence tests use
contract fakes. Verified payment settlement, result-record configuration,
publication identities, physical blob/log transports and legacy caller
retirement remain integration work. handoffs.md has no W1b entry.
No new foreign contract shape or human question is added.

Shared files touched: internal/acceptance/store_billing.go,
pkg/store/backup_restore_drill_test.go, design/rewrite/contracts/c1-data.md,
CHANGELOG.md, docs/migrations/_unreleased.md, its generated mirror,
pkg/docs/changelog.md and .apidiff/pkg_store.txt. The remaining changes are
owned credit implementation/tests, STORE-DATA.md and this handoff.

Friction was cold compilation and the inherited W9 commentcheck finding.
The injected-clock failure was reproduced and fixed. No Git writes, service
starts or Sparkwing runtime commands occurred. Git author configuration remains
Korey's default and has no core.worktree override. Scratch is deleted after
final inspection.

## Previous continuation (committed in 496ba5f1a)

Activated `storage-owner-receipts-and-stream-verification/store-data-sqlite`.
Trigger and plan uploads reserve and commit before jobs exist. The proof rejects
conflicting verification, checks hidden presence before commit and after failure,
replays exact tagged receipts, reopens the database and retains the owner ticket.
A separately seeded job in the same run cannot read or reference the digest or
adopt the owner's ticket. Upload usage stays three bytes and one object,
with no metadata download or egress charge. Award and physical verification facts
are preseeded contract fixtures. Native signed transfers and EOF verification are
still untested. The native cell remains skipped.

Added two eval-owner tests. The invariant is that an owner receipt stays bound
to its exact tagged unit and source fences. The property checks 128 forged source
and unit identities through reserve, lookup, commit and abort after reopening.
Six cases independently replace the persisted unit generation, source generation,
lease, holder, prefix or status and require every upload operation to refuse.
No refused pending commit creates a blob or receipt.

Eval's real fixed grant table is `expansion.submit,data.read`; its data scope has
no write ref. The tests first reached that refusal. They now use explicit fake
write authority to exercise store persistence without widening C2's issuance
policy or editing W1 files. These tests do not establish real eval upload support.
No foreign contract change is requested. This establishes persistence under
preseeded authority only. Trigger and plan ownership need no extra grant
beyond their preseeded awards.

Removed `Store.SetCreditRateTable`, **9 production Go lines**, including its
comments and separating blank line. The 18 test calls use the existing
`SetCreditSettings` and `CreditSettingsUpdate.RateTable`. No production caller
outside pkg/store changed. No wrapper, setting, schema, limit, environment variable
or runtime mechanism was added. No archived T1 inventory row is newly complete.
The new proof adds owner-identity evidence to the acceptance contract.

Verification uses the environment and store selection under Manual below.

- Whole-repository `go build -p 4 ./...` passed after the final code changes.
- Scoped vet over store and both acceptance trees passed.
- The focused store selection passed **156 top-level tests / 420 cases**, with
  no skips. The eval selection and changed settings selection also passed.
- Both acceptance trees passed **20 top-level tests / 208 cases**, including
  **11 storage SQLite cells and one billing SQLite cell**. The native/platform
  skeleton still has **2,995 skips**. internal/acceptance has no test files.
- Tenant SQL-scope, writer and twin guards passed.
- Documentation mirror/changelog tests, sync-docs --check, session commentcheck,
  session sleepcheck and whitespace checks passed.
- Required `commentcheck -base main` reports one inherited W9 finding at
  internal/controllerwire/contracts.go:47. This continuation's comments pass.

The full legacy store suite, provider transactions, PostgreSQL, physical backends,
real routes, native placements, candidates and pipelines were not run. The brief
assigns native checks and pipelines to the lead. Some migrated legacy test callers
still use old claim fixtures. Their compilation is covered by the store test run;
this continuation claims no new runtime evidence for those old claim paths.

Amendment status for W1b:

- A1-24: transactional data checking and W1's binding join are implemented and
  tested in the merged tree. PostgreSQL/native route evidence remains open.
- A1-27: accounting helpers are implemented with calculator fakes. W1 still
  inserts unmetered billing awards directly and owns joining award/start/wait/
  renewal/finish/reap transactions to those helpers. C2's calculator is a stub.
- A1-28: verified committed-checkout settlement is implemented. W2 owns provider
  verification and replacing caller-selected paid grant routes.
- A1-29 through A1-35: store upload, record, member, receipt, quota and reference
  owners are implemented. W7 namespace/edge policy, verified manifest reads,
  physical verification/deletion and W2 authenticated signing remain open.
- A1-53: the additional SQLite proof is active. Native acceptance stays open.
- handoffs.md has no entry assigned to W1b.

The prior imported publication-attempt, owner-publication and materialized
result-destination identity gaps remain W1 integration points. W2 must configure
SetResultBlobRecords with W7's real verified manifest reader. Final log signing,
transfer, lifecycle expiry, ring fallback and proxy egress remain foreign runtime
integrations. Old upload, log, grant and claim-credit consumers still prevent the
remaining hard cuts. W1 implementation files are unchanged.

Shared files touched: internal/acceptance/store_data.go, new
internal/acceptance/store_owner_receipts.go, c1-data.md, CHANGELOG.md, the source
migration guide, its generated mirror, pkg/docs/changelog.md and the one removed
API snapshot declaration. All other edits are owned store tests, credit_rates.go,
STORE-DATA.md and this handoff. No cross-package production caller was migrated.

Friction was foreign stubs, the inherited W9 comment, a fixture SQL placeholder
count and an enum spelling corrected before passing checks. Scratch is deleted
after final inspection.
No Git writes, service starts or candidate Sparkwing execution occurred.

## Previous continuation (committed in 52b693463)

Fixed attempt-scoped publication authorization in the shared blob resolver.
The invariant is that a publisher can act for an imported attempt only after
that attempt has a persisted finish time. The new property first reproduced
acceptance of unfinished attempts through reservation, lookup, commit, abort
and both upload/commit data checks. It now passes 96 generated ordinal and
generation pairs. Each refused case preserves existence and receipts. Restoring
the imported terminal attempt permits the ticket to be released again.

Logs use the same terminal-attempt check. Removed the duplicate query from
log_publication.go, a net reduction of **10 production Go lines**. Publication
fixtures now supply terminal imported attempts. No API, table, setting, limit,
environment variable or foreign interface was added. The C1 documentation
clarifies the existing publication identity requirement.

Activated `storage-cas-integrity/store-data-sqlite`. It rejects mismatched
verified digest, size, tenant and ticket evidence without existence or receipts.
It reopens an expired interrupted ticket, reserves its replacement at the same
quota ceiling, rolls back a receipt insertion failure and replays the successful
commit. After another reopen, only the replacement uploader can read the digest.
Usage counts exactly three bytes and one object. This uses preseeded publication
authority and a fake physical verification handoff. Signed transfers, backend
checksum verification and download EOF checks remain native integration work.

Ported `TestDirectUploadReservesAndPublishesOnlyAfterCommit` to final upload
coverage and removed **40 legacy test lines**, including its unused import.
Final ticket tests cover its
pending visibility, quota, tenant refusal, receipt attribution, settlement and
replay assertions. The new CAS cell also ports store-side assertions from
archived `TestDirectCommitAdoptsVerifiedCopyAfterOldReservationExpires` and
`TestDirectCommitRefusesExistingObjectWithDifferentChecksum`. Their complete T1
rows remain open because those require real physical bytes and controller paths.
Legacy production APIs still have callers and remain for that cutover.

Whole-repository build, scoped vet, the focused store selection under Manual,
both acceptance trees, final publication properties, tenant scope/writer/twin
guards and documentation mirror checks pass. The ten storage SQLite cells and
the billing-retention SQLite cell pass. No native cell was activated.
The CAS cell was rerun after simplifying its reopen fixture. The publication
selection was rerun after removing the legacy test. Required commentcheck
against main still reports the same three inherited W1/W9 comments below.
Commentcheck against HEAD, session sleepcheck and whitespace checks pass.

Amendment disposition for this split:

- A1-24 and A1-29 through A1-35 have their store-side transaction, upload,
  receipt, member and record implementations. The CAS and publication proofs
  extend that evidence. W2/W7 transport and physical integration remain open.
- A1-27 remains open at W1's callers. authority_issue.go still inserts
  unmetered awards directly instead of invoking recordBillingAwardTx. Final
  start, wait, renewal, finish and reap paths still need the accounting helpers.
  W1 owns those files, which this continuation leaves unchanged.
- A1-28 has the final store settlement API, but W2 must replace the old paid
  grant route with its verified provider handoff. C2 SettleBilling and payment
  verification remain panic stubs in this checkout. Accounting tests use the
  existing calculator seam rather than calling the stub.
- A1-53 remains open for native controller, PostgreSQL, filesystem/S3 and
  placement evidence. handoffs.md contains no entry assigned to W1b.

No new foreign contract shape or human question is added. The existing W1
publication-attempt import and materialized destination identity gaps below
remain. W2 must configure result-record verification with W7's reader.
W7 namespace/edge policy, physical verification/deletion and log transports
remain real integration points. The old upload, log and billing consumers
still prevent their production API removal. No cross-package production caller
changed because no API was removed.

Shared files touched are internal/acceptance/store_data.go,
internal/acceptance/store_presence.go, new internal/acceptance/store_cas.go and
design/rewrite/contracts/c1-data.md. Other edits are clearly owned store data
files and this handoff. W1 implementation files are unchanged. Friction was
the foreign stubs and inherited commentcheck failures. Scratch is removed at
the end. No Git writes or Sparkwing runtime commands ran. The lead owns review,
commits, native integration and pipelines.

## Previous continuation (committed in 19950f26c)

Implemented `nodeResultRecords` on the existing blob record owner. Configure it
with `Store.SetResultBlobRecords(manifestReader)` before sharing the store.
The adapter verifies output and manifest records in W1's state transaction.
The invariant is that only the exact tenant/run/job/attempt/generation result
keys, backed by authorized records and required bytes, may accompany a result.
The adapter uses the existing record verifier, including member metadata and
post-I/O authority checks. `data.write` authorizes attachment without requiring
an unrelated read grant. Explicit and embedded manifest digests must agree.

Seven tests exercise the real final transition and record persistence with
namespace/edge-policy and verified-manifest contract fakes. The first tests
failed because no blob implementation of the result interface existed. Passing
coverage includes 240 generated result-key forgeries, 96 generated lease-expiry
crossings, missing/expired records, corrupt manifests, missing member edges,
changed member sizes, conflicting manifest pointers and storage failures.
Successful results persist record keys in exactly one result event. Refusals
leave state, live attempts and events unchanged. Transaction tests remove and
restore a member inside the owning transaction, then roll back a successful
result. The configured adapter and embedded manifests are exercised separately.

This ports the store-side result-transaction assertions from archived
`TestClaimedAttemptRecordsOnlyAManifestItsRepositoryStored` to the final API.
The complete T1 inventory row remains open because physical transfer, final
routes and native placement evidence are outside that proof. No native
acceptance cell is activated. The existing ten SQLite persistence cells pass.

Whole-repository build, scoped vet and **153 store tests / 411 cases** pass.
Both acceptance trees pass **20 top-level tests / 203 cases**, with **2,995
native/platform skips**. All tenant SQL-scope, writer and twin guards pass.
Documentation mirror, generated store API snapshot, whitespace and session-only
comment/sleep checks pass. Required commentcheck against main still reports the
same three inherited findings in controllerwire/contracts.go,
state_materialize.go and state_result_records.go. Those W1/W9 files are untouched.
Commands use the environment and package selection under Manual below. Build
was run after the production changes. Vet and the store selection were run
again after the final test changes. Full legacy store tests, PostgreSQL,
physical backends, native transports, candidates and pipelines were not run.

Local contract addition: SetResultBlobRecords configures an existing interface.
W2/controller startup must install it with W7's real verified manifest reader.
The namespace and edge functions in internal/blob are still panic stubs here.
No foreign contract shape was changed. Two precise identity gaps need W1:

1. CausePublish currently imports terminal state without an attempt payload or
   node_attempts insertion. Its accepted job begins at attempt/generation zero.
   Blob/log publication requires the real imported positive attempt. Result
   records cannot be attached until that import contract is provided.
2. Materialization can finish an unexecuted job at attempt/generation zero.
   ResultBlobKey currently names execution attempts. W1 must define the blob
   destination identity for a materialized result, including a retry of one,
   before CopyMaterializedRecords can persist its edges. The source-side
   MaterializedResultSource contract does not supply a destination identity.

Billing integration, physical blob/log paths and the other real integrations
listed below remain open. Legacy deletion is **0 lines**. No API was removed,
and no cross-package production caller changed. W1 implementation files are
unchanged. Shared edits are c1-data.md and the generated .apidiff/pkg_store.txt.
Owned edits are blob_result_records.go, blob_result_records_test.go,
blob_lookup.go, STORE-DATA.md and this handoff.

Friction was the foreign contract stubs and inherited commentcheck findings.
Two test fixture errors (duplicate capability insertion and map comparison)
and an incorrect grant constant were corrected before final verification.
No new service, table, setting, limit or environment variable was introduced.
No Git writes or Sparkwing runtime commands ran. Scratch is removed after the
final checks. The lead owns review, commits, pipelines and real integration.

## Previous continuation (committed in 8da501ade)

Fixed authority surviving a slow verified manifest read. The invariant is that
manifest I/O cannot carry expired authority into a record, a memo attachment or
an authorized read result. CommitRecord and the shared record lookup recheck the
existing authority owner after manifest I/O. This adds ten production lines,
including two safety comments. No new mechanism, API, setting or schema is added.

The first generated test failed through all five record APIs before the fix.
It now passes 720 cases across CommitRecord, LookupRecord, LookupMemo,
LookupDepcache and CopyMemo. The reader advances an injected clock across the
job lease, leaf capability or ancestor expiry. Refusals return no read set and
persist no output or manifest edge. Namespace and edge policy remain contract
fakes because W7's implementations are still stubs.

Added a real W1 CheckBinding integration property. Both BindData and BindReadSet
see receipt changes in their owning transaction, leave rollback to the caller,
and ignore forged principal hints in 96 generated cases. The SQLite HasMany
acceptance cell now calls both binding rules for readable and hidden digests
before receipt commit, after failed verification and after reopening. Its
publication authority remains a preseeded contract fixture.

Verification uses the environment under Manual below. The repository build and
scoped vet pass. The store selection passes **146 top-level tests / 393 cases**
without skips. Both acceptance trees pass **20 top-level tests / 203 cases**,
including the same **10 SQLite persistence cells**, with **2,995 native/platform
skips**. No new acceptance cell or archived T1 port is claimed complete.

All selected tenant SQL-scope, writer and twin guards pass on this merged tree.
The documentation mirror check, session commentcheck/sleepcheck and whitespace
checks pass. Required commentcheck against main still reports three inherited
findings: internal/controllerwire/contracts.go:47, pkg/store/state_materialize.go:15
and pkg/store/state_result_records.go:9. W1/W9 own those untouched files.

Legacy deletion is **0 lines**. Existing callers still require the old APIs.
No foreign contract change or human question is added. The SQLite binding join
is verified, but PostgreSQL and its read-only/locking transaction mismatch remain
open. Billing issuance/interval/settlement, published owner identity, physical
storage, native log transports and the legacy hard cut remain the integrations
listed below. Independent review and pipelines belong to the lead.

Owned edits are blob_record_runtime.go, blob_lookup.go, the two new blob test
files, STORE-DATA.md and this handoff. Shared files are only
internal/acceptance/store_presence.go and design/rewrite/contracts/c1-data.md.
No W1 implementation file or cross-package production caller changed. Friction
was cold compilation and the inherited commentcheck findings. Scratch is removed
after the final checks. No Git writes or Sparkwing runtime commands were used
after bootstrap discovery.

## Previous continuation (committed in 12816e241)

Fixed HasMany completing its final read after the deadline and returning success.
`readableBlobsTx` checks the deadline after the full scan and discards presence
on `blob.ErrBudget`. A generated test first failed on the old implementation.
It advances the store clock through 160 generated deadline/duplicate cases.
The invariant is that a successful presence response finishes within its budget.
This adds three production lines and no new mechanism or configuration.

The new `storage-hasmany-large-budget/store-data-sqlite` acceptance cell passes.
It queries 1,025 distinct digests plus duplicates, in forward and reverse order
after reopening. All 257 readable digests appear once in request order. Unrelated,
foreign and absent digests remain hidden. Upload counters stay at 257 bytes and
objects, with no download or egress charge. Physical verification and native
placements remain separate integrations.

Added a trigger/plan owner-receipt property and job-read regression. The invariant
is that owner upload authority cannot transfer through a forged identity or an
unrelated job. Each owner uploads before any job attempt, reopens, retains its
tagged receipt without a synthetic job, rejects 96 generated identity forgeries,
and refuses presence/download/reference access to a newly awarded job in its run.

Removed `Store.CreditRateMicroPerSecond` and `Store.CreditGraceSeconds`,
**12 production Go lines**, including their comments and blank lines. Their
remaining test callers read the existing `CreditSettings` snapshot. Deleted obsolete
scalar-reader assertions. No whole test or archived T1 inventory row was removed.
No additional T1 row is claimed closed. No foreign contract shape, schema, limit
or environment variable changed. No human question or new mechanism was added.

Verification uses the environment listed below. Whole-repository build and vet
over store, controller, both acceptance trees and their subpackages pass.
The store selection passed **143 top-level tests / 382 cases** without skips.
A final focused run passed the new properties, changed settings tests and rate
tests after the final test edit. Both acceptance packages passed **20 top-level
tests / 203 cases**, including **10 SQLite cells**, with **2,995 native/platform
skips**. The new cell ran its actual assertions.

Documentation mirror tests, `sync-docs.sh --check`, session-only commentcheck,
session-only sleepcheck and diff whitespace checks pass. Required
`commentcheck -base main` still reports the inherited detached HereBearer comment
in `internal/controllerwire/contracts.go`. The SQL-scope guard still reports the
same ten inherited findings below. Both tenant writer guards pass in that run.

Three selected legacy controller tests fail in fixture setup at
`pkg/controller/credits_test.go:42` because they insert into removed `tokens`.
The changed assertions are not reached. They are
`TestCreditSettings_ReadsTheDefaultsAndSetsEachValue`,
`TestCreditSettings_RefusesAWriteWhoseOtherFieldIsBad` and the unchanged
`TestCreditSettings_LeavesUnnamedValuesAlone`. Store tests cover the settings
behavior through the final schema. Full legacy store tests, PostgreSQL, race,
providers, native transports, candidates and pipelines were not run.

Shared Go changes are `internal/acceptance/store_data.go`, new
`internal/acceptance/store_presence_large.go` and two caller replacements in
`pkg/controller/credits_test.go`. Other shared changes are `c1-data.md`, the
migration guide, `CHANGELOG.md`, generated documentation mirrors and the two
deleted API snapshot declarations. Owned files are the two new property-test
files, `blob_read_authority.go`, `credits.go`, three billing test files and
`STORE-DATA.md`.

Remaining work is the integration and legacy retirement listed below. The local
W2 billing calculator and payment verifier remain stubs. W1 issuance still writes
unmetered billing rows directly. W1/W2 must connect their real paths to the
existing transactional billing owner before the old claim-credit code can go.
No W1 file was edited. Friction was cold compilation, the foreign fixture/guard
failures and a typed-principal compile correction in the new test. Scratch is
removed after verification. Git identity and worktree configuration are unchanged.

## Previous continuation (committed in dd33fc542)

## Unit

Implemented C2's existing `DataAccessChecker` on the owning store transaction.
`Tenant.CheckDataAccess` uses that implementation. Both it and `ReadableBlobs`
share one transaction-aware presence reader. Receipt, record, revocation and
holder changes in the owning transaction govern authorization. The checker opens
no second transaction and leaves commit or rollback to its caller.

The invariant is that caller-supplied authority cannot widen persisted rights,
and data checks must see changes in their owning transaction. Three new tests
first failed because the transaction did not implement the contract. They now
prove receipt deletion/reinsertion, newly declared read edges, removed records,
rollback ownership, live rights and refusal after leaf/ancestor revocation,
generation replacement, lease expiry or a foreign team. A property generates
96 forged principals and checks all six data operations for each input.

The existing HasMany SQLite proof now also checks download, HasMany and lookup
route authorization before and after receipt commit, failed verification and
reopening. Missing, unrelated and foreign bytes remain indistinguishable. The
proof uses the final persisted publication capability as a contract fixture.

Removed the unused `Store.CreditRateTableSet` getter, **8 production Go lines**.
Its two test callers now read `CreditSettings.RateTableSet` in the settings
snapshot. The tests preserve unset-table and table-without-base-class behavior.
Removed **2 narrative comment lines** from the owned accounting file. Moved
checker code is a refactor and is excluded from those deletion counts. No test
was deleted and no new archived T1 inventory row is claimed closed.

Fixed an owned disk-fixture mismatch exposed by the final store selection.
`blobAccountingDB` now opens its connection before returning, matching production
Open's initialized database. A new regression test first observed DELETE journal
mode instead of WAL, then passed with eager connection initialization. The
original concurrent-settings test reproduced SQLITE_BUSY once in 300 repetitions
before that correction. No production locking or retry behavior changed.

The final upload reservation, receipt, quota, record, GC, accounting, payment,
output-limit and tagged-log implementations from earlier sessions remain.
`STORE-DATA.md` describes their transaction seams. The earlier abandoned-stream
maintenance integration remains in place.

## Manual

Commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
and GOCACHE/GOTMPDIR/TMPDIR beneath `/tmp/sw-astra-W1b/`. Run Go commands with
`go -C /home/bombuntu/.xwing/envs/1088/sparkwing`.

- Whole-repository `go build -p 4 ./...` passed.
- `go vet -p 4 ./pkg/store/... ./internal/acceptance/... ./tests/acceptance/...`
  passed.
- The focused store selection passed **141 top-level tests / 378 cases**,
  without skips. The expanded three-test transaction selection also passed.
- The concurrent credit-settings test passed 300 repetitions after the fixture
  correction. Reproduce with `go test -p 4 ./pkg/store -count=300
  -run '^TestStoreDataCreditSettingsIndependentWritersPreserveRateAuthority$'`.
- `go test -p 4 ./internal/acceptance/... ./tests/acceptance/... -count=1`
  passed **20 top-level tests / 202 cases**, with **2,995 native/platform skips**.
  The changed storage proof was rerun after extension and passed.
- Both tenant writer guards, documentation mirror/changelog drift tests,
  session-only commentcheck/sleepcheck and diff whitespace checks passed.

Store selection:

```text
^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore|TestStorage|TestBilling)
```

Nine SQLite persistence cells pass. The HasMany cell is extended. No native
cell was activated:

- `billing-ledger-survives-retention/store-data-sqlite`
- `storage-replica-safe-accounting/store-data-sqlite`
- `storage-quota-settlement-crash-replay/store-data-sqlite`
- `storage-log-segments-and-fallback/store-data-sqlite`
- `storage-hasmany-read-set/store-data-sqlite`
- `storage-reachability-and-retention-tails/store-data-sqlite`
- `storage-memo-and-depcache-scope/store-data-sqlite`
- `storage-reference-edge-laundering/store-data-sqlite`
- `storage-publish-one-way/store-data-sqlite`

Branch-wide checks retain foreign failures:

- Required `go run ./internal/commentcheck -base main .` reports the detached
  HereBearer comment in `internal/controllerwire/contracts.go`. The owned
  accounting-comment finding is removed. `-base HEAD` passes this continuation.
- `go run ./internal/sleepcheck -base main .` reports two wall-clock deadline
  tests in `internal/limits/deadline_test.go`. `-base HEAD` passes.
- The SQL-scope guard reports the same ten inherited W1 findings in authority,
  awards, retention and stale exemptions. No new W1b finding appears.

## Not tested

The full legacy store suite was not rerun. Earlier full-suite failures concern
removed-schema/token/claim fixtures, including a token-test panic. No PostgreSQL,
race, provider, daemon, native placement, browser, candidate, pipeline or release
check was run. The brief assigns pipelines and independent review to the lead.
The API snapshot removes only this continuation's deleted symbol. The lead must
regenerate aggregate snapshots after the remaining area cuts.

## Contracts and real integrations remaining

No foreign interface, schema, setting, limit or environment variable changed.
The C1 contract documents the existing transaction checker implementation and
removes the unused presence getter. No new foreign contract shape is requested.
No human decision is requested.

1. W1's binding adapter can use the transaction's `DataAccessChecker` after
   integration. Test it through the real adapter, including PostgreSQL. W1's
   separate branch opens a read-only snapshot in `Store.CheckBinding` and then
   invokes locking authority resolution. PostgreSQL disallows those row locks
   in a read-only transaction. W1 must resolve that transaction-mode mismatch.
   The final blob mutation paths retain their locking authority rechecks.
2. W1 must join immutable billing awards, interval starts/finishes, periodic
   charges and settlement to issuance, starts, waits, renewal, release, finish
   and reaping. Memo edges and result state must materialize atomically.
   Published run, plan and attempt import precedes blob/log publication.
3. W1/W2 must define imported trigger/plan/eval identities for published owner
   logs. Job publication uses exact imported terminal attempts. Owner publication
   remains refused without that contract.
4. W2 must supply the real billing calculator, verified payment handoff and data
   authorization/signing. Card attempts, warnings, refunds and freezes still
   need provider test/live integration coverage.
5. W7 must supply namespace/edge policy, verified manifest reads, physical
   verification/deletion and trusted replica identity. W1/W7 must wire final
   retention, upload quota release and tombstone GC to the elected worker.
   The legacy storage pass still reconciles raw counters and prunes old outputs
   and node logs. Its source and upload age-pruning calls are gone.
6. W2/W7/W9 must wire final log signing, transfer, publication, lifecycle expiry,
   proxy egress and ring fallback. Transport must register an empty stream
   through `RecordSegments` before its first transfer so abandoned empty tails
   have persisted identities. Maintenance seals existing streams only. Legacy
   controller log readers/writers and read-time physical sealing remain until
   that transport replaces them. The old periodic sweep is gone.
7. W1/W9 must define final custom-event append/sequence behavior before
   `AppendEventCharged` and `chargeStorageTx` can go. W1 owns removal of egress_day
   and remaining table classification/scope guard cleanup.
8. Legacy claim credits, paid grants, raw storage counters, direct upload and
   output persistence still have W1/W2/W3/W7/W9 consumers. W7/W9 must retire
   StateStoreOld manifest consumers. No forwarding shim was added.

## Shared files and friction

Shared Go caller changes outside pkg/store: only
`internal/acceptance/store_presence.go`. The removed getter has no production
caller, so its removal required no cross-package migration.

Other shared files: `design/rewrite/contracts/c1-data.md`,
`docs/migrations/_unreleased.md`, its generated embedded mirror, `CHANGELOG.md`,
`pkg/docs/changelog.md` and `.apidiff/pkg_store.txt`. The owned persistence
explanation and this handoff are updated. `bin/sync-docs.sh` generated mirrors.

Friction: cold compilation, strict final-schema fixtures, missing foreign runtime
owners and the inherited branch-wide guard failures above. The first read-edge
fixture used an invalid terminal row, and the first receipt-deletion query named
an absent table. Both fixtures were corrected before verification. Bootstrap ran the installed
`sparkwing info --for-agent` before the swarm brief was read. No other Sparkwing
binary command ran. No production
workaround or SQL guard exemption was added. Scratch was deleted after checks.
No owned service or background process remains. Git identity and worktree
configuration were not changed.

STATUS: CONTINUE
