# W1 continuation 34

W1 remains incomplete. This session starts at 72cfb8dde. The lead retains
review, committing, pipelines and landing. W1b implementation files are unchanged.

Removed Store.CreatePlanNode and its insertion helper. Admission creates the
operational planning or trigger unit. Removed FinishRunIfActive and
FinishRunsIfActive from Store and Tenant. Their final replacement is
Tenant.FinishRunAndUnit. No production caller used these removed methods.
The three remaining caller files were test fixtures. Controller fixtures lose
the obsolete planning-node insertion. The orchestrator's daemon fake finalizes
one run at a time through FinishRun and preserves already-terminal runs.
There is no production compatibility path.

Run-completion tests now use storetest, real account sessions, machine enrollment,
sealed admission, owner claims and accepted empty plans. The invariant is that
only the current tenant's live owner may commit the verdict, and a refusal
leaves the run, owner, events, billing and completion intents unchanged.
TestRunCompletionHasOneCommittedWinner races eight requests across two SQLite
handles. One commits and seven replay. Conflicting terminal reports fail and
preserve the finish time. TestRunCompletionAuthorityProperty checks forty
generated authority, generation, holder, run, tenant and actor mutations, plus
four invalid target states. TestRunCompletionFailureRollsBackOwnerAndVerdict
injects an event failure and proves atomic rollback before a successful retry.
The old cross-run batch rollback test retires with the batch API. Per-run
owner/outbox rollback remains covered by TestFinishRunAndUnitCommitsTogetherAndReplays.
The port inventory records these replacements.

Two harness checks move into pkg/store tests and their source files are deleted:

- TestStoreClaimMatching replaces store_matching.go and retains sixteen
  named/queued cases. Accounts, machines and the metered machine root are issued
  through real APIs. The metered root requires deployment authority. Every
  mismatch preserves the run/job snapshot and event, billing and effect counts.
- TestStoreQueueOrder replaces store_queue_order.go. FIFO and preference cases
  use real plans, readiness transitions, Actions registration, issuance and
  admission. Foreign repository scope is refused. An ineligible job retains its
  complete row across repeated scans and is acquired when an eligible runner
  arrives.

These moved files contain no hand-written INSERTs. The pool-toleration case
retains its projection UPDATE for the positive matching check. It does not prove
pool-rule mutation or policy composition. Enrollment/capacity observations use a
fake resolver. The queue-count helper remains beside its unstarted-recovery
caller. Shared recovery fixtures still serve other store, host and billing
checks. The two placement wrappers are removed. Native scenario catalogs and
lead-owned handoff/amendment ledgers are unchanged.

Net production Go lines against this session's HEAD:

- Deleted 431 and added 16, net -415, excluding test files.
- Store production deletes 123 lines and adds none.
- Moving the harness deletes 308 lines and retains a 16-line recovery helper.
- The lead's store-budget measure is +4,173 against main. The combined W1+W1b
  target is -4,250, leaving an 8,423-line overage. The budget is not met. No
  overage waiver is requested.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR beneath /tmp/sw-astra-W1.

- Whole-repository go build -p 4 ./... passes on the final production source.
- Vet passes for pkg/store/..., pkg/controller/..., internal/orchestrator,
  internal/acceptance/... and tests/acceptance/.... Controller/orchestrator vet
  passes again after the final fixture edits.
- The W1 batch passes 490 selected top-level tests and 1,540 test/subtest results
  without skips. w1-session34-tests.txt records the package-local source files.
  Extract their Test names, exclude names containing Postgres and names starting
  TestPg, and pass their anchored alternation to go test -p 4 ./pkg/store -run
  with -count=1 -timeout=5m.
- Nine remaining store-sqlite acceptance cells pass. The selector is
  ^(TestLifecycle|TestRecovery|TestPlacement|TestCredentials|TestBilling|TestSurface)/[^/]+/store-sqlite$.
  No native cell is newly unskipped.
- commentcheck -base main passes across 2,167 files. The generated API snapshot
  matches .apidiff exactly. Embedded docs/changelog and symbol references pass.
  Diff whitespace checks pass.
- TestPlanRoute_AcceptsReplaysAndRefuses fails during its legacy CreateRun setup
  with table runs has no column named args_json. The same check against HEAD's
  original controller fixtures and CreatePlanNode, restored through a Go overlay,
  fails for the same reason. This is pre-existing legacy-fixture debt. No claim
  is made that the controller's legacy plan or log suites pass.

Contract changes and real integration points left open:

- No foreign contract change is made. C1 records the legacy removals.
- W1b still must implement CopyMaterializedRecords in the existing store-local
  transaction interface. Public CopyMemo cannot substitute for atomic state and
  record materialization. This session does not establish physical record-copy
  or byte-integrity coverage.
- W9 must add committed OutputRecord and ManifestRecord selectors to
  controllerwire.ExecutionJob and project them in controller.executionJob.
  W3 must consume them for materialized output and artifact reads. W7 retains
  record authorization and byte verification after selection.
- C2 ProvisionSpec still lacks the approved seeded-bootstrap presentation.
  Store.Issue needs that input before it can implement the one-use,
  no-existing-admin transaction. No legacy token mint is restored.
- StateStoreOld, raw lifecycle/readiness, old claim/trigger/token paths and their
  remaining callers still need retirement. Recovery harness moves remain.
- Console/verified-local authority persistence and composition, SetRunnerMetered,
  console-origin services and current host/source observations remain W1 work.
  Production pool-rule mutation/composition is also unverified by these tests.
- Rotation grace, the five Operator credit settings, the store launcher fence
  and the six dead contract tables were completed before this session. Their
  previous evidence remains. They were not reimplemented.
- PostgreSQL, native placements, Kubernetes adoption/teardown, real HTTP/api.sock
  composition, providers and repository QA remain unverified here.

Shared caller files are pkg/controller/claim_dispatch_internal_test.go,
pkg/controller/log_segments_internal_test.go and
internal/orchestrator/wingd_e2e_test.go. The log fixture change removes only the
obsolete API call, under the lead's caller-migration exception. Other shared
artifacts are tests/acceptance/placement_test.go, c1-data.md,
t1-port-inventory.md, .apidiff/pkg_store.txt, CHANGELOG.md,
docs/migrations/_unreleased.md and its embedded mirrors.

No production interface, service, setting or environment variable is added.
No Sparkwing binary, provider, deployment or release runs. No Git metadata is
written. Local Git configuration has no identity or core.worktree override.
The assigned scratch is deleted after checks. The shared build cache is retained.

STATUS: CONTINUE
