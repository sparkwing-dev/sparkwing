# W3 service and store port continuation

<!-- design:begin -->

W3 remains incomplete. This continues [the debug port](w3-debug-store-port-progress.md).
Nodehost has **444 passing roots and one failing root**, down from 49 failing
roots. `TestOnlyRunsTheMatchedNodesAndWhatTheyNeed` still reaches its old
`args_json` fixture because the sealed selection fields are absent.

## Implemented

The registered process-service endpoints now derive omitted team, run, job and
parent-run selectors from the authenticated principal. Explicit selectors stay
intact and pass through the common binding checks. The invariant is that only
the admitted process's bound run and job can reach an own-job service callback.
The real-router selector property exercises omitted, equal and foreign selectors
for summary writes and child starts.

Undeclared secret refusal preserves `forbidden` with `secret_undeclared` through
the common binding checker. The retained secret property checks foreign,
undeclared and revoked reads against the real router and process registry.
Canceled service requests use the shared `cancelled` code and HTTP 409. Deadlines
use `deadline_exceeded` and HTTP 408. Existing canonical failures retain their
codes and causes.

`OwnerEvaluator.Expand` bounds output reads, reconstruction and generator eval
with the persisted source job timeout and the parent deadline through
`limits.ChildContext`. Expiry returns an expansion failure after process join
and cleanup. It writes no topology. The retained 400 ms regression uses the
native evaluator and a controlled clock.

Native admission still uses the real tenant adapter installed in the previous
session. No admission fake or alternate store path was added.

## Retained ports and cuts

HTTP tests use `ProcessAuthorities`, the shared credential validators and
authenticator, `RegisterProcessServices` and `PolicyRouter`. HTTP transport stays
in memory. Foreign credential persistence, relation checks and service callbacks
are fakes. The Go SDK component calls secrets, output, latest, masks, annotations,
summary, OIDC and children through those actual registrations.

Nine cleanup tests use `ProcessCleanups` and `CleanupScope`. They retain order,
release, environment, PATH, safe failures, deadlines, isolation, limits and child
termination. FIFO workloads replace sleeps. The legacy live-bearer cleanup test
and HTTP-only registration fixtures are removed.

The four generated-retry roots use native interfaces. Recorded target topology,
claims, execution and expansion acceptance use SQLite. They retain unfinished
job scheduling, source-and-child execution after a reuse miss, immutable accepted
declarations and refusal before executing changed generated code. Source-output
reads and the record-miss response use foreign contract fakes. Successful source
copying and selection across two runs are still unverified. The target-side
tests start after reusable results are terminal. They do not establish that
production has copied them correctly. The old SDK retry fixture is deleted.

| Previous root | Native root |
| --- | --- |
| TestARetryOfAGeneratedFailureRunsOnlyIt | TestRetryTargetSchedulesOnlyUnfinishedGeneratedWork |
| TestARetryWhoseGeneratorOutputIsGoneRunsItAndItsNodes | TestRetryReuseMissExecutesGeneratorAndChildren |
| TestACopiedGeneratorsEvalKeepsTheNodesTimeout | TestMaterializedGeneratorEvaluationKeepsTheJobTimeout |
| TestARetryKeepsTheGeneratedNodesItsSourceRecorded | TestGeneratedRetryExecutionRefusesChangedRecordedDeclarations |

The child-await timeout test uses the registered service handler, native process
activity and the native idle budget. A child wait holds that budget until it ends.
It then resumes. Revocation tests also assert canonical cancellation and join.

Retired route fixtures covered the old loopback server, claim-token parser,
forwarding-502 behavior, four-forward limit, single-output serialization and
poller coalescing. Poller cancellation moves to the retained revoke/close test.
The local output/latest/children adapter tests and child ordinal/ownership tests
no longer exercise their old implementations. Native callbacks test selectors
and refusal propagation. Concrete local adapters, child invocation replay and
child relation integration remain open. This is a coverage limit, not a claim
that those retained features are complete or removed.

The code/test diff removes **1,531 lines** and adds **914**, including the new
136-line routed test fixture. Legacy hosting implementation removed: **0 lines**.
Session, Server, old scheduler/layers and their production callers remain.

## Contracts and integration still needed

- **W9, lead decision 1:** declare and seal Only, StartAt and StopAt. The final
  runspec.Options still has none of them. The selection root remains red.
- **W1:** provide the authority-bearing debug adapter described in the previous
  report. Materialization has only an unexported transaction interface and no
  production installer. Missing output must produce the declared miss rather
  than a host-side workaround. The earlier typed admission/approval outcome,
  cancellation/capacity and canceled-snapshot findings remain open.
- **W2/W9:** supply the authorized native RunSnapshot, capacity/adoption and
  owner-delegation adapters. The wire scheduling/display projections cannot
  replace accepted run and execution seals. W2's generation-zero expansion
  delegation restriction remains open.
- **W3:** finish memo/retry source selection and reconstruction, concrete
  service/record wiring, remote local-semaphore retention and production
  local/trigger/pod composition. Then delete the legacy implementation and
  migrate its callers together. No foreign production caller changed here.
- **W3/W8/W9:** the runner still constructs a RunInput by marshaling the full
  SealedRequest, while OwnerHost validates the inner Request. Hosting also
  leaves owner and granted-runner input with the preparation callback. A shared
  awarded run-input constructor and actual hello integration remain necessary.

Lead decision 9 was already committed. The full nodeproto suite still passes
the capacity, scope and on_limit mismatch-refusal tests. Cost may differ.
Lead-owned handoffs and amendments are unchanged. The prior assignment audit
remains in force. This session advances A1-01/05/23 service integration,
A1-48 deadline integration and retained test ports. It closes no native
placement acceptance cell.

## Verification and friction

Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and at most
four package workers. GOCACHE and GOTMPDIR are under `/tmp/sw-astra-W3/`.
Reproduce with these commands from the assigned checkout:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/nodehost ./internal/lease ./internal/source
go test -p 4 ./internal/nodehost ./internal/nodeproto ./internal/lease ./internal/source
go test -race -p 4 ./internal/nodehost -run '^(TestRetryTarget|TestRetryReuseMiss|TestMaterializedGenerator|TestGeneratedRetryExecution|TestAnAwait|TestCleanups|TestCleanup|TestHTTPServices|TestSecret|TestStalledSecret|FuzzProductionRouter)' -count=1 -timeout=90s
go run ./internal/commentcheck -base main .
```

- Whole-repository build passes. Nodehost/lease/source vet passes.
- Full nodeproto, lease and source suites pass.
- Full nodehost finishes in about 18 seconds with 444 passing roots and the
  one selection failure. No root is skipped.
- Focused race checks for the changed generated-retry, timeout, cleanup and
  routed-service paths pass. This does not clear the earlier SQLite snapshot
  interruption reproduced by broader race runs.
- Secret-isolation fuzz passed 14,315 executions. Implicit-selector fuzz passed
  8,387 executions. Both use the common router and validators.
- Commentcheck against main and whitespace checks pass.

The production router gaps were hidden by the old Server's custom authorization
path. The port exposed and fixed them. One concurrent compile overlapped test
file edits and returned an empty import archive path. Stable-source package
compilation and the full run passed afterward.

Shared edits are the C3 export description and T1 port inventory. No foreign Go
contract changed. No setting, environment variable, service or customer limit
was added. The target resolver and cancellation normalization use the existing
endpoint and error contracts. No Git writes, pipelines, installed candidates or
releases ran. Scratch is deleted after checks join.

<!-- design:end -->

STATUS: CONTINUE
