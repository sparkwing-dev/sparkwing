# W1b handoff

Continues 0b8b18c03. W1b remains incomplete. The lead owns commits, independent
review, pipelines and native integration. Earlier reports remain in Git history.

## This continuation

The first two lead handoffs were already committed. The shared record mutation
owner and catalog removals were not repeated. This continuation advances the
third handoff by removing the service-token storage counter transport and the
rolling-reservation API it used.

Removed all four public counter routes: `/internal/storage/reserve`,
`/internal/storage/commit`, `/internal/storage/release` and
`/internal/downloads/charge`. Their request types, authentication helpers,
handlers and route registrations are deleted. `Store.RenewStorage`,
`storageRenewRequest`, `recordStorageRenewTx` and `replayStorageRenewTx` are deleted.
The retained download-cap builder and caller-standing projection moved from
storage_counters.go to storage_standing.go without changing their API.

The removed routes cannot mutate any tenant's accounting, regardless of the
presented credential, selected tenant or byte delta. The registered-router fuzz
property ran red before removal and passed 129 executions afterward.

The two retained signup-source tests now use the final SQLite schema without a
listener. They preserve full-tier and unreadable-source refusal and the server's
own signup-condition projection. They no longer mint a cache counter token.
Porting them exposed SetFreeTeamSlots writing the removed sparkwing_meta table.
It now uses the existing final-settings writer. Negative or sealed updates leave
the policy unchanged. The new store test checks persistence after reopening and
sealed-write refusal. Native sign-in remains separate integration evidence.

Deleted 396 production Go lines and added 63, including moved declarations, for a
333-line net reduction. Deleted 488 test lines and added 148, including retained
fixtures and replacement proofs, for a 340-line net reduction. The old rolling
block growth, renewal replay and renewal lock-order tests are removed with their
mechanism. The surviving direct-upload slot test retains its upload assertions.
No new archived T1 source row or native acceptance cell is closed.

No schema, price, minimum, quota, flag, setting, environment variable, compatibility
mechanism or foreign Go contract was added. The existing slot setting changed its
persistence owner. Final upload-ticket tests retain scoped quota admission,
immutable replay, concurrent-client, abort and restart evidence.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W1b/. Use go -C with this worktree's
absolute path. Builds, tests and vet use -p 4.

- Whole-repository go build ./... passed after the source and embedded-doc changes.
- go vet ./pkg/store/... ./pkg/controller/... ./internal/acceptance/...
  ./tests/acceptance/... passed.
- Final W1b selection passed 200 top-level store tests / 608 cases, with zero
  skips or failures, in 157.448 seconds. The complete generated record-mutation
  authority property is included. This is not a full-store suite pass.
- Eight controller top-level tests / 11 cases passed without skips. These include
  the retired-route fuzz seeds, both signup-source ports and the five retained
  maintenance/retirement checks. A separate generated fuzz run passed 129 cases.
- TestStorage and TestBilling passed 2 top-level tests / 43 cases, including all
  12 existing store-data-sqlite proofs. The 562 native/platform skips remain.
- All internal/apiref and internal/apispec tests passed: 28 top-level tests /
  46 cases. The curated OpenAPI source lost the four retired operations, then
  its generator produced identical output. The generated API page lost four rows.
- Nine focused changelog/mirror tests passed. bin/sync-docs.sh --check passed.
- commentcheck -base main passed across 1014 Go files. sleepcheck -base HEAD and
  git diff --check passed.
- cmd/apidiff regenerated snapshots in scratch. Every generated snapshot matches
  its stored counterpart after the one RenewStorage entry was removed. The
  pre-existing orphan .apidiff/pkg_runner.txt has no generated package and is
  unchanged.

Store selection:

```text
^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore|TestStorage|TestBilling|TestGrantCredits|TestCreditGrant|TestCreditReversal|TestCreditHistory|TestCreditsBalanceIs|TestCreditsDefaultTenant|TestTenantSQLScope|TestTenantWriters|TestTenantTwins)
```

Use go test -json -p 4 ./pkg/store -run '<selection>' -count=1 -timeout=240s.
Acceptance uses go test -json -p 4 ./internal/acceptance/... ./tests/acceptance/...
-run '^(TestStorage|TestBilling)$' -count=1 -timeout=240s.

Controller selection:

```text
^(FuzzLegacyStorageCountersAreGone|TestSignUpFreeTierClosesWithTheLastSlot|TestTheServerIsItsOwnFreeTierSource|TestStorageMaintenanceUsesFinalInventoryAndLeavesReplicaUsage|TestAMultiTeamStoragePassSeedsRetentionAndReleasesOldRuns|TestASingleTeamControllerKeepsItsOperatorsRetention|TestStorageRetentionPassRequiresMaintenanceFence|TestLegacyEgressTotalsEndpointIsGone)$
```

