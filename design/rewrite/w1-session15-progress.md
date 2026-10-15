# W1 continuation

Starts at 61b32209d. W1 remains incomplete. No W1b implementation was changed.
The lead owns review, commits, pipelines and landing.

## Implemented and deleted

Removed offer-round finalization, its idle-registry check, five-second timer,
events, controller endpoint and client method. Deleted the warm dispatcher and
factory that called the finalizer. The legacy runner parser refuses warm and
retains inprocess and k8s. Removed its warm-only guards, fallback tests and help
claims. Legacy readiness writers now publish queue readiness without an offer
round. Their full transition/API cut remains open.

Deleted claim_round.go (350 lines), warmpool.go (406) and
warm_runner_factory.go (28). The 121 retained pricing/snapshot lines now live in
node_charge.go. The complete change removes **695 net non-test Go lines** and
**958 net test lines**, including caller changes and new acceptance support.
Removed the unused Store.RunReaperLeased API. Storage/accounting lease methods
in the same file are unchanged.

The queue invariant is that a failed match changes neither the ready row nor its
position, and a matching claimant can receive it immediately. A property generates
32 selectors, repeats rejected polls with forged granted labels, compares complete
node snapshots and checks zero events and billing awards. A separate test proves
that both satisfied and unsatisfied soft preferences permit an immediate claim.

The new ordered-queue cell exposed missing push-bound claim support. Matching now
checks every push binding in the capability ancestry against the sealed repository,
ref and SHA. Direct award issuance repeats that same pure check against persisted
run projections. Valid push credentials can claim through the shared path. Their
workflow identity still comes from provisioning, and no claim body creates App
trust. Property tests vary repo/ref/SHA, named/queued selection, node/trigger kind,
delegated credentials and an actual awarded host with a push-bound ancestor. A
direct-issuance regression also refuses scope escape. These tests failed before
the implementation and pass afterward.

No new interface, configuration, customer limit, schema, service or trust root was
added. No foreign contract shape changed. C1 prose records the completed cuts and
push enforcement.

## Acceptance and test ports

TestPlacement/placement-ordered-matching/store-sqlite now executes. It skips an
earlier foreign-push trigger, orders eligible jobs by persisted soft preferences,
accepts an unsatisfied preference, preserves a blocked job through rejected polls
and lets a later matching claimant take it. Enrollment is a contract fake. Store,
matcher, capability ancestry, award persistence and transitions are real.

The removed offer-window, idle-presence, concurrent-finalizer and warm-fallback
tests describe the deleted mechanism. The tagged direct-source test that used
the warm dispatcher is also removed. Native source execution remains in the
acceptance inventory. Their matching, tenant-scope, queue-order
and no-fallback concerns move to the final claims and properties. The stale CLI
doctor dead-capacity repair test is also removed. Its final leased-maintenance
ports were completed in session 14. Native placement cells remain required.

## Amendment and handoff audit

The previous completed admission, seal, matching, reconciliation, owner reaping,
session, source-release and retention work is retained. This continuation closes
the offer-round portion of A1-51 and adds Actions claim evidence to A1-08/A1-26.
The following groups still need integration or contract work.

| Entries | Remaining work |
| --- | --- |
| A1-07, A1-11, A1-25, A1-26, A1-44, A1-45 | W2 must install trusted intake, enrollment and plan-secret providers. Actions evidence and runner wiring remain W2/W8 integration. |
| A1-08, A1-18, A1-55 | Source-attempt identity is still missing from C1/C3 transition requests. Identical retry failures and delayed reports need that amendment and W3/W9 transport. Here first-claim/reclaim rules remain implemented. |
| A1-13 through A1-17 | W1b must join copied records and result attachment to the lifecycle transaction. Failure-stage transport, GuardRefusal and generation-zero generator fences remain shared gaps. ReuseSource needs materialized-result identity for sources with no attempt. |
| A1-20 through A1-24 | C2 BindingState still lacks cancellation reason. Result operation/digest transport, concurrency-key selectors, teamless sessions and production source/origin adapters remain integration points. |
| A1-27, A1-28 | Committed-checkout settlement remains implemented. W1b accounting helpers exist, but lifecycle wiring, pricing and customer-wait exclusion remain open. C2 SettleBilling is still a panic stub. Metered awards remain refused. |
| A1-29, A1-31 through A1-35 | Blob, upload, log, record and accounting persistence stay with W1b. Existing transaction seams retain fake coverage. |
| A1-38 | SyncLauncher remains a stub. W8/W9 must provide immutable award generation and Kubernetes Job-instance correlation. Stable UnitID and NodeUID cannot distinguish an earlier award's Job. |
| A1-40, A1-53 | W8/T1/lead retain local-runtime readiness, native HTTP/api.sock, PostgreSQL, platform, provider and real-repository evidence. |
| A1-51 | Store.AcceptPlan and its validator, StateStoreOld, legacy claims/triggers and readiness writers still need removal and caller migration. Old offer columns remain because legacy claim/credit code still references them. |

