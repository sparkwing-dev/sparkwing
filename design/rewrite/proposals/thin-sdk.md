# Thin SDK audit and plan

Audit date: 2026-11-05. Source snapshot: `57ae1ac9b2693ed23faa28a2ffe7f19d1ed7475a`.
This plan records the decisions made on 2026-11-05.
It changes no runtime behavior by itself.
All source references are repository-relative and refer to that snapshot.

The SDKs should declare work, invoke author callbacks, and relay bytes and results.
The executing host should own process policy, validation, masking, measurement,
storage, retries and cleanup. Put that code in `internal/nodehost` and its host
dependencies. The local controller project can then move hosting into a runner
without moving policy through either SDK.

SDK releases before the thinning release keep their compiled policy and historical
engines. Only the existing engine resolver serves those pins. The new host
supports the thinning baseline and later SDK releases. Later host policy fixes
reach those pins through the signed compatibility map.

## 1. Inventory

The tables cover production Go files under `sparkwing/`, the TypeScript runtime
under `sdk/typescript/src/`, and its native transport. They also cover the public
Go test host. Test assertions and package build tools are evidence, not runtime
policy. The import ledger below includes every direct SDK import of an
`internal/` package.

In these tables, “operation” means the proposed host service envelope in section
2. Existing protocol operations and HTTP routes are named separately.
“Stays” permits language binding and wire safety. It does not grant policy
authority to the SDK.

### Processes, environment and measurement

| Behavior and Go evidence | TypeScript duplicate and agreement | Verdict |
| --- | --- | --- |
| `sparkwing/exec.go:371` resolves directories, checks them, starts commands and captures output. `sparkwing/exec.go:647` calls `procenv.HostCommandContext` or `ControlCommandContext`. These constructors inherit or copy an environment. They do not call the filtering policy in `internal/procenv/build.go:77`. | `sdk/typescript/src/command.ts:185` spawns commands too. Both resolve relative directories and support silent capture. Execution and error paths are separate implementations. | **Moves to host.** `command.exec/1` carries argv or shell text, directory, environment overrides, stdin and capture mode. The host starts the command in the job workspace. SDK builders only encode the request. |
| `sparkwing/exec.go:141` layers context and explicit environment values. `sparkwing/protocol_operations.go:211` adds body environment to the context. `sparkwing/protocol_operations.go:499` also calls `os.Setenv` before dispatch. | `sdk/typescript/src/runner.ts:217` rewrites `process.env`. It excludes `NODE_OPTIONS`, `GODEBUG` and `GOMAXPROCS` from body application. `sdk/typescript/src/command.ts:150` has its own protocol-variable deny list and Windows name folding. Go's helper path has no equivalent list. They disagree. | **Moves to host.** Use the accepted job environment and `procenv` at the process boundary. Pass requested overrides with `command.exec/1`. Apply a host-supplied callback environment once through proposed `prepare_execution`, after replan and ancestor replay. Remove SDK name lists and per-request global mutation. Preserve startup/body semantics. A small language adapter applies the snapshot. It does not filter or classify it. |
| `sparkwing/exec_unix.go:25` starts a session and kills its group. It requests diagnostics before escalation when an `execdiag` policy exists. `sparkwing/exec_windows.go:91` starts a suspended process and assigns a Windows Job. `sparkwing/exec_ledger_unix.go:19` records session identity for later cleanup. | `sdk/typescript/src/command.ts:196` uses detached Unix children. `sdk/typescript/src/command.ts:301` kills the group on Unix and runs `taskkill /T /F` on Windows. It has no matching session ledger or Go diagnostic escalation. | **Moves to host.** The host owns the process handle before acknowledging `command.exec/1`. Request cancellation, callback exit and runner shutdown join the same cleanup path. Keep `procgroup`, `sessionledger` and `execdiag` out of SDK imports. |
| `sparkwing/exec.go:510` reads command CPU and RSS, amortizes CPU over wall time and calls a context reporter. `sparkwing/exec_resource_unix.go:11` reads `Rusage`. Darwin RSS units differ from other Unix systems. `sparkwing/exec_windows.go:51` returns no sample. | `sdk/typescript/src/command.ts:214` reports wall duration only. It has no CPU/RSS reporter. They do not agree on measurement coverage. | **Moves to host.** Measure processes the host owns. No SDK resource report is authoritative. **Delete** the SDK reporter and amortization code. `sparkwing/context.go:135` has no non-test installer caller in this snapshot, so the protocol path does not establish that these samples reach storage. Do not count that dormant path as existing coverage. |
| `sparkwing/exec.go:529` drains streams for 500 ms. `sparkwing/exec.go:591` bounds capture after a diagnostic timeout. `sparkwing/exec_unix.go:16` also disables core dumps for that diagnostic path. | `sdk/typescript/src/command.ts:226` waits for child close and retains stream chunks. It has neither Go's drain grace nor its diagnostic capture policy. | **Moves to host.** `command.exec/1` returns the host's completion and termination reason. The host owns draining, diagnostics and capture policy. This move must preserve output and cancellation behavior before deleting SDK code. |
| `sparkwing/exec.go:68` formats errors, bounds the displayed command to 512 bytes and explains timeout recovery. `sparkwing/exec.go:621` renders argv for logs. | `sdk/typescript/src/command.ts:17` also bounds commands to 512 bytes. It has cancellation and signal reasons but no distinct timeout reason. `sdk/typescript/src/command.ts:192` and `:214` emit start and end records. Go emits a start record and debug completion. | **Moves to host** for canonical diagnostics and command events. Return structured started, exit, signal, termination and error fields. **Stays** for a small language error wrapper and `String`, `Lines`, `JSON` and `MustBeEmpty` adapters at `sparkwing/exec.go:259` and `sdk/typescript/src/command.ts:119`. These interpret a caller's returned value. They do not select execution policy. |

### Protocol server and callback runtime

