# W1b handoff

W1b remains incomplete. This continuation starts at `5438be851` and replaces
principal-selected storage policy and reporting with final-schema tenant and
operator APIs. Earlier metered accounting, payments, uploads, records, GC and
log persistence remain in place. W1's admission, claim, state, reaper, schema,
quota-admission and SQL-guard implementation files were not edited.

## Implemented

- `storage_quota.go` owns tenant quota configuration, retained allowances,
  historical usage and retained-byte reports. Quota, usage and ranking values
  carry `Team`. Tenant methods accept no principal selector. A mismatched team
  in a quota input is refused. Operator methods own cross-team lists/rankings.
- Quota and allowance upserts preserve each other's fields. Creating an
  allowance without a quota preserves the effective default tier. Concurrent
  independent database clients preserve both writes. Explicit unlimited rows
  override a deployment default. Negative/corrupt quotas and invalid tiers
  fail loudly.
- Usage reports sum replica rows. Run totals span periods; month totals select
  their period. Missing counters report zero. Overflow and negative individual
  counters return errors, including negative values hidden by a positive sum.
  Rankings sum before ordering and limiting, with team as the tie breaker.
- Retained-byte reports share the billing inventory: unique committed blobs
  plus persisted log bytes. Historical usage and reference multiplicity cannot
  inflate the retained report. Different teams account for their own copy.
- `storage_policy.go` owns operator policy in the final JSON settings table.
  Reads use one transaction; replacement writes are atomic. Corrupt, sealed,
  negative and overflowing settings are refused. Run/metric keys are the
  existing final `operator.run_retention_days` and
  `operator.node_metric_retention_days`. Alarm/default-tier keys retain their
  names. No schema, environment variable or service was added.
- Cloud seeds a 90-day run window only when absent. Explicit zero survives
  restarts. Metric retention keeps its independent policy. The controller
  invokes `MaintainRetention` and storage billing under the same maintenance
  fence. A foreign live fence prevents deletion. This uses W1's existing
  retention transaction without editing it.
- Controller quota paths name a registered team. Unknown teams return 404.
  Storage reports use the authenticated request's team, ignoring a query's
  team selector. Operator reports include aggregate rankings. JSON quota and
  ranking rows use `team`; settings use `run_retention_days`. These are hard
  cuts. Route scopes remain unchanged pending C2's final operator routing.
- The SQLite `storage-replica-safe-accounting/store-data-sqlite` acceptance
  proof now reads the public aggregate report after reopening and configures
  the shared quota/allowance through the new tenant APIs.

## Deleted and ported

Removed **811 Go lines from the legacy store implementation** and **1,032
legacy test lines** across store/controller files. These are gross diff
removals, including replaced declarations, not a net reduction claim.
`storage_retention.go` and its old tests are deleted. The raw Store quota,
allowance, reporting, settings, size and event-only retention APIs are gone,
as are principal validation, old sweep results and principal allowance helpers.
The new implementation files total 312 lines.

Assertions ported to the new tests include default-tier fallback, explicit
unlimited policy, independent quota/allowance updates, preserved defaults on
allowance creation, invalid input, stored policy round trips, explicit zero,
finish-time retention, operator/tenant report separation and team identity
independent of principal labels. The old run-usage cascade expectation is
reversed: final financial usage survives retention, as checked by the existing
billing acceptance proof. New tests add independent clients/reopening, atomic
settings rollback, sealed/corrupt policy, overflow, replica sums and unique
log/blob inventory.

No additional complete row of the archived T1 port inventory is claimed closed.
The old event/principal allowance sweeps are obsolete. Their oldest-first and
nonpayment eviction policy still needs a final record/pin-aware implementation.
The surviving legacy charged-event/manifest fixtures were changed only enough
to compile against the removed APIs; they still need final lifecycle/authority
migration. Their passing runtime behavior is not claimed.

## Verification

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`-p 4`, and `GOCACHE`, `GOTMPDIR` and `TMPDIR` under `/tmp/sw-astra-W1b/`.
The scratch directory is deleted at handoff. No Sparkwing binary or git write
was run, and no service was started.

- Whole-repository `go build -p 4 ./...` passed.
- `go vet -p 4` passed for `pkg/store/...`, `pkg/controller/...`,
  `internal/acceptance/...` and `tests/acceptance/...`.
- Store regression: **104 top-level tests / 199 including subtests** passed,
  without skips. Selection:
  `^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore)`.
- Controller regression: **6 tests** passed. Selection:
  `^(TestStoreDataStorage|TestAMultiTeamStoragePassSeedsRetentionAndReleasesOldRuns$|TestASingleTeamControllerKeepsItsOperatorsRetention$|TestStorageRetentionPassRequiresMaintenanceFence$)`.
  Tests use real SQLite stores and HTTP recorders. The route tests supply
  authenticated principal facts; they do not prove C2 authentication.
- Race selection: **63 top-level tests / 124 including subtests** passed,
  without skips, over store/controller. Selection:
  `^(TestStoreData|TestPayment|TestAMultiTeamStoragePassSeedsRetentionAndReleasesOldRuns$|TestASingleTeamControllerKeepsItsOperatorsRetention$|TestStorageRetentionPassRequiresMaintenanceFence$)`.
- Full acceptance packages passed: **20 top-level tests / 195 passes**, with
  **2,995 existing target skips**. SQLite product cells remain
  `storage-replica-safe-accounting/store-data-sqlite` (extended) and
  `billing-ledger-survives-retention/store-data-sqlite`. Native placements,
  PostgreSQL, provider verification and physical backends remain pending.
- `go run ./internal/commentcheck -base HEAD <repo>` passed across 18 Go files.
  API-spec validation passed. Generated API-reference output matches the
  source. Embedded-doc sync and `git diff --check` passed.
