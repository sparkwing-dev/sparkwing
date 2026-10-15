# W1 continuation 31

W1 remains incomplete. This session retires the launcher-only claim path and
unused store authority APIs. W1b implementations and the lead-owned amendment
and handoff ledgers are unchanged.

`Store.ClaimLaunch`, `LaunchClaim`, `LaunchClaimRequest`, `MinLaunchLifetime`,
`LaunchDeadlineSlack`, its separate scan/matcher and its in-process cursor are
removed. `Client.ClaimLaunch` and the caller-clock fixture helpers are removed.
Controller fixture callers provision a service root, claim through `ClaimUnit`
and exchange its bootstrap through the real issuer. The new fixture test checks
the resulting plan/job kind, run, job, generation and direct-delivery authority.
The bootstrap verifier and initial run rows remain fixtures.

The unused `Tenant.SetRepoDispatch`, `Store.SeedToken`,
`ErrSeededTokenRevoked`, `Store.CheckClaimSensitive`,
`Store.PrincipalHoldsProfileClaim`, `Tenant.SpendSourceCredential`,
`Tenant.SourceClaimant` and `Tenant.GitCredentialMachines` are also removed.
They have no remaining Go callers. Current capability issuance, sealed intake,
binding transactions and source release retain their responsibilities.
The two retired SQL exceptions are removed from the scope guard.

This removes 512 production Go lines: 474 in pkg/store and 38 in the client.
No production code is added. The combined W1+W1b production delta against main
is +4,393 lines under the lead's exclusions for tests, testdata, acceptance and
contract files. The target is -4,250, leaving 8,643 lines to remove. The budget
is not met. Remaining legacy implementations still coexist with the rewrite.
This report requests no overage waiver and makes no area-complete claim.

Six old tests are retired with their API. Their explicit dispositions are in
t1-port-inventory.md. New store ports cover two funded jobs behind 257 unpaid
jobs, four concurrent polls reaching each funded job once behind 65 unpaid
jobs, and plan reservation followed by computation from first renewal. The plan
port excludes 40 seconds of scheduling wait and charges 70 seconds of compute.
C2's zero plan minimum replaces the legacy test's job minimum. Existing unit
billing tests retain the separate job minimum and root-derived metering.
The mutable repository opt-in tests retire with that feature. Retry sealing
and distinct owner-kind tests retain the sealed-dispatch requirements.

Invariant retained by the hard cut: launcher awards use the same current
authority, matching, funding and generation fence as all other unit claims.
Existing generated matching and award-fence properties pass. This session adds
no authorization mechanism, fallback, limit, schema, setting or environment
variable. The new helper exists only in tests.

Lead decision 8 is reviewed and kept. CreditSettings, SetCreditSettings,
SetOperatorCreditSettings, CreditRateTable and WarmCPUClassCores read or mutate
deployment-wide settings. Their Operator ownership matches C1 and they expose
no embedded Tenant or raw Store. No W1b file needed a correction.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR beneath /tmp/sw-astra-W1.

- Whole-repository `go build -p 4 ./...` passes.
- Scoped vet passes for pkg/store/..., pkg/controller/..., internal/acceptance/...
  and tests/acceptance/....
- The W1 selection passes 468 top-level tests and 1,461 cases with no skips.
  Reproduce from w1-session31-tests.txt by extracting top-level Test names from
  those pkg/store files, omitting names containing Postgres or Pg, then passing
  their anchored alternation to `go test -p 4 ./pkg/store -run ... -count=1`.
- All 20 W1 store-sqlite acceptance cells pass with no skips. The selection is
  `^(TestLifecycle|TestRecovery|TestPlacement|TestCredentials|TestBilling|TestSurface)/[^/]+/store-sqlite$`.
- Thirteen current controller/client tests pass, including 19 cases. Select
  `^(TestEngineClaimHTTP|TestEngineLauncherSync|TestLauncherFixture|TestEngineClientLauncher|TestEngineClientRecovery)`.
  The strengthened fixture assertion also passes after its final edit.
