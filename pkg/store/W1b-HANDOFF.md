# W1b handoff

Continues session 5 on HEAD 57a20c17b. The lead owns commits, independent review,
pipelines and native integration. W1b remains incomplete.

## This continuation

The first two lead handoffs were already done and were not repeated.
`TestBlobRecordsMutationCannotOutliveAuthority` passed its full generated run.
The two catalog removals remain absent. No W1 implementation file was edited.

The third lead item advances by removing raw download accounting.
`Tenant.ChargeDownload` replaces `Store.ChargeDownload`. Its request carries a
trusted replica ID, bytes, delivered-byte flag and the existing cap policy.
It accepts no team or caller clock. The tenant owns scope. Store time after
locking the shared download admission row selects the UTC day.

Admission, expiry of held download reservations and additive replica usage commit
together. Unexpired generic download reservations consume capacity. Expired holds
cannot be subtracted twice by later settlement. Midnight resets admission while
earlier usage remains reportable. The download that crosses a cap finishes.
Already delivered bytes can be recorded after exhaustion. Funding changes and
the operator exemption keep their existing meaning. A zero-byte cap check writes
no replica usage. Overflow and failed usage writes roll back the entire charge.

Both production callers now use tenant handles. Signed downloads and delivered
response bytes retain the controller's existing `leaseHolder` as their trusted
replica identity. Log and live-log reads remain exempt from the download cap.
No new setting, environment variable, timeout, cap, schema or service is added.

Invariant: concurrent replicas cannot lose a team's charges, and one tenant
cannot charge another tenant's budget. A generated property varies amounts and
hostile replica strings against independent SQLite connections. It checks the
shared admission total and summed tenant reports. The race proof admits exactly
one of eight contenders for the last capacity, then reopens the exhausted store.

Deleted 106 production Go lines from team_storage.go, replacing the raw method
and its declarations with 118 lines in download_accounting.go. The controller
caller migration adds nine net production lines. Deleted 65 legacy test lines.
The replacement store proof has 214 lines, the controller proof 75, and the
acceptance helper 83. These figures exclude docs and generated API snapshots.

## Ports and acceptance

Ported TestChargeDownloadHoldsATeamToItsDailyCap and
TestChargeDownloadRaceForTheLastBytesAdmitsExactlyOne to final-schema tests.
They cover funding, cap crossing, delivered bytes, midnight, operator exemption,
shared reservations, expiry, restart, overflow and failed-write rollback.

The existing storage-replica-safe-accounting/store-data-sqlite cell now includes
real tenant charges through independent clients, a last-capacity race and the
exhausted cap after reopening. All 12 existing store-data-sqlite cells pass.
No new native cell or archived T1 source row is marked complete. The controller
proof uses real middleware and persistence without opening a socket.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
-p 4 and GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W1b/.

- Whole-repository go build -p 4 ./... passed after the final SQL change.
- Scoped go vet passed for store, controller and both acceptance package trees.
- The broad store selection passed 205 top-level tests / 627 cases. One source
  guard failed because it cannot resolve usageStatement's dynamic table name.
  The new owner now uses its explicit tenant INSERT. The final focused selection
  passed 13 top-level tests / 38 cases, including every new download proof and
  tenant scope/writer/twin guards. The broad selection was not rerun after this
  equivalent SQL spelling change.
- TestBlobRecordsMutationCannotOutliveAuthority passed separately, unchanged.
- TestStorage and TestBilling passed 43 cases, including all 12 existing SQLite
  proofs. The 562 native/platform skips remain unclaimed.
- The new controller proof and focused embedded migration/changelog/privacy
  checks passed. Four controller tests passed in the broader selection, but
  TestTeamDeletionReachesARetiredRetentionClass failed in its inherited fixture
  because newLogSegmentFixture still reads the removed sparkwing_meta table.
- commentcheck -base main passed across 1025 files. sleepcheck -base HEAD,
  gofmt, git diff --check and sync-docs.sh --check passed.
- The generated store API snapshot matches. A full snapshot-directory comparison
  also finds the inherited .apidiff/pkg_runner.txt for a package no longer
  generated. That foreign snapshot was not changed.

