# W3 committed retry selection

Implemented the lead's 2026-10-17 01:15 decision under integration authority.
The replay regression failed before the fix: changing `failed` to `all` returned
the first run with `replayed=true`. It now returns `conflict` without admission.

C1 normalizes the mode and selects successful or memoized source results once,
under the source-run lock, after replay lookup. Mode and exact result fences
commit in `lineage_json.retry`. The original request, spec, plan and their hashes
stay unchanged. Replay identity uses the canonical source ID and normalized mode.
C2 passes the mode outside the seals. W3 reads the persisted selection before
releasing work. It probes committed results through the existing reconciliation
and record-copy path. A miss continues through the existing owner layers.

Invariant: manual retry materialization can consume only the exact result fence
committed for that execution's `failed` selection. The new fuzz seeds first
failed because `all` and unselected results could materialize. C1 now checks the
committed map before allowing either attempt-based or result-based reuse.

Proofs added or exercised:

- `TestReviewRetryModeIsPartOfCommittedReplay` passes through the real router,
  authenticator, controller client and SQLite store.
- `TestRetryAliasesReplayCommittedMode` proves numeric and ID aliases replay
  each mode exactly, preserve seals and return no second bearer.
- `TestRetryExecutesCommittedSelectionAfterRestart` constructs a fresh owner
  from persisted state. `failed` executes only the unsuccessful job and gives
  the reused job no attempt. `all` executes both jobs. A source job that succeeds
  after retry admission still executes in the retry. Acquisition and bodies are
  component doubles. Admission, claims, scheduling, transitions and record-copy
  policy are real.
- `FuzzRetryMaterializationUsesCommittedSelection` generates modes, source run
  and job IDs, state sequences and selection membership. Refused inputs leave
  the target pending with no state advance. Four seeds and 10 fuzz executions pass.

No acceptance matrix cell was newly unskipped. These are ordinary package tests,
as the acceptance-harness cleanup requires. No archived test was newly ported.
The existing materialization fixtures now seed committed selections. No removed
API or caller migration was needed. The previous host and concurrency cuts
remain in place. The cross-host runtime-mask request remains closed by the lead's
accepted residual, rather than a new distributed mask mechanism.

## Contract changes made

C1 adds `IntakeRequest.RetryMode`, `RunRow.Retry` and `RetrySelection` with a mode
and per-job result fences. An unset internal intake mode means `failed`. The
public retry request still requires an explicit valid mode. C2 carries the mode
through the existing retry request. Private snapshots already serialize RunRow,
so no new wire type or endpoint is needed. C3 records persisted host consumption.
The C1, C2 and C3 protocol contract documents describe these changes. No lead-owned
handoff or amendment file changed. No further contract decision is needed.

Shared production files touched are `pkg/store/intake_runtime.go`,
`retry_selection.go`, `run_spec.go`, `state_contract.go`, `state_read.go`,
`state_materialize.go`, `store.go`, and `pkg/controller/engine_intake.go` and
`engine_lifecycle.go`. The store.go edit tags one pre-existing clock comment
reported by commentcheck. Shared tests are the controller retry-action tests and
store retry-selection, materialization and blob-materialization tests.

Session production delta is **+112/-11, net +101**. W3-owned production delta
against main is **+11,903/-26,169, net -14,266**, excluding tests, testdata and
contract declarations. W3 adds 18 production lines this session. No legacy module
remains to remove in this retry-selection slice. The replaced 11 production lines
were the dropped-mode admission call, throwaway-request retry digest and read/write
plumbing. No compatibility path, service, environment variable or limit was added.

## Verification and native handoff

Passed: whole-repository build, focused vet of store/controller/client/host/wire/
runners/localcontroller, controller retry tests and fuzz seeds, focused store
materialization tests, native-host component tests, retry proof race tests,
retry-selection fuzzing, commentcheck and diff whitespace checks.

Full store and nodehost suites pass (250.535 s and 25.374 s). Focused retry proofs
also pass under the race detector in controller, store and nodehost.

The socket-dependent retry test selection cannot run here: the credits fixture
panics on `listen tcp6 [::1]:0: socket: operation not permitted`. The lead's
`W3-retry-native-latest.md` still contains FAIL entries for compute-limit retry
identity, cross-team retry, secret-argument retry and GitHub App tests. Retry over
native HTTP with real SDK bodies, placement and PostgreSQL remains with that harness.
This report does not claim native completion or clear those existing failures.

All Go checks use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`,
`GOCACHE=/tmp/sw-gocache` and temporary paths below `/tmp/sw-astra-W3`, with at most
four package workers. No Git writes, operational Sparkwing command, pipeline,
installation, deployment or release ran. Checks joined before scratch removal.
The persistent cache is retained. No local Git identity or core.worktree override
was introduced. Friction was socket refusal and Overwing's reported source lag.
No policy question is pending.

STATUS: CONTINUE
