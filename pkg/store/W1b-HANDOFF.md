# W1b handoff

Continues `50f72efb5`. W1b remains incomplete. Changes are uncommitted.
The lead owns review, committing, pipelines and native integration.
No Git writes, Sparkwing binary runs or W1 implementation edits were made.

## Implemented

Added `Operator.SealAbandonedLogStreams` for the existing maintenance worker.
It closes existing unsealed execution streams with `writer_lost` and preserves
segments, stored bytes and execution results. Job attempts retain their final
tail grace through the exact boundary. Trigger and plan logs use their own unit
kind, generation, status and lease. Eval also checks its source generation.
Published streams retain their publisher's final-commit contract because
publication has no execution lease.

The bounded batch locks runs in tenant/run order, rechecks eligibility after
locking, and checks the maintenance fence before writes and commit. Errors roll
the whole batch back. Existing final seals remain unchanged. The controller's
leased retention pass invokes the operation before retained-storage billing.

The final upload reservation, receipt, quota, record, GC, accounting, payment,
output-limit and log implementations from earlier sessions remain in place.

## Deletion and ports

Removed `Store.AbandonedNodeLogs`, the old controller `sealAbandonedLogs`
implementation and its reaper call: **42 production Go lines** deleted.
Removed **89 old test lines** and replaced one API snapshot entry. These are
gross deleted-line counts. New implementations and tests are counted separately.

Ported `TestAbandonedNodeLogsWaitForTheGraceAfterTheNodeFinished` to the final
attempt finish/grace contract. The old live-trigger exemption does not extend a
finished job's final tail grace. The running-subject assertion from
`TestNodeLogRecordFollowsTheRunningAttempt` is covered by the SQLite acceptance
proof and the generated liveness property.

Ported the abandoned sealing and incomplete-result concerns from
`TestControllerSealsAnAttemptItsRunnerAbandoned` to the final SQLite proof and
the maintenance-fenced controller test. Its physical transfer/deletion concerns
still need the final log transport. No archived T1 inventory row is newly
claimed fully closed.

## Unit

Six new store tests cover the inclusive tail-grace boundary, live and lost
trigger/plan/eval fences, source generations, publication, existing final seals,
maintenance takeover, invalid input, bounded batches and rollback after an
injected second-row failure. The generated property checks 64 adversarial
subject/timing combinations against this invariant: a subject writable before
maintenance stays writable after it, and its job state does not change.

The job-grace and owner-loss tests ran red before their implementation, then
passed. The property initially exposed an incomplete owner-fixture job row.
That fixture now uses a valid pending job. No production workaround was added.

The controller test checks refusal under a foreign maintenance lease and sealing
after takeover. The extended acceptance proof checks preserved segments and an
incomplete seal after closing and reopening SQLite, then idempotent replay and
unchanged job state.

Nine SQLite persistence cells pass. The log cell is extended. No new native
cell was unskipped:

- `billing-ledger-survives-retention/store-data-sqlite`
- `storage-replica-safe-accounting/store-data-sqlite`
- `storage-quota-settlement-crash-replay/store-data-sqlite`
- `storage-log-segments-and-fallback/store-data-sqlite`
- `storage-hasmany-read-set/store-data-sqlite`
- `storage-reachability-and-retention-tails/store-data-sqlite`
- `storage-memo-and-depcache-scope/store-data-sqlite`
- `storage-reference-edge-laundering/store-data-sqlite`
- `storage-publish-one-way/store-data-sqlite`

## Manual

Commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
and GOCACHE/GOTMPDIR/TMPDIR beneath `/tmp/sw-astra-W1b/`. Run with
`go -C /home/bombuntu/.xwing/envs/1088/sparkwing`.

- Whole repository `go build -p 4 ./...` passed.
- `go vet -p 4 ./pkg/store/... ./internal/acceptance/... ./tests/acceptance/... ./pkg/controller` passed.
- Store selection pattern:
  `^(TestStoreData|TestLogPersistence|TestResult|TestBlob|TestPayment|TestReversePaymentTakesBack|TestCreditSettings|TestSetCreditSettings|TestUnsetRateTable|TestSetRateTable|TestAStoredRateTable|TestATableWithoutAFourCore|TestStorage|TestBilling)`.
  It passed with **137 top-level tests / 374 passes including subtests**, no skips.
- `go test -p 4 ./internal/acceptance/... ./tests/acceptance/... -count=1`
  passed with **20 top-level tests / 202 passes including subtests** and
  **2,995 existing native/platform skips**.
