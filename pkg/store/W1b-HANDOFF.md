# W1b handoff

Continues 55eb66915. W1b remains incomplete. The lead owns commits, independent
review, pipelines and native integration. Prior implementation and reports remain
in Git history and the swarm's rewrite-logs directory.

## This continuation

The first two lead handoffs were already committed and were not repeated. The
shared CommitRecord/CopyMemo mutation owner and its authority property remain
intact. This continuation advances the third handoff by retiring the independent
bucket-listing storage pass and its consumers.

Removed six public Store APIs: StorageMarks, ReconcileStorage,
TeamsOverFreeLogShare, PrunableNodeLogs, PruneStorageCommitReceipts and
PruneDownloadDays. TeamOverShare, lockCommittedTx, everHeldCredits, the one-day
receipt horizon and the S3/filesystem log measurement helpers are also removed.
The controller no longer has WithStoragePass, RunStoragePass, StoragePassEvery,
its background loop, health projection or free-share log-pruning algorithm.
Controller startup no longer configures that pass. The existing maintenance
timer continues to use the singleton maintenance lease, final retention,
tagged-stream sealing and committed-inventory billing.

Deleted 482 production Go lines and added one replacement GoDoc line, for a net
481-line production reduction. Removed 412 legacy test lines, added five replacement lines
in retained tests, and added the final-inventory controller test plus the minimal
31-line S3 fixture still needed by the legacy direct-upload test. The fixture no
longer builds a cache store or listing hook. No schema, billing price, minimum,
quota, flag, setting, environment variable or compatibility mechanism was added.
Allowance-driven physical eviction remains an integration obligation from C1.

TestStorageMaintenanceUsesFinalInventoryAndLeavesReplicaUsage verifies that the
real controller maintenance path completes without errors on the final schema,
bills committed blob inventory despite stale raw counters, and preserves summed
replica usage. The old listing-reconciliation, one-day receipt-pruning and
free-share deletion tests were retired with their algorithms. Existing store
proofs retain atomic upload replay, retained inventory, concurrent-client,
rollback and restart coverage. No archived T1 source row is newly closed and no
native acceptance cell was activated.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W1b/. Use
`go -C /home/bombuntu/.xwing/envs/1088/sparkwing`. Builds and tests use -p 4.

- Whole-repository go build ./... passed.
- go vet ./pkg/store/... ./pkg/controller/... ./internal/acceptance/...
  ./tests/acceptance/... passed.
- W1b selection: 199 top-level store tests / 607 passing cases, zero skips or
  failures, in 74.546 seconds. This is not a full-store suite pass.
- Five controller tests passed: the new inventory test, cloud default retention,
  explicit self-hosted retention, the maintenance lease refusal/takeover test,
  and the already-removed egress endpoint test.
- TestStorage and TestBilling passed 2 top-level tests / 43 cases, including all
  12 existing store-data-sqlite proofs. The 562 native/platform skips remain.
- Required commentcheck -base main passed across 1012 files. sleepcheck -base HEAD
  and git diff --check passed.
- cmd/apidiff regenerated every package into scratch. All generated files match
  .apidiff. Only this cut's entries changed in the committed snapshots.
- The nine focused changelog/mirror tests passed. Root and embedded changelog
  and migration sources match.

Store selection:

```text
^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore|TestStorage|TestBilling|TestGrantCredits|TestCreditGrant|TestCreditReversal|TestCreditHistory|TestCreditsBalanceIs|TestCreditsDefaultTenant|TestTenantSQLScope|TestTenantWriters|TestTenantTwins)
```

Use `go test -json -p 4 ./pkg/store -run '<selection>' -count=1 -timeout=240s`.
Acceptance uses `go test -json -p 4 ./internal/acceptance/... ./tests/acceptance/...
-run '^(TestStorage|TestBilling)$' -count=1 -timeout=240s`.

Controller selection:

```text
^(TestStorageMaintenanceUsesFinalInventoryAndLeavesReplicaUsage|TestAMultiTeamStoragePassSeedsRetentionAndReleasesOldRuns|TestASingleTeamControllerKeepsItsOperatorsRetention|TestStorageRetentionPassRequiresMaintenanceFence|TestLegacyEgressTotalsEndpointIsGone)$
```

Docs selection:

