# W1 continuation

Starts at 4611fbb05. W1 remains incomplete. The lead owns committing and
integration review. This continuation changes no W1b implementation or test file.

## Changes

Removed the raw Store ResetPipelineProfile, ResetAllProfiles and ProfileSamples
methods: **9 implementation lines**, plus obsolete API and package documentation.
The tenant methods remain. Local stats reset chooses DefaultTeam explicitly.
The local capacity dashboard reads through the request tenant. It refuses an
account with no registered team instead of returning default-team profiles.

The invariant is that profile reads and resets cannot expose or alter another
team's data, including identical or adversarial pipeline and job keys.
TestProfileTenantIsolationProperty covers 96 generated key pairs and both reset
forms. TestCapacityReadUsesRequestTenantProperty covers 96 requests with forged
query selectors, plus missing and unknown teams. Both use real SQLite storage.
The controller test invokes the real handler through httptest without a socket.
An overlay of HEAD's browser_reads.go and capacity.go reproduces the leak:
the first generated request receives default-only profiles with HTTP 200.
The same test passes against the working tree.

Existing profile tests now use Tenant. The profile-history fixture also supplies
the required team and scopes its SQL to that team. Its 38 subcases pass against
the final schema.

Two active SQLite acceptance cells were added:

- TestRecovery/recovery-unstarted-lapse/store-sqlite exercises five lapses and
  reopenings, exact event sequences, stale-holder refusal, zero disruptions and
  retries, advancing generations and first execution at attempt one. It leaves
  one run and one attempt.
- TestPlacement/placement-granted-identity-and-capacity/store-sqlite exercises
  named and queued claims across eight matching cases. It uses real matcher,
  authority, claims, events and award persistence. The enrollment resolver is a
  contract fake. Matching refusals must have the expected error class and leave
  no events or billing awards. Subsequent valid claims prove capacity, label,
  repository and pool restrictions can be satisfied.

The matching cell extends seven existing placement ports into acceptance. Their
source identities and coverage are recorded in t1-port-inventory.md. Their old
fixtures were already removed. No new archived source-function identity is
claimed. Native placements, process startup, launcher settlement and metered
success remain outside these proofs. No native skeleton skip was removed.

## Amendment audit

The handoffs.md entries added by the merged W4 review assign work to W3, W5, W8
and W9. None assigns a new W1 action.

| Amendments naming W1 | Store status and remaining integration |
| --- | --- |
| A1-07, A1-11, A1-25, A1-44, A1-45 | Existing acceptance/seal/settings enforcement remains. W2 supplies trusted intake facts and decryption. |
| A1-08, A1-13 through A1-18, A1-26, A1-55 | Existing matching, reconciliation, failure, cancellation, projection, expansion replay, ordinals and here reclaim remain. This continuation adds matching and unstarted-lapse acceptance evidence. Real enrollment and host/transport integration remain external. |
| A1-20 through A1-24 | Store provisioning, authentication snapshot, binding, data authorization and source-release adapters remain. W2 must install origin providers, route registration and exact-body result evidence. Resolved slot binding still lacks C2's target key. |
| A1-27 | Lifecycle accounting is still open. W1b has ledger helpers, but the remaining contract gaps below prevent a complete join. No partial metered path was enabled. |
| A1-28 through A1-35 | Payment, blob, upload, record and log persistence belong to W1b. W1's transactional materialization/result interfaces still need that owner's implementation. Physical bytes and transport remain W7/W2 work. |
| A1-38 | SyncLauncher still needs the W8/W9 generation-to-Kubernetes identity contract. The controller method remains a stub. |
| A1-40 | Existing controller recovery adapters remain. W8 owns LocalRuntime readiness and lifecycle wiring. |
| A1-51, A1-53 | SDK corpus, generated surfaces and native drivers retain their owning areas. This continuation adds store proofs and leaves native/platform cells pending. |

## Contract changes needed and open work

