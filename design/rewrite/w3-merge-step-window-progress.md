# W3 merge and sealed selections

<!-- design:begin -->

W3 remains incomplete because native composition and placement evidence remain
open. Its legacy Session, Server, scheduler, layers, pod host and trigger consumer
are deleted. Orchestrator now has 705 production Go lines.

## Merge and deletions

All 34 conflicted working paths are resolved. W3's events_test.go and deletion
of logs_test.go survive. Runners keep the shared NewRunInput constructor rather
than restoring its private copy. The incoming retry test calls that constructor.
The integration branch's CLI, documentation, orchestrator and localws deletions
survive. No lead-owned amendment or handoff file was changed.

The index still lists the 34 paths as unmerged because workers may not write Git
metadata. The lead must stage the resolved files and deletions, then finish the
merge. No conflict marker remains in the working files.

Measured with the lead's production filters, including the resolved integration:

| Comparison | Added | Deleted | Net |
| --- | ---: | ---: | ---: |
| W3 against main | 10,952 | 25,902 | -14,950 |
| W3 against session HEAD 02f00c937 | 55 | 5,308 | -5,253 |
| New W3 implementation after resolving the merge | 55 | 67 | -12 |

W3 is 12,650 lines below its -2,300 budget. Most of this session's deletion comes
from the integration branch's removal of orphan orchestrator surfaces. The new
67-line deletion removes cutOutsideOnly, the unused PipelineRequires reader and
one replaced initializer line. No legacy hosting implementation is restored.

The two obsolete CLI adapter test files, run_host_native_test.go and
run_detached_native_test.go, are removed. The merged run_host, owner_input,
owner_identity and owner_composition suites retain intake, handles, refusal,
seal, fence and retry coverage. The detached diagnostic fuzz test moves into
run_host_test.go and uses the current command adapter. Schedules no longer have
a CLI launcher, so its due-instant helper test is removed with that API.

## Sealed selection behavior

The owner reads Only from the accepted spec and commits excluded initial jobs
as skipped before releasing work. Accepted topology stays unchanged. Recovery
jobs and generated jobs retain their existing rules. The retained SQLite test
now supplies the selection at intake instead of manually applying skips.

The executor reads StartAt and StopAt from the accepted spec. It records excluded
steps as skipped, then supplies their settlement before invoking dependents.
Bounds belonging to another job leave its steps unchanged. Initial planning
rejects unknown bounds before accepting topology. Resume rejects them before
acquiring source. The native Go SDK case proves that an excluded step invokes
neither its predicate nor its body, and its dependent still runs.

A refused skip record aborts and joins already-running parallel bodies. It
cannot produce success or retry the application. The existing parallel-write
refusal test now covers this path too.

The merged CLI host-state adapter now accepts the owner's current Executing bit.
Its native owner fixture binds that method explicitly and reports preparation
as compute. The wire property supplies the opposite node-activity callback to
prove that owner renewal forwards the owner's value. Actor authority still does
not enter the request body.

## Amendment and handoff audit

| W3 assignment | Disposition |
| --- | --- |
| A1-01/02/05/21-25, service-router handoff | Existing authenticated service registration, identifier masking and process-authority tests remain. Native controller and process delegation composition still needs its placement bindings. |
| A1-03/04/06/07 | Shared error decoding, record bounds, targeted cancellation and optional concurrency consumers remain. |
| A1-10/11/12/19 | Shared source acquisition, explicit Git environment, engine checks and awarded runner inputs remain. The constructor merge keeps original retry seals distinct from current execution identity. Actual selected-engine host invocation remains open. |
| A1-13-18 | Existing reconciliation, failure, cancellation, persisted expansion and ordinal consumers remain green. A private remote execution snapshot and run-start transport remain missing. |
| A1-36 | Existing storage bindings and per-attempt refresh remain. W1b's materialization implementation and transport are untouched, as required by the 12:15 ownership decision. |
| A1-38/39/50/51 | Legacy hosting stays deleted. CLI and runner test callers use the current host inputs. Real runner, launcher and local factories remain integration work. |
| A1-44-48, environment/alias handoffs | Shared environment, argument classification, guards, deadlines and aliases remain. Sealed step windows and Only now reach the owner/executor. Current owner compute reaches the CLI renewal adapter. |
| A1-53/54 | No native placement cell is activated. Approval SkipIf still requires C1's missing owner approval-success edge. |
| Lead 09:40 items 1-3 | Already complete. No host_* production harness files remain. Scripted fakes and the single copies of keeper checks stay in their owning test packages. |
| Lead 07:40 item 9 | Already complete. The concurrency mismatch test still refuses changed capacity, scope and on_limit, while accepting changed cost. Its focused test passes. |

## Verification and remaining integrations

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR/TMPDIR under /tmp/sw-astra-W3, GOMAXPROCS=4 and package parallelism at
most four. The lead retains pipeline execution and native platform QA.