| Behavior and Go evidence | TypeScript duplicate and agreement | Verdict |
| --- | --- | --- |
| `internal/sdkserve/serve.go:62` reads requests, requires hello, remembers request IDs and joins workers. `:140` dispatches callbacks. `:197` cancels and joins selected requests. `:240` recovers panics. | `sdk/typescript/src/runner.ts:79` dispatches promises. `:109` aborts and joins requests. `:130` handles hello. `sdk/typescript/src/protocol.ts:18` frames JSON. Both provide the same six operations. | **Stays**, reduced to framing, correlation, callback lookup, panic/exception conversion, cancellation delivery and joining. The protocol server is the SDK endpoint, not the job scheduler. Host dispatch decides which callback may run and when. |
| `internal/sdkserve/serve.go:145` checks the bound job. `sparkwing/work_run.go:10` validates attempt, settled prerequisites and overlapping steps. It resets transient outputs on a new attempt. `sparkwing/work.go:193` waits for and type-checks a step result. | `sdk/typescript/src/step-state.ts:4` tracks the same attempt and settlement state. `sdk/typescript/src/context.ts:51` reads transient outputs. Both duplicate prerequisite checks. | **Moves to host** for dependency, attempt and settlement decisions. **Stays** for checking that a callback ID exists, preventing unsafe local reentry, retaining typed in-process values and rejecting malformed dispatch. Keep old checks until host dispatch has equivalent coverage. Do not add an SDK retry scheduler. |
| `sparkwing/work_run.go:76` chooses the dry-run body or rejects a step without one. `sparkwing/protocol_operations.go:297` evaluates skip, hooks, verify, memo and generator callbacks. | `sdk/typescript/src/runner.ts:260` selects the dry-run body. `:321` evaluates closures. The broad roles agree. | **Moves to host** for dry-run eligibility and body choice. Extend `run_step` with an explicit selected body, gated by capability. **Stays** for executing that author body and returning its raw result. Keep closures as declared IDs. Host policy decides skip, retry, verify and recovery order. |
| `sparkwing/protocol_operations.go:247` serializes eval. `:351` remembers generated declarations so replay does not register them twice. | `sdk/typescript/src/runner.ts:285` serializes eval. `:323` remembers generated jobs and argument declarations. It handles requests concurrently while `:217` changes global environment. | **Stays** for callback registration and replay bookkeeping. **Moves to host** for amendment validation and replay order. Remove environment mutation before relaxing either eval queue. A generator is author code, so the host still requests it with `eval` and accepts its returned declarations. |
| `internal/sdkserve/guard.go:27` blocks helpers during planning. `sparkwing/inputs_schema.go:41` documents that rule. Go's `sparkwing/git/clone.go:21` reaches `gitcmd` without this guard. | `sdk/typescript/src/scope.ts:11` guards side effects through execution scope. `sdk/typescript/src/helpers/files.ts:17` guards file reads too. Go's `sparkwing/fs.go:35` does not. Coverage differs. | **Moves to host** for enforcement. Planning receives no execution services. Each operation checks phase and binding. **Stays** for an optional local misuse diagnostic. It cannot secure arbitrary author code that calls standard libraries directly. Process isolation remains a host responsibility. |
| `sparkwing/protocol_operations.go:202` imposes the node output limit. `internal/nodeproto/doc.go:7` fixes the frame bound. `internal/sdkserve/routes.go:57` bounds service responses. | `sdk/typescript/src/protocol.ts:8` hard-codes 16 MiB output plus 1 MiB envelope limits. Its output encoding also applies that bound. The values agree in this snapshot. | **Moves to host** for product output limits. **Stays** for a transport allocation ceiling and valid JSON checks. Negotiate transport ceilings at hello. Large new host payloads use chunking, so an old decoder's ceiling does not require an SDK update. Never raise or lower a customer limit as an incidental thinning change. |
| `internal/sdkserve/hostclient.go:29` implements loopback HTTP without `net/http`. `internal/sdkserve/routes.go:30` validates origin, scopes bearer use, escapes IDs and decodes replies. `internal/protofd/protofd_unix.go:11` validates pipes and sets close-on-exec. | `sdk/typescript/src/routes.ts:22` checks loopback routes. `sdk/typescript/src/transport.ts:11`, `sdk/typescript/src/main.ts:11` and `sdk/typescript/native/protocol.c:21` open and protect pipe handles. The C runtime is transport only. | **Stays.** These protect wire integrity and handle ownership. Retain origin and handle checks. The host still owns authentication and authorization. Keep the native Windows bridge because Node needs handle conversion. Its build and ABI maintenance can require an optional SDK maintenance release. |

### Secrets, identity and host services

Result encoding is another SDK behavior. Go marshals callback output at
`sparkwing/protocol_operations.go:197` and eval output at `:289`.
TypeScript converts values at `sdk/typescript/src/protocol.ts:160` and applies
output bounds at `:154`. The native language value sets differ, including
undefined and values JSON cannot encode. **Stays** for representability checks,
JSON encoding and language error conversion. **Moves to host** for result-shape
policy and product bounds. Shared vectors must cover null, missing output and
structured errors. They cannot make the two native language value sets identical.

| Behavior and Go evidence | TypeScript duplicate and agreement | Verdict |
| --- | --- | --- |
| `sparkwing/secret.go:47` checks the returned masked classification. `sparkwing/sdk_contract.go:363` checks that a variable is unmasked. `internal/sdkserve/routes.go:81` requires both value and classification. | `sdk/typescript/src/context.ts:28` checks the pipeline declaration and optionality. `sdk/typescript/src/routes.ts:81` returns only the value and discards the host's `masked` field. It does not enforce stored classification like Go. | **Moves to host.** `secret.read/1` carries name and expected kind, secret or variable. The host checks declaration, optionality, stored kind and authorization before releasing bytes. Until that operation exists, Go must retain its classification check and TypeScript needs equivalent checking. |
| `sparkwing/protocol_describe.go:101` parses typed secret declarations. `:130` resolves fields lazily and skips missing optional fields. `sparkwing/secrets.go:32` exposes a typed struct. `sparkwing/secret.go:12` imports the host secret package for an error sentinel. | `sdk/typescript/src/describe.ts:33` describes declared names. `sdk/typescript/src/context.ts:34` maps an optional miss to `undefined`. There is no Go reflection equivalent in TypeScript. | **Stays** for declaration extraction, typed assignment and language mapping of an explicit absent result. **Moves to host** for required/optional decisions. Replace the `internal/secrets` import with a wire error code and SDK-local sentinel. No secret source, masker or trust rule belongs in either SDK. |
| `sparkwing/sdk_contract.go:358` relays mask registration. `internal/sdkserve/routes.go:192` waits for acknowledgement. `sparkwing/oidc.go:28` relays token requests. `sparkwing/protocol_operations.go:77` maps unavailable OIDC to a Go sentinel. | `sdk/typescript/src/context.ts:60` and `sdk/typescript/src/routes.ts:115` relay mask and OIDC requests. Neither SDK signs tokens. | **Stays** as relay through the generic operation service. The host already registers secrets before return at `internal/nodehost/process_service_reads.go:116` and issued OIDC at `:143`. Keep issuer, claims, expiry, rate limits and masking in the host. |
| `sparkwing/ref.go:182` decodes current-job outputs. `:200` requests previous-run output with age and trust options. `sparkwing/protocol_operations.go:539` maps host reasons to an absent-reference sentinel. | `sdk/typescript/src/context.ts:50` and `:63` relay output/latest reads. `sdk/typescript/src/routes.ts:91` encodes selection options. Both delegate record selection. Go adds typed decoding and sentinel semantics. | **Stays** for typed decoding and relay through the generic operation service. **Moves to host** for every selection, trust, age and authorization decision. Return an explicit presence state in the new operation. Keep missing recorded blob bytes distinct from absent output. |
| `internal/sdkserve/routes.go:124` submits a child, long-polls it and interprets terminal states. `sparkwing/protocol_operations.go:93` applies the caller's wait timeout locally. | `sdk/typescript/src/routes.ts:118` coerces child args, submits, polls at a fixed wait and applies a client timeout. Go accepts success or memoized and handles skipped as terminal. TypeScript `:147` accepts only success and `:155` treats those other statuses as unknown. | **Moves to host.** `child.run-await/1` carries target, args and requested wait deadline. Return terminal output or a structured failure. Keep author input serialization and cancellation relay. Waiting expiry must retain its existing child-lifetime semantics. Any change needs a separate product decision. |
| `sparkwing/cleanup/cleanup.go:47` registers with the host or writes a local session-ledger record. `:80` releases through a fixed 10-second detached context. | `sdk/typescript/src/routes.ts:162` registers cleanup through the host. Its helper cleanup uses an uncancelled context at `sdk/typescript/src/command.ts:91`. It has no Go ledger fallback. | **Stays** for declaring cleanup and releasing its handle through the generic operation service. **Moves to host** for lifetime, release deadlines and replay. **Delete** the SDK ledger fallback in the thinning release. Cleanup requires a host session. A subprocess must not inherit the callback's bearer. |

