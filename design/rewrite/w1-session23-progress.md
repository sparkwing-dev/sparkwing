# W1 continuation 23

W1 remains incomplete. This continuation removes the raw dependency writer and
supplies accepted plan concurrency to expansion validation. HEAD remains
9262bce47bee84721f42d30b63177e4faa6c9ee0. No Git write was attempted.
W1b implementation files are unchanged.

## Implementation and deletion

`UpdateNodeDeps` is removed from Store, Tenant, Client and StateStoreOld. Its
controller request type, handler, route, tenant-route entry, retry entries and
localState adapter are removed. Every Go reference is gone. The host's generated
wait calculation uses membership without rewriting a dependent's declaration.
AcceptExpansion remains the topology writer. Scheduling reads accepted plan/spec
bytes and accepted expansion bodies.

Expansion validation now receives initial plan-level concurrency through
ValidationContext.ExistingConcurrency, including replay. The change uses the
existing contract. A test first failed with an empty context, then passed with
the accepted declarations. The shared validator's policy implementation remains
with W3/W4 as described below.

The diff removes **96 Go lines**, including **79 source lines** and **17 test
lines**, and adds **301 Go lines**, for a net increase of **205 Go lines**.
These counts include test-support source, comments and formatting. They exclude
Markdown, YAML and generated snapshots. There is no new production mechanism,
setting, environment variable, schema change, compatibility shim or restriction.

## Invariant, acceptance and ports

The invariant is: accepted declarations and expansion membership alone determine
generated dependencies, and replay cannot change topology or reset member state.

TestGeneratedDependenciesUseAcceptedTopologyProperty generates 32 expansions
with zero to six members. It writes adversarial legacy deps_json projections
containing missing jobs, self-dependencies and random forged identifiers, then
checks the complete scheduling graph, decisions and persisted expansion replay.
TestExpansionValidationReceivesAcceptedPlanConcurrency checks submission and
replay against a contract fake requiring the accepted concurrency declarations.

The new SQLite cell is
TestLifecycle/lifecycle-generator-barrier/store-sqlite. It uses real admission,
strict plan validation, settings, acceptance, claims, starts, finishes and
expansion acceptance. A successful source holds the barrier. Two generated
members and an empty group survive reopening. Completing only one member keeps
the dependent blocked. Completing both releases it. Replay returns terminal
members, initial plan/hash and dependent spec stay unchanged, and a different
body fails without changing the snapshot. Intake/enrollment are contract fakes.
The expansion credential is seeded because C2 ValidateDelegation is a panic stub.
This cell does not establish the memoized-source branch, SDK output handling or
native execution.

These store checks port the persisted-dependency assertion from
TestExpansionFansOutAndHoldsItsWaiter. Its host execution-order assertions remain,
and its stored-dependency expectation now retains the original declaration.
TestGeneratedWaitsUsesMembershipWithoutRewritingDeclarations covers unresolved,
populated and empty groups through the host wait calculation. Removed HTTP,
client and tenant mutation assertions retire with their API. No archived T1
source-function identity is newly claimed complete.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4 and
GOCACHE/GOTMPDIR/TMPDIR below /tmp/sw-astra-W1. Run from this worktree with go -C.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet -p 4 passes for pkg/store/..., pkg/controller/..., pkg/storage/..., internal/nodehost/..., internal/orchestrator/..., internal/acceptance/... and tests/acceptance/....
- The W1 store selection passes **403 top-level tests / 1,210 cases**, with no skips. Extract Test function names from the files in w1-session23-tests.txt, omit names containing Postgres, and run their anchored alternation against ./pkg/store with -count=1.
- **Eleven SQLite cells** pass with go test -p 4 ./internal/acceptance ./tests/acceptance -run '^(TestLifecycle|TestRecovery)$/[^/]+/store-sqlite$' -count=1. These are three lifecycle cells and eight recovery cells.
- The new host wait test, both TestHostedRetryPolicy_ tests and the three Engine enrollment/trusted-service/bootstrap adapter tests pass.
- internal/apiref and internal/apispec tests pass. check-api-spec and sync-docs --check pass. Embedded scrub, changelog drift and selected migration tests pass.
- Required go run ./internal/commentcheck -base main . passes across **1,083 files**. git diff --check passes.
- Local Git user.name, user.email and core.worktree overrides are absent.

