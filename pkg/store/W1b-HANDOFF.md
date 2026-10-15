# W1b handoff

Continues 94167e3b6. W1b remains incomplete. The lead owns staging, the merge
commit, independent review, pipelines and native integration.

## Merge resolution

All nine conflicted file contents are resolved. No conflict markers remain.
The Git index remains unmerged because SWARM-BRIEF prohibits Git writes.
The lead must stage the resolutions and finish the merge.

- `handoffs.md` is byte-identical to the integration branch's stage-three version.
- C1 retains both areas' completed cuts. T1 retains both port inventories.
- Migration source and mirror retain both areas' changes.
- The obsolete finalize-claim-round fixture is deleted. The removed unstarted
  requeue tests stay removed, matching W1's API retirement.
- The SQL scope catalog drops entries for the three retired mechanisms present
  in its conflict. No removed API is restored.
- The store API snapshot is regenerated from the merged implementation.

The nine paths were `.apidiff/pkg_store.txt`, contracts `c1-data.md`,
`handoffs.md`, `t1-port-inventory.md`, both migration copies, and store tests
`finalize_event_deadlock_test.go`, `tenant_sql_scope_guard_test.go` and
`trigger_claim_credits_test.go`.

## Implemented and removed

Four missed `SetCreditSettings` callers in W1's unit-billing test and SQLite
acceptance fixture now select Operator. Those missed migrations prevented both
the store tests and whole-repository build from compiling after the merge.
W1's lifecycle implementation files are unchanged.

`Operator.CreditLedgerTotals` replaces the raw Store export without an alias.
The controller's deployment sampler and all callers select that owner. Reports
use the same calculator as lifecycle settlement. A regression first reproduced
the public report reaching C2's panic stub despite the lifecycle fixture having
a calculator installed. The strengthened test uses real claim, bootstrap
exchange and unstarted release, and checks the report before and after refund.

Three unused billing wrappers are removed: `settleBillingAwardTx`,
`settleBillingAwardWithTx` and `chargeBillingAwardTx`. Production lifecycle
settlement already uses `accountBillingAwardTx`. Tests now use that same owner.
The unmetered proof supplies no calculator.

The wrapper cut removes 15 production Go lines. Together with the totals-owner
change, production Go has a net reduction of 11 lines. The controller sampler
fixture port removes 23 lines and adds five. Merge resolution preserves W1's
removal of the 59-line finalize fixture and 98 lines in the trigger-credit test.
Those merge deletions are W1's cuts, not new W1b ports.

The sampler fixture now uses the final-schema store fixture and the presence
registry boundary. It retains its queue-depth, free-grant and live-runner metric
assertions. Its old CreateRun call failed on removed `args_json`. This component
proof makes no HTTP claim-registration claim.

Two other generated API snapshots are refreshed for W1's retired client/status
and heartbeat APIs. The stale snapshot for the deleted runner package is removed.
The complete generated API directory now matches the tracked snapshot files.

## Verification

Commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, with `-p 4`
for build, vet and tests. Go cache and temporary directories are under
`/tmp/sw-astra-W1b/`. That scratch directory is deleted after verification.

- Whole-repository `go build -p 4 ./...` passes.
- Scoped vet passes for store, controller and both acceptance packages.
- The final store selection passes 202 top-level tests / 619 cases, no skips.
- Seven controller tests pass, including the migrated sampler and the existing
  event/egress regressions.
- Storage/billing acceptance passes 44 cases, with 562 native/platform skips.
  Thirteen SQLite proofs pass. No native cell is newly unskipped.
- Commentcheck against main, sleepcheck against HEAD, gofmt, diff whitespace,
  changelog, documentation mirrors and full API snapshot comparison pass.

Store selection:
`^(TestBlob|TestLogPersistence|TestLogPublication|TestLogMaintenance|TestStoreData|TestRetainedStorage|TestPaymentSettlement|TestMeteredAccounting|TestMeteredUnit|TestTenantSQLScope|TestTenantWriters|TestTenantTwins|TestGrantCredits|TestCreditGrantRequest|TestCreditsBalance|TestCreditReversal|TestPayment.*(Cap|Spend))`

