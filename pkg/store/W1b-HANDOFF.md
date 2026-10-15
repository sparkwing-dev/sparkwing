# W1b handoff

Continues ce4cf5103 after integration merge 6792df2cf. The worktree started
clean. W1 implementation files and lead-owned handoffs/amendments are unchanged.

The 07:40 payment priority remains blocked on missing foreign contracts. The
09:40 harness cleanup advances below. No billing, schema, pricing or authority
rule changed in this session.

Implemented and deleted:

- Moved replica accounting, quota races, reservation release, singleton election
  and download-cap reopening into four TestReplica tests in pkg/store. They use
  pkg/store/internal/storetest targets and the selected suite dialect. Quota
  setup now calls ReserveQuota and SettleQuota instead of inserting counters.
- Moved the upload crash/replay proof into TestBlobUploadCrashReplay. It uses
  the existing upload fixture and disk-store helper. Four child processes exit
  without closing their database before/after commit and abort. The parent
  reopens and checks ticket state, all three quota dimensions, unique-byte
  usage, duplicate commit, abort replay and remaining admission capacity.
  The fixture's clock and physical-verification fake remain explicit.
- Removed production harness duplicates for events, owner receipts, job logs,
  abandoned logs and published logs. Existing package tests retain their
  assertions. Log persistence, maintenance and publication tests now reopen
  their database before the historical-read assertions. Publication also
  checks retained bytes and that operational/financial row counts do not grow.
  The existing owner-ticket test now removes fixture jobs before uploading,
  reopens the database and checks that no job was manufactured.
- Deleted seven internal/acceptance source files containing 1,021 lines:
  store_abandoned_logs, store_download, store_event, store_logs,
  store_owner_receipts, store_published_logs and store_upload. The remaining
  store_data dispatcher loses 196 net lines. W7 still needs the existing log
  and publication seed helpers, so their 141 lines move unchanged into
  blob_log_fixtures.go and blob_publication_fixtures.go. The net non-test Go
  reduction is 1,076 lines. No legacy runtime API is claimed retired here.
- Removed the acceptance test's obsolete subprocess driver and its callback
  argument. C1 documentation names the package tests that now own the proofs.
  No new acceptance cell, archived test port or scenario-registry entry was added.

Deleted-check mapping:

- store_data accounting and store_download -> TestReplicaUsageSurvivesReopening,
  TestReplicaQuotaReservationSerializesAndReleases,
  TestReplicaMaintenanceLeaseHasOneWinner and
  TestReplicaDownloadCapSurvivesReopening.
- store_upload crash proof -> TestBlobUploadCrashReplay. The physical backend
  still supplies contract verification evidence, just as before the move.
- store_event -> TestStoreDataEventsShareCapacityWithUploadReservations,
  TestStoreDataEventsTenantIsolationAndConservation,
  TestStoreDataEventsConcurrentCapsAndKindValidation,
  TestStoreDataEventsMonthAndRunUsageSurviveRestart and
  TestStoreDataEventsUseFinalReplicaRows.
- store_owner_receipts -> TestBlobOwnerReceiptCannotTransferToAJob,
  TestBlobUploadOwnerTicketsRetainUnitIdentity,
  TestBlobUploadCommitRechecksTicketAndAuthority and the TestStoreDataBuildTrust
  properties. These retain tagged identities, verification refusal, replay,
  reopening, local trust and refusal to borrow an owner's receipt or ticket.
- store_logs/store_abandoned_logs -> TestLogPersistenceRetainedBytesCountSegmentsOnce,
  FailureRollsBackWholeCommit, CompressedInventory, AttemptAuthorityDoesNotTransfer,
  LaterNodeFinishCannotExtendAttemptGrace, FinalEmptySealAndReplay,
  SealedReplayStillChecksAncestry, ReadDoesNotBorrowSealedReplayGrace and
  MaintenanceRespectsFinalTailGrace, with the TestLogPersistence prefix on each.
- store_published_logs -> TestLogPersistencePublishedAttemptsNeedNoExecutionLease,
  TestLogPersistencePublishedAttemptsRecheckAuthority and
  TestLogPersistenceMaintenancePreservesPublicationAndFinalSeals.

Contract changes and real integrations still needed, in priority order:

1. W2's internal/capability/payment.go, including its latest branch at
   3656dec97, still exposes only checkout paid/expired/failed. The opaque proof
   cannot carry setup, collection, refund, warning or dispute evidence. W2 must
   supply verified provider identities and outcomes. W1 must supply the matching
   financial schema and trusted setup/customer correlation. payment_events still
   requires a checkout FK and restricts kind to the three checkout outcomes.
   SettlePayment cannot accept a substitute unverified event or provider-metadata
   payer. W1b will dispatch the verified event and financial mutation in the same
   receipt transaction once those contracts exist. Existing Operator calls that
   open their own transactions cannot be called from that transaction.
