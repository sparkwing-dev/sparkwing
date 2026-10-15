# Store rewrite handoff

This session continues `0e1c55adf`. Its changes are uncommitted. The earlier
admission, plan acceptance, claims, matching, reconciliation, materialization
and reaper implementation remains on this branch. W1b's billing, blob and log
files were not edited. No git writes or Sparkwing executable were run.

## Implemented this session

- `Store.RenewUnit(ctx, capabilityPrefix, executing)` derives team, unit and
  fences from live authority. It shares the existing owner and job renewal
  transactions. It adds eval renewal with independent source, eval and controller
  generation checks. Bootstrap credentials must exchange before renewal.
- Executing renewal records the first trigger, plan or eval start. Repeated
  renewal preserves that time. Reaping can now distinguish an unstarted owner
  from a started disruption through the public store API. Cancellation reaches
  the renewal reply and prevents a new start. Job starts use `TransitionNode`.
- Published plan, job and completion writes share the C2 publisher check.
  `RunPublish` validates the persisted verdict and writes completion effects.
  Replays emit no duplicate event. Controller ownership cannot execute or settle
  published history, including a publication carrying controller dispatch.
- Owner recovery commits `run_requeued` with its claim clearing and run state
  sequence. A failed event write rolls the entire recovery back.
- Queue claims enforce expiry before reaping. Both named and queued paths
  refuse expired work without an award. Reaping and transitions use the accepted
  plan's claim-wait setting after seal point two. `nodes.queue_started_at`
  preserves the queue interval across repeated unstarted losses. Execution
  start clears it, so a started disruption receives a new interval.
- The SQL-scope guard now documents the exact cross-team authority, award,
  provider-checkout and operator-retention lookups in the existing implementation.

## Deleted and ported

Removed 97 lines in complete legacy blocks: 55 implementation lines from
`bumpMismatchedNodes`, `nodeKey` and `PrincipalHoldsNodeClaim`, plus the 42-line
obsolete ready-bump test. Mismatch bookkeeping, obsolete comments and guard
entries were also removed. This count excludes ordinary edited/replaced lines
and the earlier session's 2,174-line removal.

`pkg/store/node_claim_test.go` was the only caller file migrated for the removed
ownership API. Its checks use the existing exact claim fence. There were no
production callers. No caller outside `pkg/store` needed a change this session.

The placement port reverses the ready-bump expectation: an ineligible claim
leaves queue time and state unchanged, and eligible work behind it remains
claimable. New focused proofs cover publication authority and result atomicity,
completion replay, started-owner recovery, cancellation, stale renewal fences,
bootstrap exchange, independent eval generations and accepted queue deadlines.
The recovery port exercises four unstarted job lapses without consuming an
attempt or disruption, then proves expiry at the original queue deadline.
A separate started-loss test proves its next queue interval starts afresh.
The prior session's historical port coverage remains unchanged.

## Verification

All Go commands used `GOWORK=off GOFLAGS=-buildvcs=false`, with `GOCACHE`,
`GOTMPDIR` and `TMPDIR` under `/tmp/sw-astra-W1/`. Test parallelism was at most 4.
The scratch directory is removed after checks.

- Full repository `go build -p 4 ./...` passed.
- `go vet -p 4` passed for `pkg/store`, `pkg/match`, `pkg/controller`,
  `internal/nodehost`, `internal/orchestrator` and `internal/runners/warmpool`.
- The exact W1 store selection passed 147 top-level tests, 448 including
  subtests, with no skips. It includes all `TestTenantSQLScope` checks.
- Full `pkg/match` tests and `tests/acceptance` catalog checks passed.
  `internal/acceptance` has no test files. Native product scenarios still skip.
- `git diff --check` passed.

To reproduce the exact store selection, extract each top-level `Test` function
from these files and pass the joined names as an anchored `-run` expression:

