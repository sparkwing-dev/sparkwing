# W9 source, child and environment redesign

Implemented the lead's 2026-10-17 11:00 decision under integration authority.
The latest native report is green for the previous committed state. It does not
cover these edits. The lead must commit and run the new native schedule probe.

- Source selection uses `source.ResolveRevisions`. Git.Ref and
  Source.RequestedRef retain intent. Git.SHA and Source.Ref retain resolved
  execution and code commits. Inspection and hosting use ForRequest and shared
  acquisition. The dispatcher, trigger, burst and schedule paths use that
  selection. Schedule proofs also select their configured code revision.
- Deleted triggerPipelineRevision, preparedTriggerSource.sdk, pinnedPipelineAt,
  the single-SHA inspection projection and the schedule's single-SHA selection.
  Shared acquisition now prepares inspection checkouts and detects the SDK.
  Snapshot materialization retains its private bundle import.
- Children use an explicit inheritance list. Entrypoint and secret argument
  declarations are selected for the child. A Build parent starting Deploy with
  no project file reaches the real SDK planner and plans Deploy.
- Environment invariant: private values reach only a live direct execution unit
  for its own run. Release checks the unit prefix, holder, generation, lease,
  cancellation and expiry. Eval also checks the source and controller expansion
  fences. SourceRead remains independent. Property tests exercise all three
  Cloud unit kinds, stale fences, revocation and cross-tenant access.
- Cloud eval now exchanges its bootstrap, receives its submitted environment,
  executes a real SDK generator subprocess and commits the expansion over HTTP
  with unchanged narrow grants. This exposed and closed an integration gap in
  resolveExpansionTarget: authorization now receives the persisted source
  generation as well as the tagged unit source generation.
- Migration checking validates every current instruction. Before sections,
  table columns and inline references are explicit historical material. Older
  guides are visibly archived while preserving their original titles. Current
  migration instructions, timer reinstall guidance and environment/build-policy
  claims are corrected. Both stale test comments are deleted. Embedded docs
  and the team page's sidebar entry are synchronized.

The redesign changed 248 production Go lines in and deleted 252, net -4,
including the main dispatcher merge routing. This counts shared implementation
edits and the new resolver, and excludes tests and contract Markdown. W9-owned
production Go versus main is +19,769/-25,208, net -5,439, excluding testdata,
tests and contract declaration files. The -2,900 area budget is met.

Four conflict files have resolved contents: cmd/sparkwing/main.go,
cmd/sparkwing/runners_personal_test.go, docs/auth.md and its embedded mirror.
The integrated team settings command now takes the typed Invocation and its
new tests use runSparkwing. Git's index still marks those four paths unmerged
because the worker must not write Git metadata. The lead must stage them.
No file deleted by integration was restored. Lead-owned handoffs and amendment
files were not edited.

Validation passed with GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9 and at most go test -p 4:

- Repository build and focused vet, including rewrite_integration compilation.
- CLI source/pin/inspection/trigger regressions and integrated personal-machine
  enrollment/team settings tests, 47.336s.
- Controller child, generator, claim/exchange and socket-free local schedule
  tests, 1.371s. The OLD/NEW regression reaches native trigger inspection and
  SDK host planning, with OLD code reading the NEW execution checkout.
- Full source, runspec, doccheck, clisurface, controllerwire and client packages.
- Store environment tests and property seeds, 1.234s. Focused nodehost planning
  and checkout tests, 0.054s.
- Embedded docs tests excluding socket-dependent TestWebClient, 1.224s.
- Full doccheck: 490 current CLI invocations resolve. Commentcheck is clean
  across 2,860 files. Diff whitespace passes.
- Existing TestSurface and TestEngineAmendments acceptance contract cells,
  0.081s. No acceptance cell was newly unskipped. SDK-kind/dirty-tree tests were
  moved from the deleted CLI detector to shared acquisition. No archived test
  was newly ported.

The native schedule test compiles and reaches the sandbox's socket refusal.
It uses the production local runtime, credentials, queue and host, and checks
that a schedule with source: origin/main finishes its SDK step. The lead should
run this after staging and committing:

```sh
GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 -tags=rewrite_integration ./cmd/sparkwing -run '^TestNativeScheduleOriginMainRunsToCompletion$' -count=1
```

The existing offline CLI, native api.sock schedule and WebClient tests also
hit sandbox socket refusals in wider selections. Their native evidence stays
with the lead harness. Deployed Cloud/DinD remains a real-integration point;
the generator test uses the real controller, store, SDK and pipe transport with
a fixture engine executable, rather than a Kubernetes pod.

Shared implementation files: internal/doccheck/cliverbs.go;
internal/runspec/contracts.go;
internal/source/{revisions,projection,checkout}.go;
internal/controller/{schedule_source,run_selector}.go;
internal/store/submitted_environment.go. Shared tests:
internal/controller/{export_test,schedules_local_test,w9_mechanisms_test}.go and
internal/store/submitted_environment_test.go. Contract amendments are recorded in
c1-data.md, c3-controller-wire.md and c4-surface-config.md. No further contract
change is requested.

Friction: Xwing context inspection refused its session lock on the read-only
filesystem. The user-selected environment was reused. Git metadata and shared
Git configuration were left untouched. Scratch is removed at session end;
the shared build cache is retained.

STATUS: CONTINUE
