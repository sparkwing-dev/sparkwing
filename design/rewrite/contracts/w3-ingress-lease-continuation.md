# W3 runtime ingress and lease continuation

<!-- design:begin -->

W3 remains incomplete. This continuation closes gaps in the remaining runtime
without adding another transport, validator or environment policy. The
[native assembly handoff](w3-router-native-continuation.md) still describes the
common host and its open production integrations.

## Implemented

Invariant: generated SDK bytes pass strict expansion validation before becoming
job declarations or producing a row. The retained executor now carries raw eval
arrays to the scheduler. The scheduler validates all generators of a source
together with the existing topology, shared secret matcher and plan concurrency.
Unknown fields, duplicate fields, null collections, cross-batch cycles and the
256/257 fan-out boundary reach `nodeproto.ValidatePlan`. The duplicate per-group
graph check is removed because it rejected valid forward dependencies,
cross-group recovery and the validator's implicit source edges. SDK generator
fixtures now pass through eval consumption and the actual scheduler ingress.

Invariant: code reconstruction preserves every accepted member and declaration.
Replay validates the original arrays, checks complete persisted batch membership
and compares every returned hash. An unchanged leaf cannot hide a changed sibling
or ancestor. Reconstruction writes no topology. Its fuzz test changes IDs,
declarations, membership and persisted batch completeness.

Invariant: SDK startup and cleanup expose no inherited credential, and cleanup
receives the complete attempt environment. Native and retained execution share
one job-environment projection. Both use `SplitJobEnv` and `Build`. Unmarked
NODE_OPTIONS and GOMAXPROCS reach interpreter startup. Body assignments reach
cleanup, including a declared DOCKER_HOST override. Projection errors return
before route admission. Plan startup also uses procenv. Granted runner values
replace ambient values. The existing environment fuzz test checks both paths.
The nested-run dry-run consumer now parses the builder's boolean spelling.

The claimed in-process executor uses `lease.Keeper` instead of its separate
heartbeat goroutine. The renewal adapter retains the HTTP claim fence and
distinguishes explicit cancellation, lost authority and silent expiry. Stop joins
renewal. Three retained heartbeat tests use signals and synctest.

The debug tests no longer call Fatal inside the host goroutine before sending
their result. Their event and result waits observe the host outcome and a
deadline. The complete nodehost suite now reaches its schema and socket failures
in about four seconds instead of hanging in the debug test.

## Deletion and ports

`internal/orchestrator/dispatched_heartbeat.go` is deleted, including its
72-line implementation and `errClaimAbandoned`. Eleven caller lines implementing
another heartbeat lifetime are removed. Manual startup classification, runner-env
stripping and dry-run composition are removed from nodehost. The permissive
generator decoder and the per-group source-edge guard are removed.

The production diff removes **184 lines** and adds **245**, a net addition of
**61 lines**. This includes the raw-byte and replay checks. The remaining legacy
scheduler, session, layers and production entry points remain.

Ports cover the three claim-heartbeat assertions, the generator fixtures' real
scheduler ingress, startup/body/dry-run assertions and the failing debug wait.
No acceptance cell is added or marked complete. The old store-backed scheduler,
admission, debug and event test ports remain open.

## Required foreign contracts and remaining W3 work

- W9/W2 must supply full execution snapshots and typed fenced step reporting.
  `Controller.ReadSnapshot` returns scheduling data without the accepted run
  seals, owner authority and complete execution rows needed by the hosts.
- W2 must complete the declared authentication, authority and delegation
  validators. `NewAuthenticator`, `ValidateAuthority` and `ValidateDelegation`
  remain panic stubs. Native process-authority tests use the declared fakes.
- W1 must complete the owner-policy edges already requested in
  [the amendment handoff](w3-amendment-handoff.md). The table has no owner
  pending-to-failed policy edge or approval-SkipIf success edge. Typed debug
  effects also need their store contract. W3's production `OwnerHost.Layer`
  composition remains open against those contracts.
- W6's `ResolveConcurrency` remains a panic stub. `procenv.Build` still rejects
  Git as an unknown kind. Source tests use its declared environment seam.
- W4 still owns the shared validator's mismatched-concurrency refusal. W3 now
  supplies `ExistingConcurrency` on the retained expansion path too. It adds no
  second concurrency policy.
- W8/W9 still need matched-engine host invocation and placement callers. The
  legacy run, trigger and pod callers do not have the native snapshots and
  parent authority. Replacing them with fabricated adapters would not execute
  the declared contracts.

The existing cross-process mask-namespace DESIGN, default child-arguments
handoff, attempt-fence request, storage record references and native placement
matrix remain open as listed in the earlier handoffs. No foreign contract is
changed here. No new credential, environment variable, customer limit or
compatibility route is added.

## Verification

Checks use GOWORK=off, GOFLAGS=-buildvcs=false, caches under
`/tmp/sw-astra-W3/` and at most four Go packages per test command.