The generated router run uses go test -p 4 ./pkg/controller -run '^$'
-fuzz '^FuzzLegacyStorageCountersAreGone$' -fuzztime=128x -parallel=4 -timeout=120s.

The route property first failed because the registered endpoints returned 401.
The signup ports first failed because sparkwing_meta no longer exists. Both
causes were corrected in place. The OpenAPI generator initially refused removed
request types until their curated operation blocks were deleted. These failures
are not represented as passes.

No full-store, full-controller, full-docs, race, PostgreSQL, provider, physical
backend or native-platform pass is claimed. Pipelines and independent review
remain with the lead, as required by the swarm brief.

## Amendment audit and remaining work

- A1-24: transaction-owned data authorization and binding proofs remain intact.
  W2 DataController.DataAuthority and BindData still need real adapters.
- A1-27: issuance/start/renew/release/reap/finish billing joins remain open.
  authority_issue.go still refuses metered roots and persists unmetered awards
  through W1's separate writer. C2 SettleBilling remains a panic stub. Existing
  BillingRules require full unstarted refunds, but at_award auto-opened intervals
  do not distinguish actual first execution or customer-runner waits. W1/W2/W3
  still need the explicit execution/wait transport before that join can preserve
  the rule. Accounting proofs continue to use contract calculator fakes.
- A1-28: committed-checkout settlement remains implemented. W2 owns verified
  provider events and removal of caller-selected paid grants.
  VerifiedPaymentEvent.Event remains a stub.
- A1-29 through A1-35: upload tickets, tagged receipts, provenance, record/member
  persistence, read-set authorization, memo/depcache, tombstones and tagged logs
  remain implemented behind contracts. W7 RecordNamespace and AuthorizeEdge are
  still stubs. Store proofs use contract fakes. Physical verification/deletion,
  verified manifest-reader configuration, GC, trusted replica identity, binary
  execution policy, signed transfers and exact proxy egress remain integration
  work. Allowance-driven physical eviction is still open.
- The counter transport is removed. internal/storagequota.Client and
  internal/cache still contain W7's old RPC consumers. They receive no fallback.
  W7 must retire them with the cache service. Final DataBinding is the contract
  for their replacement. No foreign implementation was substituted here.
- Legacy claim-credit, principal event/download counters, raw storage helpers,
  output objects, direct uploads and node-log transport still need their full
  consumer cutovers. Direct-upload and log paths retain reserveStorageTx,
  commitStorageTx and the old reservation tables. StorageStanding still reads
  those legacy rows. The old data_download.go combines direct source, artifacts
  and binary signing through cache grants. Removing only its binary lookup would
  strand direct-source callers. Never-executed materialization source identity
  and imported owner-log identity still need their earlier cross-area contracts.
- A1-53: native placements, real provider/backend and PostgreSQL evidence remain
  with the lead and integrating areas.

W1's implementation files were not edited. No foreign contract change is newly
requested. The first two lead items remain done. The third is in progress.

## Shared files and cleanup

Owned store changes: team_storage.go, free_tier.go, credit_settings_runtime_test.go,
team_storage_test.go, free_tier_test.go, STORE-DATA.md and this handoff.

Production caller removal outside pkg/store: controller/server.go and deletion
of controller/storage_counters.go, with its surviving declarations moved to
controller/storage_standing.go. Test caller migrations: deletion of
controller/storage_counters_test.go, the retained signup proofs in
controller/free_tier_source_test.go, the unchanged freeTeamToken fixture moved
to controller/team_download_test.go, and the router property in
controller/store_data_retirement_internal_test.go.

Shared documentation: contracts/c1-data.md, contracts/t1-port-inventory.md,
.apidiff/pkg_store.txt, api/openapi.yaml, CHANGELOG.md, docs/api-reference.md,
auth.md, gitcache.md, limits.md, migrations/_unreleased.md, observability.md,
security.md and self-hosting.md. bin/sync-docs.sh generated their embedded copies
and pkg/docs/changelog.md. Historical design/credential-model.md is unchanged.
No internal/blob, internal/cache, host, protocol, SDK or W1 transition source was
changed.

Friction: old signup fixtures depended on removed token/schema paths and a
listener. The replacement tests use the final store and real gate projection.
The broader area still depends on the named foreign stubs and legacy cutovers.
No new production mechanism was introduced to hide those gaps.

No service, candidate install, provider call, deployment, release, Git write or
harness-memory write ran. Bootstrap discovery ran installed sparkwing info before
the brief was loaded. No Sparkwing command ran after the brief. The lead retains
committing, review and pipelines. Scratch and check processes are removed before
reporting.
