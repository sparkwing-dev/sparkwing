# Proposal: runner-owned orchestration

Status: draft

## Problem and current flow

The runner image claims a trigger, but the repository's SDK pin supplies the
orchestrator. A fix to dispatch, node waits, or state handling reaches a run
only after that repository rebuilds with a newer SDK. A repository pinned below
the store's `declared-run-repo` requirement can fail before planning starts.
The runner and controller can be current while the code that opens the state
database is old.

The hosted path is:

1. `internal/cluster/trigger_loop.go` claims and heartbeats the trigger,
   fetches the repository, builds or fetches its `.sparkwing` binary, then
   executes `handle-trigger` with the runner's controller configuration.
   `internal/cluster/runner_pool_cli.go` can run that loop beside its separate
   ready-node claim loop. The warm runner selection is wired in
   `internal/cluster/worker_cli.go` and `internal/orchestrator/warm_runner_factory.go`.
2. The binary's `internal/orchestrator/handle_trigger_cli.go` parses the
   configuration and calls `HandleClaimedTrigger`. In
   `internal/orchestrator/worker.go`, the binary opens a local state DB even
   on the remote path, makes controller and logs clients, heartbeats the
   trigger, and calls `Run`. It receives `SPARKWING_AGENT_TOKEN` from the
   trigger child environment.
3. `Run` in `internal/orchestrator/orchestrator.go` invokes the registered
   pipeline's Go `Plan`, creates the run and nodes, writes a plan snapshot,
   then owns admission, dispatch, waits, retries, final outcomes, and run
   heartbeats. `pkg/controller/client` supplies its controller operations.
   The controller stores state and enforces claims, but the pinned binary
   decides when to call it.
4. Node workers claim ready rows in `internal/cluster/runner_pool_cli.go`.
   `RunNodeOnce` in `internal/orchestrator/run_node.go` gets run data, invokes
   `Plan` again, finds the node, and runs its body. Remote execution in
   `internal/orchestrator/run_node_remote.go` fetches and builds the repo
   binary again. Its broker in `remote_execution_broker.go` narrows a child
   capability to the claimed run and node, but the parent still holds the
   controller token and orchestration policy.

The SDK's `sparkwing/plan.go` defines the graph and keeps executable Go
callbacks. `sparkwing/cache.go` stores memo key functions, and
`sparkwing/concurrency.go` stores group declarations. Secrets and step logging
are SDK-facing operations (`sparkwing/secret.go`, `sparkwing/context.go`),
while the orchestrator supplies their backends and seals remote child logs.
The current plan snapshot in `internal/orchestrator/orchestrator.go` describes
nodes and modifiers for persistence and display. It is not yet a complete
execution contract, because Go callbacks cannot be serialized as JSON.

## Ownership after the split

The shipped runner owns the trigger claim and heartbeat, source and binary
resolution, admission, DAG traversal, node placement, claim fencing, remote
waits, retries, cancellation, memo and concurrency state transitions, log
sealing, and final run and node writes. It uses the controller client and the
current store schema. The controller remains the authority for durable state,
leases, secrets, and globally shared concurrency. A runner restart resumes
from persisted state and claim generations, rather than assuming its memory
is the run's record.

The repository binary owns authored behavior: registration, argument schema,
plan construction, typed inputs and outputs, dynamic generators, predicates,
memo key computation, and one node's work and hooks. The SDK remains the
authoring library and node runtime. It stops importing the dispatch loop as
part of the pipeline binary. The runner executes the binary in a restricted
child process for planning and once per node attempt. Approval gates and
other declarative nodes are runner actions, not dummy user steps.

Keep the interface between these modules small. The runner consumes a plan
document and a node result, then makes every state transition. It never needs
to understand a repository's Go types. The binary never needs a general
controller client. This split also applies to local runs, which use the same
runner orchestration against local backends. Otherwise the local path would
keep a second copy of scheduling behavior.

## Pipeline binary protocol

Add `pipeline-binary protocol --json`, `pipeline-binary plan --json`, and
`pipeline-binary run-node <id>`. The first prints supported protocol versions
and required capabilities. The runner selects one common major version before
creating a run. A newer minor version may add optional fields; the runner
rejects unknown required capabilities. No common version or unsupported
required capability fails the run with a named incompatibility before any
node is dispatched. The runner records the selected version, capability set,
source revision, and binary digest with the plan snapshot so retries use the
same contract. Capabilities are semantic names such as `dynamic-expansion-v1`,
not an SDK version comparison.

`plan --json` receives one bounded JSON invocation on stdin: pipeline name,
resolved arguments, trigger and Git metadata, run identity, selected protocol
version, and declared capabilities. It returns one size-limited JSON document
on stdout. The document contains stable node IDs, edges, optional and failure
edges, resource and placement hints, timeout and retry policy, concurrency
groups, memo settings, approvals, output and artifact declarations, and
opaque callback IDs. Every field that changes scheduling belongs in this
document. The runner validates IDs, cycles, references, bounds, and feature
support, then persists the accepted document before creating runnable nodes.
Diagnostics go to stderr. Exit status and a structured error identify a plan
failure; stray stdout is a protocol error. The existing snapshot can inform
the schema, but should not be used as the wire format without filling its
execution gaps.

