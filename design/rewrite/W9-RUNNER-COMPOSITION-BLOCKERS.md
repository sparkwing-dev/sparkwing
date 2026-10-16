# W9 runner composition blockers

Base HEAD: `2ccf16448`. The runner composition is incomplete. This continuation
adds one tagged regression and changes no production code. Four installed
commands reach their first production HTTP operation, but an authenticated
trigger owner cannot construct the native host. Numeric selectors already pass.

`TestProductionRunnerOwnerReachesNativeHost` supplies a trigger award and fakes
only the external controller HTTP responses. It retains the production private
client, authentication, state adapter and native service constructor. It fails
with `owner hosting requires canonical placement eligibility` before socket
creation. The fixture also supplies storage authority and the private snapshot,
so those real clients remain in the path when placement is connected. It isolates
HOME, configuration and state. A sandbox EPERM from listen is an explicit skip,
and every earlier construction error is a failure. On a native host it requires
successful construction and joined cleanup.

The new probe extends the earlier first-HTTP evidence. It does not claim a
completed claim loop, source acquisition, SDK execution or native pipeline pass.
No acceptance cell was newly unskipped and no archived test was newly ported.

## Contract changes needed

1. **W1/W2/W3: canonical owner placement.** Invariant: the owner executes a job
   only when the shared matcher accepts its live enrolled profile and the sealed
   job demand. `productionHosting` leaves `NativeConfig.Local` nil, and
   `localcontroller.NativeServices` rejects every trigger owner. The private
   transport supplies neither a complete matching profile nor per-job
   eligibility. `RunnerIdentity` has only name, type and labels. It cannot
   reconstruct granted pool membership, location or capacity. The lead needs
   to select a contract for a private eligibility projection or complete
   canonical matching inputs. The proposed smaller shape is per-job eligibility
   tied to the current owner, accepted seal and job generation, derived through
   the store's shared matcher. Committing self-claims must keep their existing
   transactional recheck. An always-true predicate would turn remote jobs into
   failing self-claim attempts. An always-false predicate would lose in-process
   execution and can strand an owner in a single-slot runner.
2. **DESIGN: owner/eval result authority, retained from the prior handoff.**
   Invariant: evaluation reports use a live owner/eval lease and source-job
   fence without acquiring a work attempt. Both annotation and summary still
   return HTTP 409 with `reason=holder_rights` in
   `TestReviewOwnerEvaluationReportsBeforeWorkAward`.
   `checkResultWriterTx` requires a node holder and positive running attempt.
   The C1/C2 contract must define the independent source fence and committing
   result proof before the integrator can bind it. This is the existing
   two-strikes design request, so no further special-case patch was added.
   Eval still receives only `expansion.submit` and `data.read`, while process
   mask/cleanup routes require `node.report`. Persisted latest-reference binding
   also requires `UnitNode`. Changing those grants or relations changes C2's
   declared policy and needs its contract resolution.
3. **W8: production Cloud boundary.** `ProductionRuntime.Launcher` remains
   unbound. `KubernetesBoundary.Probe` and `Clear` still have no production
   implementations. The launcher interface requires actual network-enforcement
   evidence and reservation-scoped cleanup before node reuse. W8 must provide
   these bindings or a production constructor before W9 can connect
   `launcher.NewLauncher`. Fakes already establish the component interface.

The swarm brief requires stopping a piece that changes another area's contract.
These are the remaining boundaries for this assignment. No foreign contract,
lead-owned handoff, amendment, grant or default changed. The current C2
owner-process credential and W3 evaluation binder were retained. The broader
integration points recorded in earlier reports are not claimed complete here.

## Verification

All Go checks used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and GOTMPDIR=/tmp/sw-astra-W9/gotmp. Package workers
were limited to four. Commands ran in the assigned worktree with `go -C`.

Passed:

- `go build ./...`.
- `go test -p 4 -short ./internal/cluster ./pkg/controller/client ./internal/controllerwire ./internal/clisurface`.
- `go vet -p 4 -tags rewrite_integration ./internal/cluster`.
- `go run ./internal/commentcheck -base main .` across 2,573 files.
- `TestPublicNumericSelectorsReadAndCommitCanonicalRun`,
  `TestDebugNumericSelectorsThroughPersistedController`, and all 24 seeds of
  `FuzzMetadataNumericSelectorsRetainJobAuthority` in pkg/controller.
- `git diff --check`.

Still red, with reproducible commands:

- `go test -p 4 -tags rewrite_integration ./internal/cluster -run '^(TestInstalledRunnerRuntimeComposition|TestProductionRunnerOwnerReachesNativeHost)$' -count=1 -v`.
  Start/warm reach GET sparkwing.dev/versions.json. Fetch-source reaches POST
  controller.example/api/v1/runs/run/source-credential. Run-node reaches POST
  controller.example/api/v1/claims/exchange. Launch fails at the absent factory.
  The new native-binding probe fails at the absent placement policy.
- `go test -p 4 -tags rewrite_integration ./pkg/controller -run '^TestReviewOwnerEvaluationReportsBeforeWorkAward$' -count=1 -v`.
  Annotation and summary both receive 409 instead of 204.

These failures precede sandbox sockets. The lead's real self-hosted-runner
pipeline remains pending. No native acceptance pass is claimed. No whole-repo
test suite, pipeline, runner executable, installation, Git write, deployment or
release ran. The required read-only Sparkwing orientation ran.

Shared file touched: internal/cluster/production_runtime_integration_test.go.
The other changed file is this W9 report. Production additions/deletions are
0/0. No legacy code or caller was deleted or migrated. W9 production Go versus
main is +16,945/-23,817, net **-6,872**, within the -2,900 budget. Tests,
testdata, acceptance and contract declarations are excluded.

Mechanisms added: none. Friction: the contract gaps above and Overwing's reported
ten-commit installed-source lag. Git has no local user identity or core.worktree
override. Test children exited. Worker scratch is deleted after checks join.
The shared Go cache is retained.

STATUS: CONTINUE
