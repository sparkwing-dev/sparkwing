# W1b handoff

W1b remains incomplete. This continuation starts at `ca771a7a9` and ports
retained-storage billing to the final schema. Earlier upload, blob, log,
metered-accounting and payment implementations remain in place. W1 admission,
claim, transition, reaper, schema and SQL-guard files were not edited. The only
shared store edit removes obsolete watermark cleanup from `deletion.go`.
No Git writes or Sparkwing binaries were run.

## Implemented

- `Operator.ChargeRetainedStorage(ctx, maintenanceLease)` replaces the raw
  Store method. It reads unique committed `blobs` and persisted
  `log_streams.stored_bytes`. Replica usage totals and reference counts cannot
  multiply retained bytes. The free allowance applies once to the combined
  inventory, after the team's optional retained ceiling. Teams without quota
  rows are billed. Charges carry the team and no run or runner price fields.
- Both existing `team_storage.storage_charged_through` rows share the same
  watermark. The maintenance fence, credit lock, watermark update, ledger
  debit and spend counters commit together per team. The fence is checked
  again before commit. Separate database clients cannot bill one interval
  twice. The initial sample establishes billing time. Fractional seconds
  remain for the next pass. Empty samples and creation times bound idle gaps.
  A disabled price clears existing watermarks so resumed billing starts fresh.
- Corrupt settings, negative allowances, inconsistent watermarks and invalid
  or overflowing inventory return errors. Failed ledger writes roll back the
  watermark. A backward clock produces no charge until it catches up.
- The controller's existing billing caller acquires the maintenance lease and
  invokes the operator method. It logs teams rather than token principals.
  Its call to the obsolete principal allowance sweep is removed. W1's final
  maintenance coordinator still needs to unify retention, allowances and GC.
- The new SQLite acceptance cell takes a real storage debit, checks its amount
  and the expected balance, then runs leased retention and reopens the store.
  The old run, stream and segments disappear. Every field of all financial
  rows, including provider evidence, awards and intervals, remains unchanged.

The patch removes **213 legacy billing implementation lines**, **413 legacy
store-test lines** and **119 legacy controller-test lines**. New owned files
replace billing and port its assertions. These are gross removals, not a net
code reduction claim. No table, column, setting key, environment variable,
service, compatibility API or price limit was added.

## Verification

All Go checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`-p 4`, and private cache/temp directories under `/tmp/sw-astra-W1b/`.

- Final `go build -p 4 ./...` passed for the whole repository.
- `go vet -p 4` passed for `pkg/store/...`, `pkg/controller/...`,
  `internal/acceptance/...` and `tests/acceptance/...`.
- Store regression selection: **95 top-level tests / 185 with subtests**,
  no failures or skips. Selection:
  `^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore)`.
- Final race selection: **43 top-level tests / 83 with subtests**, no failures
  or skips. Selection:
  `^(TestStoreDataRetainedStorage|TestStoreDataMetered|TestPayment)`.
- Acceptance packages passed: **20 top-level tests / 195 passes**, with
  **2,995 existing target skips**. Passing SQLite product cells:
  `billing-ledger-survives-retention/store-data-sqlite` (new) and
  `storage-replica-safe-accounting/store-data-sqlite` (existing).
  Native, PostgreSQL, provider and physical-backend cells remain pending.
- `go run ./internal/commentcheck -base HEAD <repo>` passed across nine Go
  files. `sync-docs.sh --check` and `git diff --check` passed.
- Generated snapshots in scratch confirm the new method signature. Existing
  snapshot drift remains in eight packages: controller, controller/client,
  projectconfig, match, store, storage/storeurl, sparkwing and storage. Only
  the owned method removal/addition was applied to the committed snapshot.
- The tenant SQL guard still fails with **14 existing findings** in authority,
  payment and retention code. The new billing file has no finding and adds no
  exemption. W1 owns that guard. Full legacy suites and pipeline gates were
  not run. Independent review remains with the lead.

Tests first reproduced the absent operator API. The extended clock-regression
case caught rejection of a creation time ahead of a regressed clock. Its
correction passed the final regression and race runs.

## Assertions ported

`retained_storage_billing_test.go` ports daily and partial-day pricing,
fractional truncation, no charge on the first pass, allowance/free-byte caps,
no-quota charging, periodic charges before release, interval replay, idle-gap
clamping, stable inventory while fresh bytes arrive, disabled-price behavior
and monthly balance reconciliation. It adds real tagged-log commits, shared
log/blob free allowance, tenant isolation, independent clients, reopening,
ledger rollback, lease fencing, clock regression and corruption/overflow tests.
The acceptance proof ports financial preservation through real leased retention.
Old principal allowance/retention tests remain with their unported APIs.

