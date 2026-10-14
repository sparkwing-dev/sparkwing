# TypeScript SDK contract

`sdk/typescript/index.d.ts` is the target author API for `@sparkwing/sdk`. It contains declarations only. The existing prototype under `src/` is not its implementation. Decision 99 requires the full SDK at launch. A29 still excludes speculative container-image, report, matrix, always-dependency, step-timeout and YAML-SDK additions.

## Ownership and packaging

The SDK owns registration, author declarations, closure dispatch, lazy service clients and protocol framing. The host owns YAML, identity, secret preflight, risk authorization, placement, concurrency keys and storage trust. The SDK may depend on the node protocol contract, never controller or store implementation.

At implementation cutover, `package.json` must publish `@sparkwing/sdk` with `types: "./index.d.ts"` and the root export condition `types: "./index.d.ts"`. Its runtime root export must point to the compiled SDK implementation. Delete the bootstrap export and preload implementation. The package is public, stamped with the engine release version, and starts as `node <main> --sw-node-protocol`. There is no new version constant owned by this declaration file. The prototype manifest is unchanged because this work does not supply a runtime.

All durations use milliseconds. Omitted option fields mean unset. Explicit zero keeps the field's documented meaning, including indefinite approval waits. Invalid values fail validation and ceiling violations name the requested and permitted values. Values above retry 10, memo TTL 30 days or fan-out 256 are refused rather than clamped.

## Export inventory