Controller selection:
`^(TestControllerEgress.*|TestControllerEventsUseTenantAndReplica|TestCreditsCollectorReportsTheSampledLedger|TestSamplersFillTheOperationalSeries)$`

Acceptance selection: `^(TestStorage|TestBilling)$` in internal/acceptance and
tests/acceptance. Passing SQLite cells:

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

No archived T1 source identity is newly claimed complete. Existing disruption,
mutation-authority, upload, record, log and settlement properties still pass.
No full legacy suite, PostgreSQL, provider, physical-backend, native-placement or
race pass is claimed. The lead runs independent review and pipeline gates.

## Assignment audit and remaining work

- Approved record-mutation authorization and the two output catalog removals
  remain implemented. The generated mutation-authority test passes in the selection.
- A1-24's store boundary remains implemented. W2 DataAuthority and BindData are
  still panic stubs in this checkout. No SQL or credential adapter is duplicated.
- A1-27's W1 lifecycle join is now merged and verified with contract calculators.
  Awards, starts, renewal, application retry, finish, release and reaping use the
  immutable billing owner. The award-versus-start SQLite cell also passes.
  C2's production SettleBilling remains a panic stub. W3 must report current owner
  compute, including false during customer-runner waits. W1b still owns
  spend-exhaustion/grace enforcement and legacy claim-credit retirement.
  The redundant unmetered calculator override in W1's unit helper can be removed
  by W1 because the shared helper already bypasses calculation for unmetered work.
- A1-28's committed-checkout settlement and raw-paid-grant refusal remain done.
  W2 still owns verified provider events and its HTTP adapter.
- A1-29 through A1-35 retain the implemented store contracts for records/members,
  tagged receipts and uploader identity, immutable upload tickets, memo/depcache,
  read sets, tombstones and tagged logs. W7 RecordNamespace, AuthorizeEdge and
  signed/log transport construction remain stubs. Physical verification/deletion,
  manifest-reader setup, signed transfers, binary policy, proxy egress and
  allowance-driven physical eviction remain real-integration work.
- Legacy ReserveUpload/CommitUpload, output objects, ReleaseStorage and node-log
  transports still need consumer cuts. Legacy trigger/node credit windows also
  remain and must retire with their consumers. This session does not claim those
  paths or spend/grace enforcement complete.
- A1-53 remains unverified for native/provider/PostgreSQL/physical-backend drivers.

## Contract question

No new foreign contract change is requested. The existing free-slot question
remains unresolved: final tickets have no server-verified intended use before
signing, while legacy small outputs may bypass a full slot pool. A blanket gate
would remove that exception. The lead/C1/W7 must supply the intended-use contract
or an explicit product decision. No slot gate or restriction is added here.

## Shared files and friction

Shared production caller: `pkg/controller/server.go`.
Shared fixtures: `pkg/controller/prom_internal_test.go`,
`internal/acceptance/store_billing.go`, `internal/acceptance/store_unit_billing.go`,
`pkg/store/unit_billing_test.go`, `pkg/store/credits_test.go` and
`pkg/store/queue_state_counts_test.go`. Changes outside W1b implementation files
are caller migrations, the sampler fixture port and the required merge resolutions.

Shared prose/generated files: C1 and T1 contracts, the integration handoff
resolution, CHANGELOG.md, migration source/mirror, pkg/docs/changelog.md, and
API snapshots for store, controller client, storage and the retired runner.
No amendment file is edited. The only handoffs.md operation restores the exact
integration version required by SWARM-BRIEF.

Friction: four missed settings-owner migrations broke compilation. The public
ledger report bypassed the lifecycle calculator. The sampler used removed-schema
fixtures. Three API snapshots were stale. Each is corrected above. Runtime
calculator/data/blob/log integrations and the free-slot contract question remain.

No Git write, candidate installation, pipeline, service, deployment, release or
harness-memory write ran. The shared Git config has no local identity or worktree override. Session scratch
is deleted after all checks.

STATUS: CONTINUE
