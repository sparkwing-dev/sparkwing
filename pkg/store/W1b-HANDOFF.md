# W1b handoff

Continues session 6 on HEAD fd430813d. W1b remains incomplete. The lead owns
commits, independent review, pipelines and native integration.

## This continuation

The first two lead handoffs were already done. The complete generated
TestBlobRecordsMutationCannotOutliveAuthority run passes unchanged. The output
catalog removals remain absent. W1's lifecycle implementation files were not edited.

The third lead handoff advances by retiring the principal-based event writer.
Tenant.AppendEventCharged replaces Store.AppendEventCharged. Its first string
argument is the controller's trusted replica identity. It accepts no principal,
team or clock. The controller reuses leaseHolder and obtains the tenant from the
request's verified team. HTTP request and response shapes do not change.

Invariant: an event, its retained-byte count, quota debit and replica usage commit
together in the selected tenant. A refusal changes none of them. The initial
final-schema regression failed with "no such column: principal".

The writer locks the tenant's run and shares storage admission with verified
uploads. It charges monthly and lifetime run bytes without spending object
capacity. Pending uploads consume the same capacity. Aborting a ticket releases
capacity for events. Events reduce the capacity available to later uploads.
The shared quota owner selects the month after its lock. Run usage survives month
changes. Additive monthly and run rows carry tenant and replica identity.
Usage is recorded even without configured limits. The retained event total still
comes from runs.event_bytes. Kind bytes count alongside payload bytes.

The existing event-size, run-byte and event-count caps, free share, funding and
claim-fence handling remain. Priced storage checks credit using the store clock.
Overflow and any failed quota, usage or event insert roll back the whole write
and return no sequence. The generated tenant property varies byte counts and
hostile replica strings through independent SQLite clients. It checks refusal
of foreign runs and conservation across run counters, quota and usage reports.

Deleted 162 legacy implementation lines from storage.go, including the raw API,
principal quota writer, old quota checks and principal lock. Deleted 347 old test
lines. The replacement event owner is 86 lines. Shared storage helper changes
reuse the upload quota and usage owners. Including the 94-line acceptance helper,
the non-test Go diff adds 230 lines and removes 204, a net increase of 26.
No new table, setting, environment variable, cap, timeout or service was added.

## Ports and acceptance

Ported the five charged-storage tests and seven event-cap tests from storage_test.go
and event_limits_test.go to event_accounting_runtime_test.go and
event_policy_runtime_test.go. Preserved per-run/month/event/count limits, kind
validation, kind byte cost, concurrent capacity, uncharged event counters and
failed-write rollback. The old no-quota test now requires accounting because final
replica counters record all writes. Free-share, funded/held and priced-storage
checks use final-schema fixtures. The old fixture callers still compile.

storage-replica-safe-accounting/store-data-sqlite now includes the real event API
through independent clients, competition with a pending upload, a final-capacity
race, reopening and the exhausted cap. All 12 existing store-data-sqlite proofs
pass. No new native cell or archived T1 source row is marked complete. The
controller component proof exercises the actual handler and persistence without a
socket. It uses a trusted operator principal and does not prove final C2 routing.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
and GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W1b/.

- Whole-repository go build -p 4 ./... passed on the final production tree.
- Scoped go vet passed for store, controller and both acceptance trees.
- Final store selection passed 172 top-level tests / 575 cases. The additional
  priced-event test passed separately, totaling 173 tests / 576 cases.
- Four focused controller tests passed, including event and download accounting
  and final-inventory/tagged-log maintenance.
- TestStorage and TestBilling passed 43 cases, including all 12 SQLite proofs.
  The 562 native/platform skeleton skips remain unclaimed.
- commentcheck -base main, sleepcheck -base HEAD, gofmt, git diff --check,
  sync-docs.sh --check and check-changelog.sh passed.
- The regenerated store API snapshot matches. Full directory comparison still
  finds the inherited .apidiff/pkg_runner.txt for a package no longer generated.

The final store regex is
^(TestBlob|TestLogPersistence|TestLogPublication|TestLogMaintenance|TestStoreData|TestRetainedStorage|TestPaymentSettlement|TestMeteredAccounting|TestTenantSQLScope|TestTenantWriters|TestTenantTwins).
Acceptance uses ^(TestStorage|TestBilling)$ on the two acceptance trees.

