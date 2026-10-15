# C3 node protocol contract inventory

This inventory covers the process wire and the strict plan boundary in `internal/nodeproto`. It does not own authorization, error codes, numeric limits, sealed run specs, state transitions or SDK declaration builders. The implementation uses only protocol v2.

## Exported inventory

| Symbol | Purpose |
| --- | --- |
| `Version` | Integer 2, negotiated once by hello. |
| `Op`, `OpHello`, `OpDescribe`, `OpPlan`, `OpRunStep`, `OpEval`, `OpCancel` | Complete required operation set. |
| `Request`, `Request.Validate`, `ParseRequest` | Additive request decoding and operation shape checks. |
| `EngineIdentity`, `RunnerIdentity` | Engine release and the claim's granted runner identity. |
| `ProcessBinding`, `ProcessRoutes` | In-memory run/job binding and delegated node-host route bearer. |
| `HelloResult`, `SDKIdentity` | Selected major, SDK release/language and supported optional operations. |
| `RunContext` | Sealed host-supplied inputs, including invoked pipeline name. |
| `JobFailure` | Recovery source and canonical error payload. |
| `ErrorUse`, `ErrorUses` | C2 code-consumer inventory; no definitions or HTTP policy. |
| `Reply`, `Reply.Decode` | Correlated result or C2 error. |
| `RunResult`, `EvalResult` | Step-body outcomes and closure values. |
| `EvalResult.ValidateValue` | Checks a closure result against its declared kind before the host uses it. |
| `Frame`, `FrameLog`, `ParseFrame` | Reply/log multiplexing on the private frame pipe. |
| `DescribeDoc`, `PipelineDescription` | Registered pipeline metadata, with secrets declared only here. |
| `SecretDeclaration`, `ArgumentDescription`, `EnvironmentDescription` | Language-neutral input declarations. |
| `UsageExample`, `StepRisks` | Help examples and host risk-gate attribution. |
| `PlanDoc` | SDK-only top-level settings and flat jobs. |
| `JobSpec`, `StepSpec` | Work graph, hooks and execution envelope. |
| `ClosureSpec` | Opaque code references, never serialized functions. |
| `EnvValue` | Explicit startup/body split. |
| `ConcurrencySpec` | Shared plan/job declaration; no computed key. |
| `ResourceSpec`, `SourceSpec` | Explicit-or-unset code settings, checked at seal point 2. |
| `RetrySpec`, `MemoSpec`, `CacheSpec` | Application retry, result memo and directory-cache declarations. |
| `ApprovalSpec` | Approval message, timeout and timeout policy. |
| `ArtifactInput`, `PipelineReference` | Declared dependency and cross-run read edges. |
| `SecretMatcher` | Predicate over registered secrets without exporting values. |
| `ValidationContext`, `ValidatePlan` | One strict plan/expansion validation boundary. |
| `Placement` and its six constants | Local, local-triggered, runner-hosted, launcher, here and Actions topologies. |
| `PlanCapability` and its 36 constants | Exhaustive declaration feature vocabulary for the matrix. |
| `PlacementCapability`, `CapabilityTable` | Complete six-placement launch support matrix. |

## Wire invariants