| Export | Purpose |
|---|---|
| `Awaitable` | Promise-compatible callbacks allow synchronous declarations and async work. |
| `Args` | Args remain serialized strings; []string uses comma splitting, whitespace trimming and empty-item removal. |
| `ArgSpec` | Omission preserves precedence; explicit zero never means unset. |
| `SecretSpec` | Describe declares names only; values are read lazily in bound processes. |
| `RunnerInfo` | Identity comes from hello, because process env can disagree with the claim. |
| `GitInfo` | Git facts come from the sealed invocation, not a fresh branch lookup. |
| `PullRequest` | PR base and head facts remain fixed to the triggering event. |
| `Trigger` | Trigger facts remain attached to retries of the same invocation. |
| `RunContext` | Planning uses the registered entrypoint and the invoked pipeline name separately. |
| `Failure` | Failure data supports recovery without parsing display text. |
| `Outcome` | Public outcomes use the same spelling as persisted results. |
| `JobResult` | Reasons preserve distinctions that do not warrant another outcome. |
| `Level` | Logs and annotations share level spelling. |
| `Logger` | Structured attributes survive command and closure logging. |
| `Annotation` | File locations let consumers link a diagnostic to source. |
| `SDKError` | Callers branch on code; unknown codes remain ordinary failures. |
| `Context` | Bound operations use the hello bearer in memory and never expose it here. |
| `Body` | Bodies return the job or step's JSON-serializable output. |
| `Predicate` | Predicates are eval closures and cannot alter topology. |
| `Hook` | Hooks participate in the host's ordered execution layers. |
| `AfterRunFn` | Final hooks receive the attempt result even when its body failed. |
| `RetryOptions` | App retries retain the same claim and do not spend disruption allowance. |
| `SkipOptions` | Skip evaluation has a declared budget shared by every placement. |
| `MemoOptions` | Memo TTL is ceiling-checked at seal, never clamped. |
| `Resources` | Resource units match the plan wire without a GB conversion convention. |
| `Scope` | Scope is declarative; the host computes the actual coordination key. |
| `OnLimit` | Capacity behavior never implies result reuse. |
| `ConcurrencyLimit` | Optional fields distinguish inherited policy from explicit values. |
| `ConcurrencyGroup` | One declaration lets all members share the same capacity policy. |
| `ApprovalOptions` | Approval zero timeout preserves indefinite waits where the owner can wait. |
| `Approval` | Approval dependencies are allowed without exposing dead execution hooks. |
| `Dependency` | Dependencies refer to declared jobs, approvals, groups or stable IDs. |
| `EnvOption` | Startup env is applied before interpreter initialization. |
| `startup` | Startup values must not influence declarations; drift fails the attempt. |
| `FailurePolicy` | Step policy is independent of optional job failure. |
| `Step` | Step handles retain output type across dependency reads. |
| `StepGroup` | Group dependencies expand to all members and modifiers apply to each member. |
| `JobGroup` | Display grouping can also name a set of job dependencies. |
| `Ref` | Output reads carry an explicit dependency or reference authorization edge. |
| `PipelineRef` | RefToLastRun pins the latest successful output independently of retention. |
| `RefOptions` | Max age lets a consumer refuse a stale successful result. |
| `refTo` | A job output reference retains its declared result type. |
| `refToLastRun` | Declarative references let the host authorize cross-pipeline reads. |
| `ChildOptions` | Child inputs are explicit and are not inherited from the parent. |
| `CleanupSpec` | Cleanup survives SDK process loss because the host records argv first. |
| `CleanupRegistration` | Disarming removes a recorded cleanup after the resource was released. |
| `ExecOptions` | Exec inherits attempt env, including body env, without protocol credentials. |
| `ExecResult` | Command output and usage events share one execution helper. |
| `CacheSpec` | Opaque cache descriptors prevent authors from selecting privileged ref scopes. |
| `goModules` | Go cache directories restore through the common depcache path. |
| `npm` | npm cache directories restore through the common depcache path. |
| `dir` | Custom dependency files contribute to the directory cache key. |
| `noMemo` | A memo miss sentinel keeps empty keys distinct from disabled reuse. |
| `MemoKeyFn` | Keys are evaluated by the SDK; trust and namespace are owned by the host. |
| `RepoFilesOption` | Filters keep the same repository subset across machines. |
| `ignore` | Basenames match anywhere, trailing slashes match directories, and ** spans path segments. |
| `files` | Matching tracked file bytes contribute to memo identity; invalid patterns fail. |
| `env` | Sorted environment names distinguish unset from explicitly empty values. |
| `constant` | Constants supply a stable key; an empty value fails. |
| `tree` | Regular tree files contribute recursively, skipping symlinks; missing roots fail. |
| `repoFiles` | Repository-relative selection gives matching keys across checkout roots. |
| `compose` | A failed or bypassed input cannot accidentally become a reusable key. |
| `memoKey` | Structured parts form an SDK-computed key without storage authority. |
| `GenerateFn` | Generated topology is accepted once; replay only reconstructs code. |
| `Generator` | A generator handle names the settlement barrier consumed by dependencies. |
| `Generated` | Generated jobs use the same declaration surface and validator as static jobs. |
| `Job` | Jobs declare execution policy; hosts own placement, validation and persistence. |
| `Checkout` | Checkout omission inherits source policy; fullHistory requests all history explicitly. |
| `Plan` | Plan knobs are SDK-owned inputs resolved at the second seal point. |
| `PipelineDef` | Names and secret declarations belong to describe, separate from plan documents. |
| `definePipeline` | Registration exposes one entrypoint for host-side YAML alias resolution. |
| `main` | Main negotiates hello before handling any operation or opening bound routes. |
| `UsageExample` | Help examples preserve command text and its reason for running. |
| `EnvironmentDescription` | Environment metadata documents author inputs, never transport credentials. |
| `exitNotStarted` | Failed launch and signal termination both use -1, distinguished by ExecError.terminated. |
| `ExecError` | Command failure preserves output and termination cause for diagnostics. |
| `Cmd` | A command executes only when a result method is called. |
| `workDir` | Root-relative paths match command cwd and file helper resolution. |
| `path` | Absolute-first joins work even without a discovered project. |
| `readFile` | Relative reads use the pipeline root rather than a command's temporary cwd. |
| `writeFile` | Writes create or truncate with mode 0644, matching the Go helper. |
| `glob` | Glob returns absolute matches for *, ? and character classes, without recursive **. |
| `NoProjectError` | Missing project roots are distinguishable from ordinary filesystem errors. |
| `Git` | Git snapshots stay sealed while live methods inspect the current working tree. |
| `gitFromTree` | Explicit tree discovery supports author-owned sibling checkouts. |
| `githubOwnerRepo` | Slug parsing keeps owner/repo handling identical across helper packages. |
| `git` | Git helpers use promptless direct git and never probe a removed cache service. |
| `docker` | Docker helpers retain CLI behavior and host-registered cleanup under DinD. |
| `services` | Service scopes tear down owned containers on success, failure and cancellation. |
| `withCommandEnv` | Context-level command env is inherited until explicit command options override it. |
| `InputError` | Input errors distinguish invalid author arguments from execution failures. |
| `VerifyError` | Verification errors preserve postcondition failure classification. |
| `StepError` | Step errors preserve the failing step and its underlying failure. |

