# W9 remaining contract boundaries

Base: `1d107d862`. This continuation changes only this report. The requested
drain fix is already committed in `112188414`. Local-controller and local-schedule
native passes remain the lead's evidence. The SDK replacement policy is unchanged.

## Verified state

The drain invariant is that closing the controller API cannot cancel the
admission connection before its drain acknowledgement is written.
`TestLocalRuntimeDrainAcknowledgesBeforeClosingConnection` passes again, including
under the race detector. The construction, separate owner/eval binder, drain
client and service-install handover regressions also pass.

The combined public doctor probe was attempted again:

```text
go test -p 4 -tags rewrite_integration ./cmd/sparkwing -run '^TestReviewPublicDoctorSweepsAndWarnsForPrunedRun$' -count=1 -timeout=90s -v
```

It fails before the sweep with `dial unix .../d.sock: connect: operation not
permitted`. Cleanup encounters the same refusal. This does not reproduce the
reported drain acknowledgement failure or establish a native cleanup pass.
The controller's pruned-run sweep and public warning component tests pass.
The lead must rerun the combined probe natively against this commit or a
descendant, using isolated HOME and state.

Findings 7-9 retain their implementations: complete process/storage bindings,
shared log segments and record/object publication, and service installation that
joins the old owner before activating its successor. The private JSONL writer
remains deleted. Local peer intake still commits through HTTP and SQLite. Its
generated identity, forgery, foreign-team, replay and revocation property passes.

The complete `TestInstalledRunnerRuntimeComposition` was rerun. Start and warm
reach `GET sparkwing.dev/versions.json`. Fetch-source reaches
`POST controller.example/api/v1/runs/run/source-credential`, and run-node reaches
`POST controller.example/api/v1/claims/exchange`. Launch fails with
`launcher runtime is unavailable`. The full done test remains red. These four
passes prove the first production HTTP operation, not completed execution.

## Contract changes needed

1. **W2/W3 owner/eval process authority.** The invariant is that evaluating an
   owner's source job neither acquires nor impersonates a work attempt.
   `CredentialNodeProcess` in `internal/capability/policy.go` still requires
   `BindingNode`, a holder, positive generation and positive attempt.
   `ProcessAuthorities.Delegate` and `NativeProcesses.processServices` still
   equate the service job with that bound node. Supply an owner/eval process
   credential and a source-job relation that retain the owner/eval fence
   independently of a work attempt. W9's `NativeConfig.Evaluate` seam and
   `TestNativeOwnerEvaluationUsesItsOwnBinderBeforeAnyWorkAward` already cover
   the adapter with a fake. The production binder remains stopped because the
   swarm brief prohibits changing another area's contract.
2. **W8 production Cloud boundary.** The invariant is that launch uses actual
   network enforcement and reservation-scoped teardown before node reuse.
   `KubernetesBoundary.Probe` and `Clear` remain deployment callbacks with only
   test implementations. Supply their production implementation or a complete
   `CloudBoundary` constructor. W9 can then compose `ProductionRuntime.Launcher`
   with `NewLauncher` and the authenticated controller transport. Passing no-op
   callbacks would not meet the contract or complete the installed-runner test.
3. **Korey SDK pin policy.** The unpublished SDK replacement decision remains
   reserved. No pin reader, source acquisition rule or test expectation changed.

Other real integrations remain recorded in [W9-HANDOFF.md](W9-HANDOFF.md),
[W9-RUN-PAGES-CONTINUATION.md](W9-RUN-PAGES-CONTINUATION.md) and
[W9-LOCAL-SDK-CONTINUATION.md](W9-LOCAL-SDK-CONTINUATION.md): canonical remote
placement eligibility, Cloud plan/eval hosting, bootstrap checkout consumption,
cross-run record/read-set proofs, sealed scheduled intake, public step/receipt
projections, and the remaining here/cache/publication bindings. No completion is
claimed for them. The lead-owned handoffs and amendment assignments were read
and left unchanged.

## Checks and scope

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W9/go` and
`TMPDIR=/tmp/sw-astra-W9`. Package parallelism was at most four.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Vet for wingd and subpackages, cluster, CLI and controller/client.
- Focused drain, service-installation, native construction and separate
  owner/eval binder regressions in wingd, wingd/client and localcontroller.
- The drain acknowledgement regression under the race detector.
- Doctor sweep/warning and peer-intake property selections in controller and CLI.
- Surface and engine-amendment acceptance selections.
- Commentcheck against main across 2,345 files and `git diff --check`.

Failed: the combined public doctor probe at the sandbox socket boundary, and the
full installed-runner composition probe at launch's missing production factory.
No acceptance cell was newly unskipped or claimed as a native pass. No archived
test was newly ported.

Production additions/deletions in this continuation: **0/0**. W9-owned production
Go versus main remains **+16,876/-23,797, net -6,921**, within the -2,900 budget.
Tests, testdata and contract declarations are excluded. No shared source, API or
caller changed. No mechanism was added. Git reads Korey's global identity and
has no core.worktree override. No Git write, candidate installation, operational
Sparkwing command, pipeline, deployment or release ran.

The process namespace showed no surviving worker daemon or child after the
probe. Worker scratch is removed after checks join. The shared cache is retained.
Friction remains the foreign contracts above, native socket/service-manager
verification, and the installed Overwing binary's reported source lag.

STATUS: CONTINUE
