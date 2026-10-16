# W1 continuation 33

W1 remains incomplete. This session starts at 209d9b3c7. The lead retains
review, committing, pipelines and landing. W1b implementation files are unchanged.

Terminal result selectors now persist in `nodes.result_records` with state,
attempt closure and the result event. `NodeRow.OutputRecord` and
`NodeRow.ManifestRecord` expose them through ReadNode, ReadSnapshot and
reconciliation. A materialized job retains attempt zero. Result reads do not
infer keys from an attempt or depend on retained events. The local C1 contract,
schema, API snapshot and source/embedded documentation describe this addition.
There is no schema migration, following the frozen disposable-store contract.

The invariant is that readers return only the keys committed for that job's
terminal result, and a failed attachment or copy exposes none.
`TestTerminalRecordSelectorsProperty` generates 32 optional output/manifest pairs
across reports, memo hits, manual retries and coalesced results. It checks
reconciliation, terminal replay, ReadNode and ReadSnapshot after deleting events,
and refuses foreign-tenant reads. Attachment failure, holder expiry, copy failure
and an injected event-write failure roll back state, selectors and attached rows.
The record seams use fakes. These checks do not establish physical record-copy
or byte-integrity coverage.

The unused production `internal/runretry.Create` implementation is deleted,
along with `Store.CreateRetryWithRun`, `Tenant.SetRetriedAs` and
`InheritSecretArgs`. There are no remaining Go references to them. Retry tests
now use storetest, real sign-in/admission and shared sealing. A property generates
twelve pinned sources, rejects foreign tenants and forged team selectors,
retains source/secret classification, checks masked display and claims an owned
retry. Missing-source refusal and verified App repository/default-branch facts
have focused tests. A retry cannot repeat the original webhook delivery.
The obsolete reverse-link and invocation-copy tests are removed. The source
reader guard loses the deleted retry file and a stale entry for the already
absent agent-loss implementation.

`internal/orchestrator/pipeline_ref_dispatch_test.go` supplies the recorded
source inputs directly to its dispatch check. Its former foreground/detached
trigger-copy assertions are replaced by sealed-intake assertions. Reacquisition,
selected pipeline bytes, subject bytes and worktree cleanup remain asserted.
That full runtime check reaches the admission socket and fails with
`connect: operation not permitted` in this sandbox. It is unverified here.
The two source-directory selection/refusal checks pass. The initial fixture
attempt also required disabling the user's Git commit signing for temporary
fixture commits. The command uses GIT_CONFIG_COUNT=1,
GIT_CONFIG_KEY_0=commit.gpgsign and GIT_CONFIG_VALUE_0=false, without writing Git
configuration.

Three more harness files move into pkg/store tests and are deleted from
internal/acceptance:

- `store_actions_enrollment.go` becomes TestStoreActionsEnrollment.
- `store_enrollment.go` becomes TestStoreEnrolledClaims.
- `store_launcher_sync.go` becomes TestStoreLauncherSync.

All three pass. They open through storetest and seed accounts, enrollment,
credentials, runs, plans and ready work through real store APIs. There are no
hand-written INSERTs in the moved tests. Existing enrollment observations and
fault setup retain UPDATEs. Enrollment/revocation, reopening, granted identity,
award ancestry, bootstrap exchange and stale Job cleanup retain their assertions.
The placement wrappers are removed. The enrolled-claim check was part of the
still-present matching cell, so two SQLite wrappers disappear, leaving eleven.
The native scenario catalog and lead ledgers are unchanged.

Net production Go lines against this session's HEAD:

- Deleted 545 and added 21, net -524, excluding test files.
- The deleted retry implementation accounts for 159 lines. The three moved
  harness files account for 312 lines. Store production deletes 74 and adds 21.
- The lead's counted store reduction is 53 lines. Combined W1+W1b against main
  is +4,296 in this checkout. The target is -4,250, leaving 8,546 lines to remove. The budget is
  not met. No overage waiver is requested.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR beneath /tmp/sw-astra-W1.

