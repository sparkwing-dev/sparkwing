# Dashboard and API grid verification

Baseline: `91341c147`. Candidate runtimes and tests used private HOME and
SPARKWING_HOME under `/tmp/sw-codex-dashboard-api/`, Go 1.26.9,
`GOWORK=off`, `GOFLAGS=-buildvcs=false`, and `GOCACHE=/tmp/sw-gocache`.
GOPATH, GOMODCACHE and ASDF_DATA_DIR retained the harness's shared paths.
The supplied worktree was the only source checkout edited.

## Regression evidence

| Surface | Old-code result | Fixed result |
| --- | --- | --- |
| Dashboard sign-in | Real socket mint and browser redemption succeeded, then `GET /api/v1/runs` returned 401, `credential is not live`. | The authenticated request returns 200 and `whoami` names the minting OS peer. Logout invalidates the session. |
| Dashboard cookie collision | After the bearer fix, a stale `__Host-sw_session` cookie still caused 401, `credential is unavailable`. | Both controller-session cookie names are consumed at the dashboard boundary. Unrelated cookies and the original request stay intact. |
| Plan JSON | A real Go SDK plan normalized padded and repeated labels, but preview JSON omitted `requires` and `prefers`. | Preview JSON carries exactly `["sparkwing-2cpu"]` and `["nearby"]`. Unconstrained jobs retain empty label lists. |
| Local schedule health | `GET /api/v1/schedules` claimed an installed, enabled `controller loop` with no local evaluator loop or native timer. | A missing native timer reports disabled. Native activation comes from a read-only service-manager query. Hosted health follows the evaluator loop's lifetime. |
| Unobservable native timer | Review found unknown activation could hide schedule rows, or retain `enabled: true` for a mismatched launchd registration. | Rows remain readable, activation detail says unavailable, and `enabled` is false. A Darwin mismatch regression failed before the error-path correction. |

Focused checks passed, with the isolated environment above:

```text
go test ./internal/wingd/localcontroller -run '^TestDashboardLoginAuthorizesControllerAPIAsMintingPeer$' -count=1
go test ./internal/wingd -run 'TestLocalDashboard|TestDashboardSession' -count=1
go test ./cmd/sparkwing -run '^TestPlan' -count=1
go test ./internal/crontimer ./internal/crons -count=1
go test ./internal/crontimer ./internal/crons ./internal/controller -run 'TestStatus|TestControllerHealth|TestHealthCountsLocksFollowersAndStaleOverrides|TestScheduleHTTP' -count=1
go test ./cmd/sparkwing -run 'TestLocalRuntimeConstructionRetainsAdmissionPolicyAndBudget|TestLocalServicesComposeRuntimeUnitsAndPrivateLogs' -count=1
bash bin/sync-docs.sh --check
```

The plan fields are an additive CLI preview change. They do not change an HTTP
wire type, so OpenAPI regeneration is unnecessary. Schedule health uses existing
wire fields. Source docs, embedded mirrors and the orientation guards describe
the resulting behavior. Peers reviewed pinned commits for ordinary correctness.
The session did not run the full gate, browser UI suite or real native service
installation. The lead owns the full gate after this lane. Native-manager tests
use injected executors and never alter the host's timers.

## 200-job replay remains unresolved

The original grid failure was:

```text
POST http://sparkwing-api/api/v1/runs/run-c401328e3082dbe02ff4f7dc/jobs/j012/steps/finish: context deadline exceeded
```

`nodeExecutor.recordStep` bounds terminal persistence with the existing
`limits.HeartbeatTimeout` of two seconds. That error propagates as a failed job,
so the captured failure fails loudly rather than silently claiming completion.
A persistence timeout alone does not prove corrupt or incorrectly applied state.
Whether contention is the only cause remains unproven. No timeout or retry was
changed without that evidence.

The replay copied the preserved grid fixture into a temporary Git repository,
replaced its SDK path with this worktree, and ran:

```text
sparkwing pipeline plan --pipeline big -o json
sparkwing run big --sw-workers 16 --sw-no-update -o json
```

The candidate was built from the baseline source. Its SHA-256 was
`07d3508e4deacf6ba3a7b1f2634f25e1ec25a71717808f03a6eab91d1f5f8fd2`.
The plan succeeded and declared 200 jobs. Run
`run-fe7ada8cc374bae496b2b621` executed 24 jobs successfully before explicit
cancellation. No job had the original step-report failure. This is an incomplete
replay, not evidence that all 200 jobs pass.

Load was about 16 during preparation but rose to 76 before the run, reached
about 90 during execution, and was 47.66 at cancellation. Other lanes were
running compilation, lint, store and controller checks. A private `queue list`
sample measured 14.913 external cores on the 16-core machine and admitted one
one-core job. Waiters reported `needs 1.0 cores; 0.0 available (external load
14.9)`. Pausing this lane's builds did not produce a lower-contention window.
The foreground command exited after cancellation, the private daemon reported
stopped, and the process sweep found no candidate descendants.