Go closures need a narrow follow-up plan request. For example, after a source
node finishes, the runner invokes `plan --json` with an `expand` request,
callback ID, and the permitted upstream outputs. The binary returns only the
new node descriptions and edges. The runner validates and persists that delta
atomically before scheduling it. Predicate and memo-key requests use the same
plan mode to return a boolean or key. The binary computes authored values;
the runner decides whether to skip, replay, acquire a slot, or run. Give these
requests immutable run inputs and a stable binary digest, and define replay
rules so a crash cannot produce a different graph silently. Keep the
callback surface scoped to known IDs and completed predecessor outputs.

`run-node <id>` receives a versioned invocation with run ID, node ID, attempt
and claim generation, resolved arguments, permitted predecessor outputs, and
the selected plan digest. The binary verifies the node against its plan and
executes only that node's work. A framed result reports outcome, typed output,
artifacts, and step diagnostics. The runner owns terminal state writes even
if the child crashes or times out. Stderr carries human logs; structured log
and artifact messages use a bounded local channel so the runner can mask and
seal them. The same local channel serves scoped requests for a node's secrets,
OIDC token, artifacts, child pipeline spawn, and child wait. The runner
authorizes each request against the plan, run, node, and claim generation.
Neither stdout parsing nor a broad controller proxy is the security rule.

## Compatibility and feature gates

Ship two execution paths for a stated compatibility window. A binary that
reports the new protocol uses runner-owned orchestration. An older binary
still receives `handle-trigger` and its existing `run-node` contract. Select
once per run and persist the choice, including for retries. Never infer that
an old SDK supports a new capability from its Go module version. Set an
explicit minimum legacy SDK pin and supported runner range in release docs;
exercise the oldest supported pin and the current pin in the runner's
compatibility suite. After the window, reject older pins at trigger admission
with an upgrade message. The old path cannot receive runner orchestration
fixes, so the window must have an end.

The legacy path also needs a private, disposable `SPARKWING_HOME` for the
old child's local scratch DB. The remote controller remains the durable
state owner. This avoids opening a newer runner's local schema with an old
binary. Verify this with the oldest supported pin before promising coverage;
if that pin relies on shared local state, raise the minimum pin rather than
silently creating a partial run. A new-protocol child does not open the state
DB at all.

An SDK feature that needs new orchestration declares a required capability
in the exported plan. Its authoring method may compile against a newer SDK,
but plan validation fails on an older runner with the missing capability
named. Optional fields have explicit defaults that preserve existing behavior.
The runner and SDK each publish the protocol versions and capabilities they
support. This allows runner upgrades to fix policy for every new-protocol
repository without waiting for a pin bump, while keeping new feature use
honest.

## Rollout

1. Define the protocol schema, limits, deterministic plan rules, and golden
   fixtures. Add runner validation and a protocol probe while leaving the
   legacy path active. This ships without changing a run.
2. Add plan export to the SDK and binary entry point. Compare exported plans
   with the existing snapshot and observed scheduling for representative
   static, dynamic, memoized, concurrent, and retrying pipelines. Fail closed
   for features the export cannot express.
3. Move trigger dispatch, state writes, and node supervision into the runner
   behind the selected protocol. Keep the controller schema and claim fences
   authoritative. Test process loss after plan persistence, after dispatch,
   and before result commit, plus mixed runner versions.
4. Add the scoped node channel and migrate steps, secrets, logs, artifacts,
   expansion, and child waits. Exercise the oldest supported legacy pin in a
   private home. Enable the new path for a small set of repositories, compare
   outcomes and timings, then expand it. Each wave can fall back for later
   runs without changing an in-flight run's selected path.
5. Publish the compatibility deadline, reject unsupported pins before claim,
   and remove the legacy path after its last supported runs have drained.

## Risks and deletion

Repeated plan evaluation can have side effects or read changing external
state. The protocol must bound each call, persist accepted results, tie them
to source and binary digests, and refuse replay drift. Typed Go outputs need
stable JSON encoding and size limits. Dynamic graph insertion, spawn, and
child waits need explicit replay and authorization rules. A runner rolling
upgrade must not resume an in-flight run whose selected protocol it cannot
read. The compatibility suite should include at least one older runner and
one newer runner against each supported protocol major.

Executing repository code remains a trust risk even without a controller
token. Planning and node children need process isolation, controlled
environment variables, resource limits, and scoped filesystem and network
access. The runner keeps controller credentials outside child environments.
The local channel binds requests to one plan digest and claim generation;
the controller still checks fences on runner writes. Secret values and raw
outputs stay out of plan snapshots and logs. The existing remote broker is a
starting point for per-node scoping, not a reason to keep its supervisor
routes available to the child.

Once the window ends, delete `handle-trigger` in repository binaries, the
binary-owned dispatch and heartbeat path in `internal/orchestrator/worker.go`,
and the duplicate remote and local scheduling paths that exist only to let a
node binary orchestrate. Remove the old child's controller-facing run-state
client and broad broker routes. Keep the SDK's plan builder and step runtime,
the runner's orchestration and controller client, and only the scoped node
channel required by authored work.
