# W1 continuation 22

W1 remains incomplete. This continuation removes the raw node-status setter
and readiness-withdrawal APIs. HEAD remains
ac35cf30faecb1515f97d8c95f8e24eb9375848e. No Git write was attempted.
W1b implementation files are unchanged.

## Implementation and deletion

`SetNodeStatus` and `RevokeNodeReady` are removed from Store, Tenant and Client.
Their controller handlers, route registrations, tenant-route entries and hosted
retry entries are removed. StateStoreOld and localState lose the status setter.
Every Go reference to the removed methods and response type is gone.

Debug pauses write activity detail and clear it on resume. They no longer
overwrite lifecycle status. Queue withdrawal uses the existing TransitionNode
CauseRevoke edge under current owner authority. Raw MarkNodeReady and the other
legacy lifecycle APIs remain for subsequent caller migrations.

The diff deletes **299 Go lines**, including **156 source lines** and
**143 test lines**. It adds **265 Go lines**, including the new property and
acceptance checker, for a net reduction of **34 Go lines**. Counts include
comments and formatting and exclude Markdown, YAML and generated mirrors.
There is no new production mechanism, setting, environment variable, schema
change, compatibility shim or customer restriction.

## Invariant, tests and ports

The invariant is: an owner may withdraw only its own ready, unclaimed job,
and a refused withdrawal leaves claims, state and events unchanged.

TestReadinessWithdrawalPreservesHolderAndAuthorityProperty generates 32
sequences with varying withdrawal/replay/republication counts and clock steps.
It probes changed owner generations, forged holders, root authority, exact
lease expiry, foreign tenants, and claimed, running and terminal jobs.
Refusals preserve the complete snapshot and event list. Withdrawn work cannot
be claimed until republished. The property passes before and after the cut.

The new SQLite acceptance cell is
`TestLifecycle/lifecycle-transition-replay-and-fencing/store-sqlite`.
It uses real admission, acceptance, claims and transitions. It checks withdrawal,
reopening, immutable replay, competing-holder exclusion, stale-start refusal,
first-attempt allocation, cleared terminal claims, absorbing terminal state and
one continuous job-event sequence. Enrollment is a contract fake.

The old TestNodeClaim_RevokeOnlyWhenUnclaimed and HTTP revoke assertions retire
with their APIs. Their store invariants move to the property and SQLite cell.
Redundant client and tenant status writes retire. Runner scope fixtures use
activity detail. TestRunNode_MissingJobFinalizesDoneNodeWithEmptyOutcome retires
with the raw setter's two-write terminal state. Final transitions record outcome
and terminal status atomically. Adjacent missing-Job tests remain.
No additional archived T1 source-function identity is claimed closed.

The retry-policy fixture drops its stale run-heartbeat assertion, which remained
after the earlier heartbeat cut. Its replacement activity entry and both policy
tests pass. Public OpenAPI and API reference documents remove both routes.
Historical migration examples and public API snapshots remain for W9's final
documentation and snapshot pass.

## Verification

Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4 and
GOCACHE/GOTMPDIR/TMPDIR below /tmp/sw-astra-W1.

- Whole-repository `go build -p 4 ./...` passes on the final source tree.
- Scoped `go vet -p 4` passes for internal/store/..., internal/controller/..., internal/storage/..., internal/nodehost/..., internal/orchestrator/..., internal/runners/k8s/..., internal/acceptance/... and tests/acceptance/....
- The W1 package selection passes **401 top-level tests / 1,176 cases**, with no skips. Select Test functions from the files in w1-session22-tests.txt, omit TestPostgres names, and run their anchored alternation against ./internal/store with -count=1.
- Ten SQLite cells pass with `go test -p 4 ./internal/acceptance ./tests/acceptance -run '^(TestLifecycle|TestRecovery)$/[^/]+/store-sqlite$' -count=1`. These are both lifecycle cells and all eight recovery cells.
- The three Engine enrollment/trusted-service/bootstrap adapter tests and both TestHostedRetryPolicy_ tests pass.
- Full internal/apiref and internal/apispec tests pass. Embedded scrub and migration tests pass. check-api-spec and sync-docs --check pass.
- `go run ./internal/commentcheck -base main .` passes across 1,081 files. git diff --check passes. The removed APIs have no Go references.
- Local Git user.name, user.email and core.worktree overrides are absent.