### Validation, retries, caching and logs

| Behavior and Go evidence | TypeScript duplicate and agreement | Verdict |
| --- | --- | --- |
| `sparkwing/inputs_schema.go:114` walks Go types and tags, checks representable types and limits embedded depth. `:237` populates fields from strings. `sparkwing/jobargs.go:68` binds job inputs. `internal/nodeproto/arguments.go:13` also brings argument policy into the compiled package. | `sdk/typescript/src/describe.ts:33` declares argument schema. `sdk/typescript/src/runner.ts:235` receives string args. It has no Go integer, duration or reflection binding. | **Stays** for schema extraction and language decoding, including reporting an unrepresentable value. **Moves to host** for defaults, required, enum, unknown-arg and value validation. These already have a host owner. Keep wire scalar formats stable. A language representability check is not a second product validation pass. |
| `sparkwing/protocol_operations.go:149` calls full `nodeproto.ValidatePlan`. `:386` does it again for generated plans with local placement and a secret matcher. `sparkwing/plan.go:363` enforces a CPU floor. `sparkwing/tools.go:29` embeds the known-tool list through `pkg/match`. `sparkwing/sdk_contract.go:93` validates cache declarations. | `sdk/typescript/src/plan.ts:8` validates identifiers and numbers. `:402` rejects invalid retry/backoff values and `:503` rejects environment names. Go often stores those declarations and lets host validation reject them. TypeScript does not call Go's full validator. | **Moves to host.** Validate the raw `plan` or generator `eval` reply once under actual placement and current policy. Remove SDK CPU floors, tool catalogs, retry/env/cache policy checks and full plan validation. **Stays** for language type errors, duplicate local callback IDs and object ownership checks needed to construct an unambiguous declaration. |
| `sparkwing/plan.go:331`, `:348`, `:375` and `sparkwing/sdk_contract.go:88` record retry, timeout, no-progress and backoff settings. `sparkwing/concurrency.go:104` declares groups. `sparkwing/admission.go:18`, `sparkwing/priority.go:5` and `sparkwing/tools.go:40` declare admission choices. | `sdk/typescript/src/plan.ts:36` and `:302` build equivalent job/group declarations. Neither runtime contains a job retry or admission loop. | **Stays** as declarations with absent distinct from explicit zero. Host scheduling and defaults stay in the host. Move the remaining declaration-time policy checks there. Timeouts inside command, services, cleanup and child helpers are separate rows because those do execute in the SDK. |
| `sparkwing/inputs/inputs.go:13` declares memo recipes. `sparkwing/sdk_contract.go:39` gathers literal parts. `sparkwing/protocol_operations.go:325` runs an author memo callback and rejects its empty key. | `sdk/typescript/src/helpers/memo.ts:3` declares recipes. `sdk/typescript/src/runner.ts:360` handles memo callback values. Both leave built-in hashing and result reuse to the host. | **Stays** for recipe data, the bypass sentinel and invoking an author key callback by `eval`. **Moves to host** for empty-key policy, canonical hashing, reuse, TTL and trust. Do not move the author callback implementation into host code. |
| `sparkwing/depcache.go:30` calls `depcachekey.Files`, reading files and hashing platform and declaration inputs. `sparkwing/sdk_contract.go:93` separately declares host-managed caches. | TypeScript declares cache recipes in `sdk/typescript/src/plan.ts:80`. It has no equivalent public Go `DirCache.Key` evaluator. | **Moves to host** for key evaluation through `cache.key/1`. **Stays** for cache declarations. Remove `internal/depcachekey` from the SDK. Restore, save, cache scope and corrupt-byte handling remain host work. |
| `sparkwing/context.go:188` formats author log messages. `:204` builds summaries. `sparkwing/sdk_contract.go:336` defaults annotation severity. `sparkwing/protocol_operations.go:440` timestamps and encodes records. | `sdk/typescript/src/log.ts:17` formats records and command events. `sdk/typescript/src/context.ts:52` validates annotation and summary inputs. Both relay raw chunks. Their command event formatting differs. | **Stays** for language string interpolation, raw chunk encoding and structured log relay. **Moves to host** for timestamps used as authority, display banners, severity defaults, validation, bounds, retention, line splitting and masking. Use existing annotation/summary acknowledgements. The SDK does not render terminal or dashboard output. |

### Docker, Git, services and file helpers

