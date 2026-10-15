# Node protocol

Protocol 2 connects a run host to an SDK process containing pipeline code. The
host owns scheduling, placement, admission, retries, deadlines, storage, masking
and state transitions. The SDK declares jobs and runs step bodies and closures
when the host requests them. The Go and TypeScript SDKs use the same wire types.

The declarations in `internal/nodeproto` define the wire. The schemas and corpus
in `internal/nodeproto/conformance/testdata` exercise requests, replies, strict
plans, errors and generated declarations across SDKs.

## Process startup

The host starts a Go pipeline binary as `<entry> --sw-node-protocol`. It starts a
TypeScript entry as `node <main> --sw-node-protocol`. The working directory is the
acquired repository checkout.

| Stream | Unix descriptor | Purpose |
|---|---|---|
| stdin | 0 | Null device |
| stdout | 1 | Customer text |
| stderr | 2 | Customer text |
| request pipe | 3 | Host requests |
| frame pipe | 4 | SDK replies and structured logs |

On Windows the host passes inherited decimal handles through
`--sw-protocol-handles <request>,<frame>` in argv. SDK transport initialization
marks the pipes close-on-exec or clears handle inheritance before starting tools.
Go package initialization orders this before packages that import the SDK.
Independent packages can initialize earlier, so pipeline packages should start
commands inside execution callbacks.

Every process receives `hello` first. Its runner is the identity granted by the
host, including the actual runner name, type and labels. Execution processes also
receive a binding to one run and job, with a loopback route URL and a delegated
bearer. Discovery and initial planning omit that binding. The SDK keeps the
binding in memory. Credentials and protocol handles travel through the private
pipe or argv handle argument, respectively.

```text
-> {"id":"1","op":"hello","protocol":2,"engine":{"version":"v0.71.0"},"runner":{"name":"local","type":"local","labels":["os=linux"]},"binding":{"run_id":"r1","job_id":"build","routes":{"url":"http://127.0.0.1:41873","bearer":"example-bearer"}}}
<- {"reply":"1","ok":true,"result":{"protocol":2,"sdk":{"language":"go","version":"v0.71.0"},"features":[]}}
```

Hello occurs exactly once. A major mismatch returns `protocol_mismatch` with
`supported_majors` in error details. The selected major applies to the process.
Documents and HTTP requests carry their own data without another version stamp.
The SDK's optional operations are advertised in hello's `features` array.

## Framing

Each request or frame is one JSON object followed by a newline. Correlation IDs
are nonempty and unique within a process. Replies can arrive out of order.

A successful reply contains `reply`, `ok: true` and an object `result`. A failed
reply contains `reply`, `ok: false` and an inner error object. Exactly one of
`result` and `error` is present. The common error shape is:

```json
{"error":{"code":"forbidden","message":"the operation is not authorized","hint":"check the declared dependency","details":{"reason":"reference_undeclared"}}}
```

HTTP errors use that envelope. A protocol reply places the inner object directly
in its `error` field:

```text
-> {"id":"2","op":"describe"}
<- {"reply":"2","ok":false,"error":{"code":"plan_failed","message":"pipeline discovery failed"}}
```

`code` and `message` are required strings. `hint` and `details` are optional.
Clients branch on codes. Decoders accept additive error fields and future codes.
Requests, replies and log records accept additive fields while checking the types
and required fields of known members. Plans have a stricter boundary described
below. Duplicate or unsolicited replies, malformed frames and oversized frames
fail the protocol.

A log frame has `ts` and optional `level`, `step`, `event`, `msg` and object
`attrs`. The SDK leaves job attribution to the host. The host assigns the bound
job, filters debug records and masks text before storage or truncation. Planning
and closure-evaluation logs belong to the run log. Unknown events remain ordinary
records.

```json
{"ts":"2026-10-15T12:00:00Z","level":"info","step":"compile","msg":"compiling","attrs":{"packages":3}}
```