## Member inventory

Data properties on exported interfaces carry the values named in the export inventory. The following table covers the callable member surface.

| Member | Purpose |
|---|---|
| `PipelineDef.plan` | Builds declarations using resolved invocation args. |
| `Logger.debug`, `info`, `warn`, `error` | Emit structured messages. Only the host filters debug. |
| `Context.secret`, `variable` | Resolve a declared masked or plain value lazily. |
| `Context.output`, `stepOutput` | Read a dependency's typed output or an earlier step result. |
| `Context.annotate`, `summary`, `mask` | Record diagnostics, Markdown and acknowledged masking. |
| `Context.oidcToken` | Mint a token for an explicit audience through the bound host. |
| `Context.runPipeline` | Start and await a child pipeline's selected output. |
| `Context.latest`, `Ref.get`, `Ref.tryGet` | Read an authorized reference. `tryGet` treats genuine absence as a miss while other failures reject. |
| `Context.cleanup`, `CleanupRegistration.unregister` | Persist and disarm host-owned cleanup commands. |
| `Context.exec`, `bash` | Build argv or shell commands whose result methods execute with command usage/log events. |
| `Step.needs`, `result` | Declare step ordering, expanding group targets to their members, and select the job result step. |
| `StepGroup.needs`, `skipIf` | Apply upstream dependencies and skip predicates to every member. |
| `Step.skipIf`, `finally` | Evaluate a skip predicate or run after ordinary step failures. |
| `Step.optional`, `continueOnError` | Control failure propagation at step scope. |
| `Step.dryRun`, `safeWithoutDryRun`, `risk` | Declare dry-run behavior and required risk acknowledgement. |
| `Job.needs`, `needsIfPresent` | Declare required dependencies or dependencies permitted to be absent. |
| `Job.needsGenerated` | Wait for accepted expansion and the generated jobs. |
| `Job.references` | Declare cross-pipeline read edges before execution. |
| `Job.withArgs` | Register job-specific args, exposed as resolved `Context.args`. |
| `Job.step`, `groupSteps` | Declare typed steps and display groups. |
| `Job.failurePolicy` | Select fail-fast or collect-all step execution. |
| `Job.retry`, `timeout`, `noProgressTimeout` | Declare app attempts and execution limits. |
| `Job.skipIf`, `beforeRun`, `verify`, `afterRun` | Register the execution and eval closures. |
| `Job.memo`, `cache` | Declare result reuse or reusable file directories. |
| `Job.artifacts`, `consumes` | Publish files or stage an authorized producer's files. |
| `Job.onFailure` | Declare a recovery job with structured failure context. |
| `Job.concurrency`, `resources` | Declare admission cost and resource demand. |
| `Job.requires`, `prefers`, `inline` | Declare hard demands, preferences or owner execution. |
| `Job.optional`, `continueOnError` | Control failure propagation at job scope. |
| `Job.env` | Declare body env or explicitly marked startup env. |
| `Job.generate` | Register expansion driven by recorded source output. |
| `Approval.skipIf` | Evaluate a host-owned approval condition before requesting a decision. |
| `Approval.needs`, `needsIfPresent`, `needsGenerated` | Order an approval against jobs or an expansion barrier. |
| `Approval.optional`, `continueOnError`, `onFailure` | Preserve approval failure and recovery policy without execution hooks. |
| `Generated.job`, `approval`, `groupJobs` | Declare generated jobs, approvals and display groups. |
| `Plan.job`, `approval`, `groupJobs` | Add static jobs, approvals and display groups. |
| `Plan.concurrency`, `resources` | Declare run-level admission and resources. |
| `Plan.priority`, `claimWait`, `admissionClass`, `checkout` | Declare code defaults resolved at accept. |
| `JobGroup.needs`, `needsIfPresent`, `needsGenerated` | Apply dependency and expansion ordering to every member. |
| `JobGroup.retry`, `timeout`, `noProgressTimeout` | Apply retry and execution limits to every member. |
| `JobGroup.skipIf`, `beforeRun`, `verify`, `afterRun` | Apply eval and execution hooks to every member. |
| `JobGroup.memo`, `cache`, `artifacts`, `consumes` | Apply reuse, directory caches and artifact flow to every member. |
| `JobGroup.concurrency`, `resources` | Apply admission cost and resource demands to every member. |
| `JobGroup.requires`, `prefers`, `inline` | Apply placement declarations to every member. |
| `JobGroup.optional`, `continueOnError`, `env` | Apply failure policy and startup/body env to every member. |
| `Plan`, `ConcurrencyGroup` constructors | Create author-owned declarations without host state. |