| Behavior and Go evidence | TypeScript duplicate and agreement | Verdict |
| --- | --- | --- |
| `sparkwing/docker/docker.go:101` chooses Docker or buildx, checks platforms, supplies defaults, reads push metadata and aggregates registry errors. `:291` tags/pushes. `:331` logs in through stdin. `:374` discovers platforms. | `sdk/typescript/src/helpers/docker.ts:20` implements the same family. `:126` treats metadata creation/read/parse failures as errors. Go's `sparkwing/docker/docker.go:186` treats that metadata as best effort. TypeScript `:73` throws after partial pushes. Go `:231` can return a partial result plus an error. | **Moves to host.** Operations `docker.build/1`, `docker.push/1`, `docker.login/1` and `docker.platforms/1` own command choice, validation, credentials, digest parsing and partial results. Keep public options/results as relay types. Publish shared behavior vectors before choosing which differing behavior survives. |
| `sparkwing/docker/run.go:86` pulls, creates, copies, starts and removes containers. `:125` registers cleanup after creation and ignores registration failure. | `sdk/typescript/src/helpers/docker.ts:159` validates named volumes. `:165` registers host cleanup before creation. Go `sparkwing/docker/run.go:119` forwards volume strings without that validation. Cleanup windows differ too. | **Moves to host.** `docker.run/1` registers ownership before allocation, relays stdin/stdout/stderr and returns exit status. Host cleanup survives callback death. Preserve supported volume use. Do not turn TypeScript's narrower check into a new Go restriction during this move. |
| `sparkwing/docker/tags.go:30` defines deploy/prod tag strings. `:63` computes them from live Git state and a fileset hash. | `sdk/typescript/src/helpers/docker.ts:197` duplicates tag computation and formatting. The format is shared app convention, rather than process security. | **Moves to host** as `docker.tags/1`, including its fileset inputs. **Stays** for the trivial formatting of an already returned `ImageTag` value because it has no evolving host policy. An eventual separate helper library is optional. It must not block thinning. |
| `sparkwing/git/git.go:42` through `:118` read repository state. `:305` reads changes. `:334` selects semantic tags. `:381` creates and pushes tags. `sparkwing/git/clone.go:21` clones through `gitcmd` and disables prompts. `sparkwing/git_runtime.go:45` wraps those reads and exposes a trigger snapshot. | `sdk/typescript/src/helpers/git.ts:8` duplicates Git commands and state helpers. TypeScript `:199` requests NUL-delimited changed paths. Go `sparkwing/git/git.go:305` splits line output, so newline-bearing filenames differ. Both parse semantic tags, but one uses a custom parser and the other `x/mod/semver`. | **Moves to host.** `git.read/1`, `git.clone/1`, `git.fetch/1` and `git.push-tag/1` share command environment, prompt handling, cleanup and parsing. Keep sealed trigger data and pure accessors in the SDK. User-requested clones remain distinct from host source acquisition and do not grant repository proof. |
| `sparkwing/git/git.go:138` hashes tracked and untracked files with `.dockerignore`. `:223` walks the filesystem without Git, skips selected directories and ignores walk errors. `:173` follows file symlinks through stat/read. | `sdk/typescript/src/helpers/git.ts:73` and `sdk/typescript/src/helpers/files.ts:93` duplicate the hash. TypeScript's fallback includes regular directory entries and propagates read errors. Symlinks and unreadable walks can produce different inputs. | **Moves to host** with `git.read/1` fileset hashing. Keep this legacy hash distinct from memo identity until shared vectors prove equivalence. Do not silently change build tags or reuse keys while moving ownership. |
| `sparkwing/services/services.go:118` allocates names and ports, starts Docker services and tears them down. `:242` owns fallback delay, readiness polling, per-probe timeout and total timeout. `:160` fixes cleanup timeout at 10 seconds. | `sdk/typescript/src/helpers/services.ts:95` registers label cleanup before create/start. `:130` asks Docker for an assigned port. Go `sparkwing/services/services.go:210` probes a free host port before Docker binds it and only defers local cleanup. TypeScript `:68` rejects host networking off Linux. Go forwards it to Docker. | **Moves to host.** `services.start/1` returns addresses and a lifetime handle. `services.release/1` releases it. The host owns readiness, retries, allocation and cleanup. The SDK invokes the author callback between those two relays. Preserve caller-specified readiness budgets and handle platform errors through the shared host implementation. |
| `sparkwing/fs.go:18` joins paths. `:35` and `:47` wrap file IO. `:62` wraps `filepath.Glob`. `sparkwing/exec.go:29` obtains the work directory from process CWD. | `sdk/typescript/src/helpers/files.ts:6` uses scoped project root. `:17` wraps file IO with phase checks. `:27` implements glob syntax and `:67` walks directories to expand it. The root and guard behavior differ. | **Stays** for work-directory data and pure path joining. **Delete** redundant general file IO and glob wrappers in the one-time API cut. Callers can use language libraries for author-owned IO. This avoids adding a general filesystem RPC just to retain convenience wrappers. Host memo/file discovery remains a host service. This is a Breaking change in the thinning release. The changelog must list standard-library replacements. |
| `sparkwing/sparkwingtest/host.go:93` accepts plans. `:182` dispatches steps. `:398` resolves test secrets. `:497` registers masks. `:532` validates plans. `:582` masks logs. | TypeScript uses injected host services and native-host conformance tests, including `sdk/typescript/test/nativehost/conformance_test.go:44`. It does not ship the same public fake host. | **Moves to a host-owned test harness.** Keep the public fixture API small, but have policy assertions exercise the real host implementation. **Delete** copied validation/masking/scheduling policy from the SDK fake. Do not make a fake host's agreement with itself the compatibility gate. |

### Internal import ledger

These are direct production imports, including platform files and the public
test helper. Paths in this ledger identify the imports themselves. TypeScript
does not import Go `internal/` packages. Its corresponding behavior is in the
tables above.

| Internal package | Direct SDK import sites | Disposition |
| --- | --- | --- |
| `apierr` | `sparkwing/home.go:3`, `sparkwing/work_run.go:7`, `sparkwing/protocol_describe.go:10`, `sparkwing/protocol_operations.go:14`, `sparkwing/sparkwingtest/host.go:15` | Keep only stable wire error data in the SDK. Move error policy and test-host policy out. |
| `argvalue` | `sparkwing/inputs_schema.go:11` | Keep a scalar codec for language binding. Move validation policy out. |
| `depcachekey` | `sparkwing/depcache.go:6` | Move file key computation to host. |
| `execdiag` | `sparkwing/exec.go:19`, `sparkwing/exec_unix.go:12` | Move to host command execution. |
| `gitcmd` | `sparkwing/git/clone.go:8` | Move to host Git operations. |
| `limits` | `sparkwing/protocol_operations.go:15`, `sparkwing/sparkwingtest/host.go:16` | Move product limits to host. Retain a separate transport allocation bound. |
| `nodeproto` | `sparkwing/plan.go:13`, `sparkwing/depcache.go:7`, `sparkwing/sdk_contract.go:9`, `sparkwing/protocol_plan.go:8`, `sparkwing/protocol_describe.go:11`, `sparkwing/protocol_operations.go:16`, `sparkwing/sparkwingtest/host.go:17`, `sparkwing/sparkwingtest/host_contract.go:8` | Keep wire types and codecs. Move plan and argument policy to host-only packages. |
| `paths` | `sparkwing/exec_ledger_unix.go:11`, `sparkwing/exec_windows.go:21`, `sparkwing/cleanup/cleanup.go:18` | Remove SDK access to host state locations. |
| `procenv` | `sparkwing/exec.go:20`, `sparkwing/exec_windows.go:22`, `sparkwing/git/git.go:31`, `sparkwing/docker/docker.go:28`, `sparkwing/docker/run.go:13`, `sparkwing/services/services.go:33` | Remove all SDK imports. Host owns process environments. |
| `procgroup` | `sparkwing/exec_unix.go:13`, `sparkwing/exec_ledger_unix.go:12`, `sparkwing/exec_windows.go:23`, `sparkwing/git/command_windows.go:8`, `sparkwing/cleanup/cleanup.go:19` | Remove all SDK imports. Host owns processes. |
| `protofd` | `sparkwing/pipeline.go:13` | Keep private pipe transport and inheritance protection. |
| `runspec` | `sparkwing/sparkwingtest/host.go:18` | Move into the host-owned test harness. |
| `sdkserve` | `sparkwing/pipeline.go:14`, `sparkwing/sdk_contract.go:10`, `sparkwing/protocol_operations.go:17`, `sparkwing/exec.go:21`, `sparkwing/git/git.go:32`, `sparkwing/docker/docker.go:29`, `sparkwing/docker/run.go:14`, `sparkwing/docker/tags.go:7`, `sparkwing/services/services.go:34`, `sparkwing/cleanup/cleanup.go:20`, `sparkwing/sparkwingtest/host.go:19` | Keep callback server and service relay only. Delete local execution fallbacks. |
| `secrets` | `sparkwing/secret.go:7`, `sparkwing/sparkwingtest/host.go:20` | Remove SDK imports. Host owns resolution and masking. |
| `sessionledger` | `sparkwing/exec_ledger_unix.go:13`, `sparkwing/exec_windows.go:24`, `sparkwing/cleanup/cleanup.go:21` | Move all ledger writes and cleanup to host. |
| `swtags` | `sparkwing/protocol_describe.go:12` | Keep tag-to-declaration and typed assignment code. It must not acquire secret policy. |