The broad store selection is the prior handoff's selection with the new
TestStoreDataDownload functions included by its existing prefix. The final
focused command is go test -json -p 4 ./pkg/store -count=1 -timeout=180s
-run '^(TestStoreDataDownload|TestTenantSQLScope|TestTenantWriters|TestTenantTwins)'.
Acceptance uses ./internal/acceptance/... ./tests/acceptance/... with
-run '^(TestStorage|TestBilling)$'. No full-store, full-controller, race,
PostgreSQL, provider, physical-backend or native-platform pass is claimed.

## Remaining assignments and real integration

- A1-24: transaction-owned data authorization and binding proofs remain intact.
  W2 DataController.DataAuthority and BindData still need real adapters.
- A1-27: this checkout still refuses metered roots in authority_issue.go and
  persists unmetered awards separately. W1's session-18 report and its current
  worktree contain the real lifecycle join in unit_billing.go. The lead must
  integrate that parallel work. It was read and not copied into W1b's branch.
  After integration, W1b still owns spend-exhaustion/grace enforcement and the
  legacy accounting cut. C2 SettleBilling remains a panic stub. W3 must report
  actual owner compute on renewal so customer-runner waits stay excluded.
  Existing W1b accounting tests continue to use calculator contract fakes.
- A1-28: committed-checkout settlement and raw paid-grant refusal remain done.
  W2 owns provider verification and its HTTP adapter. VerifiedPaymentEvent.Event
  remains a stub. Card billing retains its payment-linked settlement contract.
- A1-29 through A1-35: upload tickets, tagged receipts, provenance, record/member
  persistence, read-set authorization, memo/depcache, tombstones and tagged logs
  remain implemented behind contracts. W7 RecordNamespace and AuthorizeEdge
  remain stubs. Store proofs use contract fakes. Physical verification/deletion,
  manifest-reader configuration, GC, binary execution policy, signed transfers
  and exact proxy egress remain integration work. Allowance-driven physical
  eviction remains open. Download callers now supply trusted replica identity.
- Legacy claim-credit, principal event and egress counters, raw storage helpers,
  output objects, direct uploads and node-log transport need consumer cutovers.
  Direct-upload/log paths still use internal raw reservations and old tables.
  The source/artifact/binary data_download adapter still uses cache grants.
  Materialization and imported-owner-log identity handoffs remain open.
- A1-53: native placements, provider/backend and PostgreSQL evidence remain with
  the lead and integrating areas. The first two lead entries are done. The third
  remains in progress. No new foreign contract change is requested.

## Shared files, assumptions and friction

Shared production caller migrations: pkg/controller/egress.go and
pkg/controller/data_download.go. Shared test caller migration:
pkg/controller/team_download_test.go. New controller proof:
pkg/controller/download_accounting_internal_test.go.

Acceptance changes: internal/acceptance/store_data.go and new
internal/acceptance/store_download.go. Owned files: team_storage.go,
team_storage_test.go, new download_accounting.go and
new download_accounting_runtime_test.go, all under pkg/store.

Shared docs: contracts/c1-data.md, contracts/t1-port-inventory.md, CHANGELOG.md,
docs/migrations/_unreleased.md, .apidiff/pkg_store.txt and sync-docs-generated
copies under pkg/docs. STORE-DATA.md documents the current owner. W1's catalog,
transition, admission, claim and reconciliation files remain untouched.

Mechanisms added: the tenant owner replaces the raw writer using the existing
shared quota row and replica table. No alternate admission store or process
counter is added. A raw-method rename alone was insufficient because its SQL
still targeted the removed day shape. Undo restores the raw method and callers.

Friction: the initial behavioral test failed on the removed day column. The
literal SQL writer guard could not recognize the shared dynamic usage builder.
The explicit equivalent INSERT resolves that guard without changing W1 files.
The inherited log fixture failure and extra runner API snapshot remain visible.
The initial cold build was slow. No Sparkwing binary, service, Git write,
deployment, release or harness-memory write ran.

All owned check processes exited before /tmp/sw-astra-W1b/ was removed.
HEAD remained 57a20c17b. The shared repository has no local user identity or
core.worktree override. No Git writes ran.