```text
authority_runtime_test.go authority_adapters_test.go rewrite_authority_test.go
rewrite_schema_test.go state_runtime_test.go scheduling_contract_test.go
scheduling_guard_test.go concurrency_adoption_test.go maintenance_rewrite_test.go
expansion_runtime_test.go expansion_success_test.go owner_reaper_runtime_test.go
unit_deadlines_test.go unit_release_test.go result_runtime_test.go
intake_plan_runtime_test.go unit_claim_test.go unit_claim_lock_test.go
reconcile_runtime_test.go materialize_runtime_test.go state_result_records_test.go
retry_recovery_runtime_test.go publication_runtime_test.go unit_renewal_test.go
queue_recovery_test.go tenant_sql_scope_guard_test.go
```

These are files under `pkg/store`. The combined selection has 147 tests.

The full legacy suite is not green. A broader reconciliation test pattern hit
`reap_pending_test.go`'s removed `triggers.pipeline` and
`team_storage_test.go`'s removed `sparkwing_meta`. Those failures were outside
the exact rewrite selection. PostgreSQL, native processes, cluster execution,
race instrumentation and Sparkwing pipelines were not run.

## Acceptance and remaining work

No native acceptance cell was activated. The T1 catalog contains native,
cluster and hosted targets, and its driver remains unimplemented. Store fixtures
with contract fakes cannot count as those placements. The new package tests
prove store portions of publication, result atomicity, loss recovery,
cancellation, lease fencing and placement queue invariants.

- C3/W6 must supply the real plan validator, hashes, sealers and setting
  resolvers. Tests continue to fake those contract boundaries.
- W2/W3/W8/W9 must wire intake policy, enrolled profiles, secret opening,
  `RenewUnit`, the engine routes and the host/runner lifecycle. No renewal
  adapter is a claim that its HTTP or process integration has landed.
- W1b must join reservation and billing settlement to award, renewal, result
  and plan-end transactions. It must connect `materializationRecords` and
  `nodeResultRecords`. Output, manifest and log integration remains unproved.
- W9's reuse source needs the terminal result/state-sequence variant already
  represented by `MaterializeSource.Result`.
- C2 still needs local credential lifetime without an execution deadline and
  lost-response recovery for a Here credential whose raw bearer is not stored.
- Published generator topology needs a C2 publication alternative for expansion
  submission: the declared route only accepts owner/eval expansion authority,
  while publication has no executable owner. Imported output-attempt identity
  also needs agreement with W1b/W7's upload contract. Metadata-only publication
  passing here is not proof of complete publication with outputs and generators.
- Completion outbox delivery and broad legacy API removal remain. In particular,
  `StateStoreOld`, old claim/plan paths and old recovery APIs still have callers.
  `RecoverExpiredNodeClaims` and `ReapExpiredNodeClaims` retain W1b test callers.
  `RunReaperLeased` remains in W1b's shared storage-lease file and was untouched.

The local contract additions are `Store.RenewUnit`, `NodeRow.QueueStartedAt`,
`nodes.queue_started_at`, its queue index, and the pending-to-failed queue-timeout
edge for a previously released job. They are documented in `contracts/c1-data.md`.
The new field is needed because `ready_at` is cleared on requeue. Keeping it set
would incorrectly derive ready state and bypass the owner's release decision.
Renewal shares the existing transaction paths because the old owner API has no
activity argument and no eval-unit path.

No other area's contract was changed. Independent review and landing remain
with the lead.

## Shared files and friction

Outside `pkg/store`, only `design/rewrite/contracts/c1-data.md` and this handoff
changed. The shared `pkg/store/schema.sql` change is confined to the job queue
column and index. W1b implementation files, controller wire and native
acceptance catalogs are untouched.

The remaining integration stubs and obsolete-schema legacy fixtures limit the
available evidence. The sandbox and swarm brief prohibit native Sparkwing runs
and git writes. Scoped Go checks work with the worker-owned cache and scratch
paths.

STATUS: CONTINUE