## Helper member inventory

| Member or nested export | Purpose |
|---|---|
| `Cmd.dir`, `env`, `envMap` | Set root-relative cwd and override inherited command env. |
| `Cmd.run`, `capture` | Execute with streamed lines or silent captured output. |
| `Cmd.string`, `lines`, `json`, `mustBeEmpty` | Capture trimmed text, trimmed nonblank lines, decoded JSON or an empty-output assertion. |
| `Git.workDir`, `shortSHA`, `name` | Read checkout location, a 12-character commit prefix and bare repository name. |
| `Git.isDirty`, `filesetHash`, `changedFiles`, `tagsAtHead`, `latestTag`, `pushTag` | Apply live git operations to the snapshot's checkout. |
| `git.CloneOptions` | Set clone depth without selecting a removed cache path. |
| `git.clone`, `fetch` | Acquire or refresh a direct git checkout without prompting. |
| `git.shortCommit`, `currentSHA`, `currentBranch`, `defaultBranch`, `remoteOriginURL` | Query checkout identity using git. |
| `git.isDirty`, `filesetHash`, `changedFiles` | Inspect changes and compute working-tree content identity. |
| `git.tagsAtHead`, `latestTag`, `tagExistsOnRemote`, `pushTag` | Inspect or publish tags with the retained remote-existence protections. |
| `docker.BuildConfig`, `BuildResult` | Carry build inputs, pushed references, available digests and completed registries. |
| `docker.RunOptions` | Configure a one-shot container with API-based file transfer and named volumes. |
| `docker.ImageTag`, `deployTag`, `prodTag`, `all` | Carry commit/content/branch/dirty facts and derive retained deploy/prod tags. |
| `docker.DockerUnavailableError`, `BuildxRequiredError`, `PlatformUnsupportedError` | Distinguish unavailable Docker, missing buildx and unsupported platforms. |
| `docker.build`, `buildAndPush`, `push` | Build local images or push requested tags to registries. |
| `docker.login` | Authenticate with the secret on stdin, never command argv. |
| `docker.buildxPlatforms`, `filterBuildxPlatforms` | Discover supported targets and filter a requested list. |
| `docker.run` | Pull, create, copy input, execute, copy successful output and always remove the owned container. |
| `docker.computeTags`, `computeTagsIn` | Compute tags from the pipeline or an explicit checkout. |
| `services.autoPort`, `defaultReadyTimeoutMs` | Preserve automatic port allocation and the 30-second readiness default. |
| `services.Service`, `Addr` | Declare container/readiness settings and report selected host ports. |
| `services.withServices`, `withServicesAddrs` | Scope ready services around a callback, optionally exposing allocated addresses. |
| `Job.needsTools`, `JobGroup.needsTools` | Add retained known-tool selectors independently of later requires calls. |
| `ConcurrencyGroup.toString` | Format the retained author-visible group description. |

## Retained Go coverage and language-native mappings

