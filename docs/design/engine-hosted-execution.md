# Engine-hosted execution

Status: proposal. This page is a design for contributors; no released
command behaves this way yet.

Sparkwing runs every pipeline through one execution model: the engine hosts
the run, and the customer's SDK process answers for the parts only it can
run. The engine evaluates nothing written in the customer's language. It
asks the SDK process for the plan, then schedules the plan, dispatches
nodes, retries, times out, caches, stages artifacts, admits work, resolves
secrets and handles logs. The SDK process builds plans, runs node and step
bodies, and evaluates closures when the engine asks for them by id.

The Go SDK keeps its in-process path until it speaks the node protocol. A
TypeScript SDK starts on the hosted path and never has an in-process one.

## Where the orchestrator lives

Today the orchestrator is a library compiled into the customer's pipeline
binary. The CLI builds the binary and executes it with the pipeline's name;
the binary plans, schedules and dispatches its own nodes, starting each node
as another copy of itself with `run-node`. A controller-dispatched run
starts the binary with `handle-trigger`, which does the same. Every engine
behavior therefore ships at whatever SDK version the customer pinned, and
none of it is reusable from another language.

```mermaid
flowchart LR
  subgraph today[Today]
    CLI1[sparkwing CLI] -->|exec: pipeline name, SPARKWING_* env| BIN1
    subgraph BIN1[customer pipeline binary]
      ORCH1[orchestrator: plan driver, DAG, retries, timeouts, memo, admission, caches, artifacts, secrets, logs]
      BODY1[plan function, bodies, steps, closures]
    end
    BIN1 -->|exec: run-node| NODE1[same binary, one node]
  end
```

```mermaid
flowchart LR
  subgraph hosted[Engine-hosted]
    CLI2[sparkwing CLI or runner] --> HOST
    subgraph HOST[engine node host]
      ORCH2[plan driver, DAG, retries, timeouts, memo, admission, caches, artifacts, secrets, logs]
    end
    HOST <-->|stdio: requests, responses, log records| SDK2[SDK process: plan function, bodies, steps, closures]
    SDK2 -->|HTTP node routes: secrets, outputs, cleanups| HOST
  end
```

## The boundary

| Concern | Hosted in the engine | Stays in the SDK process |
|---|---|---|
| Plan | asks for the plan with the run's args and run context, validates it, records the snapshot, checks each node process plans the same graph | runs the author's plan function and returns the plan document |
| DAG | readiness, fan-out, dependency failure, optional dependencies, OnFailure recovery nodes | nothing |
| Retries and timeouts | attempts, backoff, wall and no-progress timeouts, killing the node's process session | nothing |
| Memo | key from declared inputs hashed by the host, lookup and record | a key closure returns a string when the author wrote one |
| Admission | per-node leases from declared resources, measured per-node usage | may send `exec_end` records with numbers it has |
| Artifacts | staging before the node starts, publishing after success | nothing |
| Dependency caches | restore before the node process starts, save after success, keyed from declared key files | declares the cache directories |
| Secrets | resolves through the profile, registers values with the masker, answers the secrets route | asks the route by name |
| Logs | reads stdout and stderr, masks, timestamps, stores | writes log records to stdout |
| Steps | orders steps, applies fail-fast, finally, continue-on-error, skip ranges and dry-run | runs one step body when asked |
| Closures | decides when to call SkipIf, BeforeRun, AfterRun, Verify, key closures and generators | evaluates the closure by id |
| Cleanup and git cache | owns the cleanup ledger and puts the git cache rewrite into the node environment | registers cleanups through a route |

## Process model

The wire contract is the node protocol specification
(`docs/node-protocol.md` and its JSON Schemas). This page decides how the
engine uses it.

The engine starts one SDK process per node attempt with the single argument
`--sw-node-protocol`. That process is long-lived for the attempt: it answers
a `plan` request, then one `run_node` or a sequence of `run_step` requests,
plus any `eval` requests the host sends for that node's closures. Plan
evaluation before dispatch runs in its own short process, which also answers
closures the host evaluates before any node starts.

