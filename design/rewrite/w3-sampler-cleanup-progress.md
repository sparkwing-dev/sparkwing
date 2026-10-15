# W3 sampler and test cleanup

<!-- design:begin -->

Session, Server, the old scheduler/layers and their orchestrator callers remain
deleted. Lead 09:40 items 1-3 and 07:40 item 9 remain complete. No finished
migration is repeated. The worktree started clean at d411040b4.

This session removes the global sampling-interval override and its atomic state.
The usage sampler keeps its two-second production interval. Its existing native
test passes a private interval to its own sampler, so it cannot change another
sampler's timing. Two empty package files and an uncalled subprocess helper are
deleted. The underlying session sampler and its native CPU/RSS assertions remain.

The unused plan-cache policy constant is deleted. No caller used it to select
cache behavior. Unused test code is also deleted: expansionWrites and its
StateStoreOld embedding, resolveAs, jsonEqual/jsonText, errText, failureReason and
boxKey. The retained tests continue to use the native hosts and shared scripted
fakes. No behavior assertion is removed or replaced.

| Production comparison | Added | Deleted | Net |
| --- | ---: | ---: | ---: |
| This session against HEAD d411040b4 | 6 | 32 | -26 |
| W3 against main | 10,944 | 25,964 | -15,020 |

Counts follow the lead's area-budget filter, excluding tests, testdata,
acceptance and contract declaration files. W3 is 12,720 lines below its -2,300
budget. Test code changes by 2 added and 100 deleted lines, net -98.

The amendment assignments retain the dispositions in the
[previous audit](w3-merge-sampler-progress.md). This session changes none of the
foreign contract boundaries. The remaining work is concrete:

- W9/W2 must supply complete private execution snapshots and run-start transport.
  Controller.ReadSnapshot still returns ScheduleSnapshot. RemoteOwnerStore
  implements reconciliation and expansion, but cannot implement OwnerStore from
  that projection. Existing component tests use the declared local interfaces.
- W1 must supply owner policy-failure and approval SkipIf success transitions.
  NodeTransitionEdges permits approval resolution by an operator or timeout
  authority. The native SkipIf evaluator refuses approval success without its
  owner transition rather than claiming that the approval was skipped.
- W8/W9 must declare invocation of the selected engine's host and separate sealed
  pipeline/execution-source acquisition. InstallSDK is present, but an engine
  identity alone does not run the host inside that engine.
- W1/W2 must retain EndLine and Column in annotation writes. The node-host request
  carries both, while store.AnnotationWrite and controller.annotationRequest
  carry only Line.
- W1/W9 must settle usage report identity and provide the fenced writer/transport.
  C2 declares MetricWriter.WriteUsage with Authorized, but its inputs cannot
  distinguish two identical process observations from a retried report. C2's
  usage handoff explicitly leaves production persistence open. The retained
  usage sampler still has no native production caller. No new usage adapter or
  callback is added around that missing contract.

W9's resolveLocalRunRuntime still returns unavailable. Production local, runner
and launcher composition must supply source authority, state subscriptions,
captured environments, storage bindings and delegated service writers. Existing
component fakes exercise those interfaces. HTTP/api.sock, native placement
cells, PostgreSQL, S3, Windows/Darwin execution and deployment/repository QA stay
with the lead. W1b's exclusively owned materialization path is untouched.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W3/gotmp and
TMPDIR=/tmp/sw-astra-W3/tmp with go -C and this worktree's absolute path:

```text
go build -p 4 ./...
go vet -p 4 ./internal/nodehost ./internal/lease ./internal/source ./internal/orchestrator/...
go test -p 4 ./internal/nodehost ./internal/lease ./internal/source ./internal/orchestrator/... -count=1 -timeout 3m
go test -race -p 4 ./internal/nodehost -run '^(TestUsage|TestNativeHostFromIntakeThroughSDKAndPersistedCompletion)' -count=1 -timeout 2m
go test -p 4 ./internal/runners ./internal/runners/launcher ./tests/acceptance -run '^(TestHosting|TestNodePreparation|TestManagedRunner|TestBootstrap|TestLifecycle|TestRecovery)' -count=1 -timeout 2m
go run -p 4 ./internal/commentcheck -base main .
go run -p 4 ./internal/sleepcheck -base HEAD .
```

All listed checks pass on the final Go tree, including the complete nodehost
suite and existing lifecycle/recovery acceptance selections. The race command
also uses GORACE=atexit_sleep_ms=0. Formatting and diff whitespace checks pass.
The first post-edit verification encountered missing files under the shared Go
cache during compilation, before tests could run. Fresh invocations rebuilt the
missing entries and passed. The cause of the disappearing entries is unproven.
The shared cache was neither cleared nor replaced.

No new native acceptance cell or archived test port is claimed. There are no
shared-file or foreign production-caller edits, no contract changes and no new
production mechanism. No public docs impact because the removed declarations
and helpers have no production consumer, and the sampling interval is unchanged.
The sampler's GoDoc changes with its implementation. Lead-owned handoffs and
amendments remain unchanged. No Git writes or Sparkwing runtime ran. Git
configuration has no local user or core.worktree override. All checks joined
before worker scratch was removed. The shared build cache remains.

<!-- design:end -->

STATUS: CONTINUE
