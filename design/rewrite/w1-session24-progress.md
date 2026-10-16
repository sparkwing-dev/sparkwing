# W1 continuation 24

W1 remains incomplete. This continuation resolves the merge's file contents,
removes four raw Store queue-read APIs and joins W1b's unmetered settlement fix.
HEAD remains 42a06420dd5e7c44c977871c8de3460245fe6ad2. No Git write ran.
W1b persistence and accounting implementation files are unchanged.

## Merge

The only conflicted files were contracts/handoffs.md and
contracts/t1-port-inventory.md. Handoffs is byte-identical to stage 3, the
integration branch, as SWARM-BRIEF requires. The inventory retains both the W1
dependency-writer retirement and W1b egress/accounting entries. Neither file
contains conflict markers.

The index still reports both paths as unmerged. The brief forbids Git writes
and the worktree's Git metadata is read-only, so the lead must stage them and
finish the merge. This report does not claim an index resolution or merge commit.
Amendment and handoff files received no implementation-status edits.

## Implementation and deletion

CountPendingNodes, CountNodesByQueueState and CountActiveRunners now belong to
Operator. Their Store methods are removed. The controller sampler selects
Operator explicitly. CountPendingTriggers remains tenant-scoped; its raw Store
wrapper is removed, and the local consumer and detached-run fixture select
DefaultTeam explicitly. No aliases or forwarding methods were added.

Queue metrics now classify a pending held job without ready_at as claimed once.
Previously it counted as both waiting and claimed. The ready count requires
pending status, so an approval with a retained readiness timestamp is not ready.
Lease expiry does not reinterpret persisted queue state. Only recovery changes
it. The active-runner window uses the store clock and preserves the existing
distinct claimed_by and inclusive window-boundary semantics.

W1's accountUnitBillingTx no longer supplies a duplicate unmetered calculator.
It delegates to W1b's merged accountBillingAwardTx, which owns the bypass.
Renewal, release and both started and unstarted recovery of trigger, plan, eval
and job units complete without calling a calculator, charging credits or
creating billable intervals.

The continuation deletes **171 Go lines** and adds **341**, a net increase of
170 including tests and acceptance support. Production removes 98 and adds 91,
a net reduction of **7 lines**. Three existing reads moved; their moved lines
are included in both gross counts. These counts exclude the incoming staged
integration changes, Markdown and generated snapshots. There is no schema,
setting, environment variable, service, admission restriction or billing-policy
change.

## Invariants, acceptance and ports

The queue invariant is: every unfinished job of an active run belongs to exactly
one persisted queue state, independent of whether its lease has elapsed.
TestOperatorQueueCountsPartitionPersistedStates generates 256 jobs across two
teams, five parent states and every job outcome, varying readiness, holder and
lease expiration. It compares the public Operator counts with the state-machine
classification, then advances the store clock by a day without reaping and
checks the same partition. It also checks the public ready count independently.

The tenant invariant is: another team's pending triggers never affect a tenant's
backlog. TestTenantPendingTriggersIgnoreForeignQueueProperty generates 64
interleaved trigger rows across two teams and three states, checking both
tenants after every insertion. The empty-queue and runner-window tests retain
all metric labels, distinct-holder counting and the exact nanosecond boundary.

The two legacy queue-state tests in queue_state_counts_test.go are replaced by
these final-schema tests. Their separation and empty-state assertions are
retained, including approval_pending, terminal parents and lapse behavior.
The unrelated ledger tests in that file are unchanged. The retained terminal
parent claim test changes only its three queue-read callsites to Operator;
its old runtime fixture is not claimed ported or passing.

The existing placement-ordered-matching/store-sqlite cell now checks public
ready and claimed counts before and after real awards. The existing
recovery-unstarted-lapse/store-sqlite cell checks that expiry alone stays claimed
and that the reaper's committed pending state survives reopening and counts as
waiting. No native cell was unskipped or relabelled.

TestOperationalQueueSamplerReadsFinalStoreStates uses the real final-schema
database and controller sampler, then reads the Prometheus registry in-process.
It checks all five queue labels, pending jobs and active runners. Enrollment,
claim and native HTTP behavior are not inferred from this sampler test.

TestUnmeteredUnitLifecycleNeedsNoCalculator covers twelve combinations of unit
kind and release/unstarted-lapse/started-lapse through issuance, exchange where
required, renewal, job start and the elected reaper. Its calculator fails if
called. It checks final award settlement, zero charges and zero intervals.
W1b's helper-level unmetered test remains separate and unchanged.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE is /tmp/sw-astra-W1/cache; GOTMPDIR and TMPDIR are
/tmp/sw-astra-W1/tmp. Commands use go -C with this worktree's absolute path.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet -p 4 passes for pkg/store/..., pkg/controller/..., internal/orchestrator/..., internal/acceptance/..., tests/acceptance/... and cmd/sparkwing/....
- The W1 store selection passes **408 top-level tests / 1,227 cases**, with no skips. Extract the Test names from the files in w1-session24-tests.txt, exclude names containing Postgres, and use their anchored alternation with ./pkg/store -count=1 -timeout=180s.
- **Fifteen SQLite cells** pass. Run ./internal/acceptance ./tests/acceptance with -run '^(TestLifecycle|TestRecovery|TestPlacement)$/[^/]+/store-sqlite$' -count=1 for thirteen; run ./tests/acceptance with -run '^(TestBilling|TestSurface)$/[^/]+/store-sqlite$' -count=1 for the other two. These cover three lifecycle, eight recovery, two placement, one billing-start and one schedules cell.
- Controller -run '^(TestOperationalQueueSampler|TestSetQueueDepthCoversEveryState|TestQueueDepthCache)' -count=1 passes. TestDocumentedMetricsMatchTheRegistry passes separately.
- commentcheck -base main passes across **1,124 files**. git diff --check, sync-docs.sh --check and check-changelog.sh pass.
- The store API snapshot regenerated to scratch differs only by the three Operator declarations and four removed Store declarations. That generated store snapshot is copied back. No other API snapshot changed in this continuation.
- Git has no local user.name, user.email or core.worktree override.