- Whole-repository `go build -p 4 ./...` passes on the final production source.
- Vet passes for pkg/store/..., pkg/controller/..., internal/orchestrator,
  internal/acceptance/... and tests/acceptance/....
- The W1 batch passes 484 top-level tests and 1,514 cases without skips.
  The final secret-display assertion passes in a focused rerun. The source
  reader guard also passes separately. The reproducible file selection in
  w1-session33-tests.txt includes that additional guard, for 485 top-level tests.
  Extract Test names from those pkg/store files, exclude Postgres/Pg names and
  pass their anchored alternation to `go test -p 4 ./pkg/store -run ... -count=1`.
- Eleven remaining store-sqlite acceptance cells pass without skips. Use
  `^(TestLifecycle|TestRecovery|TestPlacement|TestCredentials|TestBilling|TestSurface)/[^/]+/store-sqlite$`
  in tests/acceptance. No native cell is newly unskipped.
- Five controller checks pass: TestEngineJobLifecycleRenewsAndReplaysUnderStoreFences,
  TestEngineReportedFailureReachesRecovery, TestEngineJobResultPreservesRecordsAndRetry,
  TestEngineMaterializeOnlyReturnsPrivateExecutionStateWithoutAward and
  TestEngineExpansionReplayReturnsPersistedState.
- commentcheck -base main passes across 2,164 files. The API snapshot check,
  docs mirror, embedded changelog and symbol-reference checks pass. Diff
  whitespace checks pass. Full-package and whole-repository tests are not run.

Contract changes and real integration points left open:

- W1b must implement CopyMaterializedRecords behind the existing store-local
  transaction interface. It must copy memo and terminal-result edges into the
  caller's transaction, including destinations with no execution attempt. The
  public CopyMemo transaction is not an atomic substitute. W1 can bind that
  implementation after it exists. No new persistence interface is requested.
- W9 must add the two committed record selectors to controllerwire.ExecutionJob
  and project them in controller.executionJob. W3 must consume those selectors
  for materialized output/artifact reads. This is a foreign contract change,
  so its declaration and implementation are unchanged here. W7 must retain
  record authorization and byte verification after selecting the key.
- The approved seeded-bootstrap presentation is still absent from C2
  ProvisionSpec. W1 cannot implement that input through its declared interface.
  Store.Issue still needs its one-use, no-existing-admin transaction once C2
  provides it. No legacy token mint is restored.
- Continue retiring StateStoreOld, CreatePlanNode, raw lifecycle/readiness,
  claim/trigger/token writers and their callers. Matching/queue and recovery
  harness moves remain. The shared recovery fixture still serves host and W1b
  checks. Console/verified-local authority composition, SetRunnerMetered,
  console-origin services and current host/source observations remain open.
- Rotation grace, the Operator placement of the five credit-setting methods,
  the store launcher fence and the six dead-table cuts were completed earlier.
  Their passing evidence remains. They were not reimplemented here.
- PostgreSQL, native placements, Kubernetes adoption/teardown, real HTTP/api.sock
  assembly, pricing/provider composition and A1-53 repository QA remain unverified.

Shared caller files are internal/orchestrator/pipeline_ref_dispatch_test.go,
internal/runretry/create.go (deleted) and tests/acceptance/placement_test.go.
The store source-reader guard also drops the deleted caller's catalog entry.
Other shared artifacts are c1-data.md, t1-port-inventory.md, .apidiff/pkg_store.txt,
CHANGELOG.md, docs/migrations/_unreleased.md and its embedded mirrors.
Handoffs and amendment ledgers are unchanged.

No production interface, service, setting, environment variable or compatibility
path is added. The local contract change adds two NodeRow fields and one nullable
schema column because terminal state must retain its own record selectors.
No Sparkwing binary, provider, deployment or release runs. No repository Git metadata is
written. Local Git configuration has no identity or core.worktree override.
The assigned scratch is deleted after checks. The shared build cache is retained.

STATUS: CONTINUE
