# W1 continuation: legacy generation APIs and explicit tenants

All 26 merge conflicts have resolved working-file content. Integration deletions
are preserved, documentation changes are combined, and no conflict marker
remains. Git still lists the unmerged index entries because SWARM-BRIEF forbids
git writes. The lead must stage the resolutions and finish the merge. The first
whole-repository build after resolution passed. The legacy source-credential
test restored by the conflict is removed with its retired client API.

Production Go delta this session is **-251 lines**: store.go -228, metrics.go -5,
runfilter_http.go -18. The three files delete 262 lines and add 11 replacement
GoDoc lines. Using the lead's production filter against main, this worktree's
combined W1/W1b store delta is **+2,090**, against **-4,250**, or **6,340 over**.
The area is incomplete. No new production mechanism was added in this session.

## Changes

- Removed `FinishRunAtGeneration`, `FinishTriggerAtGeneration`,
  `ReleaseClaimAtGeneration` and `TriggerClaimGeneration` from both Store and
  Tenant. Completion uses `FinishRunAndUnit`. Unstarted release uses `ReleaseUnit`
  with the awarded capability. No forwarding API remains.
- Removed 19 default-team Store wrappers, including the last three generation
  wrappers above. Remaining operations select their tenant explicitly. The
  migration guide lists the complete cut. Production callers already selected
  tenants, so caller migrations are test-only.
- Removed unused `SupportsRunIdentityFilters`, `SupportsRunCursor`,
  `SupportsRunDisplayFilters` and their parser. The advertised filter version
  and current request parsing remain.
- Updated C1, the port inventory, public migration guide, changelog, generated
  documentation mirrors and the store API snapshot. Lead-owned amendments and
  handoffs are unchanged. No materialization or W1b production file changed.

## Ports and evidence

Invariant: a stale owner cannot change the successor's verdict, owner unit,
billing or events.

`TestOwnerFinishRaceRejectsStaleAwardProperty` replaces the old 500-iteration
generation race with 24 generated cases using real issuance, claim, release,
reclaim and completion. Stale failure races current success. Same-verdict stale
replay and release cannot change the completed snapshot or billing. The two
legacy team-generation tests are removed. Current completion authority and
rollback properties retain their valid requirements. A trigger whose team
differs from its run is invalid under the final composite foreign key.

`TestReleaseUnitOwnerFenceProperty` checks 192 forged trigger, plan and eval
releases. Tenant, run, source generation, claim generation, holder, award prefix,
presented prefix and job selectors cannot change unit rows, run state or billing.
Successful release and repeated-release refusal remain covered separately.

`TestFailedRunClosesOwnerAndKeepsVerdict` uses real admission and owner claiming.
The failed run retains its masked error, its queue owner closes without a lease,
completion replays exactly, and a conflicting success changes no state. The
owner's queue status is `done`; the run carries the separate failed verdict.

The two legacy here tests that required only one claim are removed under A1-55.
`TestClaimUnitHereWindowAndReclaim` verifies the permitted reclaim behavior.
The stale legacy billing-finish assertion is retired in favor of the current
stale-settlement property. The old leftover mutable trigger credit-window test
is removed because final awards use immutable generation-bound billing records.
These are caller cuts in W1b's test fixture, under the explicit scope exception
in handoffs.md. W1b implementation is untouched.

Checks passed:

- Whole-repository `go build ./...` with GOWORK=off, GOFLAGS=-buildvcs=false and
  the assigned cache/scratch paths.
- All 21 W1 plain `TestStore...` acceptance tests selected by their exact names,
  plus current completion, release, billing, here reclaim, rotation-grace,
  launcher-snapshot and owner-recovery properties. The selection took 16.444s.
- Focused race checks for the new owner race, forged owner releases, failure
  completion and concurrent completion winner: 22.980s.
- Test compilation and `go vet -p 4` for store, controller, nodehost and opsview.
- `go run ./internal/commentcheck -base main .`: clean across 2,139 files.
- Documentation mirror check, store API snapshot and `git diff --check`.

The broader selection also reached the legacy
`TestStoreFinishRefusesAnotherTeamsNode`, which fails because CreateRun inserts
the removed runs.args_json column. Restoring store.go and that test file from
session-start HEAD with a Go overlay reproduces the same failure. The full
legacy suites are not claimed passing. Native catalog cells remain pending.

## Shared files

Mechanical tenant caller migrations outside pkg/store:

- internal/nodehost/attempt_test.go, closures_test.go, debug_test.go.
- internal/opsview/doctor_root_unix_test.go.
- pkg/controller/chunked_body_test.go, compute_limits_test.go, crons_test.go,
  crons_tick_test.go, cycle_detection_test.go, execution_location_test.go,
  here_runner_test.go, metric_timestamp_test.go, profiles_coverage_test.go,
  prom_internal_test.go, release_pause_test.go, retry_test.go,
  secret_args_display_test.go, trigger_test.go, trigger_user_test.go.

Other conflict resolutions are the 26 paths still named by
`git diff --name-only --diff-filter=U`. Their deleted source and fixture files
remain deleted. The retained conflicting runner-scopes test and default-team
caller inventory follow the integration cut. The source-credential conflict
preserves the removal of the source-mint counter test and drops its retired
trigger-client case. Public API/migration docs and mirrors retain both sides.
The store's credits_test.go, trigger_claim_credits_test.go and
node_output_test.go changes are confined to caller migration or retired callers.

## Remaining work and handoffs

- C2 ProvisionSpec still has no seeded presentation field. W1 must implement
  the one-use Store.Issue input after that declaration arrives. There is no
  alternate issuer and no foreign signature edit here.
- Console/verified-local authority, console service issuance, SetRunnerMetered,
  host/source observations and pool-rule composition remain W1 work.
- StateStoreOld, the retained node-host scheduler, raw lifecycle/readiness,
  legacy node/trigger/token claims, deletion/account authority cutover and
  GitHub automation intake still need their hard cuts and caller migration.
- Rotation grace, the SyncLauncher store fence, the five credit methods on
  Operator, W1's harness moves and the six dead contract tables remain complete.
  This session reverified grace and launcher proofs. Remaining store harness
  files concern W1b data, logs, billing or fixtures those consumers still need.
- Materialization remains W1b's end-to-end responsibility. Native PostgreSQL,
  HTTP/api.sock composition, Kubernetes, provider billing and repository
  pipelines remain real-integration evidence owned by the lead or other areas.

Friction: the unmerged index requires lead staging, retained fixtures still
target removed schema columns, and remaining legacy store APIs are coupled to
the retained host/controller runtime. No release, service, deployment, external
message or harness-memory write ran. Git configuration has no core.worktree or
repository-local user identity override. Assigned scratch is deleted after
verification. The shared build cache is retained.

STATUS: CONTINUE