| Check | Result |
| --- | --- |
| Whole-repository build | Passed. |
| Vet on nodehost, orchestrator, source, lease and both acceptance packages | Passed. |
| Focused host, raw-expansion, real SDK fixture, environment and lease tests | Passed. |
| Focused nodehost race selection | Passed in 14.85 seconds. |
| Claim-lease, dry-run and concurrency-codec race selection | Passed in 1.28 seconds. |
| Existing environment, lifecycle, recovery, protocol and credential acceptance selection | Passed in 44.6 seconds. Native matrix children remain skipped. |
| Environment fuzz through both host paths | Passed, 1,043 executions. |
| Replay-integrity fuzz | Passed, 1,011 executions with the incomplete-batch seed. |
| Commentcheck against main and sleepcheck against HEAD | Clean. |
| Full nodehost suite | Failed on removed `sparkwing_meta` and forbidden listening sockets. It terminated in 4.35 seconds after the debug wait fix. |

The first combined race selector also selected the unrelated legacy
`TestClaimedTriggerFailure_RecordsTheDispatchError`. It failed on the removed
`triggers.pipeline` column. The final scoped selectors above pass.

All code changes are inside W3's nodehost and orchestrator hosting files. The
small dry-run caller migration is `internal/orchestrator/dry_run.go`, with its
tests. Shared documents are this handoff, `handoffs.md`, `c3-protocol-sdk.md` and
`t1-port-inventory.md`. No Git writes or pipeline runs occur. Bootstrap discovery
included the read-only installed `sparkwing info --for-agent` call.

The worker's scratch directory is removed after all checks join. The lead retains
committing, the pipeline gates and native placement verification.

## Concurrency and self-claim continuation

Invariant: an expansion cannot change an existing concurrency group's capacity,
scope or on-limit policy. The shared validator now keeps one group definition
while validating existing plan memberships, existing jobs and the complete raw
expansion. It compares omitted fields using the declared global/1/queue defaults.
Membership cost remains independent. Refusals use `plan_invalid` and name the
conflicting field path without echoing the group. The scheduler tests exercise
both plan-level and job-level definitions before any row is written. The W4
membership test now expects the lead's settled refusal rule.

Invariant: self-claimed execution retains its persisted award, acquired engine
and granted runner identity. `OwnerHost` now calls `NewNodeInput`, as the claimed
host does. The placement's input-construction callback is removed because it
could manufacture a second execution identity. `AdoptClaim` receives the private
award for the existing per-attempt delegation adapter. An award with a bearer
requires this adopter. Failed adoption prevents execution. A generated property
checks changed claims, arbitrary runner names/labels, missing and refused adoption,
and an adopter mutating its claim object after input construction.

The backoff assertion now exercises the common executor under synctest. It checks
that retry keeps the job running under its generation, cancellation commits one
terminal transition and no further process starts. Its old 24-line scheduler
version is deleted. The retained plan-concurrency tests observe early host results
before waiting for their events. Eviction workers are cancelled and joined before
the test returns. These tests now report their removed-schema failures in under
a second instead of hanging or reporting an error after their test has ended.
The session test follows the canonical `frames_closed` code and accepts the shared
parser's JSON-object diagnostic.

The deleted W3 API is `OwnerHost.Bind`. Its only callers were the two owner test
files, which now use the common constructor or test private-award adoption. The
production Go diff removes 12 lines and adds 54. No legacy run-hosting file is
claimed deleted in this continuation.

### Verification for this continuation

All Go checks use the scratch cache and temporary directory under
`/tmp/sw-astra-W3/`, GOWORK=off, GOFLAGS=-buildvcs=false and at most four packages.

| Check | Result |
| --- | --- |
| Whole-repository build | Passed. |
| Scoped vet on nodehost, nodeproto, source, lease, orchestrator and both acceptance packages | Passed. |
| Full nodeproto, lease and source packages | Passed. |
| Focused owner, native host, process, SDK planner, state follower, executor, identity, strict expansion and session tests | Passed. |
| Race checks on owner, native host, expansion concurrency and backoff | Passed. |
| Self-claim identity fuzz | Passed, 1,500 executions. |
| Existing lifecycle, recovery, environment, credential and protocol acceptance components | Passed. Product placement children remain skipped. |
| Commentcheck against main | Clean. |
| Sleepcheck against HEAD and git diff whitespace check | Clean. |
| Retained plan-concurrency tests | Fail on removed `sparkwing_meta` and old store-concurrency fixtures. Both terminate without their previous hangs. |
| Full nodehost suite | Fails on legacy schema fixtures and forbidden listening sockets. It is not a passing suite. |

No acceptance cell is added or activated. W3 still owns the complete owner-policy
composition, native orchestration caller migration and deletion of the retained
scheduler, sessions, layers and fixtures. Required foreign contracts remain the
full execution snapshot, fenced step reports and source attempt identity, owner
policy/debug/approval edges, terminal storage record references and matched-engine
host invocation. C2 authentication and delegation and W6 concurrency resolution
are still stubs in this checkout. No foreign contract is changed.

Shared code edits are `internal/nodeproto/plan_validation.go` and its expansion
membership test, as authorized by the lead's concurrency decision. Shared prose
edits are this continuation, `handoffs.md`, `c3-protocol-sdk.md` and the port
inventory. No production CLI, runner or pod caller is claimed migrated. There
are no Git writes, Sparkwing executable runs or pipeline runs. Scratch is removed
after all checks join.

