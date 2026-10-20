# W1 session 55: integration merge, reporting and raw job finish cut

W1 remains CONTINUE. HEAD is bcb1e823b. The pending integration merge names
d5753fce43b6c7df62351a101b3a7583b9e70269. No Git metadata writes ran.

## Merge state

All 20 conflicted paths have resolved working-tree contents. Both sides' file
deletions are preserved. Integration's output-free storage signatures and new
log fixtures win. W1's prior claim/lifecycle cuts remain removed. Migration
source preserves nonconflicting integration additions and W1's conflicting
section. Its embedded copy is regenerated.

The index still records the 20 paths as unmerged because the brief forbids Git
writes and this sandbox cannot write worktree metadata. The lead must stage
these resolutions and finish the merge. No deleted file was restored.
Lead-owned handoffs.md and amendments-*.md are unchanged.

## Implemented and deleted

Run and trigger reports now project request_json/spec_json through the existing
C4 DisplayRequest/DisplaySpec functions. Before valid classification every arg
is masked. Accepted secret values stay masked. Raw execution and retries still
read private typed snapshots. Malformed JSON fails instead of silently losing
classification. Run.Number reports the allocated tenant number.

Run filters use repo_display and repo_key instead of removed repository
columns. Trigger displays share the run scanner and read queue-only facts from
triggers. This deletes the duplicate trigger scanner, trigger_env read and
bounded in-memory repository scan. Trigger repository filtering now runs in SQL.
Pending run cursors include NULL started_at through zero coalescing. Reported
StartedAt remains zero until execution starts.

GetNode/ListNodes now select final generation/attempt columns and actual stored
metadata. The node_outputs table and removed claim/accounting columns no longer
block these readers. Attempt history still comes from node_attempts. Typed
ReadNode/ReadSnapshot retain output/manifest record selectors. The legacy Node
report does not acquire output records or read physical bytes.

Removed Store.FinishNode, Store.FinishNodeWithReason,
Tenant.FinishNodeWithOutputRef, finishNode and settleRunningStepsTx. These had no
production callers. Current NodeTransition owns reporting and terminal writes.
The remaining presentation/concurrency fixtures use explicit SeedNodeFinish.
It grants no production authority and does not implement lifecycle or replay.

Removed unused offer-priority fields, Node.ArtifactManifest and raw ClaimToken
fields from Node/Trigger. Removed unused ClaimToken, ClaimTokenKind and its four
kind constants. ClaimTokenPrefix remains because a legacy controller test helper
still uses it. Removed obsolete field narration. The dispatch-route fixture in
controller had no tests or consumers and is deleted.

Metadata and tenant-finish ports use real admission, acceptance, claim and start
transactions. Success keeps empty reason/exit metadata. Failure keeps the
reported reason, masked error and exit code. Exact replay succeeds. Changed
terminal metadata and foreign/missing targets fail without changing the job.
The old done-with-empty-outcome test retires with that schema-invalid state.
The port inventory records each replacement.

## Production volume

W1-owned production paths compared with HEAD: **253 added, 695 deleted,
net -442 Go lines**. These are store.go, claim_tokens.go, claim_attention.go,
run_filter_display.go and teststore/run.go. The count includes adapting the
merged output-free signature before deleting its raw writer.

All store production compared with HEAD: **259 added, 950 deleted, net -691**.
The additional -249 belongs to imported storage/storetest work, not W1's claim.
Combined W1+W1b compared with main: **19,036 added, 22,864 deleted, net -3,828**.
The -4,250 target remains **422 lines away**. Counts follow the lead's script,
excluding tests, acceptance, testdata and contract declaration files. No budget
waiver is claimed.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and TMPDIR/GOTMPDIR=/tmp/sw-astra-W1. Tests use -p 4.

- Whole repository go build -p 4 ./... passes after the final production cuts.
- Scoped vet passes for store/... plus controller, web, backend and api.
- The exact 21 W1 acceptance functions plus priority placement/metering/liveness/
  rotation properties and reporting/finish checks pass in 12.984s.
- All 50 controller TestEngine functions pass in 8.505s with teststore open checks.
- The four reporting functions, metadata and tenant-finish ports, and public-node
  checks pass across store, controller, api, web and backend. A package with no
  matching function is only a compilation check.
