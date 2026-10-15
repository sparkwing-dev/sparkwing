# Node protocol

Protocol v2 is the boundary between a matching Sparkwing engine and an SDK process. The host owns scheduling, placement, admission, leases, retries, storage, secret release and output masking. The SDK declares jobs and runs customer code when the host requests it. Go and TypeScript share this wire contract.

<!-- design:begin -->
The native host and SDK cutover is unfinished. This page and the generated schemas specify the v2 contract. Schema tests establish message shapes. They do not establish native execution on every placement or Windows handle inheritance.
<!-- design:end -->

## Processes and transport

The engine acquires the sealed source SHA, reads its exact SDK pin and selects the verified matching engine. That engine installs dependencies, builds the entry and hosts execution. Go uses a locally cached pipeline binary. TypeScript runs `node <main> --sw-node-protocol`, where `main` comes from `.sparkwing/package.json`. Installation uses the shared filtered install environment and masked diagnostics.

The host starts an entry with `--sw-node-protocol`. On Unix the SDK reads requests from descriptor 3 and writes frames to descriptor 4. stdin is the null device. stdout and stderr carry customer text and are captured separately. A frame contains one JSON object followed by a newline. Customer stdout cannot forge a protocol reply.

On Windows the host appends `--sw-protocol-handles <request>,<frame>` with decimal inherited handles. Both SDKs support this transport. At transport initialization the SDK clears inheritance on the descriptors or handles before starting tools. Code in an independent Go package may initialize before the SDK, so package initialization must not spawn tools that inherit the pipes.

The first request is `hello`, exactly once. Its in-memory binding carries a delegated node-host bearer when the process is bound to a job. The reusable controller or runner credential stays with the host. A describe or plan process has no job binding. Protocol handles, route bearers and version negotiation use this transport, rather than process environment variables or HTTP version headers.

The shared process environment builder distinguishes installation, Git acquisition, planning and job execution. Startup environment applies before the process starts. Body environment applies after planning. Startup environment must not influence declarations.

## Hello and operations

Every request has a nonempty `id` unique within the process and an `op`. Replies correlate through `reply`. Requests and replies accept additive unknown fields. Recognized fields must have the required type and cannot contradict the operation.

| Operation | Required fields beyond `id` and `op` | Meaning |
| --- | --- | --- |
| `hello` | `protocol`, `engine`, `runner` | Negotiate protocol 2 and pass verified engine and server-granted runner identity. Optional `binding` carries job authority. |
| `describe` | none | Read registered pipeline metadata and secret declarations. |
| `plan` | `pipeline`, `args`, `run` | Declare a run once per process. `args` is an explicit object, including when empty. |
| `run_step` | `job`, `step`, `attempt` | Run one planned step. Attempt is the positive, node-wide ordinal. Optional `failure` carries persisted recovery data. |
| `eval` | `closure` | Evaluate a declared closure with optional job, step, attempt, input or recovery context. |
| `cancel` | `job`, optionally `step`, or `target` | Join selected work before acknowledging cancellation. `target` names an active request, including an eval without a job. |

A cancellation cannot mix `target` with job or step selectors. Job-only cancellation includes active evals for that job. The original request still receives its own reply. Work execution requires an earlier plan in the process. A body-only job declares one step.

A hello exchange looks like this. The runner identity is granted by the controller, rather than selected by the pipeline:

```json
{"id":"1","op":"hello","protocol":2,"engine":{"version":"v1.0.0"},"runner":{"name":"builder","type":"self_hosted","labels":["arch=amd64"]}}
{"reply":"1","ok":true,"result":{"protocol":2,"sdk":{"language":"go","version":"v1.0.0"},"features":[]}}
```

`features` advertises optional operations. A major mismatch returns `protocol_mismatch` with supported majors in structured details. An old SDK answering `unknown_op` to hello needs rebuilding against the matching SDK. Protocol identity is negotiated once. Describe and plan documents carry their data without another version stamp.

A plan request keeps the invoked name in the sealed run context:

```json
{"id":"2","op":"plan","pipeline":"Build","args":{},"run":{"run_id":"run-example","pipeline":"build"}}
```

`run` also carries optional sealed Git and trigger facts, the start time, dry-run and memo controls. The SDK uses those facts rather than looking them up through controller credentials.

## Replies and errors

Successful replies have `ok: true` and exactly one `result`. Failed replies have `ok: false` and exactly one `error`. Empty acknowledgements use `{}`. Null may be a result value where the operation permits it. Duplicate, unsolicited or contradictory replies fail the session.

