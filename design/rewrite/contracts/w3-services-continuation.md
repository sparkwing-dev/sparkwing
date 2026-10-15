# W3 services and process environments

<!-- design:begin -->

W3 remains incomplete. This continuation adds the complete shared process-service
registrar, carries registered pipeline names separately from invoked aliases, and
passes complete attempt environments to native cleanup. Native entry-point
migration and the legacy runtime deletion remain open.

## Implemented

`RegisterProcessServices` registers all eleven node-host routes through the C2
Endpoint contract. The handlers cover secrets, job output, latest references,
OIDC, children, masks, annotations, summaries and cleanup registration/release.
Reads and writes receive `capability.Authorized`. Their adapters must recheck
live authority and read edges in the transaction or storage operation.

The registrar uses `/jobs/{job}/output` and `job` query selectors. It has no
protocol header gate. Errors return through Endpoint.Handle for C2's canonical
writer. Missing adapters fail as unavailable. Missing output fails instead of
becoming JSON null. Recorded explicit null remains readable. Secrets and minted
OIDC tokens register with the shared mask registry before delivery. A conflicting
identifier prevents delivery. Secret-backend diagnostics cannot expose its raw
failure message. Annotation omission and empty level resolve to info.

Request evidence retains source-job, pipeline, child and cleanup selectors and
the exact POST-body digest. Existing child, OIDC and cleanup body limits remain.
Duration parsing checks overflow before conversion, including child wait bounds.
The eleven registrations are tested through their actual handlers and a ServeMux.
The policy/authentication adapter and error writer are fakes because C2's concrete
PolicyRouter and WriteHTTP still panic in this checkout.

`ProcessCleanups` owns one process's registrations, keeps the existing limits,
clones command arguments, supports release, closes registration on Run and runs
unreleased commands once in reverse order. It uses the existing native process
tree executor. Failed commands return a safe error and do not suppress later
cleanup commands. This remains loud even when no log writer is supplied.

`PlanningProcessInput.Entrypoint` selects the registered SDK pipeline for describe
and plan. Checkout planning resolves it from the matching project entry and
persists it in Resolved.Entrypoint and the plan-cache identity. RunContext.Pipeline
retains the invoked alias. Executor and owner-evaluator replans use the accepted
entrypoint without rediscovering YAML. Tests cover deploy-prod targeting Deploy
with required pipeline and job argument keys. CLI discovery and flag parsing
remain W9's integration.

Native attempt/eval startup and cleanup share one persisted-job read and one
SplitJobEnv projection. Startup receives only startup assignments. Cleanup gets
the complete attempt environment through procenv.Cleanup, including body
overrides. ProtocolSessionConfig.RunCleanups now receives CleanupScope. All its
callers are migrated. No production caller outside W3 required a change.

Native tests start a real child with unmarked GOMAXPROCS and NODE_OPTIONS, verify
that body assignments are absent at startup, revoke and join it, and run an actual
cleanup shell with the declared DOCKER_HOST. Startup-driven declaration changes
still fail as plan_drift before body execution. These are native host-process
proofs with a protocol fixture, not real Go/TypeScript SDK or placement proof.

## Invariants and handoffs

The environment invariant is that only persisted, procenv-validated assignments
change SDK startup or cleanup, and no filtered base credential reaches either.
FuzzSDKCleanupEnvironment generates names, values and startup flags. Invalid UTF-8
is normalized before JSON encoding because persisted JSON cannot retain those
bytes. Its initial failure was an oracle comparing an invalid original name with
the JSON-normalized declaration. The reproducer is retained as an explicit seed.

The service invariant is that a mismatched run/job binding reaches no backend or
service mutation. FuzzProcessServiceBinding generates both bindings and checks
every registered route. Successful secret delivery also checks mask registration.

The eval invariant is that a closure failure cannot become a successful value.
The shared sdkCode.eval already checks the canonical error before Value. Added
tests reproduce a throwing step predicate with value:null and error, assert no
body executes and a failed job is reported, and refuse a failed-null memo key.
Existing generator-failure tests cover the same representation and no accepted
success body. No second eval decoder was introduced.

| Handoff | Result and remaining work |
| --- | --- |
| Finding 1, retained events | The cited test compiles in this branch. Execution fails on missing sparkwing_meta, then sandbox-denied TCP listen. The old event assertions still need their final-store port. The orchestrator manual-retry test already replaced its RetryAuto predecessor. |
| Finding 2, production services | All eleven target handlers exist behind C2's registrar and are exercised. The old Server, /nodes, header/426 gate and legacy error writer still belong to the native runtime cut. Do not remove only its header check. Actual SDK clients and C2 authentication remain integration points. |
| Finding 3, A1-19 identity | Shared hosts retain the supplied verified engine and granted runner. One native constructor across worker/claimed/pod paths and W8's selected-engine hosting remain open. |
| Finding 4, eval | Shared decoder and throwing predicate, generator and memo behavior are covered. Legacy closure paths retire with the native cut. |
| Finding 5, expansion | The shared evaluator retains raw generated jobs through validation and the owner passes the same body to AcceptExpansion. W4's strict validator is still a stub. Plan-concurrency context is missing from its declaration. Full strict scheduler-path acceptance remains open. |
| Finding 6, settlement | Blocked on W4's missing terminal-state/attempt-boundary protocol fields. No host-only wire shape was added. |
| Finding 7, pipeline identity | Shared checkout planning, describe, plan, cache and replan carry distinct invoked and registered names. W9 must connect discovery and flag parsing to that selection. |
| Finding 9, A1-48 environment | Shared native attempt/eval startup and complete cleanup projection are implemented and exercised. Real SDK body application and native placement constructors remain open. |

