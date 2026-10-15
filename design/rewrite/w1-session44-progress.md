# W1 continuation: remove the split trigger queue

W1 remains incomplete. The whole-repository build passes. This session starts
from `28b08041f` and makes no git writes.

Production Go delta this session is **-225 lines**. `store.go` loses 201 lines
and `node_claim_fence.go` loses 24. No production code is added. Against main,
the combined W1/W1b store delta is **+1,865**, against **-4,250**, or **6,115 over**.
The remaining legacy paths still need deletion. This is not a budget exception.

## Changes and ports

- Removed `Store.ClaimNextTrigger` and `Store.ClaimNextTriggerFor`, their separate
  queue scan, cursor, candidate type and decoder. Production queue callers
  already use `ClaimUnit`. No fallback or forwarding method is added.
- Legacy fixtures that need a held trigger now select their known ID through
  the retained named claim. Billing assertions and implementations are unchanged.
- Removed the unused `NodeExecutionAttemptBelongsToLiveClaim` reader and its
  fixture. Authentication snapshots, holder properties and tagged-log tests
  retain the attempt and tail-grace requirements.
- Updated C1, the port inventory, migration guide, changelog, embedded mirrors
  and generated API snapshots. The client snapshot also records the existing
  `ReplaceSchedules` to `WriteSchedules` rename. Client source is unchanged.

Invariant: a queued trigger claim cannot cross its tenant or any ancestor's
repository scope, and skipping excluded work changes none of that work's state.

`TestQueuedTriggerRepositoryScopeProperty` uses real identity sessions, machine
provisioning, delegation and admission. Four generated queues put 65-74 excluded
runs before multiple eligible runs. It checks FIFO, scoped queue exhaustion,
private bearer delivery and every excluded run snapshot and owner row. Some cases narrow a
broader root's repository scope in the descendant.

`TestQueuedTriggerClaimsNeverCrossTeamsProperty` uses two real tenants and eight
concurrent workers. It checks that every eligible owner receives one award and
that every award belongs to the claimant's tenant. These replace the old FIFO,
repository queue-head and concurrent-trigger assertions. The legacy concurrent
node test remains. Registered Actions and push-ancestor properties retain the
replaced context-scoped GitHub trigger requirements. The port inventory names
all retired checks and their replacements.

## Verification

All commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache` and `GOTMPDIR=/tmp/sw-astra-W1`, from this worktree.

- `go build ./...` passed before edits and after the final changes.
- All 21 W1 plain `TestStore...` acceptance tests passed. The selection also
  passed the new queue properties, current completion/release properties,
  rotation-grace property, all launcher-snapshot tests and final-attempt
  authentication snapshot. It completed in 6.789 seconds.
- `go test -race -p 4 ./pkg/store -run '^TestQueuedTrigger' -count=1` passed
  after the broader-root and skipped-owner assertions were added, in 29.732 seconds.
- `go vet -p 4 ./pkg/store ./pkg/controller ./internal/nodehost` passed.
- `go test -p 4 ./pkg/store ./pkg/controller ./internal/nodehost -run '^$'`
  passed, compiling every test in the changed packages.
- `go run ./internal/commentcheck -base main .` passed across 2,140 files.
- The tenant SQL scope and default-team caller guards passed in 1.680 seconds.
- Generated API snapshot comparison, documentation mirror check and
  `git diff --check` passed.

The acceptance selection includes PlanSeals, GeneratedDependencies, Transitions,
OwnerUnstartedRecovery, ScheduleBoundary, ProvisioningOrigins,
CapabilityManagement, Sessions, ActionsEnrollment, EnrolledClaims, QueueOrder,
ClaimMatching, LauncherSync and all eight Recovery tests. No native catalog cell
is newly activated. Full legacy suites, PostgreSQL, HTTP/api.sock, Kubernetes
and repository pipelines are not claimed. The lead owns those real integrations
and final independent review.

## Shared caller files

Outside pkg/store, the minimal fixture migrations are:

- internal/nodehost/rehydrate_resume_test.go.
- pkg/controller/egress_test.go, execution_location_test.go and server_test.go.
- pkg/controller/claim_test.go is deleted after its FIFO test is ported.

Inside pkg/store, named fixture selection changes are in
claim_team_boundary_test.go, compute_limits_test.go,
execution_attribution_test.go, lease_test.go, node_usage_bounds_test.go,
secret_scope_test.go, trigger_absorbing_test.go, trigger_claim_identity_test.go
and trigger_team_test.go. github_runner_bindings_test.go and
repo_allowlist_claim_test.go lose the ported queue checks.

The lead's W1b caller exception is used for three queue selections in
trigger_claim_credits_test.go. Its financial assertions remain. No W1b
production, materialization, blob or log implementation changes. Lead-owned
handoffs and amendments are unchanged.

## Remaining work and handoffs

- **Contract change still needed from C2:** ProvisionSpec has no seeded
  presentation field. Store.Issue's one-use bootstrap input remains pending
  that declaration. The implementation must keep digest-only persistence,
  the no-admin gate and one-use consumption in its issuance transaction.
- Console sessions, verified-local authority, console service issuance and
  SetRunnerMetered remain W1 work. Host/source observations and persisted pool
  access/repository writers remain open.
- StateStoreOld, retained host scheduling, raw lifecycle/readiness, named/node
  legacy claims, trigger writers, token retirement, deletion/account authority
  and GitHub automation admission still need their hard cuts and migrations.
- Rotation grace, the SyncLauncher store fence, the five credit-method moves,
  W1's harness moves and six dead contract tables remain complete. This session
  reverified grace and launcher fencing without rebuilding those mechanisms.
- Materialization stays entirely with W1b. Native provider, placement, store
  dialect and transport composition evidence remains with the lead and owners.

Friction: remaining legacy fixtures target removed schema columns. Their
mechanical caller migration is not evidence that their full suites pass.
There is no new design mechanism or foreign signature change. Git configuration
has no repository-local identity or core.worktree override. Assigned scratch is
deleted after checks. The shared build cache is retained.

STATUS: CONTINUE
