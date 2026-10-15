# W3 merge and session sampling

<!-- design:begin -->

The integration conflict in `contracts/t1-port-inventory.md` is resolved in the
working file. Both W3 port records and W2 integration records are preserved.
There were no implementation or amended-signature conflicts. The index still
lists this file as unmerged because the swarm brief forbids Git writes. The
lead must stage the resolved file and finish the merge.

The committed branch already removed Session, Server, the old scheduler and
layers, hosted_run, trigger_host and pod_node_host. Lead 09:40 items 1-3 remain
complete: no host_* file remains in internal/acceptance, the retained checks
live beside their packages, scripted fakes are shared, and the two keeper
checks each have one test root. Lead 07:40 item 9 remains complete. The merged
concurrency test refuses capacity, scope and on_limit mismatches and permits
different cost. Its focused run passes. No finished migration is repeated.

This session deletes 36 production lines from unused router remnants:
route_names.go, terminalRun and routeFailure. There are no callers to migrate.
The session sampler loses its duplicate selection map and handles a process
that disappears between selection and resident-memory reading. Linux omits
missing/ESRCH members. Darwin omits ESRCH members. Other unreadable members
still invalidate the sample. No retry, timer, setting or interface is added.

The broad W3 check first reproduced the two previously reported metrics
failures with "session snapshot unavailable". The new
TestSessionSnapshotOmitsReapedMember reproduces the underlying race without
sleeps by reaping a selected child before memory sampling. It failed before
the fix because the returned snapshot retained the missing child. The complete
metrics suite then passed three repetitions, the broad W3 selection and the
race selection. Temporary diagnostic logging is removed. Darwin is
compile-checked only. This does not establish native Darwin runtime behavior.

| Production comparison | Added | Deleted | Net |
| --- | ---: | ---: | ---: |
| This session against HEAD 25695152c | 16 | 47 | -31 |
| W3 against main | | | -14,994 |

W3 remains 12,694 lines below its -2,300 budget. Counts exclude tests,
testdata, the acceptance harness and contract declarations, following the
lead's area-budget rules. The new regression adds 44 test lines. No additional
archived test is ported and no native acceptance cell is activated.

The amendment audit remains open at the same foreign boundaries:

| Assignments | Remaining work |
| --- | --- |
| A1-01/02/05/21-25 | Shared services, masking and persisted process-parent checks are implemented. Production route dependency composition remains open. C1/C2 annotation writes still omit EndLine and Column. |
| A1-03/04/06/07 | Shared error, record-limit, cancellation and optional-concurrency consumers remain implemented. |
| A1-10/11/12/19 | Source acquisition, captured process bases, SDK installation and granted hello identity are implemented. W8/W9 still need the selected-engine host invocation and separate pipeline/execution-source contract. |
| A1-13-18 | Reconciliation, failure reads, expansion replay and attempt fencing are implemented. C3 still returns only ScheduleSnapshot and has no run-start operation for the complete remote OwnerStore. |
| A1-36 | The native component retains real signed bytes, receipts, records and terminal attachment. Production data authority remains open. W1b's exclusively owned materialization path is untouched. |
| A1-38/39/50/51 | Owner and claimed roles share the native executor. W8/W9 production placement factories and transport composition remain open. Local run resolution still returns unavailable. |
| A1-44-48 | Sealed arguments, step selections, captured environments and shared guard/environment consumers remain implemented. |
| A1-53/54 | Native placement acceptance remains open. C1 still lacks owner policy-failure and approval SkipIf success edges. |

C2 now declares MetricWriter.WriteUsage with Authorized. C1 still has no
matching committing writer, and C3 has no typed usage transport. That narrows
the previous usage handoff but does not complete native recording. Debug
persistence, state subscriptions, source authority, delegated service writers,
HTTP/api.sock composition and platform/deployment evidence remain real
integration points. No new design question or foreign contract change is made.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W3/gotmp and
TMPDIR=/tmp/sw-astra-W3/tmp. Use go -C with this worktree's absolute path:

```text
go build -p 4 ./...
go vet -p 4 ./internal/nodehost ./internal/lease ./internal/source ./internal/orchestrator/...
go test -p 4 ./internal/nodehost ./internal/lease ./internal/source ./internal/orchestrator/... -count=1 -timeout 3m
go test -p 4 ./internal/orchestrator/nodemetrics -count=3 -timeout 30s
go test -race -p 4 ./internal/nodehost ./internal/orchestrator/nodemetrics -run '^(TestSession|TestUsage|TestNativeHostFromIntakeThroughSDKAndPersistedCompletion)' -count=1 -timeout 2m
go test -p 4 ./internal/runners ./internal/runners/launcher ./tests/acceptance -run '^(TestHosting|TestNodePreparation|TestManagedRunner|TestBootstrap|TestLifecycle|TestRecovery)' -count=1 -timeout 2m
go test -p 4 ./internal/nodeproto -run '^TestExpansionPreservesConcurrencyDeclarations$' -count=1 -timeout 30s
go run -p 4 ./internal/commentcheck -base main .
go run -p 4 ./internal/sleepcheck -base HEAD .
```

These checks pass. The race command also uses GORACE=atexit_sleep_ms=0.
GOOS=darwin GOARCH=arm64 go test -c of nodemetrics passes with output under
worker scratch. The existing lifecycle and recovery acceptance selections
pass. Native placement cells, PostgreSQL, S3, Windows/Darwin execution,
deployment QA, pipelines and independent review remain with the lead.
The sampler's source API documentation and regression change together.
No public docs impact because removed helpers have no callers and the sampler
change introduces no public command, wire field or customer restriction.

The only shared file touched is the conflicted test-port inventory. No foreign
production caller is edited. Lead-owned handoffs and amendment files are
unchanged. Git configuration retains the normal identity and no core.worktree
override. No Git write or candidate Sparkwing runtime ran. All checks are joined
before removing worker scratch. The shared build cache remains.

Friction is the unmerged index entry left for the lead under the brief's
no-Git-writes rule. The missing foreign contract shapes prevent completing the
remaining production composition within W3 ownership.

<!-- design:end -->

STATUS: CONTINUE
