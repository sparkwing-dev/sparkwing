# Run engine grid verification

All eight cells were exercised with a CLI built from this worktree before their
fixes. The starting source was `1f4998a3e`. Each fixture used a local Go SDK
replacement, so engine selection used that executable rather than the installed
QA release. The initial executable SHA256 was
`9ca6fd13f676c2ca9e888d8c5c6c2a4ef3d608fb39a4b4c976932dc6c25edc77`.
Every CLI and daemon used private HOME and SPARKWING_HOME beneath
`/tmp/sw-codex-grid-engine`. No release, cluster or external data mutation ran.

The original fixture is preserved in the grid's R03C02 evidence. Its Go SDK
replacement was changed to this worktree. The reproduction commands below use
`$CLI` for the built binary and `$FIXTURE` for that fixture checkout.

| Cell | Initial observation | Result |
| --- | --- | --- |
| R01C14 | Full release preview reached terminal failure but the foreground process continued polling successful log pages. A selected successful release preview exited. Following the stuck run independently timed out after three seconds. | Fixed the paged log path: committed records drain first, then unsealed terminal logs report incomplete after the existing seal grace. A fresh private terminal fixture exits 3 in 1.303 seconds with `conflict: logs incomplete: the runner never sealed this log`. |
| R02C19 | SIGINT exited 3, with run `run-384fd1344831b48137a1d2a7` still running and its sleep job cancelled. | Fixed. Final rebuilt CLI waits for the actual sleep child before SIGINT, persists both job and run as cancelled, emits run_finish cancelled, and exits 1. |
| R03C02 | `hello --sw-dry-run` skipped the body with `no_dry_run_defined`, then emitted `run_success` and stored `success`. | Fixed. Rebuilt CLI exits 0, emits `run_dry_run`, and stores `dry_run`. Failures and cancellations keep their outcomes. |
| R03C04 | `--sw-start-at b --sw-stop-at c` on four jobs failed with unknown step b. The same bounds on four steps executed b and c, skipping a and d. | Contract verified; correct the grid criterion. These are step ranges, as documented in CLI help and cli-run.md. Use `--sw-only` for job selection. |
| R03C13 | Three wide jobs overlapped with workers=1. wide-002 started 603084054 ns before wide-001 ended. | Fixed. The three post-fix body intervals do not overlap. |
| R03C15 | After a successful cached-source run, explain reported cached=false and a related entry differing only in build environment. | Fixed. A stable copied SDK fixture reports cached=true after its run. Copying the SDK avoided unrelated concurrent source edits changing the replacement hash. |
| R04C08 | Recovery failed with `recovery execution requires the persisted source failure`. | Fixed for foreground, detached and awarded-node paths. Rebuilt foreground and detached CLIs emit GRID_FAILURE_HOOK and the recovery job succeeds. The original job failure keeps the run failed. |
| R04C13 | Default annotation list was empty. `--steps` returned the exact warning and source range. | Already passes with documented `--steps`; correct the grid invocation. The SDK annotation belongs to step run, while the default list selects job annotations. |

## Reproduction evidence

- R01C14: `run release --sw-dry-run` used a separate release state directory,
  because the release guard refuses HOME/.sparkwing even under a temporary HOME.
  The whole release preview encountered existing step-finish RPC timeouts under
  shared load. Its terminal state did not end observation before the log fix.
  `run release --sw-dry-run --sw-only require-isolated-home` completed successfully
  and exited before changes. The final unsealed-tail CLI replay used a private
  published terminal fixture aged 61 seconds so background execution maintenance
  could not seal the open stream before observation. The HTTP regression also
  covers normal execution records, owner logs, running runs and the exact grace
  boundary.
- R02C19: the final harness started `run grid -- --mode sleep`, waited for the
  actual `sleep 60` descendant, sent SIGINT, joined the CLI and queried persisted
  status. Run `run-eb30f62e9466c02e74cf6be0` and its job are cancelled; the CLI
  emits run_finish cancelled and exits 1. A separate attempt-start timing variant
  also finishes cancelled with exit 1. Signalling before the SDK process finishes
  startup can leave that job classified failed with `credential is not live`;
  the cancelled run now still finalizes. An earlier unsealed startup log variant
  exits 3 with an incomplete-log diagnostic after the existing grace.
  The first final-harness attempt accidentally reused the pre-cut schema home;
  correcting SPARKWING_HOME to a fresh directory allowed this final check.
- R03C02: the final preview run `run-c7ee4dfa` has `dry_run` status and CLI exit 0.
- R03C04: `run grid --sw-start-at b --sw-stop-at c -- --mode chain --delay 100ms`
  rejects unknown step b. With `--mode steps --delay 0s`, the log contains
  GRID_STEP b and GRID_STEP c, with a and d skipped outside the range.
- R03C13: `run grid --sw-workers 1 -- --mode wide --n 3 --delay 1s` initially
  produced wide-001 [1793307105126355907, 1793307106126553197] and wide-002
  [1793307105523469143, 1793307106523887051]. After the fix, starts/ends are
  [1793307409070918400, 1793307410074606796],
  [1793307413592951239, 1793307414593353403], and
  [1793307418701254312, 1793307419701536381].
