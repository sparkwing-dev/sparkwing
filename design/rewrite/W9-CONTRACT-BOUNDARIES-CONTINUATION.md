# W9 remaining contract boundaries

Base: `112188414`. The drain fix is already committed on this branch. This
continuation changes no production code. Local-controller and local-schedule
native passes remain the lead's evidence. SDK replacement policy is unchanged.

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

Only this report is added. No shared source, contract, lead-owned handoff or
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
