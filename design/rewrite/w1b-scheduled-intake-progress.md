# W1b scheduled intake and concurrency continuation

Tenant implements the six ScheduleWriter methods, ScheduledRunWriter and all
eight ConcurrencyStore methods. Controller compile-time assertions cover all
three interfaces.

Schedule replacement commits versioned arming consent, declarations, withdrawals
and the existing Cloud caps atomically. Local registration commits the prepared
checkout, configured entrypoint/source ref and captured environment snapshot in
that transaction. Local pause, override and deletion use authorized transactions.
Explicit empty zones, empty args and zero catch-up survive persistence.

Firing checks current consent and cadence, resolves source outside write locks,
checks the persisted snapshot again, and uses sealed intake. Run allocation,
queueing, history and cursor commit together. Replay is scoped by tenant,
schedule and due instant or manual key. Revocation and membership loss refuse
replay. Scheduled origin grants no App provenance or pool access.

Concurrency transactions use accepted declarations, full authority ancestry,
exact holder generations and declared read edges. Release and promotion commit
together. Promotions cannot outlive the recipient's work lease. Claim group
reads preserve capacity accounting while removing unauthorized leader references
and undeclared memo keys. Existing accounting and lock helpers serve both native
and HTTP callers.

## Verification

Passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache:

- `go build ./...`.
- `go vet ./pkg/store ./pkg/controller ./internal/crons`.
- Focused schedule, concurrency, capacity and tenant SQL guard tests across those
  three packages. Store took 13.559s, controller 0.487s and crons 0.005s.
- The complete internal/crons suite passed earlier in the session.
- `TestStoreScheduleBoundary`, the existing schedules/store-sqlite proof, passes.
- `TestDeclaredRepoGuard_OnlyReviewedFilesReadTheField` passes after removing the
  stale handlers.go entry.
- New generated proofs cover revoked or changed consent during source resolution,
  concurrent firing, membership changes, forged local principals, holder fences,
  declaration forgery, leader edges and authority changes before commit.
- `TestControllerScheduleFiresThroughConcreteStore` uses real controller routing
  and store transactions for a pinned Cloud source. It verifies sealed intake,
  queueing and replay. It does not execute a Cloud worker or fetch that repository.
- The local firing proof passes over the in-memory transport. The real api.sock
  variant uses wingd.ListenSocket and kernel peer identity. This sandbox cannot
  run it because socket creation returns operation not permitted. The lead must
  run `TestNativeLocalControllerScheduleFiresOneSealedRunAtDueInstant`.
- `go run ./internal/commentcheck -base main .` and `git diff --check` pass.

No acceptance cells were unskipped and no retained retry tests were removed.
The lead's latest native report still contains FAIL entries. It predates these
edits and remains the completion gate.

## Contract changes made

C1 adds ScheduleSourcePolicy, private versioned consent, the local batch writer,
and CronOverride.TZSet. The C2 writer signatures are unchanged. C1 and C2 notes
now point to the concrete transactions and name the remaining gaps. Lead-owned
handoffs and amendments are unchanged.

## Remaining work and contract changes needed

- Automatic GitHub App schedule discovery still uses legacy ArmPushed and has no
  persisted arming consent. It cannot fire through the concrete writer. Migrate
  that origin and delete the raw arming surface together.
- Private remote branch following needs audited controller credentials during
  revision lookup. The current resolver uses public transport without ambient
  credentials. Pinned private source acquisition remains on the existing audited
  post-admission release path.
- C1/C2 need persisted host identity for HTTP box groups and accepted namespace
  declarations for memo fields. Missing authority returns binding refusal.
  Native box admission remains available. Legacy memo and inheritance branches
  remain pending their caller cut.
- Native api.sock evidence and the existing failures in the lead report remain
  with the lead harness. PostgreSQL concurrency and schedule races need its native
  checks too.

## Shared files and volume

Shared edits outside the new store transactions are internal/crons arm.go,
crons.go, local.go, local_test.go, override.go and view.go, plus controller
intake_policy.go, schedule_routes.go, schedule_routes_test.go, schedules_local.go,
schedules_local_test.go and the three new schedule source/proof files. The
concurrency cut also changes authority_binding.go, capacity_owner.go,
concurrency.go and the recovery test fixture. The intake join changes
intake_runtime.go and run_spec.go. schema.sql adds one consent column. crons.go
extracts transaction helpers instead of duplicating its persistence SQL.

This session replaces 175 production Go lines and adds 1,355 across the touched
areas, net +1,180. Store alone is +1,073 this session. Combined W1/W1b store is
-1,516 versus main under the lead's production-only exclusions, 2,734 lines over
the -4,250 budget. No whole legacy file was retired.

The added volume implements the launch-scope transaction adapters that were
previously fake. It reuses intake, billing, accounting and timer ownership rather
than adding another execution engine, database or ticker. The overage remains
open because raw App arming and memo callers still prevent the corresponding
legacy deletion. It needs the lead's review and continued cuts.

Friction: native sockets are forbidden in the sandbox. Git fixtures must disable
inherited commit signing. A stale test assumed Tenant could not implement the
writer and now supplies an explicitly missing resolver instead.

STATUS: CONTINUE
