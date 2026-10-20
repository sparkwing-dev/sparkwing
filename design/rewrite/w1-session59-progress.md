# W1 session 59

The post-merge starting store failure count was **222**, compared with 214 in the
lead's supplied list. The ending count is **35** (1178 top-level tests pass). The package is
not green. The exact failures and their first errors are saved in
[w1-session59-store-failures.txt](w1-session59-store-failures.txt).

All 14 merge conflicts have resolved file contents. No conflict markers remain.
The lead must stage and finish the merge because the swarm brief forbids worker
git writes. HEAD remains `69d6bcf81`. The lead-owned handoffs and amendments files
match MERGE_HEAD byte for byte. The integration-deleted provision contract stays
deleted. Duplicate incoming bootstrap/management implementations are removed.
The required first-real-admin seed revocation lives in W1's existing bootstrap
owner. W1's persistent consumed gate, rotation origin and affected-row check stay
in force. Console and repository authorization use the amended shared contracts.

Reporting fixtures now seed current sealed-request and owner columns. They grant
no execution authority. Execution-sensitive exchange tests use ClaimUnit. Paid
fixtures settle committed provider checkouts instead of granting paid credit
through the refused shortcut. The shared storetest opener configures its session
cipher and test payment mode. Cloud lifecycle fixtures have credit, and delegated
fixtures retain the parent's repository scope. The backup drill uses real seeded
bootstrap administration and scoped runner enrollment, and fingerprints the
capability table. Both SQLite restore cases pass.

Removed 35 tests for deleted surfaces: raw invocation persistence, token-table
minting, independent run/trigger IDs, raw run upserts, sweep claims, trigger scan
horizons, concurrency_cache and on_limit=coalesce. Current materialization,
record, coalescing and fenced capacity coverage stays. Random capacity checks
now generate retained policies and seed live owner/job rows. Expired-waiter tests
expire the lease rather than treating an old arrival as expired authority.

Bucket measurement now uses Operator and the shared fenced singleton/window
owner. The invariant is that a stale callback cannot stamp success or release a
successor's lease. The generated property covers that boundary. `meta.go` is
removed, deleting 92 production lines. `bucket_measure_lease.go` falls from 67 to
11 lines. Cron and bucket checks pass through the same implementation.

`Tenant.ExecutionActivity` owns the local controller's activity query under one
snapshot and the store clock. Its property checks that another tenant contributes
no activity and expired leases stop counting. The local controller no longer
queries concurrency tables. Waiter resolution now uses checked commits on every
return after mutation. Pipeline inventory joins queue rows to runs for pipeline
metadata, and trends use the final memoized outcome.

Production Go delta for internal/store, using the lead's budget filter and counting
untracked source: **+658 / -252 = +406** against the starting HEAD, including the
incoming integration work. Combined store delta against main: **-3,548**, still
**702 lines over** the -4,250 budget. The new imported authority, OIDC and result
adapters are included in that total. This is not an over-budget completion claim.

Verification:

- Whole-repository `go build ./...` passes with GOWORK off and VCS stamping off.
- Scoped vet passes for store, controller and localcontroller, including helpers.
- Focused race checks pass for bootstrap rollback/exchange, bucket fencing and
  activity tenant/clock isolation.
- W1 cells TestStoreEnrolledClaims, TestStoreGeneratedDependencies,
  TestStoreCacheScope, TestStoreReferenceEdges, TestStoreRecoveryCapacityAdoption
  and TestStoreRecoveryCapacityMaintenance pass. Generated capacity properties,
  store-boundary guards, backup restore and controller bootstrap checks pass. The local controller's queue/activity
  composition and drain checks pass.
- Commentcheck against main is clean. Diff whitespace checks pass. Store/storage
  API snapshots are regenerated. Source and embedded observability docs match.
- Broader object-store HTTP checks hit the sandbox's TCP-listener refusal. The
  lead still needs the native listener and PostgreSQL lanes. No service was run.

Remaining work starts with the saved store failures. Retained run-rate tests still
reach the old insertion/token queries. Port those guards and their tests to the
sealed intake/current authority rather than restoring args_json or tokens.
Deletion fixtures still mint old tokens or seed sparkwing_meta. W1 owns those
caller migrations. Billing-rate/refund fixtures still mint old tokens too. Only
caller migration is authorized in W1b's tests; W1b's persistence implementation
has not been changed. Its invalid payment-correlation seed and log standing case
remain. The writer guard also finds dead debug/storage baseline tables; keep the
invariant and remove the dead tables with their owners.

Ordered placement, rotation grace and SyncLauncher fencing were already closed
in earlier sessions and remain in place. Trusted schedule/App intake still needs
the C4 tagged committing origin and transaction-readable verified-delivery
contract. Do not synthesize a credential for a schedule or App delivery. The
remaining RunControlWriter, inventory/reporting adapters and raw run/trigger/token
cuts from session 58 remain open. Materialization is closed by W1b and untouched.
No foreign contract signature was extended here. C1 documents the local bucket
singleton and execution-activity projection.

Shared production callers changed by W1:

- `internal/controller/objectstore.go`
- `internal/wingd/localcontroller/state.go`

Shared fixture changes include `internal/controller/review_bootstrap_native_test.go`
(funded Cloud award), `internal/store/internal/storetest/storetest.go` (configuration),
and `internal/store/teststore/run.go` (current terminal metadata). Other store test
edits migrate fixture calls. Merge-only controller, acceptance, auth-document and
API snapshot conflicts keep the integration changes, adapted to W1's surviving
implementation. No lead-owned contract file was edited.

Friction: the incoming auth implementation duplicated existing W1 owners, while
old tests depended on removed columns, invalid whole-cent checkout amounts,
missing ciphers, empty repository scopes and unmetered Cloud awards. Those are
fixture assumptions, not reasons to relax the final invariants. Git config has
no local user or core.worktree override. Scratch is removed after checks finish;
the shared build cache is retained.

STATUS: CONTINUE
