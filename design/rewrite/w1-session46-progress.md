# W1 continuation: merge recovery, seeded bootstrap and raw claim retirement

W1 remains incomplete. This session starts from `4cc9502f7`. All 22 conflicted
paths have resolved contents. Integration's controller policy routes and deleted
legacy routes win. W1's launcher store adapter and prior caller migrations are
preserved. Both port inventories and migration sections survive. No conflict
markers remain. Under the swarm's no-git-writes rule, the index remains unmerged
for the lead to stage. HEAD and repository-local Git identity are unchanged.

## Implementation and deletion

- C2 now declares `ProvisionSpec.Seed`. `Store.Issue` validates it, accepts it
  only for bootstrap, and persists its digest and indexed prefix. The startup
  adapter implements `EnsureBootstrapCapability`. It locks the existing settings
  table's deployment gate, checks for a live administrator, calls Issue and
  consumes the gate in one transaction. A live repeat is a no-op. Another seed,
  an ended seed and a retained gate after credential pruning cannot reissue.
- `HasDeploymentAdministrator` checks unbound deployment operator/service
  authority, operator.tokens and the full live ancestry with the store clock.
  Startup and authenticated operator inventory now work through the real store.
- Removed `CreateTokenIfNoneExist`, both raw claim-token authenticators, their
  route-class and runner-claim types, raw result replay and result-commit types,
  the unused slot/key/fence readers and the unused run-claim reader.
- Deleted the dead attempt-report and child-enqueue writers, their types and
  private helpers. Current node transitions and `Tenant.AdmitRun` own those
  behaviors. No production caller remained after W2's route cut. Raw token
  construction, the legacy node queue and the raw trigger writers still remain.
- Updated C1, the test port inventory, authentication docs, migration guide,
  changelog, embedded mirrors and API snapshots. Lead-owned handoffs and
  amendment files are unchanged. W1b files and materialization are unchanged.

Production delta from the resolved merge tree: **-808 lines**. This removes
961 production lines and adds 153. The two deleted production files account for
413 removed lines. Combined store delta against main: **+799**, against the
-4,250 budget, or **5,049 over**. This is unfinished deletion work, not a budget
exception.

## Invariants and ports

A seed creates only the first deployment administrator. Restart, revocation,
rotation, expiry and credential pruning cannot reopen its consumed gate.
Generated seeds, different-seed and same-seed replica races, reopening and live
ancestry checks cover this. Failure injection covers capability insertion,
final gate consumption and a silently ignored gate write. Each rolls back both
rows and returns no authority. Non-bootstrap origins reject generated seeds.
The HTTP startup proof uses the actual helper, store and operator route without
a server socket.

A refused or stale capability changes no job and writes no event. Generated
current capability proofs retain cancellation's sensitive/reporting split,
tampered bearer refusal, holder/generation replacement, expiry, ancestor
revocation and bound-run deletion. Current transition proofs retain exact
canonical replay, absorbing completion and refusal after unstarted release.
The port inventory names every retired bootstrap/claim test and its destination.
The existing provisioning acceptance function now also checks the seeded Issue
path, readiness and refusal after both deployment administrators end.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and GOTMPDIR under /tmp/sw-astra-W1. Tests use -p 4.
Scratch is removed after checks. The shared cache is retained.

- Whole-repository build passes after the merge and legacy cuts.
- All 21 W1 plain acceptance functions passed after extending the provisioning
  proof, in 28.053s.
- Scoped store/controller/C2 authentication, issuance, rotation, launcher,
  renewal and new property tests passed in 11.835s / 2.189s / 0.006s.
- Real child-intake properties passed in 5.457s. Store, controller, nodehost,
  storage and acceptance test packages compile.
- Scoped vet, commentcheck over 2,205 files, documentation/changelog mirror
  tests, API snapshot comparison and diff whitespace checks pass.
- Store race selection passed in 72.374s. The final same-prefix seed and
  gate-write rollback selection passed with race in 15.732s. The first store
  race build could not run because
  referenced standard-library archives disappeared from the shared cache.
  Controller's race selection passed in 3.266s. The store retry kept the
  required cache and omitted redundant vet, which passed separately.

No full repository test suite, native PostgreSQL execution, product binaries,
pipelines or release actions were run. The swarm reserves those for the lead.
Legacy fixtures still using removed schema columns are not passing evidence.

## Contract changes needed and remaining integrations

**C2/C1 result provenance:** code inspection shows ReadAuthentication never
populates BindingState.Result. The current transition stores a canonical Go
request hash, while C2 requires the exact HTTP body digest, method, pattern and
actor fence for terminal result authentication. W2's HTTP replay test supplies
that record through a fake snapshot. The committing writer needs those trusted
HTTP facts in the shared contract before W1 can persist and project them. This
session does not add a context side channel, another issuer or a second replay
mechanism. Current store canonical replay passes. End-to-end HTTP exact-body
replay remains open.

- StateStoreOld and retained host scheduling, raw lifecycle/readiness, node
  queue, trigger/token writers, deletion/account authority and GitHub automation
  admission still need cuts and caller migration.
- Console/verified-local authority, console service issuance, SetRunnerMetered,
  runner poll/renewal observations, and persisted pool/repository management
  remain W1 work. Earlier completed roleless/session, credential, source audit,
  Actions enrollment and capability-management work is preserved.
- Rotation grace, SyncLauncher's store fence, the five credit-method moves,
  W1's harness move and the six dead contract tables remain complete. Relevant
  grace and launcher tests passed after integration.
- W1b retains all accounting, billing settlement, blobs, logs and materialization.
  Real transport/placement/provider integration, PostgreSQL locks and independent
  review remain with the lead and area owners. No native cell is newly claimed.

## Shared files

Beyond pkg/store, merge resolutions touch pkg/controller handlers.go, server.go,
engine_launcher_sync.go, engine_claim_http_test.go, claim_run_test.go,
claim_dispatch_internal_test.go, log_segments_internal_test.go,
metric_timestamp_test.go, prom_internal_test.go, team_tokens_test.go and the eight
integration-deleted conflict files: agents_test.go, chunked_body_test.go,
claim_route_internal_test.go, claim_run.go, execution_location_test.go,
here_runner_test.go, runner_scopes_test.go and tenant_routes.go. export_test.go loses an unused testing
import. bootstrap_token_test.go adds the real startup proof. server.go retains
the Operator reader calls required by W1's earlier Store wrapper cuts.

The remaining shared changes are .apidiff/pkg_controller.txt,
.apidiff/pkg_store.txt, design/rewrite/contracts/c1-data.md,
design/rewrite/contracts/t1-port-inventory.md, docs/auth.md,
docs/migrations/_unreleased.md, CHANGELOG.md and their embedded mirrors.

Friction: integration restored calls to removed Store APIs in legacy fixtures.
Their caller migrations are restored without compatibility aliases. The shared
cache failure above is a could-not-run result, not a race test failure.

STATUS: CONTINUE
