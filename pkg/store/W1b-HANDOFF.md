# W1b handoff

Continues session 7 at HEAD 201145e74. W1b remains incomplete. The lead owns
commits, independent review, pipelines and native integration.

## This continuation

The first two lead handoffs remain complete. The full generated
TestBlobRecordsMutationCannotOutliveAuthority test passes. The retired output
catalog entries remain absent. W1 lifecycle implementation files were not edited.

The third lead handoff advances by retiring principal egress persistence.
Store.RecordEgressUsage, Store.ListEgressUsage and Store.PruneEgressUsage are gone.
Tenant.AddUsage writes additive egress bytes with trusted replica identity.
Tenant.EgressUsage reports one team's UTC day or month across replicas.
Operator.EgressUsageAcrossTeams supplies team aggregates for alarm reload.
Operator.PruneEgressUsage uses the maintenance lease and store clock to preserve
thirteen months. No schema change was required.

Invariant: delivered bytes add once to the authenticated tenant, and one replica
cannot replace another replica's usage. Generated SQLite tests vary hostile
replica identities and byte counts through independent clients. They check
foreign-team isolation and public day/month totals. The initial regression failed
because the replacement tenant and operator APIs did not exist.

The controller records delivered successful-response bytes at request completion.
Its existing lease-holder identity supplies the replica. Request query fields
cannot select either team or replica. HEAD and error bodies add nothing. A short
write counts only bytes actually delivered. Cancelled request contexts cannot
cancel the accounting write. The optional process meter still owns concurrency
and alarms. Durable usage also works without that meter. Logs remain exempt from
download caps while consuming egress bytes. A failed accounting write logs an
error. A process exit before request completion can lose that request's sample.
This limitation is documented in the contract and migration guide.

The store rejects malformed reporting periods, negative persisted counters and
overflow. Counter overflow leaves the existing row unchanged. Pruning refuses a
stale maintenance fence. Reports and expiration survive reopening. Account
deletion leaves egress rows intact because they contain no user identity.

Startup alarm reload preserves both day and month totals beyond the process
meter's principal cardinality. The real 10,002-team controller regression first
returned month=10,001. The loader now supplies the summed month through the
meter's existing aggregate restoration field. The regression passes.

Deleted 139 legacy store implementation lines: all 125 lines of egress.go and the
14-line obsolete account-relabel block. Deleted 312 old test lines: 117 in the
store and 195 in the controller. Across all non-test Go changes, 167 lines were
added and 243 removed, a net reduction of 76 lines.

## Ports and acceptance

Ported the three store egress tests to final-schema replica conservation,
round-trip, ordering, invalid inputs, overflow and leased expiration proofs.
The controller's obsolete flush/backlog/retry expectations retire with its timer.
Replacement checks prove actual delivery, authenticated attribution, nil-meter
persistence, partial writes, cancelled contexts, loud failures and alarm reload.
The internal egress writer has a direct delivered-byte test with and without a
meter. Its existing stream-control implementation remains shared.

storage-replica-safe-accounting/store-data-sqlite now reads real public egress
day/month totals after reopening. All 12 existing SQLite storage/billing proofs
pass. No native cell or archived T1 source row is newly marked complete.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE, GOTMPDIR and TMPDIR are under /tmp/sw-astra-W1b/.

- Whole-repository go build -p 4 ./... passed.
- Scoped go vet passed for store, controller, egress and both acceptance trees.
- The inherited store-data selection passed 175 top-level tests / 578 cases.
  The additional corruption/overflow test passed separately, totaling 176 tests
  and 579 cases. The three egress tests also passed together after formatting.
- Five focused controller tests passed, including the four egress proofs and the
  tenant event proof. All internal/egress tests passed except two socket tests
  excluded because this sandbox cannot create a listener.
- TestStorage and TestBilling passed 43 cases, including all 12 SQLite proofs.
  The 562 platform/native skeleton skips remain unclaimed.
- commentcheck -base main, sleepcheck -base HEAD, gofmt, git diff --check,
  sync-docs.sh --check and check-changelog.sh passed.
- The regenerated store API snapshot matches. Full directory comparison still
  finds inherited .apidiff/pkg_runner.txt for a package no longer generated.

