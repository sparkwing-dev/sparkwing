# W1 continuation 40

Starts at 61167eb47. W1 remains incomplete. The lead owns review, commits,
pipelines and landing. No foreign Go production file or W1b persistence file
changes. W1b's credits_test.go loses only its retired RotateToken caller.

Removed Store.RotateToken and its private locked reader, rotation transaction
and overlap exception. Removed Tenant.MintGitHubRunnerCredential and its raw
token-table writer. Current capability rotation, provisioning, enrollment limits
and retention own those behaviors. Removed ReleaseConcurrencySlot,
ResolveCoalesceFollowers and ActiveConcurrencyHolder. Test callers use the
existing atomic ReleaseAndNotify and consume its follower/promotion results.
ConcurrencyHolder remains, with its explicit Superseded field.

Removed the unused Operator.UsageMetrics implementation, weekly report types
and test under frozen section 9.3's usage-report cut. Removed the unused
ListSpawnedChildrenByRun reader and its foreign-team test call. SpawnedChild
remains because internal/api still uses the decoration input type.

Attempt starts now derive executor kind, name and location from live capability
ancestry and immutable billing provenance. Machine subjects cannot impersonate
another executor through supplied metadata. Actions jobs, including jobs awarded
by an Actions trigger host, retain the original push's repository and workflow
run identity. Local host and cloud pod names remain bounded, filtered display
observations. No new interface, setting, environment variable or customer limit
is added. This is the existing transition's attribution owner, not a new issuer.

The invariant is that attempt attribution comes from current claim ancestry;
caller metadata cannot substitute repository, workflow, executor class or
location. The new generated SQLite property failed with persisted forged
metadata before the fix. All 48 direct/hosted machine/Actions cases now pass
through ClaimUnit, TransitionNode and ReadRunView. Native host observations and
pod-instance correlation remain integration work.

Production Go delta, excluding tests, testdata, acceptance and contract files:
59 added, 503 deleted, net -444. The physical and budget-counted deltas agree.
W1+W1b is +2,817 against main, versus its -4,250 target: 7,067 lines over.
Legacy code remains. No budget waiver is requested.

Verification passes on the final source:

- Whole-repository go build -p 4 ./.... Scoped go vet -p 4 on pkg/store/...
  and pkg/controller/.... All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false,
  GOMAXPROCS=4, GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR inside the assigned
  scratch. w1-session40-tests.txt records the exact successful selectors.
- 149 store tests and 11 controller tests pass with no skips in those selections.
  The current SQLite plan-seal, state-transition, generator, matching, launcher,
  credential and eight recovery proofs remain green. No native cell is newly
  activated. Rotation grace, the launcher store fence, the five Operator credit
  methods and the earlier W1 harness/table cuts remain intact.
- commentcheck -base main is clean across 2,188 files. API snapshot comparison,
  documentation-symbol test, changelog check, embedded docs/changelog drift and
  whitespace checks pass.

Ports and retained limits:

- Old rotation team/metering assertions use the existing capability scope
  property and metered-machine test. Raw ambiguity lookup/revocation tests stay.
- Actions issuance-limit coverage uses the existing concurrent enrollment test.
  The new expiry test proves expired roots free quota while retaining all old
  authority rows, replacing the old raw-token sweep assertion. Existing Actions
  enrollment and push-scope properties retain push persistence and refusals.
- TestGitHubRunnerAttemptRecordsWorkflowRun is replaced by the generated final
  claim/start/read-view property described above.
- The weekly deployment report test is dropped with its deleted feature.
- A broader legacy concurrency selection remains red at CreateRun's args_json
  write. The same TestConcurrency_GrantedWhenSlotAvailable failure reproduces
  against an overlay restoring every changed Go file from HEAD and omitting the
  new files. Those suites are not represented as passing. Their seeding, legacy
  memo/coalescing and terminal fixture paths still require migration.

Contract changes needed and real integrations left open:

- W1b still must implement CopyMaterializedRecords. This branch has only the
  transaction-local contract and fake-copier tests. No real byte-copy proof is
  claimed. W9 ExecutionJob still omits committed result selectors. W9 must carry
  them, W3 must consume them and W7 must authorize the selected records/bytes.
- C2 ProvisionSpec still lacks the seeded bootstrap presentation required by
  lead decision 07:40 item 3. Add that foreign declaration before W1 implements
  the one-use Store.Issue input. No alternate bootstrap issuer was added here.
- Console/verified-local authority, SetRunnerMetered, console service issuance,
  host/source observations and production pool-rule composition remain W1 work.
- StateStoreOld, raw lifecycle/readiness, claim/trigger/token writers, deletion
  admission/account anonymization and legacy GitHub automation admission remain.
  The latter still names removed trigger columns, as in continuation 39.
- PostgreSQL locking, native placements, api.sock, Kubernetes, provider billing
  and repository gates remain owner/lead integration evidence. Shared recovery
  fixtures remain while host/billing consumers still reference them.

Caller migrations are confined to pkg/store tests: concurrency_burst_test.go,
concurrency_cost_test.go, concurrency_property_test.go,
concurrency_scope_property_test.go, concurrency_test.go,
delete_run_cache_test.go, tenant_user_keys_test.go, tokens_test.go,
tenant_writer_test.go, credits_test.go, github_runner_bindings_test.go,
execution_attribution_test.go and tenant_node_state_test.go. The SQL-scope guard
loses only the deleted usage report's allowlist entry. Shared artifacts are C1,
the port inventory, .apidiff/pkg_store.txt, CHANGELOG.md, the migration guide
and its generated mirrors. Lead-owned handoffs and amendments are unchanged.

Friction is the remaining legacy fixture/schema mismatch and the missing
cross-area contracts above. This is a focused passing selection, not a green
whole-store suite. No Git writes, Sparkwing binaries, external messages,
deployments, releases or harness-memory writes ran. Git config has no user
identity or core.worktree override. Assigned scratch is deleted after final
verification. The shared build cache is retained.

STATUS: CONTINUE