The closure matters too. `internal/nodeproto/arguments.go:14` pulls in argument
validation. `internal/sdkserve/routes.go:14` pulls in product limits.
`internal/gitcmd/repository.go:10` and `internal/gitcmd/command_unix.go:11` pull
in process environment and group code. `internal/procenv/build.go:11` pulls in
credential-name and secret rules. `internal/paths/paths.go:7` pulls in secure
filesystem helpers. The public test host imports `runspec`, which imports
capability and engine identity code at `internal/runspec/contracts.go:9`.
Removing a direct call while retaining these packages does not thin the compile
closure. Split codecs from policy first. Tighten the SDK and leaf-package
depguard lists at `.golangci.yml:75` and `:111` after the deletion.

## 2. Protocol and compatibility

### What negotiates today

The node protocol is exactly version 2. `internal/nodeproto/protocol_contract.go:17`
defines it. The six requests are hello, describe, plan, run_step, eval and cancel
at `:24`. `internal/sdkserve/serve.go:227` rejects another major and permits one
hello. TypeScript does the same at `sdk/typescript/src/runner.ts:130`.
There is no node-protocol range or floor negotiation.

Hello carries engine identity, granted runner identity, an optional process
binding and host features. The reply carries SDK language/version and SDK
features at `internal/nodeproto/protocol_contract.go:32` and `:88`.
Both SDK replies advertise an empty feature list in this snapshot.
The host sends `job_cores` at `:61`. Both SDKs omit cores when an old host does
not name it, at `sparkwing/protocol_operations.go:423` and
`sdk/typescript/src/runner.ts:146`. This is a small existing capability mechanism.

`internal/nodehost/sdk_code.go:121` checks returned major, language and version
against the sealed SDK pin. A replacement relaxes release-version equality.
`internal/nodehost/protocol_session.go:463` also validates hello before other
replies. `internal/nodeproto/protocol_decode.go:40` accepts additive envelope
fields. Plans remain strict. `docs/node-protocol.md:41` describes this split.
The `/node/v1/` service path is not node protocol major 1.
Documents and HTTP headers do not carry the node protocol version.
Decoder guards remain transport work. TypeScript rejects duplicate keys through
`sdk/typescript/src/json.ts:1`. Go checks typed wire shape and exact field spelling
at `internal/nodeproto/protocol_decode.go:40`. Keep those guards in the SDK.

`internal/engine/pin.go:66` reads Go module pins and `:125` reads npm pins.
`internal/source/acquire.go:111` resolves and ensures the selected engine.
`internal/engine/resolver.go:72` verifies its executable.
`internal/nodehost/sdk_planning.go:164` and
`internal/nodehost/native_host.go:75` reuse that identity for planning and work.
The signed release policy controls support windows and revocation at
`internal/engine/policy.go:37`. It is separate from wire-major support.

The local daemon has another protocol. `pkg/wingwire/wingwire.go:18` and `:44`
define its major and supported client floor. Those rules do not give nodeproto
an older-major adapter. Runner minimum versions and signed engine support
floors are also distinct from SDK capabilities.

### Proposed contract

Use node protocol 2 with the thinning capabilities required at hello. The new
host keeps no legacy node protocol routes or dual code paths for SDK releases
before the thinning release. Move retained helper calls to the generic
operation service. Remove the superseded service routes. Capability negotiation
applies only to SDK releases from the thinning baseline and later. The host
must learn their supported transport before using it.

| Direction | Proposed hello data | Use |
| --- | --- | --- |
| Host to SDK | Existing `protocol`, `features`, identity and binding. Add `capabilities` and `transport_limits`. | Advertise host service operations, accepted declarations and transport ceilings. Unknown additive fields remain ignorable. |
| SDK to host | Existing identity and features. Add supported callback/body forms, service envelope version, chunk support and transport ceilings. | Require the thinning baseline, then select optional capabilities from the intersection. Missing baseline capabilities fail hello. |
| Host session state | Selected capabilities, actual SDK identity and required API features from the plan. | Validate dispatch once. Persist enough to explain replay or refusal. Do not trust an SDK capability as authorization. |

Give capabilities semantic names and independent versions. Examples are
`host_operations/1`, `prepare_execution/1`, `selected_step_body/1` and
`raw_chunks/1`. Optional declaration support uses the existing host feature
direction. A required user API feature must fail with an actionable
unsupported-capability error when absent. Do not silently drop a requested
security or execution property. SDK releases from the thinning baseline and
later must declare every capability required by the baseline.

Use one generic service envelope for SDK-to-host calls. Proposed endpoint:
`POST /node/v1/operations`. Its body has a request ID, an operation name and an
opaque JSON parameters object. The response streams typed chunks and a terminal
result. Each chunk names stdout or stderr and carries base64 bytes and sequence.
The result carries structured errors, started state, exit status, signal and
termination reason. Request IDs also support an explicit cancel operation.
No controller credentials enter this envelope.

This requires incremental service transports in both SDKs. The Go client at
`internal/sdkserve/hostclient.go:128` buffers even HTTP chunked responses before
decoding. It cannot serve this streaming contract unchanged. Implement framed
response decoding, per-chunk allocation bounds, backpressure, cancellation and
terminal-result handling before moving command execution. TypeScript's buffered
service fetch must change too. Keep synchronous typed capture as an adapter over
the stream, preserving the caller's raw bytes.

SDKs ship typed wrappers for existing APIs once. They also ship a generic relay
for future operations. The host can add an operation or change its internal
policy without changing that relay. A new typed author API may justify an
optional SDK release. The host accepts envelopes from the thinning baseline and
later SDK releases with their documented defaults. Large results use chunks
rather than changing a pinned decoder's allocation cap.