One process per attempt is the choice because:

- the process is the unit the host measures, so per-node CPU and memory are
  exact without SDK cooperation;
- a timeout or cancel kills one process session and nothing else;
- a retry starts clean, with no state left by the failed attempt;
- it matches the remote shape, where a node already runs in its own pod or
  launcher job.

The cost is that every node process rebuilds the plan, because bodies and
closures are captured while the plan function runs. The Go path pays the same
cost today. The host compares the node's plan entry with the accepted
snapshot and fails the node with a plan-drift reason when they differ.

One process per run was the alternative. It saves plan evaluations, but a
crash or leak in one body takes every node down, usage is no longer per
node, and it does not map onto pods.

### Framing

- The host writes one JSON request per line to the process's stdin:
  `{"id": "<id>", "op": "<op>", ...fields}`.
- The process writes one JSON object per line to stdout. A line with
  `reply` answers the request with that id, as
  `{"reply": "<id>", "ok": true, "result": ...}` or
  `{"reply": "<id>", "ok": false, "error": {"message": "..."}}`; every other
  line is a log record with the engine's log record fields.
- stderr lines become warn-level text records, as they do for Go nodes.
- The host sends one request at a time, except `eval` for a closure of the
  running node.
- End of stdin means no further requests; the SDK finishes the current one
  and exits. This replaces the inherited liveness descriptor the Go path
  uses.

```text
-> {"id":"1","op":"plan","pipeline":"ship","args":{},"run":{"run_id":"r1","pipeline":"ship"}}
<- {"reply":"1","ok":true,"result":{"protocol":"1","pipeline":"ship","run_id":"r1","nodes":[...]}}
-> {"id":"2","op":"run_node","node":"build","attempt":1,"options":{"dry_run":false}}
<- {"ts":"2026-01-02T03:04:05Z","level":"info","node":"build","msg":"compiling"}
<- {"reply":"2","ok":true,"result":{"outcome":"success","output":{"digest":"sha-1"}}}
```

| `op` | Fields | Result |
|---|---|---|
| `describe` | none | the describe document |
| `plan` | `pipeline`, `args`, `run` | the plan document |
| `run_node` | `node`, `attempt`, `options` | `outcome`, optional `output`, optional `error` |
| `run_step` | `node`, `step`, `options` | `outcome`, optional `error` |
| `eval` | `closure`, optional `input` | `value` |

`run_node` names no pipeline or args, so each node process answers `plan`
first, and the SDK runs bodies from that plan. A body that throws is a
`failed` outcome in an `ok` reply. A failed reply means the SDK could not
answer at all, such as an unknown op, node or closure; the host fails the
node with that message and the SDK's protocol version, so an author sees
"this SDK does not support X" rather than a crash.

### Closures by id

The plan document lists every closure a node or step has under `closures`,
with ids the SDK assigns, such as `deploy/skip_if/0`. Ids are opaque to the
host. The host decides when a closure runs and sends `eval` with that id;
the SDK looks the closure up in the plan it built in the same process. An
id the SDK does not know is a failed reply, never a skipped check. The host
bounds each closure's time, and a `skip_if` that fails or overruns
evaluates to don't-skip.

```mermaid
sequenceDiagram
  participant H as Engine node host
  participant P as Plan process
  participant N as Node process
  H->>P: describe
  P-->>H: protocol, pipelines, args
  H->>P: plan (pipeline, args, run)
  P-->>H: nodes, deps, modifiers, steps, closure ids
  Note over H: snapshot, admit, restore caches, stage artifacts, check memo
  H->>N: spawn with --sw-node-protocol and the node environment
  H->>N: plan (same pipeline, args, run)
  N-->>H: plan document, compared with the snapshot
  H->>N: eval deploy/skip_if/0
  N-->>H: value false
  H->>N: run_node deploy
  N->>H: GET /node/v1/secrets/name, GET /node/v1/nodes/build/output
  N-->>H: log records on stdout
  N-->>H: outcome success, output
  Note over H: save caches, publish artifacts, record output, schedule dependents
  H->>N: close stdin
```