A fresh 200-job run on a quiet host is still required before selecting a
performance fix or declaring the failure exclusive to extreme contention.

## Mechanism assumption

Native health adds a read-only `crontimer.Status` operation and supplies the
existing `Host` dependency to the local schedule service. File inspection alone
cannot establish that a timer will fire. Returning the old controller-loop
constant or treating installed files as active would retain the false-health
bug. The change adds no setting, environment variable, wire field or deadline.
Removing the observer and the local binding would undo this addition.

## Completed follow-up replay

This supersedes the open replay finding above. The original step-report timeout
did not recur. All 200 jobs, including `j012`, finished successfully on their
first attempt. Foreground observation failed separately when an aggregate log
scan exhausted its time budget. A subsequent log read with the same binary
succeeded. The original two-second step persistence timeout must fail loudly
because the host cannot confirm the step's durable completion.

The follow-up used candidate commit `c1331c8913879a1231865a620c6e2cf69eacd79a`.
Its executable SHA-256 was
`322b06ecf1ce6c1f7636aae5c4b2d7f08b1a7c63ec413460068eb37012ea1fad`.
The preserved `R06C02/rerun-replay` fixture was copied into a temporary Git
repository, with its SDK replacement pointed at this worktree. The private
runtime retained the environment described above and selected Go 1.26.9
explicitly because the temporary HOME had no asdf version selection.

```text
sparkwing pipeline plan -C REPLAY --pipeline big -o json
sparkwing run big -C REPLAY --sw-workers 16 --sw-no-update -o json
sparkwing runs status --run run-7ca225d91e9f3e02afa4c6ec -o json
sparkwing runs logs --run run-7ca225d91e9f3e02afa4c6ec -o json
```

| Measurement | Result |
| --- | --- |
| Plan | 200 jobs, 9.15 seconds |
| Persisted run | `success`, 904.062 seconds |
| Persisted jobs | All 200 `done` / `success`, each at attempt 1 |
| Foreground command | Exit 3 after 916.51 seconds |
| Subsequent log read | Exit 0 in 0.91 seconds, 600 job records and 802 lifecycle events |

The persisted run started at `2026-10-31T07:50:18.295Z` and finished at
`2026-10-31T08:05:22.357Z`. Job `j012` succeeded between
`08:00:33.856Z` and `08:00:37.131Z`.

Observed one-minute load ranged from 13.7 to 41.9 on 16 CPUs. Other lanes kept
running checks. Admission samples measured roughly 12.4 to 14.3 external cores,
leaving little capacity after the default reserve. The replay retained normal
admission and used no timeout, retry or worker-policy changes. This was lower
load than the prior attempt, but it was not an idle-host test.

### Distinct observation timeout

The foreground command reported:

```text
run run-7ca225d91e9f3e02afa4c6ec is admitted: here handoff for run run-7ca225d91e9f3e02afa4c6ec is incomplete: request failed
```

Request `68b53ed36556037a5fc82a35` identifies the cause in the private
controller log:

```text
GET /api/v1/runs/{run}/logs status=503 dur_ms=10335
err="the log scan ran out of its time budget before making progress; retry the request"
```

This occurred during execution. The job host continued to completion, and the
later log read succeeded without rebuilding or changing configuration. The
CLI's nonzero exit correctly preserves the observation failure instead of
claiming successful observation. This differs from the grid's failed step
write. Contention is a plausible cause of both timeouts, not a proven exclusive
cause.

Aggregate pages traverse selected subjects in order, rechecking consumed streams
before reaching later subjects.
Skipping sealed subjects from cursor state would reduce those reads but violate
the current authority-recheck contract. An independent correctness review agreed
to retain that contract and avoid a new cursor checkpoint, retry or deadline
change without a repeatable defect. No correctness bug was reproduced in step
completion, and no runtime code changed in this follow-up.

Focused failure-handling checks passed:

```text
go test ./internal/nodehost -run '^TestHostStepPersistence/(step_write_refused|diagnostic_refused)$' -count=1
go test ./internal/controller -run '^(TestJSONLogReadStopsAtByteBudgetAndResumes|TestJSONLogSearchStopsAtTimeBudget|TestPersistedLogPageRechecksAuthorityDespiteConsumedCursor)$' -count=1
```

These are existing contract tests, not new regressions for an unreproduced step
failure. The first session's fixes already have old-code failing regressions,
and the lead's subsequent fast gate passed. The private daemon reported stopped
with zero runs, queued triggers, held leases and dashboard connections. Temporary
runtime files were removed after recording this evidence.