`command.exec/1` sends overrides, not an SDK-assembled effective environment.
The host constructs the command environment from accepted job state and caller
overrides. It validates phase, process binding and current authority on each call.
To preserve inheritance after author code changes its environment, relay raw
overrides and deletions as request data. The host still filters and composes the
effective environment. Shared vectors must cover this case before the helper cut.

The host-session-only helper rule is pending maintainer ratification.
Under this rule, Exec, Docker, Git and services require a Sparkwing host session
in both languages.
A standalone tool importing the SDK gets a clear error before any side effect.
For example: "Exec requires a Sparkwing host session. Use os/exec in Go or
child_process in TypeScript for standalone execution, or use the sparkwingtest
host for tests." Each helper names itself in that error. Neither SDK has a local
fallback implementation. Tests cover each helper outside a host session and
confirm that it starts no process or service.

[GitHub Actions toolkit](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-commands)
commands for runner services run only inside Actions runners and offer no
standalone fallback for runner-provided commands.
[CircleCI orbs](https://circleci.com/docs/orbs/use/orb-concepts/) run their
commands inside CircleCI job runners and offer no standalone fallback for
runner-provided commands.

All operations run beside the job on its executing runner and share its workspace.
They must never execute on the controller's machine by accident.

The host cannot change a running callback process's environment remotely.
Add a host-to-SDK `prepare_execution` request, gated by `prepare_execution/1`.
It carries the host-normalized body environment for the process's bound job.
The SDK applies that snapshot once and acknowledges before bodies start.
It performs no name classification or policy filtering. Planning and ancestor
replay finish first. No active callback may overlap preparation, and the host
starts a fresh process for a different job environment. This preserves direct
author reads through `os.Getenv` and `process.env` without a per-callback policy
engine in the SDK. Startup variables still enter only at process launch.
The existing startup/cleanup split is at
`internal/nodehost/process_environment.go:80` and
`internal/nodehost/protocol_session.go:66`.

The host records command output once through its masking and persistence path.
It returns raw bytes privately to the author callback for capture and custom
writers. Masking those returned bytes would break tokens and parsers.
The SDK must not log the same returned stream a second time.
SDK-owned arbitrary subprocess execution is removed from supported helpers.
Arbitrary author code can still spawn a process directly, so the host must own
the outer process boundary too. An SDK guard is not an isolation boundary.

Host validation consumes raw declarations before policy normalization.
It validates generated amendments under actual placement too.
SDK releases from the thinning baseline and later relay raw declarations.
The host applies new policy before any SDK policy check could reject them.
That is why removing validation and known-tool catalogs is part of the thinning
release, rather than a capability flag alone.

SDK releases before the thinning release never run on the new host. Only the
existing engine resolver serves those pins on their historical engines. The new
host keeps no legacy routes. Keep that resolver path until the 12-month
automatic-run window for the last SDK release before the thinning release ends.
Existing security revocation rules still apply to individual releases.
After that window ends, retiring the path must return an actionable error:
"This SDK pin requires a retired engine. Run `sparkwing pipeline upgrade`."
A resolver-routing pass proves resolver behavior only. It is not evidence of
compatibility with the new host.

### Engine selection decision

Pins for the thinning baseline and later SDK releases run on the newest
compatible host through a signed SDK-to-host compatibility map. Pins for SDK
releases before the thinning release keep exact matching-engine routing through
the existing resolver only. This amends the matching-engine product goal.

SDK releases from the last 12 months continue to run automatically.
Existing security revocation rules stay in force.
Record both SDK and host identities on each accepted run.
Keep the SDK identity check at hello. The signed compatibility contract replaces
engine/SDK equality for pins from the thinning baseline and later SDK releases.
A development replacement cannot stand in for that contract. Host-only fixes
reach those pins through the map.
Pins for SDK releases before the thinning release retain their historical engine
behavior. The resolver path must stay until the 12-month automatic-run window
for the last such release ends. Retirement then requires the upgrade error above.

## 3. Compatibility matrix and gate

Use **N = 3 released SDK versions per language, including the latest release**.
Select only the thinning baseline and later SDK releases. Exclude QA and
prereleases. Select Go module releases and published npm releases separately.
They may not have the same available version set. Until three releases exist in
that range, test every available release in it. Retain one fixture for the
thinning baseline and the oldest supported wire/capability baseline in that
range after they leave the last three. Deduplicate anchors that name the same
artifact.

The principal matrix runs the **candidate host from today's source against SDK
artifacts from the thinning baseline and later releases**. It must not let the
engine resolver substitute a historical host and then report compatibility.
Existing Go conformance compiles current source at
`internal/nodeproto/conformance/conformance_test.go:45`. That is useful
cross-language coverage, but it does not prove pins for the thinning baseline
and later SDK releases.

| Cell | Artifact and execution | Required assertions | Tier |
| --- | --- | --- | --- |
| Candidate host x up to three Go SDK releases from the thinning baseline and later | Freeze a tiny pipeline source and exact go.mod/go.sum per release. Compile with the released SDK, no local replace. Run the resulting executable through candidate nodehost. | Real hello identity, describe, plan, args, step output, raw output and nonzero exit. | `gate` when host, SDK, protocol, environment, process, secret or pin code changes. Also `pre-release`. |
| Candidate host x up to three TypeScript SDK releases from the thinning baseline and later | Freeze source, package lock and npm integrity. Run each released package with its declared Node runtime. Use the published bridge on Windows. | The same semantic fixture and host-side outcomes as Go. | Same as Go. |
| Candidate host x thinning baseline and oldest supported later capability baseline | Keep immutable fixtures when recent versions no longer cover those branches. | Required baseline features, absent optional fields, explicit body dispatch, chunk limits and useful unsupported-capability errors. | `pre-release`. |
| Candidate host x lifecycle fixture for the thinning baseline and each supported later SDK baseline | Two dependent jobs, concurrent steps, retry, timeout, cancellation with a grandchild, generator replay, memo miss/hit, cache restore and secret kind drift. Use local fake executables for Docker/Git behavior and a local OIDC issuer fixture. | Assert host-owned subprocess policy, outer-process cleanup, classification agreement and chunk-boundary masking. No real provider is contacted. | Affected `gate` for up to three SDK releases per language from the thinning baseline and later. All anchors and the full fixture set in `pre-release`. |
| Candidate host x platform boundary | Linux, macOS, Windows x64 and Windows arm64 release jobs run their supported transport and process cases. | Pipe handles, process ownership, cancellation and platform result conventions. | `pre-release` and publication jobs. Linux focused cells run in `gate`. |

Use a manifest with SDK version, artifact checksum, fixture revision, compiler or
Node version, wire major, capability set and expected outcome. Keep a fixture
using the public API of each release. Do not rewrite old fixture source to use
the newest API. Test every available common operation. Test new API only in the
fixtures that declare it.

The direct matrix needs a test composition that installs candidate nodehost
beside the frozen pipeline from the thinning baseline or a later SDK release.
Pass the real released SDK pin as expected SDK identity and record the real
candidate host build separately. Do not forge an SDK version, use a local
module replacement, or disable hello validation. Inject the candidate host
through a test-only engine resolver and process entry. This composition does
not exercise production engine routing. Production engine selection is covered
only by the separate resolver check below.

Use isolated homes and local workspaces. Store fake secrets only in fixture
state. Use host processes and pipes, rather than transcript-only mocks, for
the principal cells. Replay vectors remain useful for malformed frames and
missing capabilities. They cannot prove process cleanup or environment behavior.

Cache immutable compiled fixtures by source digest, SDK artifact, toolchain and
platform. Cache TypeScript installs by lock integrity and runtime. Exclude the
candidate host from the fixture cache key, because it must be rebuilt and rerun
for each tested host. Keep a cold rebuild in `pre-release` to catch broken
artifact acquisition and compiler assumptions. An unavailable release artifact
is a visible acquisition failure, not a skip or a compatibility pass.

`DELIVERY.md:602` puts source policy in pre-commit and build/vet/fast lint in
pre-push. Their budgets are 3 seconds and one minute at `:634`. Neither hook
should acquire or compile released SDK fixtures. The broad gate and pre-release have
separate heavier execution classes at `:659`. Nightly and dispatch coverage is
described at `:619`.

Run the warm direct matrix in the affected **gate**, because a
host change can break pinned consumers before a release. Add explicit affected
path selection. SDK compatibility must run for host-only changes too.
Run all anchors, cold artifact preparation and platform cells in **pre-release**.
Update DELIVERY and pipeline selection to implement this tier membership.

No timing was measured in this audit. Measure warm execution and cold
preparation for each language/version before setting deadlines. Bound
concurrency during measurement. The cost is one candidate host build plus up to
six small fixture preparations and process runs, then any non-duplicate anchors
from the thinning baseline and later SDK releases. Reuse is material:
`DELIVERY.md:1229` records shared SDK fixture preparation as an existing way to
avoid repeated builds. Keep raw timings, fixture identities and per-cell
results as artifacts.

The three SDK releases from the thinning baseline and later catch recent
regressions. They do not prove 12-month support. Keep a separate resolver check
outside the candidate-host matrix. Use the real signed selection path. Pins for
SDK releases before the thinning release must select their own historical
engine. Pins for the thinning baseline and later SDK releases must select the
newest compatible host through the signed map. Cover the oldest pin within the
12-month support window in each language. Also cover revoked, missing and
incompatible artifacts, with no silent repin. A retired historical-engine path
must name `sparkwing pipeline upgrade` in its error. Verify that the resolver
path stays until the 12-month automatic-run window for the last SDK release
before the thinning release ends. Run this resolver check nightly, on dispatch
and in `pre-release`. It proves routing only. It never runs an SDK release
before the thinning release against the new host. Retain distinct older
capability fixtures from the thinning baseline and later SDK releases
regardless of N.

## 4. One-time thinning release

S means one narrow contract or deletion. M means a cross-language contract with
focused conformance work. L means process lifetime or public API migration.
Sizes describe implementation risk, not measured elapsed time.

Steps 1-2 start now. Steps 3-5 wait for the step 2 performance gate to pass.
They also wait for local job hosting to move into the runner process, as specified
in `local-controller-restart.md` step 3.
Host code then lands once in its final home.

| Order | Host work | Go SDK change | TypeScript SDK change | Size |
| --- | --- | --- | --- | --- |
| 1 | Freeze compatibility fixtures for the thinning baseline and later SDK releases and separate resolver fixtures. Agree on capability, process and API boundaries. Record Go/TypeScript differences as shared vectors. | No release yet. Preserve pinned source artifacts. | No release yet. Preserve npm packages and bridge artifacts. | M |
| 2 | Split nodeproto wire codecs from host policy. Extend hello and add the generic operation service. Require the thinning baseline and remove superseded routes. Benchmark Exec through the host operation service against direct execution in both languages. Record the stop-and-rethink threshold and results before step 3 starts. | Prepare the thin transport and error codecs. | Prepare the same transport and typed relay. Keep the native handle bridge. | M |
| 3 | Own command start, env, stdin, raw output, capture, measurement, cancellation and cleanup on the executing runner. Add explicit callback body selection and a one-time body-env preparation boundary after replay. | Replace exec implementation with relay. Remove platform process files, reporter, diagnostics, procenv and ledger calls after parity passes. | Replace spawn, env filtering, taskkill and stream collection with relay. Remove per-callback process.env rewriting. | L |
| 4 | Add host Docker, Git, service and cache-key operations. Own secret classification and child wait behavior. The maintainer ratifies every Go/TypeScript semantic difference before porting. Record each decision in shared vectors. | Keep helper APIs as thin relays. Remove local helper engines and cleanup fallback. Cut generic file IO/glob helpers once in the thinning release. Mark the cut Breaking and list standard-library replacements in the changelog. | Keep helper signatures as relay. Remove duplicate Docker/Git/service algorithms and policy checks. Apply the same Breaking file-helper cut and document standard-library replacements. | L |
| 5 | Validate all raw and generated declarations under current placement. Own policy defaults, bounds and known-tool vocabulary. Expose a real host test harness. | Remove full plan validation, CPU/tool catalogs, product limits and fake-host policy. Retain reflection and scalar codecs. | Remove declaration policy checks, secret checks and log/annotation policy. Retain API typing and callback registration. | M |
| 6 | Release host support and the signed compatibility map before accepting pins for the thinning baseline and later SDK releases. Verify the direct matrix for the thinning baseline and later SDK releases and runner ownership after the local controller move. Soak the candidate on Sparkwing's own pipelines before adding its signed map entry. | Publish one thinning 0.x release after host support is available. Upgrade pinned repos once and verify their pipelines. | Publish its thinning 0.x release in the same window, with verified native bridges. Upgrade pins once. | L |
| 7 | Gate each new host version's signed compatibility map entry on the matrix and a soak on Sparkwing's own pipelines. Remove the version with a single revert of its map entry. Verify signed host selection, identity, support and revocation. Demonstrate a host-only policy change against frozen thin artifacts. | Freeze the baseline. Later releases add optional author API or fix SDK transport/language defects. | Freeze the baseline on the same terms. | M |

### Step 2 performance gate

Measure Exec through the host operation service against direct `os/exec` in Go
and `child_process` in TypeScript. Use a minimal real `command.exec/1` service
path with child execution and result streaming. Use the same executable, argv,
environment, workspace and output handling on the same runner. Measure
end-to-end latency per command and throughput for many small commands in both
languages. Use warmed sessions, at least 10,000 commands per case and five
paired runs. Include serial execution and concurrency eight. Record p50 and p95
latency and commands per second. Keep raw samples, runtime versions, runner
details and benchmark scripts.

The performance guardrail thresholds are pending maintainer ratification.
Ratify them before using them to approve step 3.
Proposed bounds: added latency must stay at or below 1 ms at p50 and 5 ms at p95.
Throughput must stay at or above 80% of direct execution.
Apply each bound to each language and concurrency case. Use the median paired
latency differences and throughput ratios across the five runs. Record the
threshold before measuring. Record results before step 3 starts. If any bound
fails, stop and rethink the host operation path. Resolve the cause and repeat
the measurements before step 3. An in-SDK execution fallback is not a remedy.

### Release and rollback

A new host version enters the signed compatibility map only after it passes the
full matrix and soaks on Sparkwing's own pipelines. Run the candidate directly
for the soak before publishing its map entry. Record the host build, SDK fixture
identities, matrix results and soak runs. Include gate and pre-release pipelines
and repeated runs that exercise command capture, cancellation and services.
Steps 6-7 apply this rule to the first host and every later host version.

Keep each host version's map entry in its own change. Removal is a single
revert of that entry, followed by signed map publication. For later host
versions, resolver checks must show that pins for the thinning baseline and
later SDK releases select the previous compatible host without changing SDK
pins. Keep that host available during the soak and rollout.

Do not make thinning depend on aligning Go and npm release numbers.
Publish the compatibility mapping explicitly. Do not run a new SDK against a
host missing required operations and silently fall back to local execution.
The failure must name the missing capability and supported host remedy.

Keep existing caller choices while moving ownership. Volume support, readiness
budgets, command captures, output formats and child timeout behavior need
cross-language acceptance vectors. New limits and changed defaults need a
separate product decision. The maintainer ratifies every Go/TypeScript semantic
difference before porting. The port never decides which behavior wins.
Move the owner first. Change semantics separately.

The release is complete when the runtime SDK closure contains only declarations,
language bindings, callbacks and transport. Its checks must reject new imports
of process, environment, policy, secret storage, ledger and cache evaluators.
Keep the compile-closure guard in `sparkwing/tidy_closure_test.go:56` and extend
it to the agreed boundary. Add a host-only policy-change test using unchanged
released thin artifacts. That is the proof that later SDK upgrades are optional.
Update `docs/node-protocol.md`, its schema, conformance fixtures, SDK references
and their generated mirrors with each implemented contract change. This proposal
does not update those product references because it implements no behavior.

### Local controller interaction

Hosting is already assembled through `internal/nodehost/native_host.go:27` and
`:45`. Planning, execution and eval share that host. Local controller composition
is at `internal/wingd/localcontroller/controller.go:27`.
The local controller project moves job hosting from the daemon into a runner.
Thin-SDK operations belong to that runner's nodehost, beside the callback process.
The controller retains scheduling, durable state and authority issuance.

Do not add a daemon command executor, a second local SDK server, a new store, or
another process credential model. Extend the existing process service boundary
at `internal/nodehost/process_service_reads.go:116` and its authority checks.
Pass operation dependencies through nodehost composition. The runner owns
process handles, workspace paths, service lifetimes and measurement.
Route cancellation through the same job lifetime on local and cloud runners.

Prepare wire types, shared vectors, fixtures for the thinning baseline and
later SDK releases and resolver fixtures in steps 1-2. Set the operation
service and callback-environment contracts before changing shared nodehost
composition. After the step 2 performance gate passes and local job hosting
moves into the runner process, implement steps 3-5 there. This avoids building
temporary daemon hosting.

## 5. Decisions (2026-11-05)

This log is append-only. Later entries name the earlier entries they supersede.

1. **Host selection.** From the thinning release on, a thinned SDK pin runs on
   the newest compatible host through a signed SDK-to-host compatibility map.
   Pins from before thinning keep exact matching-engine routing. The 12-month
   automatic-run promise and revocation rules stay. Record both SDK and host
   identities on each accepted run. This amends the matching-engine product goal.
2. **Helper API.** Keep Exec, Docker, Git, services, refs and secret APIs as thin
   relays to host operations. Keep pure path and value helpers. Cut generic file
   IO/glob helpers once in the thinning release. Mark this Breaking and give
   standard-library replacements in the changelog. The maintainer ratifies every
   Go/TypeScript semantic difference before porting. The port never decides
   which behavior wins.
3. **Compatibility bar.** Run the candidate host x last 3 released SDKs per
   language in the affected `gate`. Run all anchors, cold preparation and
   platform cells in `pre-release`. Run separate 12-month routing checks nightly
   and in `pre-release`. Measure costs before setting deadlines. Keep raw
   timings and per-cell results as artifacts.
4. **Performance gate.** Step 2 compares host-service Exec with direct execution
   in both languages. Record latency, small-command throughput and the threshold
   before step 3. The proposed bounds are 1 ms added p50, 5 ms added p95 and at
   least 80% of direct throughput. Stop and rethink if any bound fails.
5. **Host promotion.** Add a host version to the signed compatibility map only
   after the matrix and a soak on Sparkwing's own pipelines pass. Remove it with
   a single revert of its map entry and publish the signed map.
6. **Host route cut.** Supersedes entry 1 on host compatibility and retirement.
   The new host supports the thinning baseline and later SDK releases only.
   It keeps no legacy routes or dual paths. Only the existing resolver serves
   pins for SDK releases before the thinning release on their historical engines.
   That resolver path must stay until the 12-month automatic-run window for
   the last such release ends. Existing security revocation rules still apply.
   Retirement then returns an error naming `sparkwing pipeline upgrade`.
   Entry 1's signed map and SDK and host identity recording still apply.
7. **Host-session-only helpers.** Supersedes entry 2 on standalone helper use.
   This rule is pending maintainer ratification. Exec, Docker, Git and services
   require a Sparkwing host session. Outside one, they fail before side effects.
   Errors name `os/exec`, `child_process` or the `sparkwingtest` host as remedies.
   Neither SDK has an execution fallback. Entry 2's other API choices still apply.
8. **Compatibility release range.** Supersedes entry 3's release selection.
   Test up to three released SDKs per language from the thinning baseline and
   later. Test all available releases in that range until three exist.
   Keep the thinning baseline and oldest supported later capability anchors.
   Pins for SDK releases before the thinning release use separate resolver checks.
   Entry 3's tiers, cost measurement and evidence requirements still apply.
9. **Performance guardrails.** Supersedes entry 4 on threshold approval.
   The thresholds are pending maintainer ratification. The proposed bounds stay
   at 1 ms added p50, 5 ms added p95 and at least 80% of direct throughput.
   Apply each bound to each language and concurrency case. Entry 4's comparison
   and stop-and-rethink rule still apply once the thresholds are ratified.

## Audit evidence

The audit used source inspection and static document checks.
Runtime claims describe the source snapshot named above.
Compatibility coverage describes required work, not measured results.
