# W3 integration merge continuation

<!-- design:begin -->

The sole conflict was `cmd/sparkwing/queue_contract_test.go`. Its scope fixture
now uses the integration branch's explicit global scope and run identity.
The test still exercises W3's shared concurrency codec. Conflict markers are
gone and the queue tests pass. The index still reports this path as unmerged
because the worker brief forbids Git writes. The lead must stage it and finish
the merge.

The worktree already contains the previous session's legacy cut. Session,
Server, RunHosted, HostTriggerRun, HostDispatchedNode, the old scheduler and
layers remain deleted. `internal/acceptance/host_*` remains absent. Lead
09:40 items 1-3 and 07:40 item 9 remain complete. The mismatch-refusal test
passes for capacity, scope and on_limit. No completed migration was repeated.

Production delta this session is **0 lines**. W3 against main is **10,944
added, 25,964 deleted, net -15,020**, using the lead's production-area filter.
W3 is 12,720 lines below its -2,300 budget. This session deletes no production
implementation and ports no additional archived test.

The amendment dispositions in `w3-native-composition-progress.md` and
`w3-validation-port-progress.md` still apply. The merge changes no W3
production code or amendment row. The remaining contract gaps were checked
against the merged declarations:

- W9/W2: `controllerwire.Controller.ReadSnapshot` still returns only
  ScheduleSnapshot. There is no run-start method. The private snapshot and
  run-start contracts must exist before RemoteOwnerStore can implement
  OwnerStore. The declared reconciliation and expansion methods already have
  adapters and fake-backed tests.
- W1: NodeTransitionEdges still lacks owner policy-failure and approval SkipIf
  success edges. Approval resolution is operator-only. W3's explicit refusal
  remains until those transitions are declared.
- W8/W9: engine.Resolved declares InstallSDK, but no selected-engine host
  invocation. Source acquisition still has one sealed location and SHA.
  Separate pipeline/execution source acquisition needs its foreign contract.
- W1/W2: AnnotationWrite still omits EndLine and Column.
- W1/W9: MetricWriter.WriteUsage exists, but usage report identity, its
  committing writer and typed host transport remain unresolved.

These are missing contract shapes, rather than foreign implementations that
could be replaced by test fakes. No foreign signature was changed. W1b's
materialization path and the lead-owned amendments/handoffs were not edited.
The local-run, here and local-controller factories remain unbound. The latest
lead handoff assigns local-controller and local-run composition to W8.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W3/gotmp and
TMPDIR=/tmp/sw-astra-W3/tmp. Commands use go -C with this worktree:

```text
go build -p 4 ./...
go vet -p 4 ./internal/nodehost ./internal/lease ./internal/source ./internal/orchestrator/...
go test -p 4 ./internal/nodehost ./internal/lease ./internal/source ./internal/orchestrator/... -count=1 -timeout 3m
go test -p 4 ./cmd/sparkwing -run '^TestQueue' -count=1 -timeout 90s
go test -p 4 ./internal/nodeproto ./tests/acceptance -run '^(TestExpansionPreservesConcurrencyDeclarations|TestLifecycle|TestRecovery)$' -count=1 -timeout 2m
go run -p 4 ./internal/commentcheck -base main .
```

All listed checks pass. Nodehost finishes in 22.56 seconds. Whitespace checks
pass. No acceptance cell was added or activated. Native placement, HTTP/api.sock,
PostgreSQL, S3, Windows/Darwin and deployment/repository QA remain unverified
here. Race and fuzz checks were not repeated because W3 runtime code is unchanged.
Independent review and pipeline gates remain with the lead.

The only shared file edited by this worker is
`cmd/sparkwing/queue_contract_test.go`. There are no production caller edits.
No Sparkwing binary or Git write ran. Local Git configuration has no user or
core.worktree override. Scratch is removed after all checks join. The shared
build cache is retained. No new design question is raised.

<!-- design:end -->

STATUS: CONTINUE
