# W1 continuation 32

W1 remains incomplete. This session handles the new acceptance-harness cleanup,
removes the assigned exported catalogs and deletes the remaining mutable
repository-dispatch lookup. It starts at c4761eb99. The lead retains review,
committing, pipelines and landing.

Nine store harness files move from `internal/acceptance` into package tests.
Eight plain tests retain seven former SQLite cells and the owner portion of
`recovery-unstarted-lapse`. Their mapping is in t1-port-inventory.md. They open
through `pkg/store/internal/storetest`, create accounts and membership through
sign-in and invitations, enroll the planner, admit and claim the owner, and
issue expansion authority through `DelegateCapability`. There are no handwritten
row insertions in the moved files. Fault-injection triggers and lease/session
expiration updates remain. The native scenario catalog and lead ledgers are
unchanged.

Using real sign-in exposed an account-admission failure. C1 and the schema allow
an audit event before any team exists, but RecordBusinessEvent bound the empty
string to its team foreign key. It now binds NULL for absent teams. The invariant
is that account audit events created before membership stay outside every
tenant's event query. A property generates sixteen provider identities with
varied email punctuation and checks the null team and tenant query results.
The common audit writer changes only that SQL value expression. Billing,
settlement, blob and log implementations are unchanged.

LockOrder, SingletonWork, RetentionRules, CompletionEffects, EffectPhaseRanks
and CapacityLiveness are removed, along with their unused declaration types.
Singleton validation and outbox phase advancement stay in private implementation
code. The final outbox comparison still advances pending to running or terminal,
and running to terminal. A different terminal outcome remains an error. C1's
local symbol inventory and the store API snapshot match this cut. No foreign
contract signature changes.

The retired repository opt-in no longer runs through routeRunDispatchTx.
Its query used the removed repos.repo and repos.dispatch columns and retained
a call to synthetic planning-node insertion. The calls in Store.CreateTriggerWithRun,
Tenant.createTriggerWithRun, Store.CreateRetryWithRun and Store.EnqueueChildRun
are removed. Current intake and retry retain dispatch in their sealed request.
The legacy CreatePlanNode API itself remains for its other callers.

Line counts against this session's HEAD:

- Deleted 1,276 non-test Go lines and added 16, a net reduction of 1,260.
- Nine removed harness files contain 1,134 lines. A delegated owner-check call
  removes another three harness lines. The package tests retain the assertions.
- Store code shrinks by 123 lines including the catalog files. The lead's budget
  excludes contract files and acceptance, so its counted store reduction is 44.
- Combined W1+W1b production delta against main is +4,349. The target is -4,250,
  leaving 8,599 lines to remove. The budget is not met. No overage waiver is
  requested.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR beneath /tmp/sw-astra-W1.

- Whole-repository `go build -p 4 ./...` passes on the final production source.
- Scoped vet passes for pkg/store/..., pkg/controller/..., internal/acceptance/...
  and tests/acceptance/.... Store vet also passes after the final outbox edit.
- The W1 selection passes 477 top-level tests and 1,470 cases without skips.
  Reproduce from w1-session32-tests.txt by extracting top-level Test names from
  those pkg/store files, excluding names containing Postgres or Pg, and passing
  their anchored alternation to `go test -p 4 ./pkg/store -run ... -count=1`.
- All eight moved checks and the new account-audit property pass in that selection.
  The singleton fence and stale completion-acknowledgment regressions also pass
  after the final outbox edit.
- The thirteen remaining store-sqlite acceptance cells pass without skips.
  Select `^(TestLifecycle|TestRecovery|TestPlacement|TestCredentials|TestBilling|TestSurface)/[^/]+/store-sqlite$`
  in tests/acceptance. Those include the remaining placement, recovery and
  award-versus-start billing cells. No native cell is newly unskipped.
- commentcheck -base main . passes across 2,159 files. The API snapshot check,
  documentation mirror, embedded changelog and SDK symbol-reference checks pass.
  Diff whitespace checks pass.

A broader prefix selection included the existing
TestStoreFinishRefusesAnotherTeamsNode fixture. It fails because CreateRun
writes the removed runs.args_json column. An overlay restoring every changed
non-test pkg/store Go file to HEAD reproduces the same failure. This test is
not in the passing W1 selection. Full-package and whole-repository tests were
not run. Early compile checks caught two unused imports. Initial fixture attempts
correctly refused an invalid machine repository pattern, then exposed the signup
failure. Real expansion delegation also required carrying its owner's deadline
and expiry, which the old inserted capability had bypassed. These are corrected.

Remaining W1 work and real integration points:

- Continue moving the store recovery, matching, queue, enrollment and launcher
  harness checks. The existing shared recovery fixture still serves host and
  W1b checks. It must disappear when those consumers migrate. Keep the new
  package tests on real admission and issuance rather than copying that seed.
- Retire StateStoreOld, CreatePlanNode, remaining legacy lifecycle/readiness
  and raw claim/trigger/token writers, with their callers and retained findings.
  The unused production internal/runretry.Create path now has only test callers
  and is a possible next cut. Its source-reacquisition assertions need a caller
  migration when it is removed.
- W2 still needs the approved seeded-bootstrap presentation in ProvisionSpec.
  Store.Issue must accept it once, only before an admin credential exists. The
  removed SeedToken API is not a substitute.
- Console/verified-local authority composition, console runner metering and
  console-origin service issuance remain open. Prior completed credential,
  membership, cancellation-intent, session, rotation-grace and launcher-fence
  adapters retain their existing passing evidence. Lead decision 8 remains
  accepted because the five credit-setting methods belong to Operator.
- A1-26 still needs current host observations in production ClaimProfileResolver
  composition and the enrolled-executor resolver for native source release.
- Reconciliation still needs W1b's transaction-local record copier behind
  CopyMaterializedRecords. Public CopyMemo outside the lifecycle transaction
  cannot provide atomic materialization. The existing seam has fake coverage.
- Plan/eval Cloud claims need resolved execution resources on the controller
  projection. W8's parent-expiry handling and pricing/provider composition keep
  their separate integration obligations.
- PostgreSQL, native placements, Kubernetes adoption/teardown, HTTP/api.sock
  runtime assembly and A1-53 real-repository QA remain unverified.

Shared files changed are tests/acceptance/credentials_test.go,
tests/acceptance/lifecycle_test.go and tests/acceptance/surface_test.go, which
lose the moved wrappers. Internal/acceptance/store_unstarted.go stops invoking
the moved owner check because it is now its own package test. Store caller
migrations are confined to store.go, tenant_triggers.go and child_invocations.go.
Other shared artifacts are c1-data.md, t1-port-inventory.md, .apidiff/pkg_store.txt,
CHANGELOG.md and pkg/docs/changelog.md. The catalog edits in maintenance_contract.go
leave its retention and usage implementations intact. No W1b persistence,
accounting or log implementation or test fixture changes.

No Sparkwing binary, candidate, provider, deployment or release runs. No Git
metadata is written. Local Git config has no identity or core.worktree override.
There are no new settings, environment variables, limits, services, compatibility
paths or production interfaces. The assigned scratch is deleted after checks,
and the shared build cache is retained.

STATUS: CONTINUE