Amendment rows A1-01/02/05 use the completed registrar and existing mask gate.
A1-06 cancellation, A1-13/15 reconciliation, A1-16/17 state projections,
A1-18 ordinals, A1-36 bound storage and A1-44 classifications retain the earlier
implementations and their focused tests. A1-10/11/12, A1-14, A1-19,
A1-21 through A1-25, A1-38/39, A1-45 through A1-48, A1-50/51 and A1-53/54
retain the native and cross-area integration points in the previous handoffs.
This continuation adds no waiver for those points.

## Contract changes needed in other areas

- W4 must supply the referenced settlement amendment. nodeproto.Request has no
  terminal-state or step-settlement field to populate for bodies and predicates.
- W4 must add accepted plan concurrency to expansion ValidationContext, or name
  its agreed equivalent. The declared context contains placement, secrets,
  expansion, existing jobs and source job only. W3 cannot supply a missing field.
- C2 must implement PolicyRouter, canonical WriteHTTP and live process delegation.
  Node-host child admission must derive parent authority from the authenticated
  process, since the SDK's ChildRequest does not name the bound parent run.
- W4/W5 must migrate real service clients to the target routes, job selectors and
  canonical error object. Their current native client paths still use /nodes.
- W9 must connect alias discovery/argument parsing and full restricted execution
  reads. The result/failure and materialized-result wire gaps remain.
- C1/W6 must retain optional-secret declarations through sealing. C1's owner
  policy/approval edges and generation-zero eval-log gap remain as listed before.
- W6's default procenv.Git path and W8's InstallSDK/selected-engine host invocation
  still need their implementations. Source tests use the existing contract seam.

## Acceptance, ports and deletion

environment-origin-head-and-guards/hermetic-checkout-planning now includes the
alias case, bringing that helper to seventeen cases. The existing
protocol-sdk-secrets-mask-annotations/hermetic-process-services and
credentials-secret-identifiers/hermetic-process-services helpers now invoke all
eleven registered service handlers. Their authentication and persistence are
fakes. Native target children remain skipped.

Retained cleanup ordering/failure/environment assertions and automatic startup
classification are exercised through the new process owner. No additional parked
inventory row is claimed complete. The retained event port remains open.

Deleted legacy production/test files: zero. Deleted legacy lines: zero. The
three native host entry points, old scheduler, session, layers, routes and types
still need the coordinated cut. This continuation replaces the argument-less
cleanup callback contract and its callers, with no forwarding alias.

Shared files touched are internal/acceptance/host_checkout.go,
internal/acceptance/host_services.go, c3-protocol-sdk.md, t1-port-inventory.md and
this report. All other changed files belong to W3. No external production caller
was changed. No Sparkwing binary, Git write, pipeline, deployment or release ran.

## Assumptions

The added service callbacks and per-process cleanup owner complete the frozen
host boundary. Reusing the old Server's private grants would preserve a second
authorization model, so it was not used as a target adapter. C2 supplies live
authentication and each backend rechecks its issued authority. These W3-local
contract additions can be removed with their callers without a data migration.
No customer limit, configuration key, environment variable or service was added.

## Verification

Commands use GOWORK=off, GOFLAGS=-buildvcs=false and GOMAXPROCS=4. GOCACHE,
GOTMPDIR and TMPDIR are under /tmp/sw-astra-W3/. Tests use at most -p 4.

Whole-repository build, scoped vet, the focused current-host suite and complete
source, lease and acceptance suites pass. Both new fuzzers pass 3,000 generated
cases with -parallel=2. Race checks pass for the changed host tests and fuzz seed
sets. Sleepcheck against HEAD and git diff --check pass. Native host-process tests
pass. Commentcheck against main
reports one inherited HereBearer comment in internal/controllerwire/contracts.go
and no W3 finding. The old dispatcher-event test fails for the schema/socket
reasons above. It is not counted as passing coverage.

The focused host selection is the previous handoff's selection plus
TestOwnerEvaluation, TestNativeSDK, TestProcessCleanups,
TestNodeExecutorThrowingStepPredicateCannotRunBody, the pipeline identity tests
and both new fuzz seeds. Reproduce the new checks with these Go arguments under
the environment above:

```text
build -p 4 ./...
vet -p 4 ./internal/nodehost ./internal/source ./internal/lease ./internal/orchestrator ./internal/acceptance ./tests/acceptance
test -p 4 ./internal/source ./internal/lease ./internal/acceptance ./tests/acceptance -count=1
test -race -p 4 ./internal/nodehost -run '^(TestSDKCheckoutPlanningRetainsInvokedAlias|TestNodeExecutorReplan|TestNodeExecutorThrowing|TestOwnerPolicyMemoKey|TestNativeSDK|TestProcessServices|TestProcessCleanups|FuzzSDKCleanupEnvironment|FuzzProcessServiceBinding)' -count=1
test -p 4 ./internal/nodehost -run '^$' -fuzz '^FuzzSDKCleanupEnvironment$' -fuzztime=3000x -parallel=2
test -p 4 ./internal/nodehost -run '^$' -fuzz '^FuzzProcessServiceBinding$' -fuzztime=3000x -parallel=2
run ./internal/commentcheck -base main .
run ./internal/sleepcheck -base HEAD .
```

Native HTTP/api.sock, real process authorization, real SDK clients, native
runner/launcher, Windows/Darwin, PostgreSQL/S3 and real-repository QA remain open.
The lead retains Git writes, independent review, pipelines, landing and native
acceptance. Scratch is removed before the final worker report.

<!-- design:end -->
