# W3 review fixes

All ten findings are closed. Each executable regression was run red before its
fix. The invariants below describe the resulting behavior.

| Finding | Invariant and fix | Regression evidence |
| --- | --- | --- |
| 1 | Every host capacity mutation validates the live owner and captured work generation inside its transaction. Removed the unfenced production mutation APIs and the unused explicit waiter-cancellation API. Admission locks affected work before capacity entries. | Real SQLite adoption/acquisition interruption and owner/work-generation mutation fuzz tests fail before the fix and pass after it. Stale automatic promotion is refused. |
| 2 | Supersession requests cancellation of bound work in the same operation that grants replacement capacity. Awarding a claim moves continuously held capacity to the awarded generation atomically. The shared decider drains pending intent, and the owner cancels matching active self-claimed work contexts. External holders retain admission until their claims drain. | Pending-job, actual running-unit renewal, plan-unit renewal, claim-generation handoff and real owner/self-claimed execution regressions pass. Independent executor cleanup errors survive cancellation. |
| 3 | Declared owner policy decisions persist fenced lifecycle outcomes. Typed store transitions cover admission skip/failure/timeout, approval bypass and plan refusal. A resumed refusal preserves completed work, disposes unresolved expansions, finishes unreleased work and ends the run and owner unit atomically. | `TestOwnerPersistsStoreAdmissionRefusals`, `TestOwnerPersistsPlanAdmissionRefusalAndEndsUnit`, `TestResumedPlanRefusalPreservesCompletedWork` and `TestApprovalSkipPolicyProducesSuccess` pass against SQLite. Optional jobs and generated barriers are covered. |
| 4 | Local and store admission retain the same outcome and reason, while independent failures remain errors. Local translation preserves independent joined errors; owners consume wrapped policy decisions with `errors.As`. | SQLite persisted-outcome tests and plan/job acquisition-error cases pass. The named local-admission false positive now checks typed outcomes and reasons. |
| 5 | Local execution requires machine resource admission independently of group coordination. A capacity store cannot satisfy that requirement. | `TestNativeHostCapacityStoreCannotReplaceMachineAdmission` first executed without a daemon and now refuses before execution. |
| 6 | Selection preserves the scheduler's dependency closure. Selection and scheduling share declaration and dependency projection, including present optional and generated dependencies. | `TestOnlyPreservesSchedulerDependencyKinds` first skipped prerequisites and now runs the prerequisite, generated child and consumer through the real store. |
| 7 | Terminal source generation zero is an exact expansion fence under a live positive owner generation. Issuance, descendant delegation and authentication preserve those independent fences. | Real materialization produces a generation-zero memoized source, then real delegation, authentication, expansion acceptance and settlement succeed. Removed the generation-four fake memoized variant. |
| 8 | Each unit has one keeper and one current activity projection across preparation and execution. Native hosts adopt Hosting's keeper only for its exact unit, run, job, holder and generation. | Real `Hosting` plus `OwnerHost.Run` reproduced nested renewal and false activity before the fix. Composition, race and identity fuzz tests now pass. |
| 9 | Queue expiry derives from persisted arrival, sealed policy and store time. Polling and automatic promotion adjudicate the same deadline. | Store-clock expiry and expired-waiter promotion regressions pass. The host no longer compares arrival with its own clock. |
| 10 | The current contract has one disposition for completed legacy deletion. | Reviewed the contradictory clauses before editing and removed their stale open-status claims. This editorial finding has no executable code path. |

The named local-admission false positive is supplemented by actual SQLite owner
outcome assertions. The memoized-source replacement exercises real authority
operations; its existing record-copy fixture supplies the separate materialization
data boundary.

Verification passed with `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache` and temporary Go work under `/tmp/sw-astra-W3/`:

- `go build -p 4 ./...` and `go vet -p 4 ./...`.
- Full `internal/nodehost` suite, including the corrected tests: 26.428 seconds.
- Full capability, lease, runners and runspec package suites.
- Focused store capacity, continuity, expiry, materialized expansion and fence tests.
- `tests/acceptance -run '^TestRecovery$'`, retaining SQLite capacity recovery cells.
- `go run ./internal/commentcheck -base main .` and `git diff --check`.
- Fuzz runs: owner policy 12 cases, capacity mutations 30 cases, expansion fences
  34,856 cases and exact-unit keeper adoption 72,810 cases. Keeper composition also
  passed its focused race check.

