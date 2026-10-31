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
