# W9 remaining contract boundaries

## Continuation at c535d9f2f

Production code and foreign contracts are unchanged from the preceding report.
No additional implementation can close the two assigned production gaps under
the declared contracts. The brief requires stopping a piece before changing
another area's contract. The separate evaluation binder and launcher boundary
already have fake-based tests.

The lead's next handoffs are:

- **W2/W3:** declare how an owner/eval SDK process retains its live owner/eval
  fence and separately names the source job. `CredentialNodeProcess` still
  requires `BindingNode` with a positive attempt. `ProcessAuthorities.Delegate`
  and `NativeProcesses.processServices` still equate the service job with that
  node binding. A supported production evaluator must not claim a work attempt
  to obtain its service authority. After that contract lands, W9 can implement
  `NativeConfig.Evaluate` against it.
- **W8:** supply a production `CloudBoundary` constructor, including actual
  network enforcement and reservation-scoped teardown. `NewLauncher` accepts
  that interface, but `KubernetesBoundary` still requires deployment callbacks
  `Probe` and `Clear`, supplied only in tests. W9 can then compose
  `ProductionRuntime.Launcher` and run the full installed-runner done test.
- **Lead:** rerun `TestReviewPublicDoctorSweepsAndWarnsForPrunedRun` natively
  against this branch with isolated state. The 00:55 failure predates the
  committed drain fix. No combined native verdict is available here.
- **Korey:** resolve the reserved unpublished SDK replacement policy. Its
  implementation and test expectations remain unchanged.

Fresh checks pass: whole-repository `go build -p 4 ./...`, the focused drain,
service-install handover, process-service construction, separate owner-eval
binder, pruned-run doctor, public doctor warning and peer-intake property tests,
the drain regression under `-race`, surface/engine-amendment acceptance
components, and commentcheck across 2,345 files. All Go commands used the
required shared cache and worker scratch, with package parallelism at most four.
The unchanged failing launch/native probes were not rerun. The preceding
failure evidence still applies. No native acceptance cell or archived test was
newly completed.

Only this report changed. Production additions/deletions are 0/0. W9-owned
production Go versus main remains +16,876/-23,797, net **-6,921**. No shared
source, API, caller, lead-owned handoff or amendment changed. No mechanism was
added. Scratch is removed after checks join, and the shared cache is retained.
Friction remains the foreign contracts above, native socket/service-manager
verification, and the installed Overwing binary's reported source lag.

## Previous continuation

Base: `9c19e6bdc`. The drain fix is already committed on this branch. This
continuation changes no production code. Local-controller and local-schedule
native passes remain the lead's evidence. SDK replacement policy is unchanged.

The reported 00:55 native doctor failure predates the drain fix in `112188414`,
committed at 00:59. This continuation attempted the combined public doctor probe.
It failed before reaching the sweep because the sandbox refused the api/admission
socket connection with `connect: operation not permitted`. Its cleanup likewise
could not connect. Rerun `TestReviewPublicDoctorSweepsAndWarnsForPrunedRun` natively
against `112188414` or a descendant with isolated HOME and state. The existing
in-memory drain race regression passes. There is no new native cleanup verdict.

## Verified state

The drain acknowledgement regression passes, including under the race detector.
The controller's pruned-run doctor sweep and the public command's warning
projections pass their focused tests. The combined native
`TestReviewPublicDoctorSweepsAndWarnsForPrunedRun` still needs the lead's rerun.
These component passes do not establish native daemon cleanup.

Findings 7-9 retain their implementations: complete process/storage bindings,
shared log segments and record/object publication, and service installation that
joins the old owner before activating its successor. Their focused construction
and lifecycle tests pass. The private JSONL writer remains deleted.

Local peer intake still commits through HTTP and SQLite. Its generated identity,
forgery, foreign-team, replay and revocation property passes. No peer, bearer or
committing authorization rule was relaxed.