## What describe and the plan document carry

`--describe` stays static: no run context, no args. It wraps the pipeline
array in an object with a `protocol` string, so the host can choose the path
before anything runs. The plan document is per run and comes back from the
`plan` request.

```json
{
  "protocol": "1",
  "pipelines": [{"name": "ship", "short": "Build and deploy", "args": []}]
}
```

```json
{
  "protocol": "1",
  "pipeline": "ship",
  "run_id": "r1",
  "nodes": [
    {"id": "build", "deps": [], "modifiers": {"retry": 2, "retry_backoff_ms": 1000, "timeout_ms": 600000}},
    {"id": "deploy", "deps": ["build"], "modifiers": {"has_skip_if": true},
     "work": {"steps": [{"id": "apply"}]},
     "closures": {"skip_if": ["deploy/skip_if/0"]}}
  ]
}
```

The Go plan snapshot that `plan --json` writes already carries the edges and
most of the envelope: `deps`, `optional_deps`, retry, timeouts, placement
labels, concurrency, resources, the memo flag, approvals, OnFailure, steps
with their needs, and a per-node `spec_hash`. The hosted path still needs:

- the `protocol` field on describe and on the plan;
- cache directories as data: name, path or resolver, key files;
- memo key inputs as data: file globs, environment names, constants, or a
  key closure;
- closure ids for each closure, where the snapshot has only `has_skip_if`
  and similar flags;
- step continue-on-error and optional flags, and step closure ids;
- generator closure ids for nodes expanded at run time, which the snapshot
  marks only as `dynamic`;
- plan evaluation that takes its run context in the request instead of
  reading a run row through a controller.

## Pinned Go pipelines during the migration

The host negotiates by reading describe before every run:

```mermaid
flowchart TD
  D[run binary --describe] --> Q{describe shape}
  Q -->|JSON array: Go SDK without protocol| IP[in-process path: exec the binary as today]
  Q -->|object with a protocol the host speaks| HP[hosted path]
  Q -->|object with a protocol the host does not speak| ERR[refuse the run, name the versions the host speaks]
```

- A Go binary built against any released SDK prints a JSON array. The host
  treats that as the in-process model and runs it exactly as today, with the
  orchestrator inside the binary.
- The CLI and runner learn to read both shapes before any SDK prints the
  object form, because an older CLI that meets the object form cannot list
  the binary's pipelines.
- The Go SDK moves to the hosted path by answering `--sw-node-protocol` and
  printing the object form. Its in-process path remains until every engine
  that customers run can host it, and its removal is a separate breaking
  release.
- The host keeps serving every protocol version it has shipped, because
  pinned binaries outlive engine upgrades. Node routes carry the
  `Sparkwing-Node-Protocol` header, and a host answers a version it does not
  speak with HTTP 426 and the versions it does.

## Changes by component

| Component | Today | Hosted |
|---|---|---|
| CLI `run` | builds the binary, passes run options as `SPARKWING_*` variables, execs `<binary> <pipeline>` | builds the SDK entry, runs the host in-process or through the admission daemon, keeps step windows, dry-run, only and no-cache as host state |
| CLI `pipeline describe`, `plan`, `explain` | asks the binary for its own help and plan text | renders describe and the plan document |
| Local runner | starts `<binary> run-node --coordinated` and supervises it | starts `<entry> --sw-node-protocol`, sends requests, reads results from stdout instead of re-reading the node row |
| Runner and trigger loop | execs `<binary> handle-trigger`, and the binary dispatches | dispatches in the engine from the plan document; nodes still run the SDK entry |
| Launcher and Kubernetes jobs | job args `run-node <run> <node>`, claim fence in the pod environment | job args `--sw-node-protocol`; the executor holds the claim, and the pod gets only the node environment |
| Admission daemon | leases taken by the binary's dispatcher | leases taken by the host per node |

