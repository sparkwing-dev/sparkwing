# W3 consumer and intake cut

<!-- design:begin -->

W3 removes **4,087 net production Go lines** this session: 260 added and 4,347
deleted. Across touched production packages, the session removes **4,873 net
lines**: 411 added and 5,284 deleted. Against main, W3 is **-9,697**
(11,067 added and 20,764 deleted), **7,397 below** its -2,300 budget.
Counts include untracked implementation files and exclude tests, testdata,
acceptance and contract filenames. Moving the implemented lease and source
files out of contract filenames makes those existing implementations count.
Orchestrator retains 5,500 production lines, chiefly read and display surfaces.

## Implementation and deletion

Detached intake now captures the environment in FileEnvironmentSnapshots and
submits the same typed request builder as foreground local hosting. Intake owns
idempotency. The CLI checks the returned seal, reads persisted replay status and
publishes a run handle only after a valid receipt. It never claims work, opens
state.db or starts a separate consumer. The receipt echoes the request ID without
using it for deduplication. Local requests now carry ExecutionLocation=local so
the common planner receives the local guard context.

Scheduled launches call ScheduleManager.RunSchedule with a stable key derived
from the schedule and due instant. The controller selects captured environment,
arguments and source. The launcher checks the returned schedule and run identity.
The retained CLI schedule metadata/tick store access still belongs to W9's local
controller migration. This change removes raw run creation from that launcher.

Deleted the raw trigger consumer, its election/PID service, polling, retry and
store-wedge guards, raw settlement and cancellation writers, separate claim
renewal adapter, child launcher and duplicate process cleanup. Removed the
runs-consumer CLI implementation, dispatch, dead help entries and consumer-only
run flag fields. The legacy local dashboard no longer starts that consumer.
W8's LocalController.Run owns its replacement workers.

Deleted the orphan LocalAdmission and child-queue inspection, broad state/log/
concurrency adapters, HTTP concurrency adapter, profile rollup, legacy request
priority, start-record writers, run-ID environment capture/consumption/reclamation,
standalone environment transport and private run loggers/renderers. These had
lost their production callers with the earlier scheduler cut. Native usage and
profile recording still require their integration binding.

The implemented lease keeper is now keeper.go. Source declarations are in
source.go, with Acquire, Close and the sealed projections implemented directly
in their existing files. The private forwarding functions are deleted. No
foreign contract signature changed. Lead-owned handoffs and amendments are
unchanged, and the materialization path assigned to W1b is untouched.

## Evidence and ports

The new security property is: no pipeline argument appears in an intake failure
diagnostic. FuzzDetachedIntakeDiagnosticsNeverExposeArguments passed 4,179
generated executions. Canonical and ordinary errors both pass through the shared
masker. Invalid intake seals and missing or foreign run/status identities publish
no handle. The tests also retain explicit idempotency, replayed terminal status,
request-ID echo, and repeated reads of the same captured environment.

Old raw receipt and queue-priority checks now use the sealed intake and shared
local request tests. The alias risk check uses the resolved registered declaration.
The schedule test checks equal keys for the same instant in different time zones
and distinct keys for distinct instants. Removed adapter/consumer tests with their
APIs. Native nodehost tests retain admission, recovery, retries, events, guards,
masking and process cleanup. Native usage/profile integration is still open.

Lead 09:40 items 1-3 and Lead 07:40 item 9 remain done. No host_* harness files
remain in internal/acceptance. The two keeper checks each have one in-package
copy. No scenario catalog, Harness or boundary registry was extended.
No new full native acceptance cell or archived inventory row is claimed complete.

Passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR under worker scratch and Go package parallelism at most four:

- Whole-repository go build ./....
- Scoped go vet for nodehost, lease, source, orchestrator, the CLI and localws.
- Full nodehost, lease and source suites. Nodehost has 416 top-level test/fuzz entries.
- Focused CLI intake, alias, local-handoff and parser tests, including race checks.
- Test compilation for runners, both acceptance packages, localws, the CLI and orchestrator.
- commentcheck -base main, Go formatting, docs mirror sync and whitespace checks.

An earlier full host run hit SQLite interrupted (9) in the unchanged
TestRetryTargetSchedulesOnlyUnfinishedGeneratedWork. Five isolated runs, then
50 further isolated runs, and the final full host suite passed. No cause was
established and no nodehost behavior was changed to hide it. This remains a
flaky persistence/cancellation observation for the lead.

## Remaining work and integration points

The native runtime factories remain unbound: resolveHostRuntime reports unavailable,
and the default foreground wingd command lacks its LocalController factory.
Component tests do not establish execution through api.sock, native local workers
or remote placement. W2/W8/W9 must join the existing host, authenticated store,
process authority, source/storage bindings, state subscriptions, unit renewal,
usage recording and verified engine invocation. The schedule API also needs its
server implementation and retained captured-environment selection.

W3 still needs native local source override acquisition. Detached --sw-ref,
--sw-pipeline-ref and configured pipeline source selections return policy_missing
before intake. They cannot execute the ambient checkout in place of selected
source. Separate pipeline-source and execution checkouts also need W6/W9 to
settle their sealed location representation. The removed raw trigger environment
is not a replacement for that contract.

W6/W9 still need sealed StartAt, StopAt and Only inputs. The shared local builder
continues to refuse those selections rather than discard them. W9's private
owner execution snapshot must carry the sealed request/spec, current owner,
execution rows and accepted expansion bodies. ScheduleSnapshot remains
insufficient. Authenticated output/failure/generator reads and native owner
adapters remain integration points. W1b retains ownership of materialization.

Native Windows/Darwin, PostgreSQL, actual HTTP/api.sock, storage/placement and
real-repository acceptance remain open. The socket sandbox prevents that evidence
here. No Sparkwing runtime, installer, pipeline, git write or release was run.

## Shared files and friction

Production caller changes outside W3 are cmd/sparkwing/crons.go, main.go,
help_registry.go, run_detached.go, run_dispatch.go, run_host.go, trigger_host.go,
the deletion of runs_consumer.go, and pkg/localws/localws.go.

Shared test changes are crons_test.go, pipeline_identity_test.go,
pipeline_source_test.go, run_detached_process_test.go, run_host_native_test.go,
run_pipeline_ref_test.go, run_priority_flag_test.go, run_risk_gate_test.go and
trigger_intake_test.go. The obsolete detached admission/daemon/priority tests and
submit_receipt_test.go are deleted. run_detached_native_test.go is new. Shared
interface fakes are reused instead of adding another controller or schedule fake.

Source docs changed in local-execution.md, admission.md and environment-variables.md,
with their generated mirrors. C3's source pointers and the test-port inventory
link this report. Friction was the missing native compositions, source/step
selection contracts, obsolete raw-schema fixtures and the transient SQLite read.
Worker scratch is removed after all checks join. The shared build cache stays.

<!-- design:end -->

STATUS: CONTINUE