2. W1's accountUnitBillingTx calls the working accounting owner but returns only
   error. Spend/grace still needs W1's committing policy/renewal result and an
   exhaustion fence that preserves each award's prepaid runway, customer-runner
   waits, unit minima and holder drain. The production C2 calculator is implemented
   and is already the default. Earlier wording suggesting it was missing is
   superseded. Legacy claim-credit callers still prevent deleting their helpers.
3. Output clients still use OutputReadGrant/NodeOutputObject and unbound
   Store.GetNodeOutput. W2/W3/W9 must bind the final authenticated reader and
   selected record key. W1's materialization source can be attempt-free, while
   ResultRecordKey and write authority still require a positive attempt. W1/W7
   must settle that destination/read-edge contract. CopyMaterializedRecords
   remains supplied only by tests. W1b must join it to the transaction-local
   record mutation owner after that contract exists.
4. The legacy LogSegmentUploader still calls retired writer routes. Controller
   reads still select node_logs, which the final schema does not contain.
   Production SignSegments/VerifyUpload implementations of blob.LogTransport
   remain absent. W2/W9 own the authenticated transport and W3 the persisted
   tagged subject. Final store persistence and logseg byte implementations exist.
5. The 09:40 cleanup remains incomplete. W1b harness files still present are
   store_billing, store_cache_scope, store_cas, store_data, store_gc_fence,
   store_presence, store_presence_large, store_reachability and
   store_reference_edges. Some seeds also support W7's remaining physical-backend
   harness. The two relocated blob fixture files retain that dependency without
   copying or widening their authority. W1 owns store_unit_billing and the other
   lifecycle harness files. The lead owns catalog/skeleton cleanup and can update
   old t1-port-inventory proof paths from the mapping above.
6. Source-before-run binding, ReadBlob/ChargeDownload composition, physical
   allowance eviction and native/provider/PostgreSQL evidence remain integration
   obligations. A1-24 and A1-29 through A1-35 persistence stays implemented.
   A1-27 policy and A1-28 expanded payment settlement remain open.

Verification:

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W1b/gotmp.
Build, vet and tests used -p 4. Passed:

- go build -p 4 ./... for the whole repository.
- go vet -p 4 ./pkg/store ./internal/acceptance ./tests/acceptance.
- 42 selected store tests / 178 passing cases, 13.438 seconds. Selection:
  ^(TestReplica|TestLogPersistence|TestBlobOwnerReceipt|TestBlobUploadOwner|TestBlobUploadCommitRechecksTicketAndAuthority|TestBlobUploadCrashReplay|TestStoreDataEvents|TestStoreDataBuildTrust)
- After removing the owner fixture's jobs, the focused
  TestBlobUploadOwnerTicketsRetainUnitIdentity rerun passed both trigger and
  plan cases, 0.148 seconds.
- Seven existing TestStorage/.../store-data-sqlite cells, 1.677 seconds:
  storage-cas-integrity, storage-hasmany-read-set, storage-hasmany-large-budget,
  storage-reachability-and-retention-tails, storage-memo-and-depcache-scope,
  storage-reference-edge-laundering and storage-local-remote-cache-opt-in.
  Use -run '^TestStorage$/^storage-(cas-integrity|hasmany-read-set|hasmany-large-budget|reachability-and-retention-tails|memo-and-depcache-scope|reference-edge-laundering|local-remote-cache-opt-in)$/^store-data-sqlite$'.
- go run ./internal/commentcheck -base main . was clean across 2,116 files.
- git diff --check.

Five former SQLite wrapper proofs now live in package tests: replica accounting,
quota crash replay, log persistence, publication logs and owner receipts. Their
old TestStorage cell paths are removed. No native placement, provider, PostgreSQL,
whole-store suite, Sparkwing binary or pipeline pass is claimed.

Budget and shared files:

The lead's production-Go metric remains +3,645 lines for pkg/store versus main,
7,895 above the combined -4,250 target. This session's production deletion is in
the acceptance harness, which that metric already excludes. No new runtime
mechanism needs an overage exception. Legacy lifecycle/credit/output/log cuts
remain necessary before completion.

Shared edits are design/rewrite/contracts/c1-data.md, the W1b-owned harness
removals/dispatcher, the two relocated W7 seed files and the minimal caller
migration in tests/acceptance/storage_test.go. No other package's runtime caller
changed. No W1 implementation file changed. The lead owns review and commits.

Friction: an initial acceptance selector ran no tests. The corrected slash-split
selector above executed all seven cells. A migrated publication assertion first
expected zero billing rows, but the shared issued-award fixture already contains
one. It now compares the before/after inventory, preserving the intended
no-new-units assertion. Final selected checks pass. Sandbox/native limitations
remain unchanged. No service or child process remains from these tests.

Scratch /tmp/sw-astra-W1b is removed after final checks. /tmp/sw-gocache is kept.
No Git metadata or harness memory was written. Git author remains Korey Gambill
and core.worktree is unset.

STATUS: CONTINUE
