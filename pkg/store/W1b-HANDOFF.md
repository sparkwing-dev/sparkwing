# W1b log reader cut

Base: 3c7750a4d. Net production Go delta: **-308 lines** (9 added,
317 deleted). The area-budget classifier assigns -311 to shared storage/log
code and +3 to controller code. There is no pkg/store production delta.
Combined store production remains +3,225 against main, **7,475 above** its
-4,250 budget. The overage remains unjustified. Store-data retirement is
incomplete.

Removed the physical-manifest/listing reader: logseg.Open, Log, Objects,
SealAbandoned, Assess, ReasonRunnerLost and SealedByController. Their methods,
listing discovery, partial-success corruption path, physical abandoned seals
and manifest-derived completeness are deleted. Bounded object reads and gzip
verification remain private helpers for CommittedReader and upload verification.
Controller object adapters now require only SegmentObjects. Unsealed-tail grace
is evaluated at the controller's persisted reader. The exported snapshot,
source docs, migration guide, changelog and generated docs mirrors are updated.

Invariant: only currently authorized persisted segment records select readable
bytes. Arbitrary physical manifests and extra objects cannot add bytes or
bypass refusal. FuzzCommittedReadsUseOnlyAuthorizedRecords passed 20,379 cases.
It uses fake record authorization and real compressed reads. It is not provider
authentication evidence.

Ports: sink size/interval cuts, filters, truncation, upload loss and corruption
now read through CommittedReader with fake records. Corruption returns an error
before the bad segment. Killed-writer coverage checks committed bytes and an
unfinished stream. The former physical controller-seal race is covered through
TestLogPersistenceMaintenanceRespectsFinalTailGrace, strengthened to refuse a
late final writer after the real maintenance seal. Grace boundary cases now run
through TestCommittedDashboardUnsealedLogGrace. Object-manifest subject checks
retire with the reader; persisted subject substitution and the generated
record-only property cover the retained boundary. The removed tail GET-count
assertion described the unused manifest reader's optimization.

Verification:

- Whole-repository go build -p 4 ./... passes.
- go vet -p 4 ./pkg/store ./pkg/storage/logseg ./pkg/controller passes.
- The complete pkg/storage/logseg suite passes.
- TestCommittedDashboard* passes, including the final SQLite award/start,
  segment commit, terminal transition and revoked-reader integration.
- TestLogPersistence* and seven preserved store-data acceptance tests pass:
  TestStoreCASIntegrity, TestStorePresenceReadSet, TestStoreLargePresenceBudget,
  TestStoreCacheScope, TestStoreReferenceEdges, TestStoreReachabilityRetention,
  TestStoreBillingRetention. These cells were already implemented.
- Four existing storage-log-segments-and-fallback cells pass in 28.122s:
  filesystem-records, s3-records, filesystem-sink and s3-sink. These join real
  storage components and SQLite records with fixture authority. No native
  acceptance cell is newly unskipped.
- Both docs/changelog mirror guards pass. The generated logseg API snapshot
  matches. The full generated snapshot comparison also reports preexisting
  pkg_store.txt drift; no store production source or store snapshot changed.
- commentcheck -base main is clean across 2,125 files. git diff --check passes.
- An initially broad TestReader filter also selected three unrelated tests:
  TestReaderAndEditorCannotInvite, TestReaderCannotMintRunnerTokens and
  TestReaderMemberTokenOnlyReads fail on the removed tokens table. Replacing
  every changed Go file with HEAD through a Go overlay reproduces all three.
  These are legacy fixture failures, not passing acceptance evidence.

Contract and integration work still open:

- W1: merge persisted output/manifest selectors and materialized-result identity
  into this checkout. NodeRow and nodeReadColumns here still lack selectors.
  W1's newer report describes them on its branch. CopyMaterializedRecords needs
  those committed selectors before W1b can implement the real copy. W2/W3/W9
  must carry selection to authorized readers. The legacy local output/report
  and remote upload/read clients still have scheduler and rehydrate callers.
  They cannot be deleted by dropping their interfaces alone.
- W2: PaymentEvent here still contains only checkout paid/expired/failed facts.
  Extend its verified card-setup/refund/warning/dispute facts and correlation.
  W1 must extend payment_events beyond checkout correlation and its three-kind
  constraint. W1b's transaction-local mutation owners remain ready. No foreign
  event type or schema was changed.
- W1: provide per-award exhaustion anchoring and a committed renewal result.
  accountUnitBillingTx still returns only error, while renewals build LeaseBeat
  before accounting. Legacy node anchors cannot fence every billing unit.
  Spend/grace and legacy claim-credit retirement remain open.
- W1: imported terminal-attempt seeding remains absent. CausePublish supports
  pending-to-terminal but TransitionExtra.Attempt is start-only. The seven
  relocated store tests still use teststore.SeedPresence's direct fixture SQL.
  The approved catalog removals were already completed and were not repeated.
- W2/W3: supply the authenticated LogTransport/SegmentRecorder and persisted
  logseg.Data to the actual host. podNode.logs still has no constructor binding.
  Physical allowance eviction, source-before-run binding and the
  ReadBlob/ChargeDownload composition remain separate joins.

Shared production files: pkg/storage/logseg/reader.go, manifest.go and doc.go;
pkg/controller/log_reads.go and log_segments.go. Shared tests: the logseg
committed, manifest_subject, memory and sink tests, plus controller log_reads.
The owned store edit is log_maintenance_test.go. Shared prose/generated files:
pkg/storage/logseg/README.md, .apidiff/pkg_storage_logseg.txt, CHANGELOG.md,
docs/migrations/_unreleased.md and their two pkg/docs mirrors.
No W1 implementation or lead-owned handoff/amendment changed.

No new mechanism, pricing decision, restriction, service or compatibility API
was added. No Git writes or candidate Sparkwing runs occurred. Git config has
no local user identity or core.worktree override. PostgreSQL, native hosts,
provider HTTP and production signing/billing integration remain unverified.
Worker scratch is deleted at completion; /tmp/sw-gocache is retained.

STATUS: CONTINUE
