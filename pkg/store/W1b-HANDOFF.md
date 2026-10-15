# W1b handoff

Continues 2c5b5a7d2 and W1b-session1792067157.out. W1b remains incomplete.
The lead owns staging, the merge commit, review, pipelines and native integration.

## Merge resolution

All six conflict contents are resolved. The index remains unmerged because
SWARM-BRIEF forbids worker Git writes. The lead must stage the resolutions.

- `design/rewrite/contracts/handoffs.md` is the exact stage-three integration version.
- `t1-port-inventory.md` also takes the integration version. It already contains
  the W1b egress/caller inventory once, after W1's inventory.
- The migration source retains W1b's sections plus the merged integration sections.
  The embedded mirror is regenerated from the source.
- The controller-client and storage API snapshots drop the retired
  SetNodeArtifactManifest entries, matching their merged implementation.

No amendment file or W1 implementation file is edited.

## Implemented and deleted

Removed the unused Store exports `BinaryObject`, `CommittedObjectFor` and
`SourceBoundToRun`. They had no production callers. Binary and provenance
lookups use authorized logical blob records rather than `data_objects`.
This deletes 84 production lines and 212 test lines. Production Go shrinks
by 57 lines after the data-authorization fix.

Four tests retire with that removed key-based resolver:
TestCloudBinaryLookupRefusesLocalProvenanceUntilTeamTrustsIt,
TestSameBinaryCanBeCommittedByLocalAndCloudRunners,
TestCloudBinaryLookupNeverReturnsLocalDuringCloudExpiry, and
TestClaimBinaryCacheIsScopedToItsRefAndImmutable. No archived T1 source identity
or native binary-provenance cell is newly claimed. W7 still owns final binary
execution policy. The retained direct-upload tests cover paths still present.

The W2 logical-record handoff is fixed. `CheckDataAccess` resolves persisted
read authority for named DataLookup selectors before a digest is selected.
Remote-cache selection still refuses executable binaries and source bundles.
The selector-only DataRecord precheck is limited to memo copying under a live
job with both read and write grants and declared memoization. Downloads,
presence, uploads and ordinary record publication keep their digest checks.
Actual record selection, memo copying and member-edge authorization remain in
the existing transaction owners.

Invariant: a selector precheck grants no byte or reference authority. The new
regression first failed with `invalid blob digest`. It now checks selector-only
lookup/copy, strict byte operations and an unrelated job's final record refusal.
A 96-case property checks persisted scope against forged hints, revocation,
expiry, tenant substitution, remote binary/source selection and write attempts.
The record tests use contract fakes for W7 namespace/edge behavior.

The existing public ledger report/unstarted release regression now calls C2's
real SettleBilling calculator. C2's calculator is implemented on this merged
branch. It is no longer a panic-stub integration dependency. W1's lifecycle
implementation and its separate test calculator are unchanged here.

## Verification

Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
and GOCACHE/GOTMPDIR/TMPDIR under `/tmp/sw-astra-W1b/`.

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for store, controller and both acceptance packages.
- The W1b store selection passes 204 top-level tests / 621 cases with no skips.
- The migrated ledger report/release test and C2's operational classification,
  billing minima and settlement tests pass with the real calculator.
- Storage, billing and cache HTTP acceptance selections pass. Thirteen existing
  SQLite cells pass. There are 562 skipped native/platform cases. No new native
  cell is unskipped.
- Commentcheck against main, gofmt, diff whitespace, changelog, docs mirrors and
  the complete generated API snapshot comparison pass.

Store selection:
`^(TestBlob|TestLogPersistence|TestLogPublication|TestLogMaintenance|TestStoreData|TestRetainedStorage|TestPaymentSettlement|TestMeteredAccounting|TestMeteredUnit|TestTenantSQLScope|TestTenantWriters|TestTenantTwins|TestGrantCredits|TestCreditGrantRequest|TestCreditsBalance|TestCreditReversal|TestPayment.*(Cap|Spend))`

Acceptance selection:
`^(TestStorage|TestBilling|TestDataCacheHTTP)` in internal/acceptance and tests/acceptance.

Passing SQLite cells:

