# W9 integration recheck

Base HEAD: `0c1ec21af`. No foreign contract or implementation has landed since
the previous report. This session changes only this report and its handoff link.
The lead's native local-controller, schedule and public-doctor passes stand.
The SDK replacement pin policy is unchanged.

## Remaining contracts

- **DESIGN: owner/eval result authority.** Invariant: an evaluation reports
  under its live owner/eval lease and source-job fence without acquiring a work
  attempt. `TestReviewOwnerEvaluationReportsBeforeWorkAward` still fails for
  both annotations and summary with HTTP 409, `reason=holder_rights`.
  `pkg/store/result_metadata.go:checkResultWriterTx` requires a node holder and
  positive attempt. C1/C2 must carry and commit the independent evaluation
  source fence. This remains the previous two-strikes design handoff.
- **C2 eval process grants and source references.** `PurposeEval` still awards
  only `expansion.submit` and `data.read`. Masks and cleanup require
  `node.report`. Persisted latest-reference binding still requires `UnitNode`.
  W2 must reconcile these contracts before full evaluation services can work.
  The separate production evaluation binder and fake-boundary tests already
  exist. No work attempt or extra grant was fabricated.
- **W8 production Cloud boundary.** `KubernetesBoundary.Probe` and `Clear`
  remain required deployment callbacks with only test implementations.
  `TestInstalledRunnerRuntimeComposition/launch` still fails with
  `launcher runtime is unavailable`. W8 must provide actual network-enforcement
  evidence and reservation-scoped cleanup. W9 can then bind
  `ProductionRuntime.Launcher` through the existing factory and shared client.
  Start, warm, fetch-source and run-node pass their first-production-HTTP
  assertions. Those assertions do not prove completed execution.
- **DESIGN: committed retry selection.** Invariant: failed/all selection is
  part of committed execution intent and replay identity while source seals
  remain immutable. `TestReviewRetryModeIsPartOfCommittedReplay` still fails:
  the same key with a changed mode returns the old execution with
  `replayed=true`. C1/C2/W3 must retain and consume the mode independently of
  the source request/spec. The HTTP validator already rejects invalid modes.

The swarm brief requires stopping a piece that needs another area's contract
change. These failures reach those boundaries through the actual router,
client and SQLite or shipped runner entrypoint. Another local adapter cannot
close them. No foreign contract, lead-owned handoff or amendment was edited.

Other retained integration requests are listed in
[the preceding report](W9-RETRY-ACTIONS-CONTINUATION.md): annotation location
persistence, canonical placement and here hosting, cache/read-set proofs,
publication, Cloud plan/eval and checkout consumption, scheduled sealed intake,
settled receipts and Kubernetes debug targets. No completion is claimed for
those paths.

## Verification

Passed: whole-repository build, focused vet, full short clisurface,
controllerwire, controller/client, cluster and localcontroller suites, plus
service-installation handover, drain acknowledgement, peer-intake, numeric/debug
selector and surface acceptance selections. The existing owner/eval binder,
required construction and shared-log properties pass in localcontroller.
Commentcheck against main and diff whitespace checks pass.

Failed as described above: the two tagged controller blocker tests and the
full installed-runner composition test's launch subcase. The full short CLI
suite stops at `TestResolveSparks_WithManifest_WritesOverlay` because its
httptest server cannot create a TCP socket in this sandbox. Native CLI cells
were not rerun or newly unskipped. No archived test was newly ported.

All Go checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W9/go` and
`TMPDIR=/tmp/sw-astra-W9`, with at most four package workers. No whole-repository
test suite, pipeline, candidate installation, operational Sparkwing command,
Git write, deployment or release ran. The required read-only Sparkwing info ran.

Production additions/deletions: **0/0**. W9-owned production Go versus main is
**+16,886/-23,807, net -6,921**, within the -2,900 budget. The count uses the
lead's exclusions for tests, testdata, acceptance and contract declarations.
No implementation, legacy code or caller changed. Shared files touched: none.
Mechanisms added: none.

Friction remains the foreign contracts above, socket/service-manager access and
Overwing's reported ten-commit source lag. Git has no local identity or
core.worktree override. Scratch is removed after checks join. The shared build
cache is retained.

STATUS: CONTINUE