- Reporting and finish checks pass under the race detector in 11.404s.
- Commentcheck passes across 2,350 files. Changelog and whitespace checks pass.
  Changelog checking prints the preexisting rename-detection warning.
- Both store API snapshots match the generated final surface. Three imported
  snapshots still drift: pkg_controller.txt, pkg_storage.txt and
  pkg_storage_s3.txt. Their owners/lead must regenerate them. A clean whole-repo
  API-snapshot gate is not claimed.
- Embedded migration/docs/changelog copies are synchronized.

The broader tenant guard selection initially failed on stale function names,
exemptions and table expectations. Removed dead exemptions for deleted output
readers/lock helper and the now-scoped trigger list. Renamed the global account
admission/deletion exemptions to their transaction owners. Recorded the existing
explicit nullable-team business-event writer, console installation lookup and
checked deletion selector. The payment-events table exception matches its
operator ownership. Removed pool_access_rules from the missing-writer backlog
because its current writer sets team. SQL-scope, table-classification, default-
team-twin and backlog checks now pass in that selection.

**One broader guard still fails:** TestTenantWriters_EveryTenantTableHasAWriterThatSetsTheKey
finds no production INSERT for child_invocations, debug_pauses,
storage_commit_receipts and storage_reservations. These are unresolved schema/
writer gaps, not waived checks. No table was hidden from that guard.
No whole-repository tests, PostgreSQL/native matrix, pipelines, release or
independent review are claimed. The lead owns those checks.

## Contract changes needed and remaining integration

Trusted schedule/App intake still needs C4's tagged committing origin and C2's
verified-delivery fact reader. IntakeRequest/IntakeFacts/SealedRequest currently
require a credential AuthorityRef before the policy runs. An arming account or
verified delivery is not that credential. The committing store transaction must
re-read current schedule/version or installation/consent facts. Then route both
origins through admitRun and delete triggerIntake, triggerRows,
CreateTriggerWithRun and the legacy insertion path. No synthetic session,
second admission implementation or foreign contract amendment was added.

The priority Cloud metering, ordered matcher preferences, rotation grace,
launcher fence, store harness move and dead contract tables were completed in
prior sessions. Current acceptance/property checks preserve them. Materialization
remains closed and untouched.

Remaining code-inspection gaps include full runner capacity/renewal inventory,
raw CreateRun/CreateNode/trigger paths and legacy projections that still consume
trigger env/repository columns outside the repaired reports. GitHub completion
still selects installation data from TriggerEnv and must consume sealed App
facts instead. GitHubCommitTriggers still queries removed trigger repo columns.
C2 TeamWriter/GitHubAppWriter concrete transactions, publisher metric binding
and usage exact-body identity remain open. Billing composition stays with W1b.
The four table/writer guard findings above need ownership or deletion decisions.

W1b storage/log/accounting production was accepted from integration and not
edited. Deleting the raw finisher leaves legacy helpers in node_output.go
unreferenced. That W1b/W7-owned file still needs its owner's hard cut.

## Shared files and friction

W1 caller migrations outside store only change fixtures:

- internal/controller/concurrency_team_test.go
- internal/controller/github_checks_test.go
- internal/controller/profiles_test.go
- internal/controller/profiles_coverage_test.go
- internal/controller/profiles_aggregation_test.go
- internal/controller/claim_dispatch_internal_test.go (deleted unused fixture)

Merge conflict choices also touch internal/web/server_test.go and
internal/controller/log_submission_routes_test.go, and keep the integration-deleted
controller log/output test files deleted. Other staged shared paths came from
the integration merge. No W1 production edit outside store was needed.

C1, the port inventory, migration source, changelog, embedded copies and both
store API snapshots are updated. Reporting tests are in the new
internal/store/reporting_seals_test.go. Git config has no injected core.worktree or
fixture user.name. Scratch is removed at the end; the shared build cache stays.

Friction: Git staging is prohibited, so conflict contents and index status must
be reported separately. Imported legacy fixtures still reference deleted APIs
and were migrated or removed. The sealed-intake origin contract remains the
cross-area blocker. Source guards exposed real remaining writer gaps after
stale expectations were corrected.

STATUS: CONTINUE