Before the queue fix, the generated test counted waiting=13 instead of 10 and
the window test counted four runners instead of three. Both pass after the
change. The unmetered test initially omitted the job actor's AuthorityRef; its
fixture was corrected without changing production authority checks.

TestExpositionParsesAndNamesTheDocumentedSet could not run: httptest.NewServer
panicked on listen tcp6 [::1]:0 with socket: operation not permitted. Its
in-process registry/documentation counterpart passes. No native HTTP, api.sock,
PostgreSQL, race, provider, pod, SDK-process, full legacy-suite or pipeline pass
is claimed. The lead owns independent review, gates and landing.

## Amendment and handoff audit

The W1b handoff's unmetered override removal is complete. Its moved billing
settings and removed raw grant/event wrappers already have migrated callers in
the incoming integration content; the whole-repository build confirms those
signatures join. No W1b implementation was copied or rebuilt.

The previously completed W1 transactions remain covered by the inherited test
selection: A1-07/08/11, A1-13 through 18, A1-20 through 26, A1-40,
A1-44/45 and A1-55. Their production integrations remain distinct from store
component evidence. A1-27 now also exercises the merged unmetered owner through
real lifecycle calls. A1-51 advances through this queue-read hard cut.
A1-28/29/31 through 35 remain in W1b's billing/blob/log split. A1-53 remains
native integration work for the lead and owning areas.

No new foreign contract shape is requested. The existing requests remain:

- A1-21: C2 BindingState still lacks cancellation reason. W1 persists and renews it separately. W2 must extend that snapshot and bind production authentication.
- A1-23 through 26: W2 must install real authorization/evidence, secret opening, enrollment and delegation validation. ValidateDelegation is still a panic stub. Store fakes cannot establish native credential issuance.
- Attempt identity: W9 JobTransitionRequest still has no SourceAttempt. W9 must carry it, and W3 must send the captured source ordinal and consume Applied.Attempt. W1's holder transition refuses its omission.
- A1-38: LauncherObservation still lacks claim generation and Kubernetes Job-instance identity. SyncLauncher remains a stub because UnitID alone cannot distinguish re-awards. W8/W9 own that contract amendment.
- A1-27: C2 SettleBilling remains a panic stub. W3 must report current owner compute through Executing, including false during customer-runner waits. W1b still owns spend/grace policy and legacy accounting retirement.
- W3/W4 still own strict expansion concurrency mismatch enforcement. W1 passes accepted ExistingConcurrency and tests that handoff with the contract fake.
- A1-40/53: real local-controller readiness/drain, host/source execution and native placement remain owning-area and lead integrations.

The remaining W1 deletion work is unchanged: StateStoreOld, CreatePlanNode and
legacy bootstrap, UpdatePlanSnapshot, raw readiness publication, raw run/node
lifecycle writes, and old claim/token/trigger paths and callers. This report
does not mark those cuts or the full W1 area complete.

## Shared files and cleanup

Production caller migrations outside pkg/store:

- pkg/controller/server.go
- internal/orchestrator/trigger_consumer.go

Shared test and acceptance files:

- cmd/sparkwing/run_detached_process_test.go
- pkg/controller/operator_queue_internal_test.go
- internal/acceptance/store_queue_order.go
- internal/acceptance/store_unstarted.go

Within pkg/store the changed files are store.go, unit_billing.go,
unit_billing_test.go, operator_queue.go, operator_queue_test.go,
queue_state_counts_test.go, terminal_parent_claim_test.go,
tenant_sql_scope_guard_test.go and default_team_callers_test.go.
The removed wrapper's stale default-team caller exemption is deleted;
the three cross-team SQL exemptions move to Operator with their reasons.

Shared prose is c1-data.md, CHANGELOG.md, docs/observability.md and
docs/migrations/_unreleased.md. The docs mirror and embedded changelog are
regenerated. The two conflicted contract documents have only the resolution
described above. The exact store-test selection is w1-session24-tests.txt.

The friction points are the merge index left to the lead, socket denial, the
existing foreign shapes and production stubs, and remaining legacy fixture
migrations. No new product decision is requested. The only local contract edit
assigns the existing queue reads to their explicit Operator or Tenant owner.
Reverting that owner cut would restore the four raw Store methods and their
listed callers; it would not require a schema change.

Scratch is deleted after final verification. No owned service, background
process, Sparkwing binary, Git write, release or harness-memory write remains.

STATUS: CONTINUE