stdout and stderr remain separate from frames. Customer text cannot supply
protocol replies. Closing the request pipe cancels active operations and ends the
SDK process. Cancellation acknowledgements and the original operation replies
allow the host to wait for cleanup before closing the process.

## Operations

| Operation | Request fields after `id` and `op` | Result |
|---|---|---|
| `hello` | `protocol`, `engine`, `runner`, optional `binding` | `protocol`, `sdk`, `features` |
| `describe` | None | `pipelines` |
| `plan` | `pipeline`, `args`, `run` | Flat plan document |
| `run_step` | `job`, `step`, positive `attempt`, `settled`, optional `failure` | `outcome`, optional `output` or `error` |
| `eval` | `closure`, optional `input`; step predicates also carry `attempt`, `settled` | `value`, optional `error` |
| `cancel` | `job` with optional `step`, or `target` | `{}` |

### Describe and plan

`describe` lists registered pipelines and their inputs, help, risks and secret
names. Secrets are declared here once. A pipeline description always includes
`name` and `args`. Each argument includes `name`, `type` and `required`, with
optional description, default, enum, secret classification and owning `job_id`.
Describe failures fail discovery instead of returning a partial schema.

```text
-> {"id":"3","op":"describe"}
<- {"reply":"3","ok":true,"result":{"pipelines":[{"name":"ship","args":[{"name":"target","type":"string","required":false,"default":"staging"}],"secrets":[{"name":"DEPLOY_TOKEN"}]}]}}
```

`plan.pipeline` names a registered SDK entrypoint. `args` is an explicit object,
including `{}` when empty. `run` requires `run_id` and `pipeline`. Its pipeline
name is the invoked name, which may be a project alias for the SDK entrypoint.
Optional context includes `git`, `trigger`, `started_at`, `dry_run` and `no_memo`.

Planning occurs at most once per process. Execution processes reconstruct the
plan from the accepted inputs and compare job declarations with the accepted
hash. A changed declaration fails with `plan_drift` before execution. Generated
jobs reconstruct their ancestor generator chain from recorded outputs under the
same rule. Reconstruction cannot change persisted topology.

```text
-> {"id":"4","op":"plan","pipeline":"ship","args":{"target":"staging"},"run":{"run_id":"r1","pipeline":"ship","dry_run":false,"no_memo":false}}
<- {"reply":"4","ok":true,"result":{"jobs":[{"id":"build","steps":[{"id":"compile"},{"id":"test","needs":["compile"]}],"result_step":"test"},{"id":"deploy","deps":["build"],"retry":{"attempts":1,"backoff_ms":1000},"timeout_ms":600000,"steps":[{"id":"apply"}]}]}}
```

### Plan document

A plan has a required `jobs` array. Its optional top-level declarations are
`concurrency`, `resources`, `priority`, `claim_wait_ms`, `admission_class` and
`source`. The host resolves these settings and checks operator ceilings when it
accepts the plan. Run identity, accepted hashes, authorization and computed
concurrency keys remain host state.

Each job is flat:

| Fields | Meaning |
|---|---|
| `id`, `groups` | Job and grouping identifiers |
| `deps`, `deps_if_present`, `needs_generated` | Required, optional and generated dependencies |
| `steps`, `result_step`, `failure_policy` | Step graph, selected typed result and failure handling |
| `retry`, `timeout_ms`, `no_progress_timeout_ms` | Application retry and execution deadlines |
| `optional`, `continue_on_error`, `on_failure_of` | Failure propagation and recovery |
| `closures`, `skip_if_budget_ms` | Opaque callback IDs and condition budget |
| `approval` | Manual decision with message, timeout and timeout policy |
| `memo`, `caches`, `artifacts`, `consumes`, `pipeline_refs` | Result reuse, files and declared read edges |
| `env`, `requires`, `prefers`, `inline`, `concurrency`, `resources` | Execution declarations |