Protocol errors use the shared inner object, with a nonempty code and a present string message:

```json
{"reply":"3","ok":false,"error":{"code":"unknown_step","message":"step is not declared","hint":"rebuild the pipeline"}}
```

Optional `hint` and `details` retain structured diagnostics. Unknown future codes and additive fields remain readable. The shared decoder refuses null, duplicate keys, trailing input and nested error envelopes. HTTP errors wrap this object once as `{"error":{...}}`.

A body failure is a successful invocation with a failed outcome. Failure to invoke the requested body is a failed protocol reply:

```json
{"reply":"4","ok":true,"result":{"outcome":"failed","error":{"code":"internal","message":"compiler failed"}}}
```

`RunResult` admits `success` or `failed` and optional JSON output. Recovery requests carry `failure` with the source `job`, `stage` and this same inner error object. The host reads persisted failure data instead of inferring it from a status string.

## Describe and plan documents

Describe returns `pipelines`. Each pipeline declares `name`, an explicit `args` array and optional help, examples, environment documentation, risks, step risks and secrets. Shared argument descriptions use `name`, `type`, `required`, optional `desc`, default, enum, secret classification and `job_id`. Step risks use `job_id` and `step_id`. Secret declarations carry names and optionality, never values. Describe is the sole source of secret declarations.

A plan contains an explicit `jobs` array and optional concurrency, resources, priority, claim wait, admission class and checkout policy. Jobs carry their execution envelopes directly:

```json
{"jobs":[{"id":"build","env":{"MODE":{"value":"test","startup":false}},"steps":[{"id":"compile"},{"id":"test","needs":["compile"]}],"result_step":"test","retry":{"attempts":0},"artifacts":["dist/**"]}],"priority":0,"concurrency":[{"group":"builds"}]}
```

| Declaration | Fields and interpretation |
| --- | --- |
| Job edges | `deps`, `deps_if_present`, `needs_generated`; recovery uses `on_failure_of`. |
| Steps | `id`, `needs`, `groups`, `finally`, risks, optionality, error policy, dry-run support and closures. `result_step` chooses the job result. |
| Environment | A name maps to `{value,startup?}`. Startup and body values follow their separate process boundaries. |
| Placement | Job `requires`, `prefers` and `inline`; the host resolves inherited run requirements separately. |
| Artifacts | `artifacts` selects published paths. `consumes` entries name `producer` and optional `into`. |
| References | `pipeline_refs` declares `pipeline` and `job` read edges. A declaration grants no authority itself. |
| Retry | `retry.attempts` counts additional application attempts. Optional `backoff_ms` stays under the same claim and generation. Lease-loss disruptions have their own counter. |
| Timeouts | `timeout_ms`, `no_progress_timeout_ms` and `skip_if_budget_ms` retain independent meanings. Parent deadlines still apply. |
| Memo | `memo.ttl_ms` controls result reuse. `closures.memo_key` names code that computes a key. |
| Directory cache | `caches` entries contain `name`, exactly one of `path` or `resolver`, optional `key_scope`, and explicit `key_files`. |
| Concurrency | Plan or job arrays contain `group` and optional `scope`, `capacity`, `cost`, `on_limit`, queue timeout and cancel timeout. Only the host computes scoped keys. |
| Resources | `cores` and `memory_bytes` preserve omission. The host resolves winners and refuses requests above operator ceilings. |
| Approval | A control job with `approval` and no step body. `message`, `timeout_ms` and `on_timeout` describe its gate. Zero timeout remains indefinite where the owner can wait. `SkipIf` is supported. |
| Failure policy | `fail_fast` or `collect_all`. Persisted outcomes use `success`, `failed`, `cancelled`, `skipped`, `superseded` and `memoized`. |

Plan JSON is strict at every nested level. Unknown and duplicate keys, null required collections and host-owned fields fail with `plan_invalid`. Run identity, accepted hashes, secret declarations and resolved authority belong to the host. The v1 `nodes`, `modifiers`, `work`, computed concurrency keys and duplicate step-group/result wrappers are removed.

Omission and explicit zero or false stay distinct. Omitted concurrency fields resolve to `global`, capacity 1, cost 1 and `queue`; explicit nonpositive counts fail. The shared settings owner checks group consistency, cost versus capacity and operator ceilings. It never silently clamps an explicit value.