1. Host requests use one JSON object per line. Fds 0-4 retain their existing roles; stdout/stderr are captured separately from the frame pipe. Windows appends `--sw-protocol-handles <request>,<frame>` with decimal handles, as `protofd.HandlesArg` declares. TS starts with `node <main> --sw-node-protocol`, with no preload. SDK transport initialization clears inheritance before SDK tools spawn, preserving the existing initialization-order limitation for independent packages. No credential or protocol handle travels in env.
2. Hello is first and occurs exactly once. It requires protocol, engine and granted runner identity. Binding is absent for describe/plan. Bound execution gets only a delegated node-host bearer, kept in memory. No HTTP version header, 426, document version field or per-request version stamp remains.
3. A major mismatch is `protocol_mismatch`, with supported majors in error details. An old SDK's `unknown_op` becomes an actionable rebuild diagnostic. Each fetched engine serves its matching SDK node protocol. Decisions 101-103 replace A32: the controller API supports each engine for twelve calendar months from release, and older versions continue until an explicit signed security policy ends them. Support-window expiry alone never revokes a release.
4. All requests have nonempty unique correlation IDs. `plan` requires a registered SDK pipeline name, an explicit args object, and run context with the invoked pipeline name. Planning occurs at most once per process. `run_step` requires job, step and positive node-wide attempt ordinal. `eval` requires an opaque closure ID. `cancel` names the in-flight job/step and still leaves that request entitled to its own reply. Describe and repeated-plan requests cannot block cancellation. Binding and active-step checks precede body-environment preparation. A completed step releases its active registration before publishing its reply. No `run_node` is accepted.
5. Requests, replies and log records ignore additive unknown fields; the plan decoder is strict. Required field types, nullability and contradictory fields still fail. Replies require exactly one result or canonical error, consistent with `ok`. Duplicate or unsolicited replies fail. `RunResult` admits `success` or `failed`; a body failure is a successful protocol reply with failed outcome, while failure to invoke the body is a failed reply. The persisted outcome vocabulary remains C1's owner.
6. `EvalResult.value` is boolean for skip conditions, nonempty string/null for memo keys, null for hooks and verify, and an array of `JobSpec` for generation. Its error is the same C2 inner error object. `ValidateValue` takes `skip_if`, `memo_key`, `before_run`, `after_run`, `verify` or `generate`. Failed evaluations carry a null value. The host wraps generated jobs into `PlanDoc{Jobs: ...}` before `ValidatePlan` with expansion context. The SDK cannot supply plan settings during expansion.
7. SDK frame logs omit job attribution. The host assigns the bound job, filters debug and routes plan/eval output to run logs. Unknown events remain ordinary records. Host-side masking occurs before storage or truncation. Transport/output bounds come from C4 `internal/limits`, not new constants here.
8. C2 owns `internal/apierr.Error`, `Code`, the registry and serialization. `Reply.Error`, `RunResult.Error`, `EvalResult.Error` and `JobFailure.Error` use `*apierr.Error`, serialized as **the inner** `{code,message,hint?,details?}` object. `ParseRequest`, `ParseFrame` and `Reply.Decode` use `apierr.DecodeInner` before accepting recovery, reply, body or eval errors. It rejects malformed objects and double envelopes while allowing additive fields and unknown future codes.
9. An `after_run` eval carries the final `RunResult` in `input`. Successful results pass nil to the hook. Failed results pass the decoded inner error. Other final outcomes are represented as failed results with their canonical error so a callback cannot mistake cancellation for success.
10. An execution process bound to a generated job may replay declared ancestor generators before that job exists in its reconstructed plan. Replay keeps the target's delegated bearer and does not apply body environment. Other eval kinds and steps remain bound to the target job. The host checks the persisted expansion and spec hash before executing the reconstructed body.

## ValidatePlan invariants

- Decode one object with no trailing JSON, duplicate keys, unknown keys, null required collections or invalid field types. Reject unknown keys at every nested level with `plan_invalid` and an upgrade hint naming the field. Reject old wrappers (`modifiers`, `work`), old nouns, all duplicate booleans/fields, computed concurrency keys, `spec_hash`, plan `secrets`, run identity, pipeline identity and top-level placement requirements. Job `requires` remains SDK-owned; inherited YAML/run requirements are resolved by the host.
- Preserve explicit zero/false with pointers. Unset differs from explicit zero. Reject negative durations, retry counts, sizes and resource requests; reject nonfinite cores. Apply C4 limits at seal point 2, never silently clamp: retry maximum 10 additional attempts (`RetrySpec.Attempts=0` means no retry), memo TTL 30 days, generated fan-out maximum 256. Initial plans do not inherit the 256-job fan-out cap. Output bounds are enforced on outputs, not counted as plan fan-out.
- Validate job IDs are nonempty and unique across initial and existing expansion topology; step IDs are nonempty and unique within their job. No self-edges or cycles. Required dependencies must resolve; absent optional dependencies are permitted. Generated dependencies name declared generator closure IDs. Recovery sources must resolve and cannot recover themselves. Artifact producers and result steps must resolve; step edges stay within their job. A job with a body has one or more steps; an approval is a control job with no step body. Reject contradictory result, recovery and approval declarations.
- A job's `result_step` is the only result selection: no duplicate `is_result` flag. Step groups are represented through step `groups`, not a duplicate `step_groups` member list. Job groups, step groups and concurrency groups are all identifiers.
- Decision 90 applies before persistence to every job ID (including approvals), step ID, group identifier and identifier-bearing reference, in both initial plans and expansions. `SecretMatcher` must be nonnil even when the registered set is empty. Match contained registered values, not just equality, with the same registry used by masking. Diagnostics identify the **field path** (for example `jobs[2].id`), never echo the offending value. Empty registered values are not treated as matching every identifier. Do not serialize the matcher or raw values.
- Known enums are exact snake_case. Failure policy is `fail_fast|collect_all`; concurrency scope is `global|run|box`; on-limit is `queue|fail|skip|cancel_others`; approval timeout policy is `fail|deny|approve`. Nonempty concurrency groups require positive capacity and cost, with cost no greater than capacity and consistent definitions wherever reused. Membership cost may differ between jobs sharing a group. Only the host computes run/box/global key prefixes.
- Closure identifiers are opaque, uniquely attributable to their declaration and correct kind. Hooks, verify and conditions must not masquerade as generated dependencies. `memo_key` and memo policy must agree. Removed approval before/after hooks stay removed; approval conditions remain a launch parity requirement, subject to the open conflict below.
- `EnvValue.Startup` and the engine's fixed startup names are startup env. They must not affect declarations. Body env is applied only after plan, before execution; cleanup inherits the attempt env. Host credential/runtime filtering remains C4's responsibility.
- Every feature used has exactly one matrix row; unknown placement or feature is an error, never implicit support. Decision 99 makes all rows supported for all six launch placements. Historical launcher gaps (generators, plan concurrency, box scope, approval conditions) are implementation work, not final refusals.
- Expansion context provides the existing topology, source job and plan-level concurrency declarations through `ExistingConcurrency`. Existing job and plan memberships constrain every generated membership's capacity, scope and on-limit policy. Membership cost can differ. Refuse plan-level setting changes, duplicate existing IDs, unresolved references and cycles introduced across the boundary. Accept at most 256 jobs per generator fan-out. Each added job must name exactly one generator declared by the current source in its groups; inherited ancestor groups do not count again. Count separately for each source generator, preserving empty groups from its declarations. This does not impose a 256-job aggregate cap on a source with multiple generators. Validation cannot authorize expansion: C1 `AcceptExpansion` separately checks owner/eval lease, immutable source generation, expected state and cancellation in its transaction.
- The host computes canonical hashes only after validation. Generated-code replay compares its validated spec with the persisted hash and fails `plan_drift` on mismatch; it never changes topology. `settings.ResolvePlan` runs after structural validation and returns the resolved checkout policy and per-job execution settings with origins. Acceptance persists those winners separately from declarative specs and reconstruction hashes. C1 rechecks the sealed-spec hash and transactional authority in `AcceptPlan`/`AcceptExpansion`. No second ceiling table lives here.