## Shared steps and alias caller continuation

W3 remains incomplete. The retained executor now uses the common step graph.
The separate 166-line scheduler and its `stepRun`/`stepDone` state are removed.
The shared driver owns readiness, terminal snapshots, cancellation, failure
policy and Finally ordering. Native execution retains its fenced step writer.
The retained writer now fails the job on a refused write and suppresses retries.
Its terminal writes use the existing heartbeat bound.

`ResolvePipeline` owns project-name to SDK-entrypoint selection. CLI describe
selects the registered entry before parsing pipeline and job flags. Help keeps
the invoked alias, and risk discovery uses the registered entry. The CLI passes
the pair to local hosting, which refuses a changed checkout selection. Trigger
and pod callers carry the same pair through describe, initial planning and
attempt replan. Native checkout planning uses the same resolver. The invoked
name stays in the SDK run context and persisted pipeline identity.

The CLI regression builds and describes a real Go SDK fixture for project alias
`deploy-prod` targeting `Deploy`, with required `target` and job-owned `image`
arguments. It parses both flags and calls the real planning path. Author code
records the invoked alias and target in its returned declarations. The existing
native planning and replan tests retain their request/context assertions. The
retained step-window test uses its real Go SDK process, the shared graph and a fake step writer.
It covers the start boundary, unknown-bound refusal and refused persistence
before any body, plus strict v2 predicates and failure-before-body behavior.
Its hello uses an explicit fixture binding. It does not exercise
production authorization, a store transaction or sealed native step windows.

Admission, run-admission and event-test workers now cancel and join at cleanup.
The old helper could call `t.Error` after its test had completed when the host
failed before emitting an event. The full nodehost run passes that point and
reaches its remaining schema and socket failures. They are failures, not a
passing suite.

The production Go change adds 407 lines and removes 476, including the extracted
common driver and identity resolver, for a net reduction of 69 lines. The old
run scheduler, Session, Config, Execute and hosting entry points remain. No
acceptance cell is added, unskipped or marked complete in this continuation.

### Verification

Checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, at most four Go
test packages and the worker scratch cache/temp directory.

- Whole-repository build and scoped vet pass.
- Core host, native process, owner policy, alias, shared step, window and
  reconstruction tests pass. The focused host run takes 3.85 seconds.
- Race checks for executor, owner, native host, alias and retained windows pass.
- The existing `protocol-step-failure-policies/hermetic-native-go-steps` cell
  passes, as do the existing hermetic lifecycle, recovery, environment and
  credential component selections. These do not complete native placement cells.
- Full source and lease package tests pass.
- The existing step-fence/masking property passes 1,029 generated cases.
- The full nodehost run still fails on removed-schema fixtures and listening
  sockets denied by the sandbox. An overbroad focused selector also selected
  three socket tests. The final core selector excludes those tests.
- A broad docs-package run hit a forbidden httptest listener. The focused
  content-mirror check passes, as does the repository's docs-sync drift check.
- Commentcheck against main and sleepcheck against HEAD pass.

### Remaining contracts and integration

The existing assignment audit still applies. C1 lacks the owner policy-failure
and approval-SkipIf success edges and typed debug operations. C4 lacks sealed
step-window inputs. C3 `Controller.ReadSnapshot` returns only scheduling facts,
and its interface has no fenced step writer. `NodeTransition` and
`JobTransitionRequest` still lack the source attempt fence. Terminal execution
rows omit the output/manifest record references needed by native owner reuse.
The matched engine has no typed run/node/eval-host invocation contract.

C2's authenticator, authority validator and delegation validator remain panic
stubs. C4's `ResolveConcurrency` remains a panic stub, and `procenv.Build` rejects
the Git kind. Existing tests continue to use their declared adapters and fakes.
Those stub implementations do not justify a second host policy. Missing foreign
shapes remain contract changes for their owners. No foreign contract is changed.

Local W3 API additions are `PipelineIdentity`, `ResolvePipeline`, the retained
Config/Run/PlanRequest entrypoint field and `HostedEntry.Pipeline`. The graph
driver is private. Its adapters use the existing transports and writers.

W3 still owns complete native owner policy composition, production placement
caller migration, the cross-process mask-namespace design and removal of the
remaining legacy runtime and store fixtures. Local, trigger and pod callers in
this continuation migrate alias handling only. They still invoke the old host.
Native HTTP/api.sock, controller credentials, storage backends, matched-engine
handoff and the placement matrix remain unverified integration points.

Shared caller edits are `cmd/sparkwing/compile.go`, `main.go`, `pipeline_args.go`,
`pipeline_args_test.go` and `run_host.go`. W3 hosting callers changed in
`internal/orchestrator/hosted_run.go`, `trigger_host.go`, `pod_node_host.go` and
`project_config_test.go`. Source protocol docs and their generated mirror change
together. This continuation, handoffs, the C3 contract and the port inventory
record the boundary. There are no Git writes, installed candidates, pipeline
runs, releases, new environment variables or customer limits. Scratch is removed
after all checks join.

<!-- design:end -->