- W1b's recordBillingAwardTx opens an interval immediately for at_award. Its
  settlement helper rejects RefundUnstarted when intervals exist. W1 needs an
  agreed transactional unstarted-release operation that preserves immutable
  provenance/pricing while allowing the required full refund. The public
  lifecycle cannot merely feed the existing interval to C2 as unstarted.
- C2's SettleBilling remains a panic stub. W1b's calculator-injection helper
  supports component tests, but there is no completed production calculation.
- RenewUnit's executing flag records first execution only. No declared host
  operation names entry to or exit from customer-owned runner waits. That
  operation is needed before metered host intervals can exclude those waits.
- W1b must implement materializationRecords.CopyMaterializedRecords and
  nodeResultRecords.AttachResultRecords in the calling transaction. Its public
  RecordStore methods start their own transaction and cannot be substituted.
- StateStoreOld, old plan/claim/trigger writers and offer rounds still need
  retirement. Offer finalization has a live W8 fallback caller. Old requeue and
  sweep APIs also have W1b-owned test callers. No replacement shim was added.
- Completion delivery, physical retention/GC, real origin/enrollment policies,
  exact-body replay evidence, launcher correlation and native drivers remain
  the integration points listed in w1-amendments-progress.md.

No foreign contract shape, billing policy, schema, limit, setting, environment
variable or production mechanism was added. The local C1 documentation records
the three retired Store twins. The dashboard now uses its existing tenant
selection. The local stats command retains its default-team scope.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Package parallelism is -p 4.

- Whole-repository go build -p 4 ./... passes.
- go vet -p 4 passes for pkg/store, pkg/controller, pkg/match,
  internal/acceptance, tests/acceptance and cmd/sparkwing.
- The previous exact store file selection plus provision_origins_test.go,
  profile_scope_property_test.go, capacity_reset_test.go, capacity_samples_test.go
  and capacity_test.go passes **271 top-level tests / 733 cases** without skips.
  Extract top-level Test names from those files and the Earlier verification
  file list in w1-amendments-progress.md, then join them into an anchored -run
  alternation. The separate -run '^TestProfileHistory$' passes **39 cases**.
- pkg/controller -run '^(TestEngine|TestCapacityReadUsesRequestTenantProperty)'
  passes **34 top-level tests / 57 cases**.
- cmd/sparkwing -run '^(TestRunCapacityReset|TestGroupCapacityStats_)' passes
  **6 tests**.
- Full tests/acceptance passes **20 top-level tests / 207 executed cases** with
  **2,995 native/platform skips**. Full pkg/match passes **14 tests / 42 cases**.
  internal/acceptance has no package tests.
- Required commentcheck -base main reports only the existing detached
  HereBearer comment in internal/controllerwire/contracts.go. That W9 file is
  unchanged. commentcheck -base HEAD is clean for this continuation.
- Go files are formatted. git diff --check passes. Git has no local user.name,
  user.email or core.worktree override.

The full legacy suite, native PostgreSQL, races, lint, delivery pipelines,
Docker, operating-system matrices and independent review remain lead checks.
The aggregate API baseline still needs regeneration with the merged rewrite.

## Shared files and friction

Production caller migrations outside pkg/store are
cmd/sparkwing/runs_capacity.go and pkg/controller/browser_reads.go. The added
controller fixture is pkg/controller/capacity_tenant_test.go. Other shared files
are internal/acceptance/store_recovery.go, store_unstarted.go and
store_matching.go, tests/acceptance/placement_test.go, c1-data.md,
t1-port-inventory.md and the W1 handoffs.

The matching fixture first failed on empty rather than nullable capability
bindings, and then on nullable rather than empty required seal hashes. Its
projection now follows the schema and envelope. Those were fixture errors.
The main-based commentcheck finding remains the only failure against the working
tree in the reported selection. The baseline-overlay failure is expected.
No Sparkwing binary, daemon, provider call, Git write,
deployment or release ran. Scratch is deleted before reporting.

STATUS: CONTINUE