TestExpansionFansOutAndHoldsItsWaiter was attempted with -timeout=30s. It fails
at CreateRun with the removed runs.args_json column before host execution. W3
must migrate the fixture to final admission and lifecycle. Its source-level
assertion change is not a full host-runtime pass.

A read-only overlay of internal/nodeproto/expansion_concurrency_test.go changes
the expected code to plan_invalid for capacity, scope and on_limit, retaining
success for cost. All six job/plan mismatch cases fail because ValidatePlan
returns nil. W1's context handoff passes independently with a fake. The existing
lead-approved W3/W4 concurrency-definition handoff remains open. No W4 file was
edited and no failing overlay test was left in the repository.

No PostgreSQL, race, native HTTP/api.sock, pods, SDK processes, platform execution,
full legacy suite or release gate is claimed. The full docs suite retains the
previous socket and SDK-symbol-drift limits. Aggregate API snapshots were not
regenerated. Only this cut's four removed method declarations were deleted.
The lead owns independent review, pipelines and landing.

## Amendment and handoff audit

A1-07 now carries the accepted plan concurrency into the existing expansion
validation interface. W3/W4 still own mismatch enforcement. A1-51 and W1's scope
handoff advance through removal of the raw dependency mutation and its callers.
The prior completed W1 implementations remain covered by the store selection.
No completed mechanism was rebuilt.

StateStoreOld, CreatePlanNode/legacy bootstrap, UpdatePlanSnapshot, raw readiness
publication, run/node lifecycle writers and legacy claim/token/trigger paths
still need retirement. No new architectural decision or foreign contract shape
is requested. Existing integration points remain:

- A1-21: C2 BindingState lacks cancellation reason. W1 persists it separately and delivers it through renewal. W2 must extend the shape and bind production authentication.
- A1-24/25/26: W2 must supply production authorization/evidence, secret opening, enrollment and delegation validation. The store implementations and fakes do not establish those integrations.
- Attempt identity: W9 JobTransitionRequest lacks SourceAttempt. W3 must send the captured ordinal and consume Applied.Attempt. The holder execution adapter remains open.
- A1-38: LauncherObservation lacks award generation and Job-instance identity. W8/W9 must add those fences before safe SyncLauncher cleanup can be implemented. UnitID alone can be re-awarded.
- A1-27: lifecycle/billing joins remain implemented. C2's production calculator and W3's current owner-compute reporting remain integrations. W1b owns accounting policy and legacy accounting retirement.
- A1-23/40/53: production route, host/source, local-controller lifecycle and full native acceptance remain with their owning workers and T1/lead.
- A1-28/29/31-35 remain in W1b's billing, blob, log and retention split. No persistence/accounting implementation in that split changed.

## Shared files

The caller-migration exception covers these Go files outside pkg/store:

- internal/nodehost/expand.go and expand_test.go
- internal/orchestrator/backends.go, hosted_retry.go, hosted_retry_test.go and wingd_api_routes_test.go
- pkg/controller/client/client.go
- pkg/controller/handlers.go, node_client_contract_test.go, runner_scopes_test.go, server.go, server_test.go and tenant_routes.go
- pkg/storage/state.go

Shared acceptance files are internal/acceptance/store_generated_dependencies.go,
internal/acceptance/store_recovery.go and tests/acceptance/lifecycle_test.go.
The existing credential seeder now persists parent and expansion indexes.
Shared prose is c1-data.md, handoffs.md, t1-credential-routes.md,
t1-port-inventory.md, CHANGELOG.md, docs/auth.md and
docs/migrations/_unreleased.md. API YAML/reference and embedded mirrors are
regenerated. The three .apidiff package snapshots remove this cut's four method
entries. The package selection is w1-session23-tests.txt.

## Friction and cleanup

C2's delegation validator prevents a real issued expansion credential in the
SQLite proof. The existing test seeder needed parent and expansion indexes for
its seeded envelope. The legacy host fixture fails on the removed schema, and
the shared validator still lacks the approved concurrency-definition check.
Those limits are recorded above. No workaround, duplicate validator or shim
was added to production.

Scratch is deleted after final verification. No owned service, background
process, Sparkwing binary invocation or Git write is retained.

STATUS: CONTINUE
