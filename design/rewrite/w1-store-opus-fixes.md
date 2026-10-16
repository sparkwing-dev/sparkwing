# W1 store review fixes

All assigned items are implemented. M4 is unchanged. No Git write ran.

| Item | Invariant | Status and evidence |
| --- | --- | --- |
| M3 | Ordinary fence readers coexist while lease keepers remain exclusive. | Shared owner locks cover claims, renewals, releases, reaping, secret/source releases and metric writes. Acquire, renewal and singleton release retain exclusive locks. Real-store SQLite paths pass. PostgreSQL contention tests use `storetest.NewPostgres`. |
| M5 | Issuance and billing use only the final capability schema and immutable awards. | Removed raw Store/Tenant token minting, duplicate node/trigger credit settlement, mutable credit columns and unused raw token declarations. Real metered claim/release drives compute usage. API/schema retirement tests ran red before deletion. |
| M6 | Contention preserves one claim winner, generation fences and capacity. | Two store handles race 12 claimants and renewal/release for eight generated rounds in both dialects. An overlay disabling `match.Fits` makes the new property fail with `capacity boundary: <nil>`. The normal source passes. |
| Reap pass | A failing target cannot prevent later independent recovery. | A real database trigger rejects the first target. The test ran red. Recovery now continues and returns committed results with joined target errors. |
| Run number | Committed admission advances exactly one tenant counter row. | A real trigger suppresses the update. The test ran red. `RowsAffected != 1` now rolls the whole admission back. |
| Lease clock | Lease decisions use database time after lock waits. | Transaction clocks and expiry scans use database time. Clock errors refuse commit. SQLite timestamps use integer arithmetic. PostgreSQL skew tests ignore an injected replica clock. Test fixtures now use the authoritative clock. |
| Sealed writes | Only correctly tenant-bound sealed values reach credential writes. | Opaque `SealedSecret` and `SealedGitCredential` values replace raw rows at the writers. The plaintext property ran red first. Generated real-cipher round trips, copied accessors and foreign sealed writes pass. |
| Dead tables | The schema contains no abandoned storage ledgers. | Removed both `storage_reservations` and `storage_commit_receipts`, their indexes and inventory entries after a failing real-store schema test. |
| Claim negative | A refused claim fails for the intended matching or holder fault. | The negative test checks the exact error code or holder-rights reason. |
| Release negative | A refused release fails for the intended fence, cancellation or revocation fault. | Error types/reasons are asserted. Parent revocation uses the committing API. |
| Renewal negative | A refused renewal fails for the intended credential or holder fault. | Error types/reasons are asserted. Revocation uses the committing API. |

Store failures were **15 before and 3 after**. The baseline completed in
233.871 seconds and the final full store run in 246.818 seconds. The new
missing-singleton-fence regression also passes in the focused lease run. Eleven failing tests retired with
the deleted billing APIs. Removing dead tables fixed the tenant-writer inventory
failure. No remaining failure was skipped, weakened or deleted. The remaining
inherited tests are `TestCollectionBindingCannotChangePayerProperty`,
`TestAWarnedCardRepaysTheDebtOnPayNow` and `FuzzStorageStandingTenantInventory`.

Whole-repository `go build ./...` and `go vet ./...` pass. Focused race tests,
credential HTTP tests through real handlers, singleton refusal/recovery tests,
fixture issuance, documentation tests and commentcheck against main pass.
The changed API snapshots match regeneration. The PostgreSQL branches skip
because `SPARKWING_TEST_PG_URL` is unset. SQLite executes their lifecycle logic.
Native PostgreSQL lock behavior is unverified here. The original 17 top-level
skips remain, with two new PostgreSQL-only skips. The existing
`TestOwnerFinishRaceRejectsStaleAwardProperty` already races stale/current
finishers and passes under the race detector. No new acceptance cell was
unskipped. Existing store recovery cells pass with database-time fixtures.

Production Go delta from starting HEAD is **-1,049 lines**
(330 added, 1,379 deleted). Under the lead's
budget filter, which also excludes contract-named files, the delta is
**-1,052**, and store alone is **-1,062** this session.
Store against main is **-4,256**, within the combined -4,250 budget.
Removed code includes the duplicate credit writer, token-table API, raw token
helpers and test-only helpers for those retired mechanisms.

The required new mechanisms are opaque sealed write constructors and a
transaction clock-error latch. The constructors prevent raw or detached values
from reaching the writers. The latch preserves existing clock call signatures
while refusing commit after a failed clock query. Fixture issuance takes final
`ProvisionSpec` values and calls the real issuer. It provides no legacy token
adapter. No environment variable, customer limit or compatibility path was added.

No foreign contract change remains pending. C1 documents the local API cut.
The controller credential interface and callers consume the new sealed types.
Lead-owned handoffs and amendments are unchanged.

The socket-based controller fixture test could not run because the sandbox
refuses listeners. Direct-handler credential HTTP tests pass. Full controller
socket suites, native PostgreSQL and product pipelines remain lead checks.
The API generator also found two pre-existing controller-client snapshot
additions (`NewDebugControl` and `ResumeDebugPause`). That unrelated snapshot
was left unchanged.

Files outside `pkg/store`, including caller migrations and documentation:

- `.apidiff/pkg_controller.txt`
- `.apidiff/pkg_store.txt`
- `.apidiff/pkg_store_teststore.txt`
- `CHANGELOG.md`
- `design/rewrite/contracts/c1-data.md`
- `design/rewrite/contracts/t1-port-inventory.md`
- `docs/migrations/_unreleased.md`
- `internal/web/browserfixture/main.go`
- `pkg/controller/compute_limits_test.go`
- `pkg/controller/concurrency_team_test.go`
- `pkg/controller/credential_mutations.go`
- `pkg/controller/credential_ssh_test.go`
- `pkg/controller/credits_test.go`
- `pkg/controller/crons_team_test.go`
- `pkg/controller/crons_test.go`
- `pkg/controller/flood_team_test.go`
- `pkg/controller/flood_test.go`
- `pkg/controller/git_credentials_team.go`
- `pkg/controller/github_app_test.go`
- `pkg/controller/github_runners_test.go`
- `pkg/controller/identity_fixture_test.go`
- `pkg/controller/pipelines_list_test.go`
- `pkg/controller/reaped_run_conflict_test.go`
- `pkg/controller/refund_authority_test.go`
- `pkg/controller/secrets.go`
- `pkg/controller/secrets_dashboard_test.go`
- `pkg/controller/team_boundary_test.go`
- `pkg/controller/team_download_test.go`
- `pkg/controller/team_tokens_test.go`
- `pkg/controller/trigger_test.go`
- `pkg/controller/trigger_user_test.go`
- `pkg/docs/changelog.md`
- `pkg/docs/mirror/migrations/_unreleased.md`
- `tests/acceptance/credential_inventory_http_test.go`
- `tests/acceptance/credential_maintenance_http_test.go`
- `tests/acceptance/credential_mutations_http_test.go`

This report is the only additional area report. Scratch under `/tmp/sw-astra-W1`
is removed after verification. `/tmp/sw-gocache` is retained.

STATUS: COMPLETE