## Acceptance evidence to build

- Drive the same valid and invalid corpus through local, triggered-local, runner, launcher, here and Actions hosts; assert identical structured verdicts and every capability-table cell.
- Go and TS transcripts cover hello, describe, plan, step, every eval kind, cancellation, duplicate reply, unknown op, version mismatch, optional feature advertisement and unknown additive fields.
- Strict-plan corpus covers duplicate/unknown nested keys, host-owned fields, all removed wrappers, graph cycles, bad references, invalid enums, explicit zero versus omission and every boundary/ceiling refusal.
- Secret tests place a registered value and a containing string into each job/step/group/approval ID, including expansions and references; assert refusal and absence of both complete identifier and secret value from returned errors, logs and persisted state.
- Expansion tests include successful/memoized sources, failed/skipped/cancelled sources, exact replay, differing replay, generation mismatch, lease loss, cancellation race, 256/257 fan-out and reconstructed-code drift.
- Scope and parity tests cover plan/job concurrency including box, approval conditions, startup/body env, artifacts, memo, caches, refs, recovery and risks on every topology.
- Windows tests prove argv handle inheritance, separate frame/stdout streams and secret-free env. Both SDK binaries must pass the shared wire-schema/conformance suite. Snapshot generated schemas and require a major bump for incompatible wire deltas.

## Cross-area dependencies and open questions

- **C2:** `apierr.Error`, `Code`, `DecodeInner`, `WriteHTTP` and `WriteJSON` own the common object. `ErrorUses()` contains typed code references, checked against `apierr.Codes`. Missing resources use `not_found`; unavailable services use `unavailable`; undeclared secrets/references, unrelated dependencies and unauthorized child repositories use `forbidden` with explicit reasons. Output not ready uses `conflict`, rate limits use `rate_limited`, declaration cycles use `plan_invalid`, and runtime child cycles use `bad_request` with `child_cycle`.
- **C1/C4:** C1 owns canonical hashing and transactional authority; C4 owns numeric limits and seal-time ceiling resolution. The validator receives only facts needed for structural/secret/capability checks. The secret matcher must use the host's registered values and refresh when expansion or SDK masking adds values; describe itself carries names only.
- **TS Windows:** decision 104 supersedes the frozen §6.8 exclusion. TypeScript and Go both support Windows, carry the bearer in hello and inherited handles in argv, and clear inheritance before spawning tools.
- **Approval condition conflict:** §6.6 cuts approval hook methods while §3.2 treats approval plus SkipIf as a parity gap. Both SDKs and the matrix support SkipIf as a condition and omit before/after hooks. The lead should confirm that reading of A28.
- **Decision 90 diagnostics:** “names the identifier but not the value” cannot mean printing the full identifier when it contains a secret. This contract names the field path instead. Lead should retain that interpretation in the integrated contract.
- **Wire spelling:** retain chapter §4.1 job `deps`, `deps_if_present` and existing `pipeline_refs`; artifacts use `artifacts` per frozen §6.6/§9.1 rather than the chapter's superseded `outputs`. Steps retain `needs`. Step grouping uses `groups` because the duplicate `step_groups` wrapper is removed. Both SDK builders and schema generators must agree.

- **SDK review:** retained Go `ParallelFailurePolicy` currently uses hyphenated values while final wire/TS use snake_case; the final SDK contract must replace those constants or specify conversion. Retained primitive Go declaration storage also needs presence bits so explicit zero remains distinguishable from omission. These are replacement-work requirements, not permission to silently default.
