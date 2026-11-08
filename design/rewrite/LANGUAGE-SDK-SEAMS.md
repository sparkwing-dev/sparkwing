# Adding a language SDK

A new SDK builds declarations, retains user callbacks, and serves protocol 2.
The host runs commands and Git, Docker and service helpers, and owns execution
policy. Python needs a typed client and callback dispatcher of roughly **3,000-
4,000 runtime lines**, plus tests and host integration. It does not need a port
of the scheduler. This is a sizing note for review, not an implementation plan
approved for execution.

Evidence baseline: main commit `2f4a12f5c28c298a0dde8088437a8fdcefadc74c`, including
host-run commands, host helpers and the local `sparkwingtest` command host.
The [protocol guide](../../docs/node-protocol.md) explains the model. The
[wire declarations](../../internal/nodeproto/protocol_contract.go),
[plan declarations](../../internal/nodeproto/plan_contract.go) and executable
fixtures settle details where the guide differs.

## Process and wire minimum

Launch an entry with `--sw-node-protocol` in the acquired checkout. On Unix,
stdin is null, stdout/stderr are customer text, fd 3 receives host messages and
fd 4 emits frames. Set close-on-exec on private pipes. Windows receives decimal
handles through `--sw-protocol-handles` and clears inheritance. Keep reading
while callbacks await host responses. Serialize each complete JSON line onto
the frame pipe, correlate requests, and permit concurrent operations and replies
out of order. Request-pipe EOF cancels operations and joins their cleanup.
See [Go serving](../../internal/sdkserve/serve.go) and
[TypeScript transport](../../sdk/typescript/src/transport.ts).

| Host request | SDK responsibility |
|---|---|
| `hello` | Accept exactly once and first. Check major 2. Retain engine version, granted runner `{name,type,labels}` and optional `{run_id,job_id,routes:{url,bearer}}` binding in memory. Reply `{protocol:2,sdk:{language:"python",version:"v0.x.y"}}`. Mismatch returns `protocol_mismatch` and `supported_majors:[2]`. |
| `describe` | Return registered pipeline names, argument/help metadata and secret declarations. Preserve per-pipeline discovery errors. Read secret values only through bound execution services. |
| `plan` | Accept once per process. Decode string-valued `args`, distinguish registration key `pipeline` from invoked `run.pipeline`, build a flat `PlanDoc`, and retain callbacks under stable opaque IDs. |
| `run_step` | Find the bound job and step, apply job arguments/body env, check positive `attempt` and explicit `settled`, run the callback, and encode JSON output. Body exceptions become `ok:true` with `outcome:"failed"` and inner error. Dispatch failures use `ok:false`. |
| `eval` | Dispatch the declared closure ID, using `input` and optional step settlement state. Return `{value,error?}` and generated argument metadata when applicable. |
| `cancel` | Select `target` alone or bound `job` with optional `step`. Stop selected work, return its original reply, and acknowledge cancellation only after it stops. |

**Hello capabilities:** there is no `features` member in either implemented
hello type or SDK reply. Core operations, host exec and routes are direct calls.
The 36-feature [placement table](../../internal/nodeproto/capabilities_contract.go)
belongs to host plan validation, not SDK negotiation. The guide's operation
table and version-selection paragraph still mention `features`. Implement the
wire types above, without an old-peer fallback. The host also checks SDK language
and version against the sealed pin in [sdkHello](../../internal/nodehost/sdk_code.go).

Frames are newline-delimited objects with nonempty unique correlation IDs.
Replies carry exactly one object `result` or inner `{code,message,hint?,details?}`
error, consistent with `ok`. Check required fields, duplicate keys, known field
types and bounds. Output is at most 16 MiB and encoded frames at most 17 MiB.
Emit structured `{ts,level?,step?,event?,msg?,attrs?}` logs without job attribution.
The host assigns jobs and masks output. See
[framing](../../internal/nodeproto/frame.go),
[errors and request validation](../../sdk/typescript/src/protocol.ts) and
[limits](../../internal/limits/wire.go).

Commands add SDK-to-host `exec` and `exec_cancel` frames. `exec` carries ID,
`argv`, absolute `dir`, complete `env`, optional `step`, `capture` and base64
`stdin`. Demultiplex host `exec_output` and `exec_done` lines on the request pipe
without treating them as operations. Captured bytes arrive in stream order as
base64 chunks. Logged commands return up to 64 KiB per stream in their result.
Preserve exit, signal, missing-executable and start-error distinctions. Wait for
`exec_done` after cancelling. Linux/macOS use this path. Existing SDKs still
execute Windows commands locally, so Windows parity needs a platform executor.
See [exec wire types](../../internal/nodeproto/exec_contract.go) and
[command adapter](../../sdk/typescript/src/command.ts).

## API and callback seams

The production minimum covers these semantic families. Python spelling and
typing can differ from Go's interfaces or TypeScript's fluent API.
Encode snake_case wire fields and required empty collections. The host strictly
rejects unknown plan fields and validates declarations at acceptance.