Executable jobs declare one or more steps. An approval has no step body. Steps
have `id`, optional `needs`, `groups`, `finally`, `risks`, `optional`,
`continue_on_error`, dry-run declarations and `closures`. `result_step` selects
the job output. Failure policy is `fail_fast` or `collect_all`.

`closures` may contain `skip_if`, `before_run`, `after_run` and `generate` arrays,
a `verify` ID and a `memo_key` ID. These identify code without serializing it.
Approvals support skip conditions and separate recovery jobs.

`env` maps names to `{value, startup?}`. Body values take effect after planning,
before execution. `startup: true` and the engine's fixed interpreter variable
list apply before process startup. Startup values must leave declarations
unchanged. Commands and cleanups inherit the attempt environment.

Concurrency entries contain a `group` with optional `scope`, `capacity`, `cost`,
`on_limit`, `queue_timeout_ms` and `cancel_timeout_ms`. Omitted values resolve to
`global`, `1`, `1` and `queue`. Explicit zero capacity or cost is invalid. The host
computes scoped keys and checks consistent group definitions. Scopes are
`global`, `run` and `box`. Policies are `queue`, `fail`, `skip` and `cancel_others`.

`resources` carries optional `cores` and `memory_bytes`. `source` carries
optional checkout declarations `full_history`, `depth`, `tags`, `submodules` and
`lfs`. Presence is significant for all optional numeric and boolean settings.

`nodeproto.ValidatePlan` validates initial plans and expansions for every
placement. It rejects duplicate and unknown keys at every nesting level, null
required collections, invalid enums, cycles, unresolved required dependencies
and contradictory declarations. Omitted optional fields remain unset. It
rejects registered secret values contained in any job, step, group, approval or
identifier-bearing reference. Errors name the field path without echoing the
secret. Host acceptance separately checks authorization and resolved settings.

### Steps and settlement

The host schedules step dependencies. Every `run_step` carries the positive,
node-wide attempt ordinal and an explicit array of terminal step IDs for that
job and attempt. `settled: []` declares an empty snapshot. Step-condition evals
carry the same attempt and settlement state.

```text
-> {"id":"5","op":"run_step","job":"build","step":"compile","attempt":1,"settled":[]}
<- {"reply":"5","ok":true,"result":{"outcome":"success","output":{"packages":3}}}
-> {"id":"6","op":"run_step","job":"build","step":"test","attempt":1,"settled":["compile"]}
<- {"reply":"6","ok":true,"result":{"outcome":"failed","error":{"code":"sdk_error","message":"tests failed"}}}
```

The SDK checks known, unique settled IDs and all transitive prerequisites before
invoking a predicate or body. A terminal step cannot execute again in that
attempt. Parallel steps share completed local outputs. Settled predecessors
without local output read as their output type's zero value. A new,
non-overlapping attempt resets outputs before applying its snapshot. Stale,
incomplete or overlapping state fails with `protocol_error` before mutation.

Body outcomes are `success` or `failed`. A body error, panic or output encoding
failure returns `ok: true` with a failed result. Failure to invoke the body
returns `ok: false`. Persisted job outcomes also include `cancelled`, `skipped`,
`superseded` and `memoized`, with a separate reason.

Application retries advance the attempt ordinal under the same claim. Lease-loss
recovery uses a separate disruption allowance.

### Closures and generated jobs

`eval.closure` names a declaration's opaque ID. Result shapes depend on its kind:

| Kind | Successful `value` |
|---|---|
| `skip_if` | Boolean |
| `memo_key` | Nonempty string, or null to bypass reuse |
| `before_run`, `after_run`, `verify` | Null |
| `generate` | Array of flat job declarations |

Failed evals return `value: null` and a canonical inner `error`. `after_run`
receives the final step-result shape in `input`, preserving the final failure.
Successful completion passes no error to the Go hook.