## Remaining work and integration

1. W1 must wire `recordBillingAwardTx` into issuance using server-derived
   metering, resources and pricing. `authority_issue.go` still writes unmetered
   awards directly. Start intervals at execution, exclude customer-runner
   waits, charge renewals and settle releases, terminal transitions and reaping
   at their persisted end or lapse. The primitives and fake-calculator tests
   are implemented. Reconciliation still needs atomic memo materialization.
2. W1 must finish final-settings callers. `computeLimitsSQL` still selects
   `sparkwing_meta`; `SetFreeTeamSlots` still uses the legacy settings writer.
   `upsertCreditSettingSQL` and `parseCreditSetting` remain until those callers
   move. W1 must merge its parallel admission/claim/retention implementation.
3. W2 must implement `capability.SettleBilling`, payment signature verification
   and `VerifiedPaymentEvent.Event`, configure payment mode and wire verified
   settlement. Those functions remain stubs in this worktree. Existing store
   tests use contract fakes. The new retention proof seeds provider evidence
   directly and makes no claim about Stripe authentication.
4. Remaining W1b cuts include claim-time credit APIs in `credits.go`, legacy
   paid-grant paths, `node_logs.go`, principal quota/report APIs and legacy
   allowance/retention helpers. Their live consumers must be migrated or cut
   without touching W1's transition/claim implementation. The raw Store
   retained-billing API and its metadata watermarks are now gone.
5. W7 must connect upload reserve/lookup/verified commit/abort to physical
   integrity checks and trusted `ReplicaID`, and supply manifest reads and
   blob deletion. `blob.AuthorizeEdge`, `RecordNamespace`, `MemoNamespace`
   and `DepcacheNamespace` remain stubs and are tested through contract fakes.
   Confirm the cloud-builder provenance mapping.
6. W1's elected maintenance coordinator must order final retention, allowance
   policy, both blob GC phases and storage billing under the same lease. The
   migrated controller billing call is concrete, but the surrounding legacy
   maintenance/settings/report path is not a final-schema integration proof.
   Storage billing still samples retained bytes per pass; it is not an exact
   historical byte-time integral for objects removed between samples.
7. W2/W7 log signing must use `LogSegmentObjectKey` and sealed retention
   classes. Exact proxy egress, lifecycle-rule checks, physical expiration and
   live-ring segment fallback remain external obligations.

## Contract changes needed and questions

The new operator billing method is a local C1 contract correction, recorded in
`c1-data.md` and the store API snapshot. No foreign contract was changed.
Inherited questions remain:

- W1/W2/W9 must coordinate test/live identity for retained cards, attempts,
  warnings, freezes and refund work. Those tables/API inputs lack a mode
  discriminator, so provider IDs can collide after a configuration switch.
- W1 must update its SQL guard's legitimate cross-team authority/payment and
  retention exemptions. Remove stale `(*Store).PaidGrantTeam` and
  `(*Store).RecordPaymentWarning` entries, add their scoped lookup owners as
  appropriate, and review `(*Store).settlePaymentFacts`. The new billing scan
  correlates each inventory lookup to a known team and requires no exception.

## Shared files touched

- `pkg/controller/storage.go`: migrate the removed billing caller and remove
  its obsolete principal sweep call.
- `pkg/controller/storage_allowance_test.go`: remove two superseded billing
  tests and their view helper; retain unrelated route and licensing assertions.
- `pkg/store/deletion.go`: remove the deleted watermark-prefix cleanup. Final
  team-storage rows already carry the billing watermarks and tenant ownership.
- `internal/acceptance/store_billing.go` and `tests/acceptance/billing_test.go`:
  execute the new SQLite retention proof.
- `design/rewrite/contracts/c1-data.md`, `.apidiff/pkg_store.txt`,
  `CHANGELOG.md` and generated `pkg/docs/changelog.md`: align the contract,
  method snapshot and user-facing note.

Other changes are owned storage-credit implementation/tests and this handoff.
Mechanisms added: none beyond implementing the existing maintenance fence and
watermark columns. The local raw Store API was replaced, without a shim.
Friction: foreign contract stubs, legacy consumers and fixtures, existing SQL
scope findings and broader snapshot drift. `commentcheck` was absent from PATH;
its repository entry point ran successfully. No local Git identity or
core.worktree override was found. All checks exited, no services were started,
and the scratch directory was deleted.

STATUS: CONTINUE
