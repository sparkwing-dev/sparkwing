# W4 review fixes

Snapshot of the fixes against the W4 review of `1c5c6619840717538d4399aa3e2bf7751bf6a284`.
The SDK fixes are verified. Integration remains blocked on the host and CLI work below.
No commits or Sparkwing CLI invocations were made.

## Findings

| Finding | Result |
| --- | --- |
| 1, blocker: retained tests and pod fixture use deleted APIs | Pod fixture ported and protected by a build regression. Node-host test port remains W3-owned and fails compilation. |
| 2, major DESIGN: production node-host router still speaks v1 | W3 handoff below. SDK route transport and fake-router tests do not prove production-router compatibility. |
| 3, major DESIGN: remote hello identity is missing | W3/W8/W9 handoff below. No ambient identity defaults added. |
| 4, major DESIGN: failed evals become successful decisions | W3 handoff below. `ValidateValue` remains a shape validator; it does not consume `EvalResult.Error`. |
| 5, major DESIGN: expansions lose original bytes and validation context | W3 handoff below. Shared strict validation remains available. |
| 6, major DESIGN: skipped steps strand downstream predicates | Fixed in the Go SDK with explicit host settlement, shared by predicates and bodies. W3 must send the snapshot and W5 must implement the same contract. |
| 7, major DESIGN: project aliases fail in real callers | W3/W9 handoff below. Removed the SDK comment claiming the missing mapping exists. Renamed the SDK acceptance case to describe its actual resolved-name boundary. |
| 8, major DESIGN: required pipeline args erase job metadata | Fixed through separate discovery input handling, complete metadata or an attributed canonical failure, and independent execution validation. |
| 9, major DESIGN: automatic startup names and cleanup env | W3 handoff below. The SDK declaration test now separates an arbitrary explicit startup variable from an unmarked interpreter variable. It makes no claim about production classification. |
| 10, major: published docs teach deleted APIs | W9 must replace the published protocol/SDK docs and regenerate references and mirrors. W4 source contracts and its wire snapshot were updated here. |
| 11, minor: stale comments | Corrected RunContext alias, NoMemo and DryRun comments. Removed false snapshot-preflight and RetryAuto comments in orchestrator, plus stale registration/lookup API comments. |

## Structural changes and tests

The settlement invariant is: a step predicate or body receives the current attempt's terminal predecessor state before it can read an output.

`Request.Settled` carries terminal step IDs. Both predicates and bodies enter the same state transition. It validates the complete prerequisite ancestry before mutation, clears outputs at the attempt boundary, and settles missing local outputs to zero. Active step identities replace a scalar active count, so a snapshot cannot finish running work or overlap the same step. Terminal state is monotonic: completed or accepted-terminal steps cannot run or evaluate again at the same attempt. Unknown, repeated, current, active, stale or incomplete state fails before changing outputs. Bodies no longer infer completion from their own dependency list. The fake host records actual completed bodies and true/failed step predicates per plan and job.

`TestProtocolPredicateObservesSkippedPredecessor` ran red before the fix: the initial attempt waited until cancellation, while a retry read `42` from the previous attempt. Both cases are green. Further red/green regressions cover active-step settlement, omitted transitive predecessors, and failed fake-host predicates. A generated-ordinal property checks that later attempts never reuse earlier output. Invalid snapshots must preserve the previous attempt's usable output. The real process retry transcript now evaluates a predicate after successful predecessors and after a skipped predecessor in a later attempt.

**DESIGN: per-attempt settlement authority.** An independent final check found that accepted-terminal steps could execute again at the same ordinal, repeating side effects while retaining their previous local output. The invariant is that terminal state cannot become executable again within an attempt. `TestProtocolTerminalStepCannotExecuteAgain` ran red for both completed and accepted-skipped steps, then passed with a terminal-state fence in the common transition. `TestProtocolFailedBodyAlwaysSettlesBeforeReply` also ran red for panic and dry-run refusal outcomes. One body finalizer now settles every terminal return before releasing active state, so the fence covers all outcomes. A higher non-overlapping attempt remains executable. This replaces rollback-prone invocation authority with monotonic terminal state.

The discovery invariant is: missing execution inputs cannot silently produce an incomplete describe schema.