The node-host debug run does not establish debug execution behavior. Its bounded
`TestDebugPausesHoldTheNodeUntilReleased/before` run fails because the fixture
calls legacy CreateRun against the final schema, which has no args_json column.
The earlier full debug test timed out. The changed test bounds its pause and
completion waits and expects activity detail. W3 must migrate that fixture with
its host integration.

Full pkg/docs tests cannot run here because TestWebClient_Versions_HappyPath
opens a socket. The separate symbol-reference test reports 17 missing SDK
exports in existing W4/W9 prose. Those pages are unchanged. No full docs-suite
pass is claimed. Native PostgreSQL, HTTP/api.sock, actual pods, SDK processes,
platform execution, full legacy suites and release gates remain unverified.
No Sparkwing binary or pipeline was run. The lead retains review and landing.

## Amendment and handoff audit

A1-51 and the W1 scope handoff advance through the two API cuts and their callers.
StateStoreOld, CreatePlanNode/legacy bootstrap, raw readiness publication,
run/node lifecycle writers and the remaining claim/token/trigger paths still
need retirement. The other completed W1 implementations remain covered by the
package selection. No completed mechanism was rebuilt.

The previously recorded foreign gaps remain present in this checkout:

- A1-21: C2 BindingState still lacks cancellation reason. W1 persists the reason and delivers it through unit renewal. W2 must extend the authentication shape and bind its production middleware.
- Attempt-identity handoff: W9 JobTransitionRequest still lacks SourceAttempt. W3 must transport the captured ordinal and consume Applied.Attempt. Holder execution through that adapter remains open.
- A1-38: LauncherObservation still has only UnitID, JobName and NodeUID. W8/W9 must add immutable award generation and Job-instance correlation. SyncLauncher remains a stub because UnitID can be re-awarded.
- A1-27: W1 lifecycle/billing joins remain implemented. C2's production calculator and W3's current owner-compute reporting remain integration points. W1b retains accounting policy and legacy accounting retirement.
- A1-23/24/25/26/40/53: production route/evidence providers, host/source integration, local-controller lifecycle and native acceptance remain with their owning areas and T1/lead. Store contracts and fakes do not establish those results.
- A1-28/29/31-35 remain in W1b's billing, blob, log and retention split. No implementation in that split was changed.

No new foreign contract change or architecture decision is requested.

## Shared files

The lead's caller-migration exception covers these edits outside internal/store:

- internal/acceptance/store_transitions.go
- internal/nodehost/debug.go and debug_test.go
- internal/orchestrator/backends.go, hosted_retry.go, hosted_retry_test.go and wingd_api_routes_test.go
- internal/runners/k8s/k8s_test.go
- internal/controller/client/client.go
- internal/controller/handlers.go, node_claim_test.go, node_client_contract_test.go, runner_scopes_test.go, server.go and tenant_routes.go
- internal/storage/state.go
- tests/acceptance/lifecycle_test.go

Shared prose is c1-data.md, handoffs.md, t1-credential-routes.md,
t1-port-inventory.md, CHANGELOG.md, docs/auth.md and
docs/migrations/_unreleased.md. API YAML/reference and embedded mirrors are
regenerated. The package selection is w1-session22-tests.txt.

## Friction

The API generator validates named schema types before removing stale paths.
Deleting the two removed route blocks first lets regeneration pass. The initial
debug test hid its schema failure behind an unbounded pause wait. The bounded
subtest exposes the removed-column error. The retry-policy test also exposed
the stale heartbeat assertion and now passes. The sandbox's socket restriction
and existing SDK-document drift limit the docs suite as stated above.

Scratch is deleted after verification. No processes or Git writes are retained.

STATUS: CONTINUE