| Retained Go author surface | TypeScript equivalent |
|---|---|
| Pipeline registration, entrypoint registration, Main | `definePipeline`, registered name, `main`. YAML aliases remain host-owned. |
| `Pipeline`, `Base`, `Workable`, `Work`, `NewWork`, `Job`, `JobNode`, `Produces` | `PipelineDef.plan`, `Plan.job`, typed `Body<T>`, `Job<T>`, `Step<T>.result`. JS functions need no embedded base type or marker interface. |
| Typed `Inputs`, `WithArgs`, `NoInputs`, extra argument bag | `ArgSpec`, `PipelineDef.args/extra`, `Job.withArgs`, resolved string `Args`, and native parsing/destructuring. An empty spec list replaces `NoInputs`. Arrays use the documented CSV rules. |
| `JobFanOut`, group constructors and membership | Native loops/maps calling `plan.job`, then `groupJobs`; groups expose readonly members. No speculative matrix controls. |
| Dependency, retry, hooks, approval, failure, resources, source, placement, risk, env and plan knobs | Corresponding `Job`, `Step`, `Approval`, `JobGroup`, `StepGroup`, `Plan` members and their options. Resource memory uses bytes; Go `MemoryGB(n)` maps to `memoryBytes: n * 2 ** 30`. |
| Option functions, enum constants, `FailureStage.String` | Options-object fields and literal unions. Failure stage strings retain action/verify. |
| `Outcome.Terminal`, `Outcome.OK` | Native literal comparisons or set membership. All six valid outcomes are terminal; success, memoized and skipped satisfy dependencies. Unknown strings are not valid outcomes. |
| `RunnerInfo.HasLabel`, `Runner`, `IsDryRun` | `ctx.runner.labels.includes(label)`, `ctx.runner`, `ctx.dryRun`. |
| `Info`, `Warn`, `Error`, `Debug`, `LogRecord`, `Logger` | `ctx.log` structured methods. Runtime log-envelope construction remains SDK-owned. |
| `Secret`, `MustSecret`, `PipelineSecrets`, `Variable`, `MustVariable`, `SecretsProvider` | Registered `SecretSpec` plus `ctx.secret/variable`; native objects and `Promise.all` compose named reads. Required values reject when unavailable; optional values can be checked explicitly. |
| `Ref`, `RefTo`, `RefToLastRun`, `StepGet`, freshness options | `Ref`, `PipelineRef`, `refTo`, `refToLastRun`, `ctx.stepOutput`, `RefOptions.maxAgeMs`. |
| `RunAndAwait`, every `WithFresh*` option | `ctx.runPipeline`, `ChildOptions.inputs/args/repo/branch/timeoutMs`. |
| Memo and all inputs helpers, `DirCache`, `KeyFromFile`, cache constructors | `memo`, `memoKey`, `noMemo`, `files/tree/env/constant/compose/repoFiles/ignore`, `goModules/npm/dir`. `dir` key-file arguments replace `KeyFromFile`. |
| `Into`, artifacts, cleanup, annotation, summary, masking, OIDC | `consumes({into})`, `artifacts`, `ctx.cleanup/annotate/summary/mask/oidcToken`. |
| Exec/Cmd, command env and process diagnostics | `ctx.exec/bash`, `Cmd`, `withCommandEnv`, `ExecResult`, `ExecError`, `exitNotStarted`. |
| File helpers and no-project failure | `workDir/path/readFile/writeFile/glob`, `NoProjectError`. |
| Git runtime and git helper package | `Git`, `gitFromTree`, `githubOwnerRepo`, `git.*`. |
| Docker helper package and detectable errors | `docker.*`. |
| Services package and allocation constants | `services.*`. |
| Help/short-help/examples/env metadata | `PipelineDef.help/short/examples/envVars`, `UsageExample`, `EnvironmentDescription`. |
| Input, verification, step errors and Go Error/Unwrap | `InputError`, `VerifyError`, `StepError`, standard JS `Error.message/cause`, and promise rejection. |
| Context cancellation | `Context.signal` and helper cancellation propagation. |

Helpers enforce planning purity before side effects. They do not expose the Go planguard context setters, which the target Go inventory deletes. Engine plumbing, route credentials, old SDK getters and removed aliases stay excluded.

## Invariants