The installed runner's `rewrite_integration` probe passes start, warm,
fetch-source and run-node. It fails launch with `launcher runtime is unavailable`.
The four passes establish the first production HTTP operation, not completed
pipeline execution. The full done test remains red.

## Contract changes needed

1. **C2/W3 owner/eval process authority.** The invariant is that evaluating an
   owner's source job neither acquires nor impersonates a work attempt.
   `CredentialNodeProcess` in `internal/capability/policy.go` requires
   `BindingNode`, a holder, positive generation and positive attempt.
   `ProcessAuthorities.Delegate` in `internal/nodehost/process_authority.go`
   requires the service job to equal that bound node. `NativeProcesses` retains
   the same relation in service lookup. Supply an owner/eval process credential
   and a source-job relation that preserve the owner/eval fence independently of
   a work attempt. `NativeConfig.Evaluate` already supplies the integration seam,
   and `TestNativeOwnerEvaluationUsesItsOwnBinderBeforeAnyWorkAward` passes with
   a fake binder. A production binder cannot satisfy the current policy. The
   swarm brief requires stopping this piece before changing a foreign contract.
2. **W8 production Cloud boundary.** The invariant is that a launch uses actual
   network enforcement and reservation-scoped cleanup before a node is reused.
   `KubernetesBoundary.Probe` and `Clear` remain required deployment callbacks.
   The only supplied implementations are test callbacks. `launcher.NewLauncher`
   provides the authenticated controller composition, but the installed runtime
   cannot construct an enforced production boundary from the merged components.
   W8 must supply that constructor or those deployment implementations. The
   existing runtime and boundary fake tests cannot establish deployment
   enforcement. No no-op callback, manifest-only proof or altered probe verdict
   was added to make launch pass.
3. **Local SDK pin policy.** The native local-run failure on an unpublished SDK
   replacement remains Korey's reserved decision. No pin reader, source
   acquisition rule or test expectation changed.

The other foreign integrations remain in W9-HANDOFF.md and the subsequent
continuation reports. They include canonical remote placement eligibility,
Cloud plan/eval hosting, bootstrap checkout consumption, cross-run record/read-set
proofs, sealed scheduled intake, and the remaining here/cache/publication and
public step/receipt projections. No new completion is claimed for them.

## Checks

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9. Package parallelism was at most four.

Passed:

- Whole-repository `go build -p 4 ./...`.
- `go vet -p 4 ./internal/wingd/... ./internal/cluster ./cmd/sparkwing ./pkg/controller/client`.
- Focused local-runtime, drain, service-installation, native-service and
  owner-evaluation binder tests in wingd, wingd/client and localcontroller.
- Focused doctor sweep/warning, peer-intake and private hosted-state tests in
  controller and the CLI.
- The drain acknowledgement regression under the race detector.
- Surface and engine-amendment acceptance components.
- Installed-runner composition for start, warm, fetch-source and run-node.
- Commentcheck against main across 2,345 files and `git diff --check`.

Failed: the full installed-runner composition test, solely at launch's missing
production factory. The assertion and production error remain unchanged.

No acceptance cell was newly unskipped or marked as a native pass. No archived
test was newly ported.

## Scope and friction

Only this report is updated. No shared source, contract, lead-owned handoff or
amendment changed. No API was removed and no caller migration was needed.
Production lines added/deleted in this continuation: **0/0**. W9-owned production
paths versus main remain **+16,876/-23,797, net -6,921**, within the -2,900
budget. Tests, testdata and contract declarations are excluded.

No Git write, candidate installation, operational Sparkwing command, pipeline,
deployment or release ran. Git still reads Korey's global identity and has no
core.worktree override. Worker scratch is removed after checks join. The shared
build cache is retained. Friction is the unchanged foreign contracts above,
native socket/service-manager evidence unavailable in this sandbox, and the
installed Overwing binary's reported source lag.

STATUS: CONTINUE