| Seam | Representation and frequency | Ownership |
|---|---|---|
| Pipeline registration, typed inputs, help, secrets | `describe`, then `plan` per fresh process. Replanning also runs before execution. | SDK reflects or explicitly declares input schemas, binds values, builds plans and seals callback registries. |
| Jobs, steps, groups, dependencies, recovery | Flat plan data. `onFailure` becomes a job with `on_failure_of`. | SDK emits stable IDs, edges and `result_step`. Host validates graphs and schedules jobs/steps, including finally and collect-all policy. |
| Skip predicates, hooks, verify | `eval` for each requested callback. Predicates run before eligible work. Before/after hooks run per executed attempt, verify after successful bodies. | SDK invokes code. Host decides ordering, budgets, retries and whether callbacks run. |
| Memo | Declarative `memo.input` recipe or `memo_key` eval before reuse lookup, after skip checks. | SDK builds recipes or returns callback JSON/null. Host hashes canonical JSON, evaluates file/env recipes and stores results. |
| Generated jobs | `generate` eval after successful/memoized source, plus ancestor replay in fresh execution processes. | SDK returns a **plan object with `jobs`**, retains generated callbacks, and reports `arguments`/`declarations_known`. Host accepts topology once and checks replay hashes. |
| Typed step/job outputs, refs | Local step output lookup. Bound output/latest HTTP read per requested external value. | SDK encodes/decodes JSON and preserves absence versus failure. Host stores outputs and authorizes declared dependency/reference edges. |
| Approvals, concurrency, placement, source, retry, deadlines, risks, dry run, caches/artifacts | Plan declarations, including omitted versus explicit false/zero. | Host resolves and enforces policy. SDK selects its declared dry-run callback when asked to execute. |
| Secrets, variables, OIDC, masking, annotations, summaries | Bound HTTP call per API use. Structured log frame per log record. | SDK waits for writes to be acknowledged and maps typed errors. Host owns authority, classification, masking and storage. |
| Child start/await (`RunAndAwait`) | POST child, then GET await until terminal. Declare permitted child targets in job `children`. | SDK serializes inputs and wait cancellation. Host owns child intake, provenance, lifecycle and output. Go also exposes detached start. |
| Cleanup and Git/Docker/services | Cleanup register/disarm routes or typed helper POST per operation. | SDK wraps resource scopes in language `finally`/context managers. Host executes helper behavior and records crash cleanup. |
| Debug pauses | Host execution boundary. | Host retains/releases pauses. The SDK emits debug logs through its normal logger. |

Owners: [Go callback dispatch](../../sparkwing/protocol_operations.go),
[TypeScript plan builder](../../sdk/typescript/src/plan.ts),
[session and generated reconstruction](../../sdk/typescript/src/runner.ts),
[host executor](../../internal/nodehost/executor_process.go),
[host replay](../../internal/nodehost/sdk_replay.go) and
[owner debug boundary](../../internal/nodehost/native_assembly.go).

Eval values are boolean for `skip_if`, JSON or null for `memo_key` (empty strings
fail), null for hooks/verify, and a plan object for `generate`. Failed callbacks
return null plus the inner error. `after_run` receives the final outcome/error
in its input. See [value validation](../../internal/nodeproto/eval.go).

Settlement is SDK runtime state, separate from host scheduling. Reject unknown,
duplicate, stale, incomplete or overlapping snapshots before mutation. Retain
parallel completed outputs within an attempt. A settled predecessor reconstructed
without local output yields the language's documented zero/absent value. Reset
outputs for a new non-overlapping attempt. Port the behavior of
[StepState](../../sdk/typescript/src/step-state.ts), not the scheduler.

Implement one authenticated HTTP client for the binding's loopback URL. Escape
each opaque path segment and use the in-memory bearer. The routes cover secrets
(including `?variable=true`), job output, latest output, OIDC, children, cleanup,
masks, annotations, summaries and helpers. Preserve latest-reference flags,
including `from_proven_run`. Missing required response fields are errors.
See [client](../../sdk/typescript/src/routes.ts) and
[host route owner](../../internal/nodehost/process_services.go).

Helpers POST `/node/v1/helpers` with `{operation,args,dir,env,step?}` and decode
`{result,error?,code?}`. `args` is an array even when empty. Match operation
names and positional/config encodings from the
[dispatcher](../../internal/hosthelpers/helpers.go). Thin wrappers translate
snake_case configs, base64 byte results and sentinel errors, and invoke local
user scopes. Host-owned behavior includes Git commands, Docker/buildx behavior,
service readiness, environment filtering, output masking and resource cleanup.

## Language work and host integration

Python owns its public generics/type hints, JSON codecs, input binding,
async/cancellation model, callback registry, context propagation, imports and
package/wheel metadata. Use explicit presence markers where `None` would erase
omission versus an explicit value. Python JSON encoding must reject NaN/Infinity
and preserve required empty objects/arrays. Built-in memo recipes stay data,
including `const`, so file matching and hashing stay in the host.
The [TypeScript public surface](../../sdk/typescript/index.d.ts) is a compact
parity checklist.