No additional acceptance cells were unskipped. The existing recovery acceptance
cells use the fenced capacity API. Retained port coverage now checks durable local
and store refusal outcomes, generation-zero expansion, dependency closure and
composed renewal ownership.

Production delta for this session is **+279 lines**, with **761 added and 482
removed**, excluding tests, testdata and acceptance harness files. The budget
convention also excludes contract paths and yields the same net delta. W3's
cumulative production delta against `main` is **-14,910 lines**, within its
-2,300-line budget. Existing policy matrices remain in test-only fixtures after
their unfenced production APIs were removed. The unused standalone supersession
operation and three tests for retired controller mutation endpoints were deleted.
Read/projection and tenant assertions retain real store rows.

Mechanisms added: typed owner policy results make the missing lifecycle edges
explicit. Exact-unit lease context sharing preserves preparation coverage while
using native execution activity. Admission uses the store's existing advisory
lock mechanism to discover and lock all affected work before capacity entries.
Existing owner workers now have individual cancellation contexts so persisted
job intent can stop the affected body while other work and remote holders drain.
Removing either keeper without sharing would lose preparation coverage or current
activity; keeping independent mutation checks would leave the transaction race.
There are no new settings, environment variables or schema changes.

No contract request remains pending for these findings. PostgreSQL locking was
reviewed but not executed because this sandbox has no PostgreSQL fixture or
socket access. A broader legacy store test, `TestConcurrencyKeyIsUniquePerTeam`,
still fails before admission on the removed `runs.args_json` column; the full
store suite was not claimed green. Production materialization record-copy wiring
remains the separately assigned integration gap. This report closes the review
findings, not the rewrite's remaining placement composition.

No repository Git metadata was written. One bootstrap command,
`sparkwing info --for-agent`, was mistakenly run despite the no-binary instruction.
All subsequent verification used Go tools. The temporary build directory was
removed and the shared Go cache retained.

Every changed file is listed below. Shared edits are marked by their group.

W3 implementation and tests:

```text
internal/lease/activity.go
internal/lease/activity_test.go
internal/nodehost/capacity_store_integration_test.go
internal/nodehost/claimed_node.go
internal/nodehost/concurrency_key.go
internal/nodehost/concurrency_key_test.go
internal/nodehost/host_expansion_test.go
internal/nodehost/local_admission.go
internal/nodehost/local_admission_test.go
internal/nodehost/native_admission.go
internal/nodehost/native_capacity_test.go
internal/nodehost/native_host.go
internal/nodehost/only.go
internal/nodehost/only_test.go
internal/nodehost/owner.go
internal/nodehost/owner_admission_result_test.go
internal/nodehost/owner_policy.go
internal/nodehost/owner_schedule.go
internal/nodehost/store_admission.go
internal/nodehost/store_admission_test.go
```

Shared implementation, fixtures and documentation:

```text
design/rewrite/contracts/c3-protocol-sdk.md
design/rewrite/w3-review-fixes.md
internal/acceptance/store_capacity.go
internal/acceptance/store_capacity_maintenance.go
internal/capability/authentication.go
internal/capability/issuance.go
internal/capability/owner_expansion_test.go
internal/runners/hosting.go
internal/runners/hosting_owner_test.go
internal/runspec/concurrency_key.go
internal/controller/claim_run_test.go
internal/controller/server_test.go
internal/controller/team_boundary_test.go
internal/store/capacity_owner.go
internal/store/capacity_owner_fence_test.go
internal/store/concurrency.go
internal/store/concurrency_adoption_test.go
internal/store/concurrency_continuity.go
internal/store/concurrency_contract.go
internal/store/concurrency_legacy_test.go
internal/store/materialized_expansion_test.go
internal/store/node_state.go
internal/store/owner_policy_result.go
internal/store/run_state.go
internal/store/scheduling_contract.go
```

Decision 38 supersedes row 3's resumed-refusal behavior. A resumed run with
released work queues to reacquire plan capacity. Completed results and expansion
state survive. `TestResumedPlanWaitsToReacquireAndPreservesCompletedWork` replaces
the refusal regression. Initial refusal of unreleased work remains covered.