An earlier broad selection also reached 15 old metered Store tests that fail on
removed tokens/sparkwing_meta tables or the old triggers.pipeline column. Controller's TestMeteredRoutesStillServeTheRunnerAndCLITokens
and TestStorageWritesUnboundedWithoutAQuota fail on the removed tokens table.
TestBillingTrust_OperatorGrantsAndRevokes tries to open a forbidden socket and
panics. The first controller event fixture failed on sparkwing_meta. It now uses
the existing final-schema engineRuntimeStore fixture. These broad failures are
retained as friction, not represented as fixed by a narrower selection.
No full-store, full-controller, PostgreSQL, provider or native-platform pass is claimed.

## Remaining assignments and real integration

- A1-24: transaction-owned data authorization and binding proofs remain intact.
  W2 DataController.DataAuthority and BindData still need real adapters.
- A1-27: this checkout still refuses metered roots in authority_issue.go and
  persists unmetered awards separately. W1's session-18 work contains the real
  lifecycle join in unit_billing.go. The lead must integrate that parallel work.
  W1b did not copy or edit it. W1b still owns spend-exhaustion/grace enforcement
  and the legacy accounting cut after integration. C2 SettleBilling remains a
  panic stub. W3 must report owner compute so customer-runner waits stay excluded.
  Accounting tests continue to use calculator contract fakes.
- A1-28: committed-checkout settlement and raw paid-grant refusal remain done.
  W2 owns provider verification and the HTTP adapter. VerifiedPaymentEvent.Event
  remains a stub. Card billing retains its payment-linked settlement contract.
- A1-29 through A1-35: upload tickets, tagged receipts, provenance, record/member
  persistence, read-set authorization, memo/depcache, tombstones and tagged logs
  remain implemented behind contracts. W7 RecordNamespace and AuthorizeEdge are
  still stubs. Store proofs use contract fakes. Physical verification/deletion,
  manifest-reader configuration, GC, signed transfers, binary execution policy
  and proxy egress remain integration work. Allowance-driven eviction stays open.
- Legacy claim-credit, principal egress counters, raw storage reservations,
  output objects, direct uploads and node-log transport still need consumer cuts.
  The principal event accounting cut is complete. The custom-event controller
  transport still uses the inherited claim-fence adapter. C2 routing and real
  capability-backed event writes are not claimed by its trusted-operator proof.
  The source/artifact/binary data_download adapter still uses cache grants.
  Materialization and imported-owner-log identity handoffs remain open.
- A1-53: native placements, provider/backend and PostgreSQL evidence remain with
  the lead and integrating areas. The first two lead entries are done. The third
  remains in progress. No new foreign contract change or open decision is raised.

## Shared files, assumptions and friction

Shared production caller migration: pkg/controller/handlers.go.
Shared controller proof: pkg/controller/event_accounting_internal_test.go.
Acceptance changes: internal/acceptance/store_data.go and new store_event.go.
Owned tests with minimal caller migrations: free_tier_test.go,
credit_freeze_standing_test.go and storage_credits_test.go.

W1 catalog-only change: tenant_sql_scope_guard_test.go loses the extinct
(*Store).chargeStorageTx exemption and the now-scoped refuseEventOverLimitsTx
exemption, five lines total. The scope guard requires deleting stale exemptions.
This is a caller/catalog migration under the third lead handoff. No W1 admission,
claim, transition, reconciliation or reaping code changed.

Shared docs: contracts/c1-data.md, contracts/t1-port-inventory.md, CHANGELOG.md,
docs/migrations/_unreleased.md, .apidiff/pkg_store.txt and sync-docs-generated
copies under pkg/docs. STORE-DATA.md documents the persistence owner.

Mechanisms added: the tenant event owner replaces the raw writer using existing
shared quota and replica rows. The quota helper and usage writer are shared with
uploads because separate read-modify-write counters would admit the same bytes
twice. An API rename alone leaves the deleted principal schema in use. Undo means
restoring the raw writer, its callers and the helper implementations. There is no
compatibility wrapper or second admission store.

The lead still needs independent review. No Git write, candidate binary, service,
pipeline, deployment, release or harness-memory write ran. Installed CLI discovery
ran before implementation. HEAD remains fd430813d. The shared repository has no local user identity or
core.worktree override. All owned check processes exited. Scratch was deleted
after the final checks.