- Four controller tests passed under the pattern
  `^(TestStorageMaintenanceSealsAbandonedTaggedLogs|TestAMultiTeamStoragePassSeedsRetentionAndReleasesOldRuns|TestASingleTeamControllerKeepsItsOperatorsRetention|TestStorageRetentionPassRequiresMaintenanceFence)$`.
- Documentation mirror and changelog drift tests passed. Commentcheck,
  sleepcheck and `git diff --check` passed.
- Both tenant writer guards passed. The SQL scope guard reports the same
  **10 inherited W1 findings** in authority, awards and retention. It reports
  no new W1b finding.
- No local Git identity or core.worktree override was found.

## Not tested

The full store suite was not rerun. The predecessor's unresolved full-suite
failures concern removed-schema/token/claim fixtures, including a token-test
panic. This continuation proves only the stated focused tests and compilation.
No PostgreSQL, race, provider, daemon, native placement, browser, candidate,
pipeline or release check was run. The brief assigns runtime pipelines and
independent review to the lead because this sandbox cannot open sockets.
API snapshots update only this session's removed and added symbols. Integration
must regenerate the full snapshots after all areas finish their cuts.

## Contracts and real integrations remaining

The local C1 contract adds `Operator.SealAbandonedLogStreams` and removes
`Store.AbandonedNodeLogs`. No foreign interface, schema, setting, limit or
environment variable changed. This is an operation on the existing leased
maintenance worker. Retaining the legacy sweep would read retired tables and
would omit tagged owner units. Removing the new operation and its controller
call reverses the local addition.

1. W1/W2 must define imported trigger/plan/eval identities for published owner
   logs. Job publication uses exact imported terminal attempts. Owner publication
   remains refused without that contract. No new foreign contract request here.
2. W1 must join immutable billing awards, interval start/finish, periodic charges
   and settlement to issuance, starts, waits, renewal, release, finish and reaping.
   Memo edges and result state must materialize atomically. Published run, plan
   and attempt import precedes W1b blob/log publication.
3. W2 must supply the real billing calculator, verified payment handoff and data
   authorization/signing. Card attempts, warnings, refunds and freezes still
   need end-to-end test/live identity coverage.
4. W7 must supply namespace/edge policy, verified manifest reads, physical
   verification/deletion and trusted replica identity. W1/W7 must wire final
   retention, upload quota release and tombstone GC to the elected worker.
   The legacy storage pass still reconciles raw counters and prunes old outputs
   and node logs. Its source and upload age-pruning calls are gone.
5. W2/W7/W9 must wire final log signing, transfer, publication, lifecycle expiry,
   proxy egress and ring fallback. The transport must register an empty stream
   through `RecordSegments` before its first transfer so an abandoned empty tail
   has a persisted identity. Maintenance seals existing streams only. Legacy
   controller log readers/writers and read-time physical manifest sealing remain
   until that transport replaces them. The old periodic sweep is gone.
6. W1/W9 must define final custom-event append/sequence behavior before
   `AppendEventCharged` and `chargeStorageTx` can go. W1 owns removal of egress_day
   and remaining table classification/scope guard cleanup.
7. Legacy claim credits, paid grants, raw storage counters, direct upload and
   output persistence still have W1/W2/W3/W7/W9 consumers. W7/W9 must retire
   StateStoreOld manifest consumers. No compatibility shims were added.

No human decision is requested. Next work is caller migration and atomic
integration through those contracts, with fakes for unavailable implementations.

## Shared files and friction

Caller changes outside pkg/store: `pkg/controller/log_segments.go`,
`pkg/controller/log_segments_internal_test.go`, `pkg/controller/server.go`,
`pkg/controller/storage.go` and new `pkg/controller/log_maintenance_test.go`.

Other shared changes: `internal/acceptance/store_logs.go`, new
`internal/acceptance/store_abandoned_logs.go`, `design/rewrite/contracts/c1-data.md`,
`docs/migrations/_unreleased.md`, its embedded mirror, `CHANGELOG.md`,
`pkg/docs/changelog.md` and `.apidiff/pkg_store.txt`.

Friction: missing foreign runtime owners and inherited schema/scope fixtures
remain. Cold compilation took longer than the focused tests. No W1 production
file or SQL guard exemption changed. Scratch directory was deleted. No owned service or background process remains.

STATUS: CONTINUE