The full nodehost, lease, source, orchestrator and runner suites pass. The new
selection tests, real SDK case and race selection pass. Focused CLI owner,
intake, here, renewal and diagnostic checks pass. Existing lifecycle and recovery
SQLite acceptance cells pass. No new acceptance cell is added or unskipped.
The port inventory names the retained selection proofs.

Whole-repository build, scoped vet, commentcheck against main, sleepcheck against
HEAD, Go formatting, the documentation mirror test and diff whitespace checks
pass. The detached argument diagnostic invariant remains: no supplied argument
appears in an intake failure diagnostic. Its migrated fuzz test passes 1,536
generated executions, including ordinary and canonical errors. Local Git config
has no user or core.worktree override.

Reproduce the focused checks from the worktree with the Go environment above:

```text
go build -p 4 ./...
go vet -p 4 ./internal/nodehost ./internal/lease ./internal/source ./internal/orchestrator ./internal/runners ./cmd/sparkwing
go test -p 4 ./internal/nodehost ./internal/lease ./internal/source ./internal/orchestrator ./internal/runners -count=1 -timeout 3m
go test -p 4 ./cmd/sparkwing ./pkg/docs -run '^(Test(Local|Owner|CLI|Here|ControllerSocket|InitialTrigger|PkgDocsContentMatchesDocsRoot)|Fuzz(Local|Detached))' -count=1 -timeout 2m
go test -p 4 ./tests/acceptance -run '^(TestLifecycle|TestRecovery)$' -count=1 -timeout 1m
go test -race -p 4 ./internal/nodehost -run '^(TestNativeExecutorSteps|TestOnlyRunsTheMatchedNodesAndWhatTheyNeed|TestOwnerRefusesUnknownStepWindowBeforeDispatch|TestNodeExecutorUsesSealedStepWindow|TestNodeExecutorStepWriteRefusalJoinsParallelBodies)$' -count=1 -timeout 2m
go test -p 4 ./internal/nodeproto -run '^TestExpansionPreservesConcurrencyDeclarations$' -count=1
go test -p 4 ./cmd/sparkwing -run '^FuzzDetachedIntakeDiagnosticsNeverExposeArguments$' -fuzz '^FuzzDetachedIntakeDiagnosticsNeverExposeArguments$' -fuzztime=3s -parallel=2 -timeout 30s
go run -p 4 ./internal/commentcheck -base main .
go run -p 4 ./internal/sleepcheck -base HEAD .
```

The broader tests/acceptance run is not green. W8 fixtures fail on missing
granted runner identities in Cloud bootstrap, runner authority, controller,
mixed claims and store lifecycle. The run reaches its two-minute budget in
TestSizingHTTPInvalidMeasurements/pin/{}{}. These failures are outside W3's
changed implementation. Full native acceptance is not claimed.

Foreign contracts and integration points remain:

- W9/W2 need an authenticated private RunSnapshot containing request/spec,
  owner, execution rows and exact expansion bodies, plus the run-start operation.
  Controller.ReadSnapshot still returns only ScheduleSnapshot.
- W1 must declare owner policy-failure and approval SkipIf success edges. The
  current node transition table cannot express them. W3 does not invent edges.
- W2/W8/W9 must compose local/runner/launcher source and storage authority,
  captured snapshots, process delegation, subscriptions, usage and matching
  engine execution. Default local and here runtime factories still refuse.
- Separate pipeline-source and execution-source acquisition still needs its
  sealed contract and native adapter. No ambient checkout substitutes for it.
- Native HTTP/api.sock, Windows/Darwin, PostgreSQL, storage backends and real
  placement/repository QA remain open.

No foreign contract shape, setting, environment variable, service or customer
limit is added. No new design mechanism is added. Selections use the existing
policy helpers and state writers.

## Shared files

Production outside W3 is limited to the conflict resolution in
internal/runners/execution.go and the renewal signature migration in
cmd/sparkwing/host_state.go. Shared tests changed in internal/runners/run_input_test.go
and cmd/sparkwing/controller_socket_test.go, here_attach_test.go,
here_resume_test.go, trigger_intake_test.go, host_state_test.go,
owner_composition_test.go and run_host_test.go. The two obsolete CLI test files
are deleted. The other CLI, docs, localws and store paths listed by the original
merge conflict list take the integration versions unchanged.

Source documentation changes in docs/cli-run.md and its generated mirror,
c3-protocol-sdk.md and t1-port-inventory.md. Old continuation reports remain
snapshots. Friction was the unstageable merge index, overlapping CLI adapter
tests and missing native composition contracts. Worker scratch is removed after
checks join. The shared Go cache stays. No Git write, Sparkwing runtime,
installer, deployment or release ran.

<!-- design:end -->

STATUS: CONTINUE