- Ten root-provisioning HTTP tests pass with no skips. Select
  `^TestRootProvisioningHTTP` in tests/acceptance.
- Race checks pass for TestLauncherConcurrentQueueClaimsEachAwardOnce and
  TestMeteredPlanRenewalExcludesSchedulingWait.
- commentcheck -base main . passes across 2,156 files. The API snapshots,
  documentation mirrors, embedded changelog, SDK symbol references and diff
  whitespace checks pass.

Two broader selections hit existing legacy fixtures. An overlay restoring every
changed Go file to HEAD and removing the new Go files reproduces both failures:
TestUnscopedWritersStayInTheDefaultTeam writes the absent tokens table, and
TestCronLauncherActive_TreatsAnUnclaimedRunAsStale writes the removed
triggers.pipeline column. Neither is claimed passing. The full package and
whole-repository test suites were not run. Early compile checks caught the
client's remaining removed result type and one unused import. The fixture's
first seed used TEXT for spec_json and was corrected to BLOB. The embedded
changelog mirror was synchronized after its drift check failed.

Remaining W1 work and real integration points:

- StateStoreOld, CreatePlanNode, legacy lifecycle/readiness, remaining raw
  claim/trigger/token writers and their controller/host callers still need
  retirement. The old controller HTTP tests still submit the retired wire and
  cannot count as final-route evidence merely because their claim helper moved.
- W2 must expose the approved trusted seeded-bootstrap presentation in C2's
  ProvisionSpec before W1 can implement it inside Store.Issue. SeedToken was
  unused and is not a substitute. No foreign contract was changed here.
- Console-session and verified-local authority composition, console runner
  metering and current console-origin service issuance remain open.
- A1-26 host observations still require ClaimProfileResolver. ClaimRequest
  supplies available CPU/memory but no total capacity, OS or architecture.
  Persisted enrollment alone cannot provide these observations. Native source
  release still requires its enrolled-executor resolver.
- Reconciliation materialization still needs W1b's transaction-local record
  copier wired to CopyMaterializedRecords. Calling public CopyMemo before the
  state transaction would not make the records and transition atomic.
- The controller still clears Execution for non-job claims, while plan/eval
  Cloud awards need resolved execution resources. W8's optional parent-expiry
  handling and production pricing/provider composition retain their separate
  integration obligations.
- PostgreSQL, native placements, actual Kubernetes adoption/teardown,
  HTTP/api.sock runtime assembly and A1-53 repository QA remain unverified.
  Launcher award snapshots and synchronization retain their inherited passing
  SQLite evidence. No additional native cell is unskipped or claimed complete.

Shared caller files changed are pkg/controller/claim_dispatch_internal_test.go,
pkg/controller/claim_run_test.go, pkg/controller/claim_source_test.go,
pkg/controller/export_test.go, the new pkg/controller/launcher_fixture_test.go
and pkg/controller/client/launcher.go. Deleting pkg/store/claim_run_test.go uses
the lead's explicit exception for callers in W1b test fixtures. Its sole
remaining old ClaimLaunch billing test is ported to the W1 tests above. No W1b
persistence, accounting or log implementation changed.

Other shared files are c1-data.md, t1-port-inventory.md, docs/api.md,
docs/migrations/_unreleased.md, CHANGELOG.md, their embedded mirrors and the two
changed API snapshots. No amendment or handoff ledger was edited.

The initial AGENTS orientation invoked the installed `sparkwing info --for-agent`
before reading the swarm brief. No candidate, stateful command, pipeline,
provider, deployment or release ran. No Git metadata was written, and the local
Git config has no identity or core.worktree override. The lead retains review,
committing, pipelines and landing. Assigned scratch is deleted after verification.
The shared build cache is retained.

STATUS: CONTINUE