- `ArgSpec` uses the Go describe vocabulary: string, bool, int, int64, float64, duration and []string. Args stay serialized strings, preserving int64 precision. String arrays serialize by comma joining and parse by splitting commas, trimming each item and discarding empty items, without quoted-CSV escaping. An enum constrains string values or each string-array item. Nonempty enums require a default or required input, and defaults must satisfy the enum. Per-job args use the same parser and enum validation.
- Describe carries secret declarations once. Plans contain no secret values or duplicate secret-name table. Reads are lazy per process. Undeclared reads fail with `secret_undeclared`. Missing optional declarations return `undefined`, while unavailable required values fail.
- `mask` resolves only after host registration. Annotation and summary delivery errors propagate. Registered secret values in job, step, group or approval IDs fail shared validation. A refusal never echoes the matching secret value.
- SDK processes never obtain route bearers from env and never expose bearers to tools. Hello supplies runner identity and binding. No caller chooses run, job, attempt, generation or storage authorization through public context methods.
- Body env is applied after planning and inherited by tools and cleanup. Startup env, including the engine's fixed interpreter-start list, is installed by the host. Reading startup env to change declarations is undefined and differing replans fail as `plan_drift`.
- Dependencies, consumed artifacts and declared references create authorized read edges. Knowing a digest or naming an unrelated job grants no access. Memo namespace and trusted fallback selection remain server-owned.
- Step groups expose a readonly member list. A downstream dependency on a step group expands eagerly to every member. Group `needs` and `skipIf` apply to every member. Generated approvals use the same condition and dependency policy as static approvals.
- Successful and memoized sources generate from their recorded output. Failed, skipped, cancelled and superseded sources resolve their barrier without successful generation. Replay reconstructs code only, and the persisted spec hash must match. Expansion acceptance remains a host transaction under live owner/eval authority.
- Every placement enforces risk and uses the same plan validator. Application retries retain their claim and remain separate from disruption accounting. No SDK emits lifecycle events owned by store transitions.
- Child args and typed input properties are explicit. Typed input property names are argument names, support string/number/bool/string-array values and are overridden by overlapping raw args. An omitted child repo inherits parent repo/SHA. An explicit repo selects its fresh branch tip, defaulting to main unless branch is supplied. No typed repo value grants authority. A wait timeout stops waiting, not the child. Child submission checks ancestry and authorization. `refToLastRun` resolves pinned successful output and checks max age.
- Service cleanup runs on callback success, rejection, readiness failure and cancellation. The first startup/readiness/callback error wins. Empty service lists invoke the callback once without Docker. Service port zero selects host networking, which retains its Linux-only requirement; hostPort -1 allocates a port reported by `withServicesAddrs`. Ready commands run inside containers. Omitted or zero readiness timeout uses the retained 30-second helper default. The retained no-command readiness fallback waits two seconds.
- Docker container file transfer uses `docker cp`, not host binds, so DinD works. Named volumes retain their existing semantics. A requested output path that is absent fails. Removal always runs and cleanup registration protects against SDK process loss.
- `Cmd.capture` suppresses per-line log records but retains command events. `string` trims stdout; `lines` trims and removes blank lines; `json` decodes captured stdout; `mustBeEmpty` rejects nonempty trimmed output with its reason. All result methods preserve ExecError output and termination fields on failure.
- Exec helpers emit `exec_start`, `exec_line`, and `exec_end` records and propagate process/cancellation errors. Cleanup inherits the exact attempt env and remains host-owned after registration.
- `files` hashes matching tracked content. `repoFiles(ignore(...))` hashes tracked content except excluded paths. `tree` hashes regular files and skips symlinks. Missing roots, invalid patterns and read failures reject. `env` sorts names and distinguishes unset from empty. `constant` rejects empty keys. `compose` propagates errors and `noMemo` before evaluating later inputs.
- `Checkout.fullHistory` requests all history and takes precedence over depth. An omitted field inherits source policy. Explicit depth zero is passed to host validation and never treated as unset. Tags, submodules and LFS preserve their declared source behavior. Local working trees remain unchanged by checkout options.
- Outputs must be JSON serializable and respect 16 MiB. Annotation payloads respect 128 KiB. The host applies the same limits to both SDKs.

