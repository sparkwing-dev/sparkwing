# W3 nodehost green continuation

<!-- design:begin -->

Priority 1 is green. The full nodehost suite passes after porting its last
legacy-schema fixture. Priority 2 remains verified from the committed fix.
Priority 3 remains incomplete. Session, Server, the old scheduler and layers,
and their orchestrator callers are still present.

## Implemented and ported

`TestOnlyRunsTheMatchedNodesAndWhatTheyNeed` now uses tenant intake, the owner
claim, accepted seals, fenced transitions and the shared OwnerHost scheduler.
It retains matched work, prerequisite order, excluded jobs, recovery behavior
and terminal skip reasons. Reasons are checked on the store's committed events.
The recovery reason uses the canonical `parent_did_not_fail` spelling.

The fixture computes exclusions with the existing `onlySkips` policy and applies
them through Tenant.TransitionNode before scheduling. It proves selection policy
and scheduling against the final store. It does not prove selection ingestion
or sealing. W9's Only, StartAt and StopAt fields are still absent from
`runspec.Options`. No mutable flag or invocation field substitutes for them.

The real tenant admission binding in NativeHost is retained. No admission fake,
alternate persistence path or production admission workaround was added.

`nodehost.NewRunInput` now owns the award projection used by runner hosting.
SealedRequest contains the inner Request expected by OwnerHost. RequestHash
retains its separate seal. The constructor captures the accepted plan, granted
runner and trigger-owner fence before renewal or preparation. Hosting preserves
that fence and identity while retaining the preparation adapter's authenticated
authority and service bindings. Trigger and here awards require a complete
granted runner identity, including a present labels collection.

Invariant: preparation cannot replace the awarded request, owner fence or runner
identity. The existing `FuzzHostingRunInputKeepsAwardSeals` now varies forged
prepared values, new/resumed runs and trigger/here purposes. It also mutates
awarded arguments, plan bytes and runner labels after capture. The actual
Hosting path and shared constructor are real. The controller and final host
remain fakes, so this is not evidence of an actual SDK hello or placement.

The retired `hostWith` fixture was **14 lines**. The two runner-local projection
helpers were removed from their old location and moved into nodehost. Legacy
hosting implementation deleted in this session: **0 lines**.

## Contract changes needed and remaining integration

- **W9, lead decision 1:** declare and fill sealed Only, StartAt and StopAt
  settings. The retained test is ported, but production seal integration remains.
- **W2/W9:** provide an authorized persisted run/execution snapshot.
  Controller.ReadSnapshot still returns ScheduleSnapshot. Native OwnerStore and
  ClaimStore require RunSnapshot with the request, accepted spec/plan, owner and
  complete execution rows. ReadRun's display projection cannot supply these.
  Remote capacity/adoption and owner-delegation adapters also remain open.
- **W2/W9:** declare the remote run-start operation needed by
  OwnerStore.TransitionRun. Controller has TransitionJob and terminal Finish,
  but no run-start transition. AcceptPlan does not start the run, and executing
  renewal starts the owner unit rather than changing the run's state.
- **W8/W9 with W3:** the old local RunHosted entry point carries old state
  backends and invocation inputs, without an authenticated admitted RunInput or
  native host composition. Supply that authority-bearing placement input before
  replacing the local caller. The trigger/pod cut also needs the snapshot and
  remote authority contracts above. No production authority is inferred from
  display fields, flags, environment or a repository string.
- **W1:** the earlier admission/approval transition, debug-persistence and
  canceled-snapshot findings remain open. No foreign transition table or raw
  debug writer was changed. The focused race pass below does not clear the
  broader interrupted-snapshot reproducer from the previous reports.
- **W3:** complete concrete service/record wiring, memo/retry source selection,
  remote local-semaphore retention and native local/trigger/pod composition.
  Then delete the old hosting stack and migrate its remaining callers together.

The earlier amendment audit in
[the service/store continuation](w3-service-store-port-progress.md) remains in
force. This session advances retained-test finding 1, A1-16/19/38/39 and the
execution-identity handoff. It does not close their full placement integration.
Source-attempt and typed step-report fields are present in controllerwire now.
Their presence does not supply the missing native remote store assembly.

Lead decision 9 is already committed. The full nodeproto suite passes the
capacity, scope and on_limit mismatch refusals and permits differing cost.
No further W4 edit was needed. Lead-owned handoffs and amendments are unchanged.
No acceptance cell was added or activated, and no native matrix cell is newly
claimed passing. No policy question needs a new human decision in this slice.

## Verification

Commands run in the assigned checkout with GOWORK=off,
GOFLAGS=-buildvcs=false, GOMAXPROCS=4, and GOCACHE/GOTMPDIR beneath
`/tmp/sw-astra-W3/`. The final checks also set TMPDIR beneath that root.

```sh
go build -p 4 ./...
go vet -p 4 ./internal/nodehost ./internal/nodeproto ./internal/lease ./internal/source ./internal/runners
go test -p 4 ./internal/nodehost ./internal/nodeproto ./internal/lease ./internal/source
go test -p 4 ./internal/nodehost ./internal/runners
go test -race -p 4 ./internal/nodehost ./internal/runners -run '^(TestOnly|TestHosting|FuzzHostingRunInputKeepsAwardSeals)' -count=1
go test -p 4 ./internal/runners -run '^$' -fuzz '^FuzzHostingRunInputKeepsAwardSeals$' -fuzztime=3s
go run ./internal/commentcheck -base main .
git diff --check
```

All final checks pass. The full host suites include nodeproto, lease and source.
The full runner suite passes. The identity fuzz run completed **9,634 executions**.
Commentcheck is clean across 2,122 files. No test skip or sleep was added.

Two checks overlapped moving the constructor and compiled against an incomplete
package snapshot. Both were rerun after the source stopped changing. Stable
source passes normal tests, race tests, fuzzing, build and vet.

Shared Go files touched are `internal/runners/execution.go`, `hosting.go`,
`hosting_test.go` and `run_input_test.go`. The C3 export/assembly description and
T1 port inventory point to this change. The new constructor is a W3-owned local
contract completion. No foreign Go contract was changed.

No package, interface, setting, environment variable, service, customer limit or
compatibility path was added. Existing award projection moved to its shared
owner. No Git writes, Sparkwing binaries, pipelines, installs or releases ran.
Checks are joined before scratch removal.

<!-- design:end -->

STATUS: CONTINUE