The registration has a private discovery path that applies pipeline defaults and initializes extra-argument bags without execution-only required checks or job binding. Ordinary and recovery argument collision checks still run. Describe reports provider, factory and declaration failures and panics as secret-masked, pipeline-attributed `plan_failed`, with no successful partial document. Ordinary invocation still validates pipeline/job required inputs, enums, types and unknown arguments. The discarded-error helper and context marker are removed.

`TestProtocolDescribeRequiredInputsPreserveJobMetadata` ran red with only pipeline flags instead of ordinary/recovery flags and step risks. `TestProtocolDescribeReportsDiscoveryFailures` ran red for returned errors, error/string panics and argument collisions. The extra-bag initialization regression also ran red before its fix. All pass. `FuzzProtocolDescribeMasksSecretDefaults` ran red before failure redaction and then passed more than 37,000 generated cases across direct, wrapped, panic and provider failures, including canonical hints/details. The intentional failing-plan process fixture now requires `fail=true`, so generic Describe does not depend on swallowed failures.

`TestPodNodeFixtureBuilds` ran red on deleted SDK symbols, then green after porting to Memo, Artifacts, Env, Generate, typed references and NeedsGenerated. The fixture's auto retry input and branch were removed. Ordinary retry hooks remain. No compatibility aliases were added.

Removed 216 old lines from tracked files, including 22 lines of context-marker plumbing, the 10-line swallowed-error discovery helper, body-only completion inference, obsolete fixture APIs and stale comments. Replacement code and tests are in the same diff.

## Contract changes needed

W4's C3 protocol and Go SDK notes document the settlement amendment, and the generated v2 wire snapshot contains `settled`.

- W3 must send `settled: []` even for a first root body. Step-condition evals carry both positive `attempt` and `settled`. The snapshot includes terminal skipped/failed steps and all transitive prerequisites, excludes current/running steps, and comes from the scheduler. Job-level evals carry neither field. The SDK refuses missing state; there is no compatibility fallback.
- W5 must consume the same explicit state before TypeScript predicates and bodies and update its shared wire transcript requests.
- W9 must regenerate published wire/SDK schemas and docs after the coordinated cutover. Public docs remain an integration blocker.

No W1/W2/W3/W5-owned contract files were changed.

## Cross-area handoffs

**DESIGN: W3 production service router (finding 2).** Invariant: the SDK and negotiated production service router implement one route/error contract. Replace the header/426 gate, `/nodes` routes, legacy errors and missing service handlers together. Test SDK calls against the actual registered node-host router for secrets, output, mask, annotation, summary, OIDC and children. Removing the version gate alone is insufficient.

**DESIGN: W3/W8/W9 execution identity construction (finding 3).** Invariant: every launched process receives the verified selected engine and server-granted runner identity. Carry both through one execution-input constructor used by worker, claimed execution and pod paths. Test real constructors through hello; forged ambient values must never select identity. Amendment A1-19 already assigns this work.

**DESIGN: W3 eval consumption (finding 4).** Invariant: a closure failure cannot become a successful value. Centralize host eval decoding so it checks the canonical error before exposing a typed value. Reproduce throwing step guards, generators and memo keys returning `{value:null,error:...}`; assert failed work, no body execution, no accepted expansion and no NoMemo success. Shape-only tests do not prove this invariant.

**DESIGN: W3 expansion acceptance (finding 5).** Invariant: original generated bytes cross strict expansion validation before any typed reconstruction or persistence. Carry raw job JSON to `ValidatePlan` with source, existing topology, secrets and plan concurrency. Test unknown/duplicate fields, null collections, 256/257 fan-out, cross-boundary cycles and mismatched concurrency through the real scheduler acceptance path. Re-marshalling permissively decoded structs cannot preserve evidence.

**DESIGN: W3/W5 terminal-state delivery (finding 6).** Invariant: terminal state and attempt boundaries reach SDK predicates and bodies before invocation. Consume the explicit settlement amendment above. Production `internal/nodehost/steps.go` still omits the new state on both body and predicate requests. The verified Go process tests use the new contract; production execution remains blocked until host and TypeScript parity land.

