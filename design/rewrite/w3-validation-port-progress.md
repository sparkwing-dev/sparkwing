# W3 validation and retained test ports

<!-- design:begin -->

The worktree started clean at 10c27ce3b. Session, Server, the old scheduler and
layers, and their hosting callers remain deleted. Lead 09:40 items 1-3 and
07:40 item 9 remain complete. No finished migration is repeated.

Invariant: an SDK secret default cannot enter an accepted identifier or appear
in its refusal diagnostic. TestPlanningDefaults now runs the real shared plan
validator, checkout resolver, canonical hashes and SQLite owner lifecycle.
It replaces the hand-written validator and fake acceptance helper. The safe
case verifies the persisted default and its secret classification. Rejected
job, group, step and concurrency identifiers leave a failed intake with no
accepted plan or jobs. Both SDK sessions close and clean up before return.
FuzzPlanningDefaultsRefuseSecretIdentifiers varies the identifier kind and
escaped, Unicode and control-bearing defaults. Its 16 seeds and a ten-second
fuzz run pass. The fuzz run executed 56 cases with two workers.

This strengthens the ports of TestLaunchedNode_RefusesASecretDefaultInAGroupName
and TestHosts_RefuseASecretDefaultInANodeID. The shared scripted SDK, source
acquisition and engine resolution remain test adapters. These checks do not
establish native placement execution.

TestOwnerExpansion now sends deps for the null-collection and cross-boundary
cycle cases. Their old needs spelling exercised unknown-field rejection.
Assertions require the actual null, cycle, duplicate-field, unknown-field,
positive-capacity or fan-out refusal reason. New cases prove that the real
owner/evaluator path supplies existing concurrency definitions to the default
shared validator. Capacity, scope and on_limit mismatches submit only a failure
with the field path. A different cost submits a valid expansion. The redundant
validation callback is removed. Expansion persistence remains the existing
scripted store contract.

| Production comparison | Added | Deleted | Net |
| --- | ---: | ---: | ---: |
| This session against HEAD 10c27ce3b | 0 | 0 | 0 |
| W3 against main | 10,944 | 25,964 | -15,020 |

Counts follow the lead's area-budget filter. W3 remains 12,720 production lines
below its -2,300 budget. Test changes add 122 lines and remove 102, net +20.
There are no production-caller migrations or new production mechanisms.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W3/gotmp and
TMPDIR=/tmp/sw-astra-W3/tmp. Commands use go -C with this worktree's absolute
path:

```text
go build -p 4 ./...
go vet -p 4 ./internal/nodehost ./internal/lease ./internal/source ./internal/orchestrator/...
go test -p 4 ./internal/nodehost ./internal/lease ./internal/source ./internal/orchestrator/... -count=1 -timeout 3m
go test -p 4 ./internal/nodehost -run '^$' -fuzz '^FuzzPlanningDefaultsRefuseSecretIdentifiers$' -fuzztime=10s -parallel=2 -timeout 60s
go test -race -p 4 ./internal/nodehost -run '^(TestOwnerExpansion|TestPlanningDefaults|FuzzPlanningDefaultsRefuseSecretIdentifiers)$' -count=1 -timeout 2m
go test -p 4 ./internal/nodeproto ./tests/acceptance -run '^(TestExpansionPreservesConcurrencyDeclarations|TestLifecycle|TestRecovery)$' -count=1 -timeout 2m
go run -p 4 ./internal/commentcheck -base main .
go run -p 4 ./internal/sleepcheck -base HEAD .
```

All listed checks pass. The race command uses GORACE=atexit_sleep_ms=0.
The full nodehost package passes in 25.55 seconds. Existing lifecycle/recovery
acceptance selections pass. No native acceptance cell is newly activated.
Formatting and diff whitespace checks pass. No production behavior changes,
so public source docs and their generated mirrors need no update. The retained
test-port inventory is updated beside the tests.

The remaining amendment assignments retain the previous audit's dispositions.
No foreign contract changes are made. These integration points remain open:

- W9/W2 must expose the complete private execution snapshot and run-start
  transport. Controller.ReadSnapshot still returns ScheduleSnapshot.
  RemoteOwnerStore implements reconciliation and expansion against declared
  interfaces. It cannot implement OwnerStore from that projection.
- W1 must add the owner policy-failure and approval SkipIf success edges.
  The native evaluator keeps its explicit refusal while that transition is
  absent.
- W8/W9 must declare selected-engine host invocation and separate sealed
  pipeline/execution-source acquisition. InstallSDK alone does not run the
  host in the selected engine.
- W1/W2 annotation writes still omit EndLine and Column present in the
  node-host request.
- W1/W9 must settle usage-report identity and supply the committing writer
  and typed transport. C2's MetricWriter.WriteUsage declaration alone cannot
  distinguish a repeated report from identical observations.

W9's resolveLocalRunRuntime still returns unavailable. Production composition
must supply source authority, state subscriptions, captured environments,
storage bindings and delegated writers. W1b's exclusively owned materialization
path is untouched. Native HTTP/api.sock, all placement cells, PostgreSQL, S3,
Windows/Darwin execution and deployment/repository QA remain with the lead.

The only shared file touched is contracts/t1-port-inventory.md. No foreign
production file changes. Lead-owned handoffs and amendments remain unchanged.
No new design question is raised. The foreign contracts above remain the
completion constraint. Independent review and Sparkwing pipelines remain with
the lead under the worker brief. No Sparkwing binary or Git write ran. Git
configuration has no local user or core.worktree override. All checks joined
before worker scratch was removed. The shared build cache remains.

<!-- design:end -->

STATUS: CONTINUE