No new question requires a human decision. The foreign contract requests above
are unchanged. This session does not implement another area's contracts.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Package parallelism is -p 4.

- Whole-repository go build ./... passes.
- Scoped vet passes for pkg/store/..., pkg/controller/..., internal/orchestrator,
  internal/runners/k8s, internal/cluster, internal/acceptance/..., tests/acceptance/...
  and cmd/sparkwing.
- The store selection passes **332 top-level tests / 998 cases**, with no skips.
  The file list is w1-session15-tests.txt. Extract Test names, omit names prefixed
  TestPostgres and join into an anchored -run expression.
- Full teststore, internal storetest, pkg/match, internal/apispec and internal/apiref
  pass. The package internal/acceptance has no test files.
- Controller TestEngine.*, the session-route property, runner-presence test,
  duplicate-registration test and both elected reaper tests pass.
- Four changed runner-parser validation tests pass. CLI registry, Markdown,
  worker-help and partial-doctor-render tests pass.
- tests/acceptance with -skip '^TestProtocol$' passes **19 top-level tests /
  197 executed cases**, with **2,422 required skeleton/platform skips**. The new
  ordered-matching cell also passes after its fixture refactor.
- commentcheck -base main, sleepcheck -base HEAD and git diff --check pass.
  Changed Go files are formatted.
- API docs/OpenAPI and API snapshots are regenerated. CLI docs use the real
  runCommands function through a temporary Go test overlay. No Sparkwing binary
  is run. sync-docs.sh --check passes.

The protocol group remains excluded because the TypeScript compiler is absent.
No full legacy suite, native placement, PostgreSQL, race, lint, pipeline or
independent-review pass is claimed.

## Shared files and friction

Caller removals/migrations outside pkg/store are:

- internal/runners/warmpool/*.go and internal/runners/k8s/warm_fallback_test.go.
- internal/orchestrator/warm_runner_factory.go and trigger_runner_factory.go.
- internal/cluster/runner_pool_cli.go, runner_pool_cli_test.go and trigger_loop.go.
- pkg/controller/client/client.go, handlers.go, server.go, tenant_routes.go,
  runner_presence.go, runner_presence_test.go, runner_scopes_test.go,
  node_claim_ownership_test.go, principal_limit.go, credits.go, credits_test.go,
  direct_source_e2e_test.go and client/poll_advice.go.
- cmd/sparkwing/help_registry.go and doctor_test.go.

Shared acceptance changes are internal/acceptance/store_queue_order.go,
store_recovery.go and tests/acceptance/placement_test.go. Documentation changes are
C1, handoffs, T1 acceptance/port inventory, migration notes, observability, auth,
CLI/API references, changelog and generated mirrors. Snapshots are
.apidiff/pkg_store.txt and pkg_controller_client.txt.

The first full selection found an obsolete SQL-guard exemption for the merged
ExpiredOutputRuns removal. Deleting that exemption makes the guard pass. An
inherited CLI doctor test still named the removed repair counter and prevented
the CLI test package from compiling. Removing that obsolete test completes the
prior cut. The old TestNodeClaim_MarkReadyIsIdempotent fixture still fails while
creating its legacy run because sparkwing_meta is gone. No table or compatibility
path was restored. The final claim properties provide the new queue evidence.

No Git write, Sparkwing binary, deployment, release, service, harness-memory write
or outside-worktree edit ran. Local Git user.name, user.email and core.worktree
have no overrides. Scratch is removed before reporting.

STATUS: CONTINUE
