# W1 continuation 20

W1 remains incomplete. This continuation retires raw unstarted-owner recovery
and ports its retained checks. No W1b implementation file changed.

## Merge resolution

The only conflict was handoffs.md. Both independent entries are retained: W1's
session-19 progress and the lead's W3 expansion-concurrency decision. Conflict
markers are gone. The index still reports that file as unmerged because the
swarm brief prohibits Git writes. The lead must stage the resolution and finish
the merge. No contract shape conflicted and no Git write was attempted.

## Removed and implemented

`Store.RequeueUnstartedClaim` and its Tenant twin are removed. Every Go caller
is removed. Expired owners use the elected `ReapUnits` pass. No compatibility
method or alternative recovery writer was added.

The invariant is: only the elected reaper may release an expired award, and a
stale holder cannot affect its replacement. The new generated property varies
24 combinations of initial generation, pending/running run, runner/launcher
owner and repeated recovery. It checks live-owner preservation, exact generation
advancement, cleared authority, no execution/disruption consumption, stale
release/renewal/finish refusal, terminal absorption and unchanged settlement
on replay. Future-dated ledger rows cannot create a second refund.

C2's start rule governs billing independently of lifecycle start. An already
executing metered runner starts at award and retains that compute charge. An
unstarted launcher pod reservation refunds. The old test expectation that raw
requeue refunds already-started host compute is removed with that API.

The Go diff removes **472 existing lines**, including **31 source lines** and
**441 test/fixture lines**. It adds **222 lines** of ports and acceptance code.
The net Go reduction is **250 lines**. Markdown and generated mirrors are
excluded from these counts.

## Ports and acceptance

The following retained checks now run through final store APIs:

- RequeueUnstartedClaim_AnswersByRunStatus, claimant-clearing tests and the
  terminal-revival branch use the generated recovery property and existing
  owner-reaper policies. A trigger without a run is prohibited by final foreign
  keys and is no longer a fixture.
- TestASupersededDispatchCannotFinishTheRunItLost and
  TestHeartbeat_StopsWhenTheClaimIsSuperseded use stale FinishRunAndUnit and
  RenewUnit checks against actual replacement awards. The old sleep is removed.
- Requeue refund and earlier-generation refund tests use immutable reservations,
  repeated settlement and future-dated ledger rows. Existing unit-billing tests
  retain price snapshots and compute/wait semantics.
- The cancelled-requeue branch remains covered by the passing owner-reaper
  cancellation policies and recovery-cancel-beats-loss cell. Tenant and elected
  reaper boundaries remain covered by the final claim and reaper properties.

`TestRecovery/recovery-unstarted-lapse/store-sqlite` now also proves owner
recovery. It uses real AdmitRun and ClaimUnit, three requeue/reclaim cycles with
SQLite reopening, unchanged intake identity, stale AcceptPlan/RenewUnit refusal
and successful plan acceptance by the replacement. The intake policy and
registered enrollment are contract fakes. This strengthens an existing passing
cell. It does not claim new native or HTTP evidence, or an additional archived
T1 source-function identity.

## Verification

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4 and
GOCACHE/GOTMPDIR/TMPDIR below /tmp/sw-astra-W1. The scratch directory is removed
at completion.

- Whole-repository `go build -p 4 ./...` passes.
- `go vet -p 4` passes for pkg/store/..., pkg/controller/...,
  internal/orchestrator/..., internal/acceptance/... and tests/acceptance/....
- The W1 selection passes **399 top-level tests / 1,118 cases**, with no skips.
  Select Test functions from w1-session19-tests.txt plus owner_requeue_port_test.go,
  omit TestPostgres names, and run their anchored alternation against ./pkg/store.
  The final future-ledger variant also passes independently.
- internal/acceptance and tests/acceptance with `-skip '^TestProtocol$'` pass
  **19 top-level tests / 200 cases**, with **2,422 native/platform skips**.
- Commentcheck against main passes across 1,062 files. Sleepcheck against HEAD,
  focused embedded docs/changelog/migration tests, sync-docs --check and
  git diff --check pass.
- No Go reference to the removed API remains. Git identity has no local override
  and core.worktree is absent. No Sparkwing binary, pipeline or socket was used.

Full legacy suites, protocol processes, PostgreSQL, native placements, providers,
independent review and release checks are not claimed. The lead owns pipelines,
review, Git writes and landing.

## Amendments and remaining integration

The session-19 amendment audit remains applicable. No new foreign contract
change is requested. A1-08/18/21/24/25/26/27/44/55 retain their implemented store
behavior. This cut advances A1-51's old API retirement and the W1 scope handoff.
A1-29/31-35 remain W1b's persistence scope. A1-40/53 still need their owners'
production and native proofs.

Open foreign points remain C2's production billing calculator and trusted
intake/enrollment providers, W3's current owner-compute reporting, W2's
cancellation-reason authentication field, W9/W3 source-attempt transport and
execution projections, and W8/W9 immutable award plus Kubernetes Job-instance
correlation for SyncLauncher. No foreign wire declaration was changed.

Remaining W1 work includes StateStoreOld, raw lifecycle and readiness APIs,
legacy claim/trigger writers, CreatePlanNode and its old controller/log fixtures.
W1b still owns legacy accounting, blob and log retirement and spend policy.

## Shared files and friction

Shared caller retirement touches only tests:

- internal/orchestrator/lapsed_lease_repro_test.go
- internal/orchestrator/trigger_consumer_test.go
- pkg/controller/runner_scopes_test.go
- pkg/controller/trigger_claim_ownership_test.go

Shared acceptance files are internal/acceptance/store_unstarted.go and the new
store_owner_unstarted.go. Shared prose is c1-data.md, handoffs.md,
t1-port-inventory.md, CHANGELOG.md and docs/migrations/_unreleased.md, plus the
generated documentation mirrors.

The lead's caller-migration exception is used for trigger_claim_credits_test.go:
only removed-API cases are retired, and the remaining lease-end test is renamed.
W1b's accounting and persistence implementations are unchanged.

Two fixture mistakes were corrected: the first port assumed all unstarted
lifecycle claims were unbilled, and the first acceptance fixture moved lease end
before award time. The final proofs use C2's start rule and a current-clock
expiry without sleeps. A combined -run pattern initially selected no acceptance
cases. The corrected exact cell selection ran and passed.

STATUS: CONTINUE