The shared validator also checks graphs, references, closure IDs, placement support and secret-bearing identifiers. Diagnostics identify the field path without echoing a secret or the identifier containing it. Every accepted and generated identifier uses the same secret registry as masking. Hashing follows validation. The store commits the accepted declarations, resolved winners and hashes at the second seal.

## Closures and generated jobs

`closures` contains `skip_if`, `before_run`, `verify`, `after_run`, `memo_key` and `generate` IDs. The IDs are opaque references to code in the SDK process. The host decides when they run. Approval jobs retain skip conditions while executable before/after hooks are removed.

`EvalResult.value` is boolean for skip conditions, string or null for memo keys, null for hooks and verify, and an array of flat job declarations for generation. Its optional error is the shared inner error object. The host wraps generated jobs in a plan for validation with expansion context.

Successful and memoized sources with declared generators keep settlement open until expansion is accepted or failed. Empty successful expansion is explicit. Exact replay preserves the persisted topology and state. A fresh execution process reconstructs generated code from persisted source outputs and compares its validated declaration with the persisted spec hash. A mismatch fails as `plan_drift` before executing that body. Reconstruction never inserts jobs or resets their lifecycle.

## Bound node-host services

The hello binding contains `run_id`, `job_id` and `routes: {url,bearer}`. Each service authenticates this delegated bearer and checks the bound process and its allowed read edges. Repository identity and caller-selected resource names confer no authority.

| Method and route | Purpose |
| --- | --- |
| GET `/node/v1/secrets/{name}` | Release a declared secret under current authority. |
| GET `/node/v1/jobs/{job}/output` | Read output through an authorized dependency edge. |
| GET `/node/v1/pipelines/{name}/latest` | Resolve a declared pipeline reference. |
| POST `/node/v1/oidc-token` | Mint a provider session under the job binding. |
| POST `/node/v1/masks` | Register `{value}` across output sinks before acknowledging. |
| POST `/node/v1/annotations` | Submit `level`, `message`, optional file, line, end line, column and step. |
| POST `/node/v1/summary` | Replace a summary with `markdown` and optional step. |
| POST `/node/v1/children` | Start an authorized child run. |
| GET `/node/v1/children/{run}/await` | Await a child belonging to the bound job. |
| POST `/node/v1/cleanups` | Register cleanup for the bound process. |
| DELETE `/node/v1/cleanups/{cleanup}` | Remove its registered cleanup. |

Mask registration that collides with an accepted identifier fails before acknowledgement. The response names the field path without printing the value. Successful registration also protects later generated declarations. Annotation and summary writes validate optional step membership, mask text and apply the shared record bound before persistence. A failed service call returns an error to the SDK.

## Logs, output and limits

An SDK log frame contains `ts` and optional `level`, `step`, `event`, `msg` and `attrs`. Levels are `debug`, `info`, `warn` and `error`. The SDK omits job attribution. The host assigns the bound subject, validates step attribution, filters debug and masks all streams before persistence or truncation. Plan and eval output retain their own run-level subjects. Unknown events remain ordinary records.

The host measures process usage. A partial platform measurement is labelled partial. SDK-reported command measurements do not replace the host's measurement.

The shared limits package owns numeric bounds. Typed job or step output is bounded at 16 MiB, and an annotation or summary at 128 KiB after masking. Application retries permit at most ten additional attempts. The 256-job fan-out bound applies to a generated expansion rather than an initial plan. Parent, execution, queue-wait, approval and inactivity deadlines remain separate.

## Schemas and conformance

The generated schemas are [requests and replies](schemas/node-protocol.schema.json), [describe and plan](schemas/describe.schema.json) and [SDK log records](schemas/log-record.schema.json). Use `$defs/plan` for a plan and `$defs/job` for a generated job. Other definitions name their corresponding message types.

From the repository root, `GOWORK=off go generate ./internal/doccheck` regenerates all three from the final Go wire types. The documentation check and its tests reject drift. Shape tests cover v2 hello, cancellation, flat plans, explicit zero, additive fields and common errors. JSON Schema cannot establish duplicate-key refusal, session ordering, graph validity, secret exclusion, transaction fencing or native process behavior. Those require the shared decoders and runtime acceptance suite.

The matching engine owns its SDK node protocol. Separately, the controller supports each engine's API for twelve calendar months after its release. Older releases continue until an explicit signed security revocation or minimum-version policy ends them. Age alone never revokes a version. Shared version notices explain policy changes, and `sparkwing upgrade` updates the exact SDK pin and verifies the selected engine.