Generators run after successful or memoized source outputs. Each generated job
names exactly one of the source's generator IDs in its `groups`. The host wraps
the returned jobs in a plan and validates them against existing topology and
concurrency declarations. It accepts at most 256 jobs per generator, preserving
empty generator groups. Acceptance, replay, source generation and cancellation
checks occur at the store boundary. Failed, skipped, cancelled and superseded
sources settle their barriers without invoking generators.

### Cancellation

`cancel` selects either a job with an optional step or an active correlation ID
in `target`. Job-only cancellation includes active evals for that job. Target
cancellation can select an eval without a job or step. Selectors cannot be mixed
or cross the process binding. The SDK acknowledges after the selected operation
has stopped. That operation also receives its own reply.

```text
-> {"id":"7","op":"eval","closure":"build/verify/0"}
-> {"id":"8","op":"cancel","target":"7"}
<- {"reply":"7","ok":false,"error":{"code":"cancelled","message":"evaluation cancelled"}}
<- {"reply":"8","ok":true,"result":{}}
```

## Host service routes

Bound SDK calls use the URL and bearer supplied by hello. Every request carries
`Authorization: Bearer <bearer>`. Identifiers are opaque path segments, escaped
before transport. The host derives run, job and claim authority from the bearer.
Hello supplies the protocol negotiation. HTTP failures use the canonical error
envelope, including its code and masked details. A failed child embeds the inner
error object in its `error` member.

<!-- node-routes:start -->
| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/node/v1/secrets/{name}` | None | `{value, masked}` |
| GET | `/node/v1/jobs/{job}/output` | None | `{output}` |
| GET | `/node/v1/pipelines/{name}/latest` | `job`, `max_age_ms` query | `{output}` |
| POST | `/node/v1/oidc-token` | `{audience}` | `{token}` |
| POST | `/node/v1/children` | `{pipeline, job, args, repo?, branch?}` | `{run_id}` |
| GET | `/node/v1/children/{run}/await` | `job`, `wait_ms` query | `{status, output?, error?}` |
| POST | `/node/v1/cleanups` | `{argv, description}` | `{id}` |
| DELETE | `/node/v1/cleanups/{id}` | None | Successful status |
| POST | `/node/v1/masks` | `{value}` | `{}` |
| POST | `/node/v1/annotations` | `{level, message, file?, line?, end_line?, column?, step?}` | `{}` |
| POST | `/node/v1/summary` | `{markdown, step?}` | `{}` |
<!-- node-routes:end -->

Secret responses require both a string value and a boolean masking
classification, including empty values and false. Output responses require an
`output` member, which can be JSON null. OIDC tokens and cleanup IDs must be
nonempty. A successful child result requires output. Missing result fields fail
the call instead of becoming an absent optional reference.

Mask registration waits for acknowledgement before returning. It also updates
the matcher used to validate later generated identifiers. Annotations and
summaries wait for acknowledgement, and write failures fail the operation.
Annotation levels are `debug`, `info`, `warn` and `error`. Positions are one-based
when supplied. The host masks text before applying record bounds.

## Limits

`internal/limits` owns the limits used by both SDKs and the host.

| Boundary | Limit |
|---|---|
| Typed output per job or step | 16 MiB |
| Encoded reply or log frame, including newline | 17 MiB |
| Generated jobs | 256 per generator |
| Additional application retries | 10 |
| Memo TTL | 30 days maximum |
| Individual annotation or summary | 128 KiB after masking |
| Registered cleanups | 16 per job |
| Cleanup arguments | 64 per registration |

Operator ceilings and inherited deadlines also apply. Invalid declarations fail
when accepted. Dependency-cache failures allow cold execution. Artifact staging
failures and missing required output reads fail the job.

## Version selection

The pinned SDK version selects a matching engine. Each engine negotiates its
node protocol once through hello. Optional wire fields, routes, error codes and
log events are additive. Optional operations are advertised in `features`.
Breaking wire changes require a new major. Strict plan fields must be understood
by the accepting engine.