## Acceptance evidence required from implementations

1. Drive Go and TypeScript through the same hello/describe/plan/run_step/eval/cancel fixtures. Compare normalized v2 plans for every public policy, including plan concurrency, box scope, groups, per-job args and risk.
2. Run a declared secret fixture under a real host. Verify lazy reads, optional absence, undeclared refusal, plain variables, acknowledged dynamic masks, and secret-bearing ID refusals on plans and expansions.
3. Produce and consume artifacts, reuse memo outputs, restore each depcache helper, and refuse foreign or unrelated-job digests. Verify file/env/tree/repo memo helpers are deterministic and bypass composition cannot create a hit.
4. Assert annotation file/line/end-line/column, summary and debug filtering. Assert exec usage events and cleanup after SDK termination.
5. Exercise OIDC audience selection, child inputs and wait timeout, cross-pipeline latest-success refs, max-age refusal and retention pins.
6. Reconstruct generated code after owner loss and from memoized output. Refuse spec drift and duplicate changed expansions. Test source failure and cancellation without a stranded barrier.
7. Verify body env cannot alter plan declarations and startup env drift fails. Verify tools receive no protocol credential and no inherited protocol handle.
8. Run the author fixture on Unix and Windows with handles in argv, plus local, here, runner-hosted, launcher and GitHub Actions execution. Include risk refusal, application retries, generator recovery and plan-level admission.
9. Exercise command capture/string/lines/JSON/empty assertions and cancellation diagnostics, Docker build/push/login/platform selection and DinD file transfer, direct git and tag operations, root-relative file helpers, and service cleanup after readiness/callback/cancellation failures. Compare describe help/examples/env metadata.
10. Compile a consumer fixture importing only the public root, including typed result references, every method and omitted versus explicit-zero options. Assert removed aliases and engine-plumbing exports fail type checking.

The declaration file passes the installed TypeScript compiler with `--noEmit --strict --lib es2022,dom --skipLibCheck false`. Runtime and behavioral acceptance tests require implementations. No dependency installation or broad check suite was run.

## Cross-area contracts

| Owner | Types or values this SDK must match |
|---|---|
| C3 node protocol | `PlanDoc`, job and step declarations, describe args/secrets, closure IDs, hello runner, error envelope, op/frame registry and generated-spec hashes. TS camelCase is encoded to the single snake_case wire contract. |
| C3 Go SDK | `JobNode`, `Generator`, `ApprovalConfig`, `Annotation`, memo inputs and `Plan` knobs must preserve equivalent behavior. TS async methods replace Go context/error pairs. |
| C2 credentials | Bound secret, OIDC, child, reference and data routes authorize every operation and retain the common error code. |
| C1 data | `AcceptExpansion`, source generation, barrier resolution and result read sets stay authoritative. |
| C4 environment | Startup/body phase, fixed interpreter-start names, inherited cleanup env and argv handle transport must match `procenv`. |
| C4 limits | Retry, memo, fan-out, output, annotation and deadline limits are validated by the shared host contract. |

## Open questions for the lead

- A28 cuts approval hook methods, while §3.2/A27 name approval plus SkipIf as parity work. The chosen contract retains `Approval.skipIf` as a condition and cuts executable before/after hooks. Decision 99 requires the condition on every placement. Both SDKs and the protocol capability table use this reading.

- Decision 104 supersedes the frozen §6.8 exclusion. The entire TypeScript API is supported on Windows. Protocol initialization uses hello for the bearer and argv for inherited handles, so no preload or prototype platform refusal remains in the target.
- The frozen chapter names Go `MemoKey` alongside input resolvers but does not fix TypeScript signatures. This contract uses `memoKey(...parts)` for a value and `compose(...resolvers)` for lazy inputs, matching the Go SDK's split. This is an API spelling decision, not a new capability.
- A29 explicitly names annotation end-line. The current Go API also has a column field. Both locations are retained here so neither existing diagnostic detail nor approved additions disappear.
- The root package manifest still exports the prototype. The implementation must make the packaging cut above atomically with runtime replacement. Type declarations alone do not make `@sparkwing/sdk` executable.