```text
^(TestEmbeddedChangelogMatchesRoot|TestPkgDocsContentMatchesDocsRoot|TestReadChangelogTopicReturnsBody|TestListIncludesChangelogTopic|TestSearchFindsChangelog|TestChangelogSection_|TestChangelogHeadingMatches)
```

The first controller compile found an unused io import left by deleting its
measurement test. Removing that import fixed compilation. Full pkg/docs testing
cannot run because TestWebClient_Versions_HappyPath opens a forbidden socket.
A broader docs selection also failed TestDocsNameOnlySymbolsThatExist on 17 SDK
symbols in inherited reference pages. Those pages and SDK exports were not
changed here. Neither result is claimed as a pass. No full controller, full docs,
race, PostgreSQL, provider, physical-backend or native-platform pass is claimed.

## Amendment audit and remaining work

- A1-24: transaction-owned data authorization and binding proofs remain.
  W2 DataController.DataAuthority and BindData still need real adapters.
- A1-27: issuance/start/renew/release/reap/finish billing joins remain open.
  authority_issue.go still refuses metered roots and persists unmetered awards
  through W1's separate writer. C2 SettleBilling remains a panic stub. W1/W2 must
  specify how an unstarted at_award award refunds its automatically opened
  interval, and how hosts explicitly report customer-runner waits. RenewUnit's
  executing flag records first execution, not pause/resume. No new semantics were
  inferred here. Accounting proofs continue to use contract calculator fakes.
- A1-28: committed-checkout settlement remains implemented. W2 owns verified
  provider events and removal of caller-selected paid grants.
  VerifiedPaymentEvent.Event and provider verification remain stubs.
- A1-29 through A1-35: upload tickets, exact tagged receipts, provenance,
  manifests/members, reference authorization, depcache, tombstones and tagged
  logs remain implemented behind contracts. W7 RecordNamespace and AuthorizeEdge
  remain stubs. Store proofs use contract fakes. Physical verification/deletion,
  verified manifest-reader configuration, GC, trusted replica identity, binary
  execution policy, signed transfers and exact proxy egress remain integration
  work. This continuation removes raw bucket reconciliation and its pruning
  path. It does not complete allowance-driven physical eviction.
- Legacy claim-credit, principal-selected event counters, storage reservation
  and download counters, output objects, direct uploads and node-log transport
  still need complete hard cuts with their callers. The old data_download.go
  handler combines direct source, artifacts and binary signing through cache
  grants. Do not remove only its binary lookup and strand direct-source callers.
  Source identity for never-executed materialization and imported owner-log
  identity still need the previously requested cross-area contracts.
- A1-53: all native placement, real provider/backend and PostgreSQL acceptance
  evidence remains with the lead and integrating areas.

W1's bucket_measure_lease.go is untouched. Its now-unused RunStoragePassLeased
wrapper and storage.pass keys remain for W1 to remove. Its reaper and bucket
measurement concerns were outside this cut. The legacy expired-claim accounting
helper was already removed before this continuation and was not recreated.

No foreign Go contract changed. C1 documents the removed surface. The first two
lead items remain done. The third remains in progress.

## Shared files and cleanup

Owned store changes: team_storage.go, node_logs.go, team_storage_test.go,
STORE-DATA.md and this handoff. The only W1 test edit removes five SQL-review
exemptions for deleted functions. Its guard passes. No W1 runtime file changed.

Minimal production caller removals outside pkg/store:
cmd/sparkwing-controller/main.go and controller handlers.go, server.go,
storage.go, log_segments.go, plus deletion of storage_pass.go. Shared fixtures:
direct_upload_test.go, log_segments_internal_test.go, the extracted
direct_upload_bucket_test.go, the new storage_inventory_internal_test.go and
deletion of storage_pass_test.go. No internal/blob, internal/cache, host,
 protocol or SDK implementation changed.

Shared documentation: contracts/c1-data.md, contracts/t1-port-inventory.md,
.apidiff/pkg_store.txt, .apidiff/pkg_controller.txt, CHANGELOG.md,
docs/migrations/_unreleased.md and their two embedded copies. The report names
all caller migrations. Product docs state that physical eviction is unfinished.

No service, candidate installation, provider call, deployment, release, Git write
or harness-memory write ran. Bootstrap discovery ran installed sparkwing info
before the brief was loaded. No Sparkwing command ran after the brief. There is
no local Git identity or core.worktree override. Scratch and owned check processes
are removed before reporting. The lead retains committing, review and pipelines.
