# Store implementation handoff

This snapshot continues commit `6fb00e9a75d8aa272983135ccdf0e405fbce363e`.
The work is uncommitted. W1b's accounting, blob and log implementation files
were not edited.

## Implemented

- Admission allocates team run numbers, seals intake, deduplicates requests,
  preserves retry seals and lineage, handles publication without an owner, and
  derives pool access from authenticated facts. Here intake issues its award.
- Plan acceptance validates declarations and resolved settings, checks secrets,
  inserts jobs with the second seal, rejects drift, and transfers accepted
  controller plans to the elected owner. Refused guards persist and prevent
  execution. Replays do not repeat state changes.
- Named, queued and self claims share matching, resource checks and capability
  issuance. Claims enforce runner selection, granted pools, repository access,
  Here windows and independent eval/source fences. Pending eval work can move
  to a new elected controller.
- Reconciliation preserves terminal results, waits for held work until the
  reaper commits, and awards only one self-claim under contention. It reads
  materialized outcomes from persistence and supports repeated retries of
  results that did not create an execution attempt.
- Self-claimed jobs record their owner capability separately from C2 ancestry.
  Owner renewal uses this fence, refuses expired claims and excludes revoked
  leaf credentials. New retry queue deadlines use the new invocation's creation
  time rather than the preserved request seal's admission time.
- Transactional interfaces connect materialization and reported result records
  to the state writer. Refused copies and attachments roll back state and events.
  These interfaces have fake implementations in tests and still need W1b wiring.
- Controller recovery uses the elected `ReapUnits` path. Public store adapters
  expose consistent authority/digest lookup and committed bootstrap exchange.

## Deleted and ported

The legacy cut removes 2,174 old lines. The full diff removes 2,256 old lines,
including replaced stubs and edited lines. Removed surfaces include
`ResetNodeForAutoRetry`, automatic cross-run retry construction and policy
snapshots, `RetrySourceAuto`, old controller lifecycle sweeps,
`settleExpiredTriggerClaim`, and `EvaluateOld`.

Recovery ports replace reset/retry-run assertions with same-run/team recovery,
preserved accepted settings, monotone attempt/generation fences, cancellation
precedence and terminal absorption. The nodehost resume test distinguishes
requeued jobs from terminal disruption failures. Controller tests exercise the
elected reaper. Admission ports cover immutable request fields, actor/PR/SHA/App
trust on retry, secret classification, idempotency and expiry races. Claim ports
cover named/queued capacity, identity spoofing, runner choice and Here windows.
These are store and adapter proofs, not completed native acceptance cells.

## Verification

All commands used `GOWORK=off GOFLAGS=-buildvcs=false`, with Go cache and temporary
directories under the worker scratch directory. Test parallelism was at most 4.

- `go build -p 4 ./...` passed.
- `go vet -p 4` passed for `pkg/store`, `pkg/match`, `pkg/controller`,
  `internal/nodehost`, `internal/orchestrator`, and `internal/runners/warmpool`.
- The combined store selection passed 124 top-level tests, 345 including
  subtests, with no failures or skips.
- The full matcher suite passed. Focused recovery/controller/nodehost/
  orchestrator tests passed. Acceptance catalog tests passed, but native
  scenario cells remain skipped because the T1 driver is unimplemented.
- `git diff --check` passed.

The combined store selection uses every top-level test in these files under
`pkg/store`: `authority_runtime_test.go`, `authority_adapters_test.go`,
`rewrite_authority_test.go`, `rewrite_schema_test.go`, `state_runtime_test.go`,
`scheduling_contract_test.go`, `scheduling_guard_test.go`,
`concurrency_adoption_test.go`, `maintenance_rewrite_test.go`,
`expansion_runtime_test.go`, `expansion_success_test.go`,
`owner_reaper_runtime_test.go`, `unit_deadlines_test.go`, `unit_release_test.go`,
`result_runtime_test.go`, `intake_plan_runtime_test.go`, `unit_claim_test.go`,
`unit_claim_lock_test.go`, `reconcile_runtime_test.go`,
`materialize_runtime_test.go`, `state_result_records_test.go`, and
`retry_recovery_runtime_test.go`.