- API snapshots were generated in scratch. Only the owned store API changes
  and Cloud retention constant were applied. Existing broader drift remains
  in eight packages: controller, controller/client, projectconfig, match,
  store, storage/storeurl, sparkwing and storage.
- SQL guard fails with **28 findings**: 14 unscoped statements and 14 stale
  allowlist entries. Two statements are the moved operator quota/ranking
  scans. The other statements concern existing authority, payment and
  retention owners. Twelve stale entries arose from this cut, alongside the
  two inherited payment entries. W1 owns that guard; it was not edited.
- An earlier, overly broad controller selection also ran
  `TestASingleTeamControllerCountsNoShare`, which failed while inserting a
  token into the removed `tokens` table. The exact owned selection above
  passed. Full legacy suites and pipeline gates were not run. Independent
  review and gates remain with the lead.

Tests first reproduced the absent tenant APIs. Subsequent failures found an
incomplete fixture import migration and an overbroad test selection. Both are
accounted for above; no gate failure is claimed as a passing check.

## Remaining work and integration

1. W1 must wire immutable billing awards into issuance with server-derived
   metering/resources/pricing, open/close billable intervals at execution and
   waiting boundaries, charge renewals, and settle terminal/release/reaper
   transactions at persisted finish/lapse. `authority_issue.go` still writes
   unmetered awards directly here. Reconciliation still needs atomic memo
   materialization. The existing accounting and blob interfaces/tests remain.
2. W1's quota admission must consume the effective tenant policy. Its
   `quotaPolicyTx` still requires an explicit storage quota row, while the
   report can resolve a deployment default. Final upload admission also needs
   the configured per-run byte/object policy. Do not add default-row caching
   that would freeze future deployment policy changes.
3. W2 must implement billing calculation, payment verification/opaque event
   exposure, payment configuration and final operator route authorization.
   Store tests use the existing contract fakes. W1/W2/W9 must settle test/live
   identity for retained cards, attempts, warnings, freezes and refund work.
   Their persisted inputs still lack a mode discriminator.
4. W1b still needs legacy cuts in claim-time credits, paid grants, node logs,
   old event/manifest charges, raw storage counters and legacy uploads.
   `chargeStorageTx` still contains principal-column writes and its callers
   need replacement. This patch only moves its quota lookup to the owning
   team's policy. `computeLimitsSQL`/free-slot settings remain W1 integration.
5. W7 must wire upload reserve/lookup/verified commit/abort to physical integrity
   checks and trusted ReplicaID, manifest reads and blob deletion. Shared blob
   edge/namespace functions remain stubs and are tested through contract
   fakes. Confirm the cloud-builder provenance mapping.
6. Final maintenance must integrate allowance/nonpayment eviction, both blob
   GC phases and the remaining task ordering. Retention plus billing share a
   fence now, but `PruneStorageCommitReceipts`, `PruneSpentIdentity` and the
   separate legacy storage pass remain outside that final coordinator.
7. W2/W7 log transport must use tagged subjects, canonical object keys and the
   sealed retention class. Proxy egress, lifecycle rules, physical expiry and
   live-ring fallback remain external obligations.

## Contract corrections and guard handoff

The tenant/operator storage APIs, `Team` fields and run-retention policy are
local C1 contract corrections, recorded in `c1-data.md` and snapshots. No foreign
contract declaration was edited. C2/W9 still own the final operator route tree.

W1 should review/allow the cross-team operator methods
`(*Operator).ListStorageQuotas` and `(*Operator).TopStorageTeams`. Remove these
stale SQL exemptions from the guard:

`(*Store).ListStorageQuotas`, `(*Store).TopStorageTeams`,
`(*Store).StorageUsageFor`, `(*Store).StorageRetainedBytes`,
`(*Store).SetStorageQuota`, `(*Store).SetStorageAllowance`,
`(*Store).storageQuotaRow`, `storageQuotaForTx`, `(*Store).sweepTable`,
`(*Store).retainingPrincipals`, `(*Store).expireRunStorage`,
`(*Store).expiredRetainedRuns`.

The inherited `(*Store).PaidGrantTeam` and `(*Store).RecordPaymentWarning`
entries also need replacement/removal. `tenant.go` retains an obsolete comment
about raw StorageQuotaFor; leave its W1-owned orientation consistent when merging.

## Shared files touched

- Controller callers: `pkg/controller/storage.go`, `team_storage.go`,
  `server.go`, `tenant_routes.go`. Route parameter names change to `{team}`.
- Controller tests: `storage_test.go`, `storage_allowance_test.go`,
  `team_storage_test.go`, plus the new `storage_data_test.go`.
- `pkg/store/free_tier_test.go`: migrate the removed settings/expiration calls
  to operator settings and leased retention. Its legacy fixture is not ported.
- `internal/acceptance/store_data.go`: extend the SQLite boundary proof.
- Contract/snapshots: `design/rewrite/contracts/c1-data.md`,
  `.apidiff/pkg_store.txt`, `.apidiff/pkg_controller.txt`.
- Product docs: `docs/self-hosting.md`, `auth.md`, `limits.md`,
  `api-reference.md`, `api/openapi.yaml`, `CHANGELOG.md`, and their generated
  `pkg/docs` mirrors/changelog.

Other changes are owned storage implementation/tests and this handoff.
Mechanisms added: none beyond the contracted tenant/operator policy and reports,
using the existing settings, counters and maintenance lease. No new customer limit was
introduced. The Cloud window and independent metrics follow the frozen policy.
Friction remains in foreign stubs, legacy fixtures/consumers, SQL-guard ownership
and broader snapshot drift. No local Git identity/core.worktree override was found.

STATUS: CONTINUE