- billing-start-award-versus-start/store-sqlite
- billing-ledger-survives-retention/store-data-sqlite
- storage-cas-integrity/store-data-sqlite
- storage-replica-safe-accounting/store-data-sqlite
- storage-quota-settlement-crash-replay/store-data-sqlite
- storage-log-segments-and-fallback/store-data-sqlite
- storage-hasmany-read-set/store-data-sqlite
- storage-hasmany-large-budget/store-data-sqlite
- storage-reachability-and-retention-tails/store-data-sqlite
- storage-memo-and-depcache-scope/store-data-sqlite
- storage-reference-edge-laundering/store-data-sqlite
- storage-publish-one-way/store-data-sqlite
- storage-owner-receipts-and-stream-verification/store-data-sqlite

A supplementary `Test.*Billing.*` regex was too broad. It ran two retained
legacy fixtures that fail before the changed behavior:

- TestLaunchBilling_ReleaseRefundsAndTheFirstBeatBills fails at tokens insertion
  because the final schema has no tokens table.
- TestTriggersAreNeverRefusedForBilling fails at trigger insertion because the
  final triggers table has no pipeline column.

The narrowed real-calculator checks pass. These legacy fixtures remain part of
the unfinished claim/trigger cutover. No full package, PostgreSQL, physical
backend, provider, native-placement or race pass is claimed.

## Assignment audit and remaining work

- The approved record-mutation owner, two output catalog removals, retired
  expired-claim helper and explicit blobResultFixture attempt remain done.
- A1-24: store authority/binding/data-edge contracts remain implemented. The new
  selector fix closes W2's named logical-record request. W2's DataAuthority and
  BindData implementations now exist. Concrete ResolveAuthority, ReadBlob and
  ChargeDownload callback wiring and physical adapters still need integration.
- A1-27: W1's issuance/start/renewal/retry/finish/release/reaper join remains
  implemented and passes its selected tests. C2's production calculator is now
  exercised by the ledger/release regression. Spend-exhaustion/grace enforcement
  and legacy trigger/node credit-window retirement remain W1b work. W3 must
  report actual owner compute, including false during customer-runner waits.
- A1-28: committed-checkout settlement and raw-paid-grant refusal remain done.
  Verified provider and production deployment evidence remain W2/T1 work.
- A1-29 through A1-35: store records/members, read sets, tagged uploader receipts,
  immutable upload tickets, memo/depcache pointers, tombstones and tagged log
  persistence remain implemented. W7 namespace/edge, signed-store, binary policy,
  log transport and physical backend implementations remain integration points.
- A1-53: native/provider/PostgreSQL/physical-backend cells remain unverified.

Next cutovers still include ReserveUpload/CommitUpload, ReleaseStorage, old
output objects and old node-log transport. Their live controller/client and
legacy lifecycle callers remain. Final upload tickets, logical records and
segment persistence already supply the replacement store contracts.
Allowance-driven physical eviction and final manifest-reader setup remain open.

No new foreign contract change is requested. The existing free-slot question
remains: final tickets have no server-verified intended use before signing,
while legacy small outputs can bypass a full slot pool. The lead/C1/W7 must
supply intended-use authority or an explicit policy decision. No slot gate or
new customer restriction is added here.

## Shared files and friction

No production caller outside pkg/store changes in this session. Shared paths
are CHANGELOG.md, docs/migrations/_unreleased.md, pkg/docs/changelog.md,
pkg/docs/mirror/migrations/_unreleased.md, and API snapshots for store,
controller client and storage. Handoffs and T1 inventory change only through
the required integration-side conflict resolution. W1b implementation paths
are blob_data_access.go, blob_selection_access_test.go, direct_upload.go and
its test, credit_totals_runtime_test.go and this report.

Friction: W2's preflight selected no digest, while C1 required one. The new
selector path fixes that mismatch without moving record/member authorization.
The supplementary billing regex also reached retained pre-rewrite fixtures.
The original failure and narrowed verification are both recorded above.

No Git write, candidate installation, pipeline, service, deployment, release or
harness-memory write ran. The lead still owns the six unmerged index entries.
The shared Git configuration has no local identity or worktree override.
Session scratch is deleted after all checks.

STATUS: CONTINUE