The node environment shrinks to what an SDK reads: run and node ids, the
node route location and its bearer, runner identity, dry-run, trace context,
and the dependency proxy names other tools already define. Everything that
carries a CLI flag into the binary today becomes host state.

## Failure modes

| Failure | Host behavior |
|---|---|
| SDK process exits without answering | the node fails with the exit status or signal as its reason; the retry policy applies; records already read are kept |
| SDK process hangs | the wall or no-progress timeout kills the session; no-progress counts log silence |
| Response id the host never sent, or two answers to one id | the node fails with a protocol error; the host never guesses which answer counts |
| Non-JSON stdout line | forwarded as a text record, as for Go nodes; it is never parsed as a response |
| Node process plans a different graph | the node fails with a plan-drift reason |
| Protocol version outside the host's range | the run is refused before any node starts |
| Unknown method or closure | the node fails with the SDK's error code and the SDK version in the reason |
| Host restarts mid-node | the node process sees end of stdin and exits; the run's recovery marks the attempt lost and the retry policy decides, as it does when a dispatcher dies today |
| Secrets or outputs route fails | the SDK raises inside the body, so the node fails with that error |

## Phases

1. **Spike (in the tree).** An internal entry point reads a Go binary's
   describe document, writes the run row, asks the binary for
   `plan --json`, writes node rows and schedules the DAG itself, starting
   each node through the local runner as `run-node`. A test runs a two-node
   plan with one dependency and a dependency failure. No command reaches it.
2. **Smallest first step: plan without a controller.** Let the Go binary
   answer the protocol's `describe` and `plan` requests under
   `--sw-node-protocol`, with the run context in the request instead of a
   run row read through the controller, and have the spike use it. This is
   additive, needs no new variable, and makes plan evaluation a pure stdio
   exchange.
3. **Hosted local runs for protocol 1 entries.** The CLI recognizes the
   object describe form and runs the host locally: scheduling, retries,
   timeouts, log forwarding, and the secrets and outputs routes on loopback.
   The TypeScript SDK runs end to end here.
4. **Envelope moves.** Memo, dependency caches, artifacts, steps and closure
   calls run host-side for hosted entries, from the plan document.
5. **Go SDK on the protocol.** The Go SDK runs nodes under
   `--sw-node-protocol` and prints the object form from `--describe`.
   Runners, the launcher and Kubernetes jobs use the hosted path for
   protocol 1 binaries and the in-process path for older ones.
6. **In-process removal.** A breaking release removes the in-process path
   once supported engines all host protocol 1.

## What the spike proved

- The engine can own the run row, node rows and DAG order while the binary
  only evaluates the plan and runs one node per process. Each node ran in
  its own process, the dependent started only after its dependency
  succeeded, and a failed dependency skipped its dependent without starting
  it.
- A node reads its dependency's typed output through the engine, and its
  log records reach the host through stdout unchanged.
- The current verbs couple plan evaluation to a controller: `plan --json`
  reads the run row through the controller, and `run-node` re-reads the run
  row, rebuilds the whole plan and writes its own terminal row, which the
  host reads back instead of receiving a result.
- `--describe` carries no nodes, no edges, no envelope and no protocol
  version, so the host cannot schedule from describe alone.

## Open questions

- Whether each node process must answer `plan` before `run_node`. This page
  assumes it does, because `run_node` names no pipeline or args. The
  alternative is to carry pipeline, args and run in every `run_node`.
- Whether a failed reply carries a machine-readable code next to its
  message, so the host can tell "unknown op" (an old SDK) from "unknown
  node" (plan drift) without matching text.
- Where a node reads another node's output under `/node/v1/`. This page
  assumes `/node/v1/nodes/{other}/output`, answering the same signed grant as
  the run-scoped route.
- Whether describe names the SDK's language and version, which would let the
  host's refusal name the package to upgrade. The describe schema allows no
  field for it.