- R03C15: `run hello` then `cache explain -o json` initially differed in build
  environment. After sharing native Go environment preparation, the stable run
  reported `cached=true`, key `1efdc4a0-adccb277`.
- R04C08: `run grid -- --mode failure-hook --delay 100ms` initially failed before
  the callback. Fixed foreground run `run-746359efdc2ac980f51970ae` and detached
  run `run-301b23302f4c66b94cfbe2df` both execute the marker.
- R04C13: run `run-d9473bc9d060edfcaae49cbf`, queried with
  `runs annotations list --run RUN --steps -o json`, returns
  `{"job_id":"annotate","step_id":"run","level":"warn","message":"GRID_ANNOTATION","file":"grid.go","line":40,"end_line":44,"column":5}`.

## Regression evidence

Each production fix has a regression observed failing before its change:

- `TestPersistedLogPageEndsUnsealedTerminalObservationAfterGrace`: expired
  unsealed owner and job log pages previously never ended observation. Tests
  cover draining the final records before the error and exact clock boundaries.
- `TestLocalRunInterruptCancelsAndWaitsForTerminalRun`: initially timed out
  without invoking cancellation or reporting a terminal run.
- `TestOwnerHostCancellationDrainsARemoteAttemptBeforeFinishing` and
  `TestOwnerHostResumesAlreadyRequestedRemoteCancellation`: initial or subsequent
  cancellation beats previously stopped the owner without committing completion.
- `TestCancellationReportsUseTheLiveClaimLifetime`: a valid final report taking
  three seconds initially expired under the two-second heartbeat budget. Reports
  now retain the claim lifetime and still stop at hard expiry.
- `TestOwnerFinishesCancellationAfterACommittedPolicyResult`: an extra credential
  error after a committed cancellation previously left the owner unfinished.
- `TestOwnerReportsCancellationOnTheFirstSelfClaimRenewal`: initially left a
  cancelled claim unsettled. It now reports cancellation before starting work.
- `TestRunCompletionRecordsDryRun`: initially failed with
  `dry run completed as "success"`. Real-store coverage verifies completion effects.
- `TestDryRunKeepsFailureAndCancellationVerdicts` preserves unsuccessful previews.
- `TestOwnerHostWorkersBoundLocalExecution`: initially started all three blocked
  jobs with workers=1 or 2. Limits 1, 2 and 3 pass after the fix.
- `TestCacheExplainFindsInstalledSDKBinary`: real SDK installation initially
  produced cached=false with changed_inputs=[build environment]. Normal and
  qatrust tests pass after the fix.
- `TestNodeExecutorRecoveryReceivesPersistedFailure`: initially returned the
  exact missing-reader error. `TestPrivateNodeRecoveryReadsOnlyItsPersistedSource`
  also failed before scoped source projection and again for direct-store reader
  wiring. It now verifies both host stores, exact generation and artifact selectors.
- `TestEngineSnapshotRetainsSealedDryRun`: initially omitted dry_run from the
  scheduling JSON despite a sealed preview.
- `TestRunStatisticsCountsPreviewsSeparately`: initially counted a completed
  preview as running. The fixed JSON has dry_run=1, running=0 and passed=1 for
  one preview and one applied success.

## Validation and handoff

Focused CLI, owner, store, controller and SDK installer tests passed. CLI tests
include cancellation, terminal exit codes, statistics, cache explanation and
here-host observation. Source docs, embedded mirrors, CLI references, OpenAPI,
protocol schemas and API snapshots were regenerated through their owning tools.
Schema and selected documentation drift checks passed. Focused vet and sleepcheck passed. Targeted cancellation race tests passed.
The final runtime binary was built from code commit `8f541241a`, SHA256
`4a2269e45ed33c9b2087cd0e079c86093e458432a88c46922615d69a8b621a87`.
The full gate remains with the lead, as requested. PostgreSQL, clusters and cloud
execution were not exercised in this lane.

Independent reviews covered worker placement, cancellation lifecycle, cache
parity, recovery source confinement, scheduling wire state, statistics and log
grace. Findings led to tests and fixes for statistics, scheduling projection,
accepted-resume cancellation, direct-store recovery wiring, and final-report
lifetime. The fake-clock test timer was moved into its lexical synctest body
after review so sleepcheck recognizes it.

No new customer limits or flags were added. Worker slots use the existing
--sw-workers setting. Final log handling uses the existing LogSealGrace. Final cancellation reporting
uses the existing hard claim lifetime instead of the heartbeat RPC budget.
The Go environment helper replaces duplicated preparation. Recovery source
metadata remains separate from executable nodes.

The lead must coordinate dashboard recognition of `dry_run` in
`internal/web/reads.go:isRunTerminal`, and regenerate
`web/src/lib/runEventKinds.json` for `run_dry_run`. Those files belong to the
other lane. Temporary homes, binaries and test processes are removed after
verification; this file retains the measured results and regression commands. Schema baseline 26 is a hard cut, documented in the migration guide.
