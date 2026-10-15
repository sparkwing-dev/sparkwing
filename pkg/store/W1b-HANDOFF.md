# W1b handoff

Continues f9cbf87b2 and W1b-session1792073641.out. W1b remains incomplete.
The worktree started clean. The lead owns review, commits and pipelines.

## Implemented

Tenant record operations now use the manifest reader installed by
Store.SetResultBlobRecords when their caller supplies no explicit reader.
Server.WithDataObjects already configures that backend. Previously it reached
terminal result attachment only. C2 DataTransactions returns Tenant, whose
CommitRecord, LookupRecord, LookupMemo, LookupDepcache and CopyMemo methods all
constructed records with a nil reader. Manifest operations therefore failed
with "verified manifest reader is not configured" after normal controller setup.

The existing record factory now selects the configured reader. An explicit
BlobRecords reader still wins. No schema, pricing, public signature, authority
rule, settings field, compatibility adapter or extra backend was added.
Configuration still happens before sharing the store.

Invariant: tenant record reads and commits verify manifests through the
configured physical backend before exposing members or attaching results.
The generated controller property now uses Tenant.CommitRecord and
Tenant.LookupRecord after WithDataObjects, instead of injecting a separate
reader into its fixture. It failed on its first input before the fix. All 12
payload trials now pass. The property checks live claim/start, verified uploads,
manifest commit/read, corrupt physical manifest refusal with no exposed hit,
terminal rollback, restored bytes and exact completion replay.

Production Go changes add three executable lines and delete zero legacy
executable lines. The controller fixture deletes its explicit-reader bypass.
Previous warning identity, payer ambiguity, accounting, receipt, record mutation,
quota and retention work remains in the earlier commits. No finished acceptance
cell was reimplemented or newly activated.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1b/gocache, GOTMPDIR=/tmp/sw-astra-W1b/gotmp and
TMPDIR=/tmp/sw-astra-W1b/gotmp. Build, vet and test use -p 4.

- Whole-repository go build ./... passes.
- go vet ./pkg/store ./pkg/controller passes.
- Focused store record, result, manifest, scope and selection tests pass.
- Controller TestDataStorage tests pass, including the strengthened property.
- Commentcheck against main passes across 2,118 files.
- Documentation mirror and git diff --check pass.
- TestBilling and TestStorage pass 287 cases and skip 562 cases across both
  acceptance packages. Fourteen existing SQLite cells pass:
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
  - storage-local-remote-cache-opt-in/store-data-sqlite

Reproduce the focused tests with:

`go test -p 4 ./pkg/store -run '^(TestBlobRecords|TestBlobResult|TestResultRecords|TestBlobManifest|TestBlobDataAccessLogicalSelection|TestBlobDataBinding|TestBlobDataAccess|TestBlob.*Memo|TestBlob.*Depcache|TestBlob.*Output)' -count=1`

`go test -p 4 ./pkg/controller -run '^TestDataStorage' -count=1`

`go test -p 4 ./tests/acceptance ./internal/acceptance -run '^(TestBilling|TestStorage)' -count=1`

No old T1 function was ported or retired in this slice. No full-suite,
PostgreSQL, race, provider, native placement or pipeline pass is claimed.

## Contract changes needed and prioritized work

1. Lead decision 07:40 item 2 remains blocked on the undeclared expanded C2
   contract. Both this checkout and bw-3095-w2-credentials still declare only
   checkout paid/expired/failed, with checkout/payment IDs, currency, amount and
   mode. No setup, refund, warning or dispute event facts are declared. W1b
   cannot implement a dispatcher against a missing foreign interface. W2 must
   declare verified setup/customer/method/fingerprint/completion facts,
   collection attempt/payment correlation, refund identity/amount/status,
   warning identity/payment/card/actionability and dispute identity/payment/
   outcome. Provider metadata must never select the payer.
2. W1 must extend payment_events and durable setup correlation. The current
   schema requires checkout_id, a matching credit_checkouts row and one of
   three checkout outcomes. Initial card setup has no pending setup/customer
   to tenant binding. Broader event correlation must retain payment mode.
   Existing settlement transaction helpers remain ready for the verified
   dispatcher. No speculative event schema or foreign API was added.
3. W1 spend/grace join remains open. billing_awards has no per-award exhaustion
   anchor, accountUnitBillingTx returns only error and renewal fills LeaseBeat
   before accounting. W1 must expose the committing renewal policy result for
   job/host/plan/eval while retaining prepaid runway, customer-wait exclusions,
   existing grace/spend policy and holder drain rights. W1b supplies the policy
   once that join is declared. No W1 implementation was edited.
4. Legacy output consumers still use OutputReadGrant and localState forwards
   GetNodeOutput without final blob authority or record keys. W2/W3/W9 must
   supply those bindings for the final clients. Legacy log reads still query
   node_logs. LogSegmentUploader calls the removed node log writer routes.
   W2/W3/W9 must compose the declared blob.LogTransport with tagged subjects.
   Removing these consumers without replacement would remove output/log
   behavior. No removed writer was restored.

## Amendment and remaining integration audit

- A1-24 and A1-29 through A1-35 retain their implemented persistence behavior.
  This slice closes the configured-manifest dependency for C2's default tenant
  transactions. Live DataAuthority resolution, ReadBlob/ChargeDownload
  composition, source-before-run binding, verified small-output use and
  allowance-driven physical eviction remain integration work.
- A1-27 lifecycle accounting joins remain implemented. Spend/grace and legacy
  claim-credit retirement still depend on W1's join above.
- A1-28 and decision 07:40 item 2 remain checkout-only for the stated reasons.
- Materialized memo/retry/coalesced destinations need W1/W7's declared record
  identity and read edge. Public CopyMemo requires a live positive attempt.
  Synthetic attempts remain forbidden.
- A1-53 native/provider/PostgreSQL evidence remains with T1/lead. Lead-owned
  handoffs, amendments and port inventories are unchanged.

## Shared files and friction

Outside pkg/store, changed only pkg/controller/data_result_test.go,
design/rewrite/contracts/c1-data.md, CHANGELOG.md and its generated
pkg/docs/changelog.md mirror. The controller change migrates the fixture to
its real tenant interface and strengthens its existing generated property.
No W1 file was edited. The C1 note records the local default-reader behavior.

The first modified controller property reproduced the missing-reader failure.
The expanded C2 event contract and W1 renewal/schema seams remain unavailable,
including on the parallel W2 branch. No other area's stub was treated as a
reason to skip its declared interface. This slice uses the real SQLite store,
filesystem objects and existing controller configuration.

No Git write, Sparkwing executable, candidate, pipeline, service, release,
subagent or harness-memory write ran. The repository Git configuration retains
the normal user identity and has no core.worktree override. Scratch /tmp/sw-astra-W1b/ is deleted after verification. Independent review remains with the lead.

STATUS: CONTINUE
