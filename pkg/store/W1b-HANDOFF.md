# W1b record rules and log completeness

Base: 051fcbe96. Net production Go delta: **-37 lines** (28 added,
65 deleted). Store accounts for -13. Shared log, controller and caller code
accounts for -24. The combined store area is +3,212 against main, **7,462
above** its -4,250 budget. That overage remains unjustified. Legacy output and
credit paths remain, so this area is incomplete.

Removed the blobRecordRules interface, sharedBlobRules adapter, permissive
fakeBlobRules implementation and namespace timing hook. Store record writes,
reads and memo copies now call W7's real RecordNamespace and AuthorizeEdge.
Fixtures carry valid sealed refs and SHA-256 memo hashes. The former fake had
accepted empty cache refs and invalid memo hashes. Manifest I/O remains fake.
The mutation property prepares real records, advances the controlled clock and
invokes the transaction-local mutation owner. It checks lease, leaf and ancestor
expiry at the exact boundary, rollback and a live control. Public CommitRecord
and CopyMemo still have generated manifest-I/O expiry and atomic-copy coverage.
No production authority rule changed.

Removed logseg.NodeProgress, the backend's legacy-node projection and the
logseg.SealGrace alias. NodeLogs.NodeLogCompleteness now takes context, run and
job IDs only. Dashboard completeness selects its subject and matching attempt
finish from ReadRunView and uses RunSnapshot.StoreNow. It no longer accepts a
legacy node finish or caller clock. A persisted final seal still wins. The
shared bound is limits.LogSealGrace.

Invariant: only the selected log attempt's persisted finish time and store clock
can expire its unsealed tail. The generated controller property varies ordinals,
generations and times around the grace boundary. Foreign team, run, job, attempt
and generation finish rows and a conflicting logical-job finish cannot change
its verdict. Existing reader-authority and committed-byte properties still pass.
This is component evidence with fake inventory/objects, not provider auth proof.

Verification:

- GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache go build -p 4 ./...
  passes for the whole repository.
- Scoped go vet -p 4 passes for pkg/store, pkg/controller, pkg/controller/client,
  pkg/storage/logseg, internal/backend, internal/web and internal/orchestrator.
- Store TestBlob*, FuzzBlob* seeds and TestResultRecords* pass, including the
  full generated mutation and manifest-expiry runs. The seven preserved store
  cells pass: TestStoreCASIntegrity, TestStorePresenceReadSet,
  TestStoreLargePresenceBudget, TestStoreCacheScope, TestStoreReferenceEdges,
  TestStoreReachabilityRetention and TestStoreBillingRetention (37.579s together).
- TestCommittedDashboard* and TestNodeLogCompleteness* pass across the affected
  controller/backend/web/client/orchestrator packages. The dashboard handler test
  now uses an in-process client fixture instead of retired CreateRun schema.
  It retains complete, incomplete, unknown and missing-job responses and checks
  JSON decode errors. No socket is opened.
- The complete pkg/storage/logseg suite passes.
- Four existing TestStorage/storage-log-segments-and-fallback cells pass:
  filesystem-records, s3-records, filesystem-sink and s3-sink (28.547s).
  No new native acceptance cell is claimed.
- commentcheck -base main is clean across 2,129 files. git diff --check and both
  docs/changelog mirror guards pass. The two changed API snapshots match fresh
  generation. No whole-repository test suite or native PostgreSQL run was made.

Contract changes and integration still needed:

- W1: merge the persisted output/manifest selectors and materialized-result
  identity described on its branch. This checkout's NodeRow and nodeReadColumns
  still lack selectors. CopyMaterializedRecords cannot read a committed source
  selection here. W2/W3/W9 must carry it to authorized readers. Removing the old
  store/client output APIs also requires migrating scheduler finish, rehydrate,
  claim-input and child-output callers. Inventing an event-derived selector would
  create a second read model. No W1 implementation was edited.
- W2/W1: verified PaymentEvent still has only checkout paid/expired/failed facts.
  payment_events requires a checkout FK and permits only those three kinds.
  Card setup, refund, warning and dispute settlement needs the approved verified
  facts and receipt/setup correlation. Existing W1b transaction-local mutation
  owners remain implemented and tested. No foreign proof or schema was changed.
- W1: per-award exhaustion anchoring and the committed renewal result remain
  absent. accountUnitBillingTx returns only error and renewals construct LeaseBeat
  before accounting. The legacy node credit_exhausted_anchor cannot fence every
  operational billing unit. Spend/grace and legacy claim-credit retirement stay
  open until that join is available.
- W1: imported terminal-attempt seeding remains absent. The seven relocated
  store cells still use teststore.SeedPresence's SQL fixture. The approved catalog
  removals and harness relocations were already done and were not repeated.
- W2/W3: the actual pod host still has no logseg.Data constructor binding.
  Authenticated LogTransport/SegmentRecorder adapters remain production joins.
  Physical allowance eviction, source-before-run binding and the
  ReadBlob/ChargeDownload composition also remain open. NewDataClient is already
  implemented here and is not reported as a stub.

Shared production callers changed: internal/backend/completeness.go,
internal/orchestrator/jobs_cli_logs_backend.go, internal/web/reads.go,
pkg/controller/client/logs.go, pkg/controller/log_reads.go and
pkg/storage/logseg/manifest.go. Shared tests are controller/log_reads_test.go and
internal/web/log_completeness_test.go. Shared docs/generated files are
pkg/storage/logseg/README.md, CHANGELOG.md, docs/migrations/_unreleased.md,
their pkg/docs mirrors and the logseg/client API snapshots. Other changes stay
in W1b's pkg/store files. Lead-owned handoffs and amendments are unchanged.

No new production mechanism, setting, schema, pricing decision or restriction
was added. Independent landing review remains with the lead. No Git writes or
Sparkwing runs occurred. Local Git config has no user identity or core.worktree
override. Worker scratch is deleted at completion. /tmp/sw-gocache is retained.

STATUS: CONTINUE