The full legacy store suite is not green. Broader selections still hit final
schema mismatches such as removed `triggers.pipeline` and `sparkwing_meta`.
PostgreSQL runtime, native placement execution and Sparkwing pipelines were
not run in this sandbox. Transient compile failures during parallel file edits
were resolved before the final checks.

## Contracts and integration still open

- C3/W6 must supply the real plan validator, sealers, hashes and setting
  resolvers. Store tests use their contract interfaces with fakes.
- Controllers must configure `IntakePolicy`, `ClaimProfileResolver` and
  `PlanSecretOpener` from trusted server data.
- W1b must connect metered award reservation, plan-unit billing settlement,
  `materializationRecords` and `nodeResultRecords`. Its blob/log persistence
  remains separate work.
- W9's `controllerwire.ReuseSource` needs the terminal job/state-sequence
  variant represented locally by `MaterializeSource.Result`. Its current
  attempt-only wire cannot represent every repeated materialization.
- C2 needs a contract for local connection lifetime when no execution deadline
  exists. Issuance still refuses an award with neither a deadline nor parent
  expiry. No arbitrary local timeout was added.
- Here lost-response recovery cannot reconstruct a bearer from its persisted
  digest. Identical intake replay returns the original capability prefix.
  C2 needs to define credential recovery without storing raw bearers.
- W2/W3/W8/W9 still need engine-route, host and runner wiring. W8's old fallback
  profile must supply trusted identity fields instead of advertised labels.
  Completion outbox delivery and native acceptance remain open.
- `RecoverExpiredNodeClaims` still has legacy test callers in
  `credit_expired_claim_settle_test.go`, `run_control_test.go`, and W3's
  `trigger_host_test.go` / `trigger_host_generators_test.go`. The first belongs
  to W1b and was left untouched. Unused `RunReaperLeased` remains beside W1b's
  storage leases. Other legacy store APIs and `StateStoreOld` callers still
  need migration and deletion.

The local contract additions are recorded in `contracts/c1-data.md`: server
policy/profile interfaces, authority adapters, the self-claim owner column,
creation-time projection, guard refusal, terminal-result source identity,
transactional record interfaces and delivery of the self-claim bearer.

## Shared files touched

- `api/openapi.yaml`
- `design/rewrite/contracts/c1-data.md`
- `docs/api-reference.md`, `docs/auth.md`, `docs/observability.md`
- `pkg/docs/mirror/api-reference.md`, `pkg/docs/mirror/auth.md`,
  `pkg/docs/mirror/observability.md`
- `internal/nodehost/scheduler.go`, `internal/nodehost/recovery_resume_test.go`
- `internal/orchestrator/backends.go`, `internal/orchestrator/hosted_run.go`,
  `internal/orchestrator/hosted_rehydrate_test.go`,
  `internal/orchestrator/retry_environment_test.go`,
  `internal/orchestrator/submission_environment_test.go`,
  `internal/orchestrator/trigger_host.go`,
  `internal/orchestrator/wingd_api_routes_test.go`
- `internal/runners/warmpool/warmpool.go`
- `pkg/controller/claim_attention.go`, `pkg/controller/client/client.go`,
  `pkg/controller/error_response_test.go`,
  `pkg/controller/expired_claim_internal_test.go` (deleted),
  `pkg/controller/handlers.go`, `pkg/controller/reaper_lease_internal_test.go`,
  `pkg/controller/server.go`
- `pkg/match/contract.go`, `pkg/match/contract_test.go`,
  `pkg/match/labels_test.go`, `pkg/match/match.go`,
  `pkg/match/match_test.go`, `pkg/match/tools_test.go`

STATUS: CONTINUE