**DESIGN: W3/W9 pipeline identity resolution (finding 7).** Invariant: invoked project name and registered SDK entrypoint remain distinct through discovery, parsing and execution. Resolve the configured pair once and carry it through CLI argument parsing and host planning. Test project YAML alias `deploy-prod` targeting `Deploy`, with required pipeline/job flags and the invoked alias visible to author code. The SDK-only test already receives a resolved pair and cannot prove this mapping.

**DESIGN: W3 environment projection (finding 9).** Invariant: automatic startup classification and complete cleanup environment come from one procenv projection. Use `SplitJobEnv`/the shared builder before process creation and supply cleanup with the complete attempt environment. Test unmarked GOMAXPROCS/NODE_OPTIONS at interpreter startup, unchanged declarations on replan, and body/cleanup using the same DOCKER_HOST. An explicitly marked startup variable cannot establish automatic classification.

W3 also owns finding 1's retained test port. `go test -p 4 -gcflags=-e ./internal/nodehost -run '^TestHostedRunEmitsTheDispatchersEvents$' -count=1` fails compilation. `events_test.go` still references PlanDocOld, PlanModifiers, PlanNode, PlanConsume, PlanApproval, PlanClosures and SkippedConcurrent. Its auto retry branches and node_auto_retry expectations describe removed behavior. Other node-host test files fail on old APIs too. The two orchestrator RetryAuto tests still pass removed `auto=true` to the fixture and need porting to retained retry semantics.

## Verification

All Go commands used `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`, and private GOCACHE/GOTMPDIR under `/tmp/sw-astra-W4`.

| Check | Result |
| --- | --- |
| `go build -p 4 ./...` | Pass. |
| `go vet -p 4 ./internal/nodeproto/... ./internal/protofd ./internal/sdkserve ./sparkwing/...` | Pass. |
| Tests for nodeproto/conformance, protofd, sdkserve, main SDK, cleanup, docker, inputs and sparkwingtest | Pass. |
| Focused SDK, fake-host and sdkserve race tests | Pass. |
| Native Linux Go protocol hello/handles, hard cut, additive fields, envelopes, SDK fake-host/linkage cells | Pass, including real process retry predicates. No new placement cells unskipped. |
| Secret-default discovery fuzzing | Pass after red reproductions. |
| Independent focused checks | Discovery has no blocker/major finding. Settlement lifecycle findings reproduced and fixed, then rechecked without a remaining hole in that boundary. |
| `git diff --check` | Pass. |
| `go run ./internal/commentcheck -base main .` | Fails on two pre-existing W1 comments: `internal/store/state_materialize.go:15` and `internal/store/state_result_records.go:9`. No W4 violations. |
| Broader SDK git/services tests | Blocked: empty sandbox `/tmp/.git` changes no-repository fixture discovery; service tests cannot bind sockets. |
| Node-host retained tests | Cannot compile on removed APIs, as detailed above. |

The lead owns full placement pipelines, native Windows/Darwin, production router checks and integration gates. They were not represented as passing here.

## Shared files and assumptions

Shared production files contain only the two comment removals in `internal/orchestrator/hosted_run.go` and `pod_node_host.go`. The retained pod fixture was ported in `internal/orchestrator/testdata/podnodefixture/main.go`.

Shared acceptance edits are `internal/acceptance/protocol_go.go`, `tests/acceptance/protocol_test.go`, `tests/acceptance/protocol_{additive,arguments,closure,envelope,plan_error,process,recovery_chain,replay_env,retry}_test.go` and the `protocolgo/plan_error.go` and `retry.go` fixtures. They send explicit state, name the alias boundary accurately or explicitly trigger intentional declaration errors. C3 notes and the conformance wire snapshot record the changed W4 contract.

Mechanisms added: explicit protocol settlement replaces inference because invocation alone cannot distinguish skipped from running work. Private discovery input handling replaces a context marker because execution validation cannot serve argument-free schema discovery. Undo requires reverting each mechanism with its consumers; no new public SDK knobs, environment variables, services, packages or compatibility modes were added.

Open integration work is assigned above. Friction was sandbox-only: Xwing context inspection wanted an unwritable index lock, socket tests cannot run, and empty sandbox git mounts affect fixture ancestry. Build caches are removed after checks. Git metadata was not written.