A Python equivalent of `sparkwingtest` should expose a test context, plan,
run-step/eval, fixtures for secrets/outputs/OIDC/children, captured records and
close. Prefer driving the real host through protocol fixtures for policy and
process tests. Go's [test host](../../sparkwing/sparkwingtest/host_contract.go)
also runs library commands/helpers locally, but explicitly refuses registered
cleanup API calls. Reproducing its masker, validator or process manager in
Python would move host policy back into the SDK.

The shared execution path already consumes a command vector, but discovery and
installation recognize only Go and TypeScript. Adding Python also needs SDK-kind
and pin/lockfile parsing, reproducible dependency installation, interpreter entry
resolution and runtime provisioning on runners. Cover source snapshots,
dependency/cache identity, local/runner/cluster/GitHub Actions startup, CLI
scaffolding/upgrade/references and distribution. `pipeline lint`, `explain`,
`list` and agent info can then consume the same describe/plan documents.
These are host adapter changes, not new protocol operations. See
[entry discovery](../../internal/pipelineentry/pipelineentry.go),
[SDK identity](../../internal/engineid/identity.go),
[pin reader](../../internal/engine/pin.go) and
[installer](../../internal/engine/sdkinstall/install.go).

## Admission tests for a new SDK

Compatibility here means conformance to the same contract as Go/TypeScript.
Use shared fixtures rather than a Python-only fake of the protocol.

1. Run an equivalent Python entry through all eight sessions in
   [v2-corpus.json](../../internal/nodeproto/conformance/testdata/v2-corpus.json):
   describe, plan-once, execution, body/hook failure, unknown op, major mismatch
   and generated reconstruction. Validate replies/documents against both schemas
   beside that corpus. Follow the [Go](../../internal/nodeproto/conformance/conformance_test.go)
   and [TypeScript](../../sdk/typescript/test/nativehost/conformance_test.go) drivers.
2. Build equivalent declarations for its 11 plan vectors and exercise its six
   inner-error vectors. Compare host canonical hashes, explicit false/zero versus
   omission, generated membership and secret-identifier refusals. The host owns
   validation/hash computation. Also run shared
   [job IDs](../../internal/nodeproto/testdata/job-ids.json) and
   [helper/group vectors](../../sdk/typescript/test/fixtures/helper-vectors.json).
3. Port protocol, settlement, cancellation, output/serialization and route
   scenarios from [SDK tests](../../sdk/typescript/test/), including malformed
   frames, duplicate IDs, pipe inheritance, EOF cleanup, generated arguments,
   missing response members, ref absence, write acknowledgements and secret-safe
   logs. Add Python import/type/package-consumer tests.
4. Add Python to [host command scenarios](../../internal/nodehost/host_exec_test.go)
   and [helper scenarios](../../internal/nodehost/helpers_test.go): exact captured
   bytes, masked logged output, stdin, cwd/PATH/env, failures, cancellation,
   descendants, resource cleanup and measurement. Run a packaged pipeline through
   the real local host and one runner path. Windows requires separate inherited
   handle, executor and wheel tests.

The guide also retains stale text describing generated values as arrays and
commands running under older hosts. The executable contracts require a generated
plan object and direct host exec on Unix. Keep those doc fixes separate from
this research note.

## Size estimate

Counts at the evidence baseline use tracked physical lines, including comments
and blanks. Tests and generated/build output are excluded from runtime counts.

| Existing source scope | Files | Lines |
|---|---:|---:|
| `sdk/typescript/src/**/*.ts` | 21 | 2,830 |
| `sdk/typescript/index.d.ts` | 1 | 385 |
| `sdk/typescript/test/**/*.{go,ts,mjs}`, including fixture programs | 40 | 4,928 |
| `sparkwing/**/*.go`, excluding tests and `sparkwingtest` | 49 | 7,113 |
| `internal/sdkserve/*.go`, excluding tests | 8 | 1,175 |
| `sparkwing/sparkwingtest/*.go`, excluding tests | 3 | 874 |

These scopes are reproducible with `git ls-files`, the suffix/path filters above
and line counts. They are source-volume anchors, not linked dependency totals.
The Go scopes exclude shared wire/platform packages. TypeScript's plan builder
is 755 lines, runner 444, command adapter 358, protocol 207 and routes 215.
Its helper wrappers, including memo recipes, total 233 lines. Those responsibilities
are the closer model for a new SDK than the Go API's reflection and interface code.

Estimate, inferred from those components: **3,000-4,000 runtime lines**, including
typed plan/context APIs and thin helpers, **3,000-5,000 test/harness lines**, and
**500-1,500 host adapter/packaging lines**. Total: **6,500-10,500 lines** for a
usable SDK with conformance coverage. Allow roughly **1-2 engineer-weeks** for
Linux/macOS parity with the shared host, with lower confidence in delivery time
than source size. Windows and runner interpreter provisioning are the main
uncertainties. This estimate assumes reuse of protocol 2 and the host's helper,
memo, masking, scheduling and cleanup implementations.