The store selection is
^(TestBlob|TestLogPersistence|TestLogPublication|TestLogMaintenance|TestStoreData|TestRetainedStorage|TestPaymentSettlement|TestMeteredAccounting|TestTenantSQLScope|TestTenantWriters|TestTenantTwins).
The controller selection is
^(TestControllerEgress.*|TestControllerEventsUseTenantAndReplica)$.
Internal egress uses -skip
^(TestServeKeepsTheStreamControlsAHandlerAsksFor|TestServeForwardsHijack)$.
Acceptance uses ^(TestStorage|TestBilling)$ on both acceptance trees.

An earlier broader component regex reached TestHandleClaimNode_NamesAPollIntervalWhileIdle
and TestHandleClaimTrigger_WithholdsAdviceRightAfterAnAward. Their old fixtures
fail on missing sparkwing_meta or triggers.pipeline. Controller's
TestHeadRequestsChargeNothingAndHoldNoSlot and
TestConcurrentDownloadsAreCappedPerPrincipal fail on the removed tokens table.
The internal egress stream-control test panics when httptest cannot bind a socket.
These failures remain reported. No full-store, full-controller, PostgreSQL,
physical-backend, provider or native-platform pass is claimed. The minimally
migrated deletion fixtures still depend on the unrelated legacy identity APIs.

## Remaining assignments and real integration

- A1-24 remains implemented at the store boundary. W2's real DataAuthority and
  BindData adapters remain open.
- A1-27 still needs W1's parallel lifecycle join merged. This checkout's
  authority_issue.go still refuses metered roots. unit_billing.go is absent.
  W1's session-18 report contains the issuance/start/renewal/finish/reap join.
  C2 SettleBilling remains a panic stub. W1b's calculators remain tested through
  contract fakes. Spend/grace enforcement and the old claim-credit cut remain.
  W3 must report owner compute so customer-runner waits stay excluded.
- A1-28's committed-checkout settlement and raw paid-grant refusal remain done.
  W2 still owns provider verification and the HTTP adapter. VerifiedPaymentEvent
  remains a stub. Card billing retains its payment-linked settlement contract.
- A1-29 through A1-35 remain implemented behind the store contracts: tagged
  uploads and receipts, provenance, records and members, memo/depcache, read-set
  authorization, tombstones and logs. W7 RecordNamespace and AuthorizeEdge remain
  panic stubs. Physical verification/deletion, manifest-reader setup, GC,
  signed transfers, binary policy and proxy egress remain real integration work.
  Allowance-driven physical eviction stays open.
- The principal event and egress cuts are complete. Legacy raw storage
  reservations, output objects, direct uploads, claim-credit and node-log
  transports still need consumer cuts. The cache-grant source/artifact/binary
  download adapter, capability event transport, materialization and imported
  owner-log identity remain open.
- A1-53 remains with the lead and integrating areas for PostgreSQL, provider,
  physical-backend and native-placement evidence.

No new foreign contract change or unresolved product decision is raised. The
first two lead entries are done. The third is still in progress.

## Shared files and assumptions

Shared production caller migrations: pkg/controller/egress.go, server.go and
storage.go, plus internal/egress/egress.go. The egress Handle method returns actual
delivered bytes through its existing writer, preserving flush/hijack controls.
Shared acceptance change: internal/acceptance/store_data.go.
Shared controller test: egress_accounting_internal_test.go replaces the retired
egress_internal_test.go.

Minimal W1 file edits: deletion.go removes only the obsolete egress relabel SQL.
deletion_review_test.go migrates only egress columns and assertions.
tenant_sql_scope_guard_test.go replaces the retired egress exemptions with the
named operator read and leased prune. No admission, claim, transition,
reconciliation or reaping code changed.

Shared docs: contracts/c1-data.md, contracts/t1-port-inventory.md, CHANGELOG.md,
docs/limits.md, docs/migrations/_unreleased.md, .apidiff/pkg_store.txt and generated
copies under pkg/docs. STORE-DATA.md records the persistence owner.

Mechanisms added: tenant and operator egress reports replace the deleted raw
surface using the existing replica table and AddUsage writer. Request completion
replaces the controller's periodic principal flush. The existing maintenance
lease owns expiration. The old high-water API could not represent summed replica
usage or write the final schema. Undo would restore those APIs and their callers.
No new table, setting, environment variable, cap, timeout, queue or service exists.

The lead still needs independent review. No Git write, Sparkwing binary,
candidate, pipeline, service, deployment, release or harness-memory write ran.
HEAD remains 201145e74. The shared Git config has no local user identity or
core.worktree override. Scratch is deleted after the final checks.
