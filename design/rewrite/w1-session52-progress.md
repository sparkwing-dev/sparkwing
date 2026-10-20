# W1 session 52: merge resolution and legacy API retirement

STATUS remains CONTINUE. Trusted schedule/App admission, legacy schema callers
and the store budget remain open. The integration merge has resolved working
contents, with no conflict markers. `internal/store/authority_provision.go` remains
unmerged in the index because the brief prohibits Git writes. The lead must
stage the resolution and finish the merge. Integration deletions stay deleted.

## Implemented

Machine provisioning uses W2's amended direct-account-session predicate and
claim-grant allow-list. W1's enrollment, rotation and bootstrap implementation
remains. The merged bootstrap property now removes the canonical
`bootstrap_admin` marker and expects refusal while a live administrator exists.
It previously removed the retired implementation's marker and expected no error.
The merged all-grants property now supplies canonical registration/repository
scope and distinct runner names so accepted cases reach the grant boundary.

Metric insertion names the final `(team, run_id, node_id, ts)` conflict key.
Invariant: a metric write affects only its tenant's sample, and a conflicting
retry never changes the stored sample. The generated 64-case property covers
foreign writes and reads, exact replay and changed CPU, memory and process-time
readings. It fails against an overlay restoring the old conflict key and passes
with the fix.

Ordering tests now admit and accept five jobs, reconcile and start them in
reverse order, and read their declaration order through `ReadSnapshot`.
Tenant node-start and metric tests use real admission, acceptance and claims.
The Open example shows selecting a tenant without the retired raw lifecycle.

## Deleted and volume

- Four default-team Store writers: `CreateRun`, `CreateTrigger`,
  `CreateTriggerWithRun` and `FinishRun`. Their test callers select a tenant.
- Store/Tenant `StartNode`, replaced by `TransitionNode` and `CauseStart`.
- Store/Tenant `SetNodeArtifactManifest`, its mutation helper and three old
  tests. Current result-record tests cover terminal attachment, replay,
  absent selectors and forged keys. They are unchanged.
- `CreateSourceTriggerWithRun` and the unused controller source-upload
  admission branch. The remaining schedule/App legacy insertion path stays open.
- Both context-carried claim-fence types, constructors, readers and their
  private mutation checks. No caller or test constructed these contexts.
  Current typed actor/holder checks remain in canonical transitions. Tenant
  checks remain in the legacy metric and metadata callers.
- Six unused legacy claim-header constants and five unused private helpers:
  `forUpdateSkipLocked`, `nullableUnix`, `lockEventSequenceTx`,
  `snapshotNodePrefers` and `snapshotRunPriority`.

Session production delta is **7 added, 451 removed, net -444 Go lines**,
excluding integration imports. Store contributes -434 and controller -10.
Combined W1+W1b against main is **18,715 added, 21,724 removed, net -3,009**
under the lead's contract/test/harness exclusions. It remains **1,241 lines over**
the -4,250 target. No exception or COMPLETE claim is made.

## Verification

Go uses GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache and
GOTMPDIR=/tmp/sw-astra-W1. Tests use -p 4. Final commands also pin TMPDIR there.

- Whole-repository `go build -p 4 ./...` passes after the production deletions.
- Vet passes for store, controller, internal/backend and internal/web. All four
  packages compile their tests after the final API/header removals.
- All 21 W1 package acceptance functions pass. The same 11.570s selection
  includes placement metering, ordered preferences, eligible-runner liveness,
  rotation grace, launcher snapshots, committing account origins, metadata
  fences, the three ported checks, the metric property and ExampleOpen.
- Focused store race checks pass in 21.719s. They cover the four new/ported
  checks, committing account origins, metadata fences and the three current
  result-record replacement tests.
- Controller bootstrap and launcher-sync checks pass in 0.455s after adapting
  the merged bootstrap test.
- W2's machine-grant and data-scoped-session properties pass after adapting
  the enrollment fixture.
- The metric property's old-key overlay fails with the expected SQL conflict
  target error (0.088s). The ported metric fixture also failed before the fix.
- Commentcheck passes across 2,289 Go files. API snapshot, embedded docs,
  changelog and whitespace checks pass.

The broader four-package short suite was interrupted. No pass is claimed.
`go test -p 4 ./internal/store -run '^TestTenantEventsStayInTheRunsTeam$' -count=1`
reproduces the remaining legacy fixture failure: `runs` has no `args_json`
column. That test's raw fixture and `createRunTx` predate this session. The
same baseline failure is recorded in W2-opus-review-fixes.md. Remaining raw
run/token fixtures require migration, not restoration of removed schema.

No native PostgreSQL, Windows/Darwin, sockets, pipeline, release or whole-repo
test pass is claimed. No new matrix cell is promoted.

## Contract changes needed and real integration

Trusted schedule/App intake still needs a C4/C2 contract decision. C1 currently
requires an AuthorityRef before IntakePolicy runs. A schedule's ArmedBy account
and an HMAC-verified delivery are neither a session nor an independently minted
capability. They cannot pass that path without inventing credential authority.

Proposed contract: add a tagged committing origin to IntakeFacts and the sealed
request: credential AuthorityRef, schedule identity/due instant and arming
version, or verified delivery identity. The store must select the current
schedule or installation/consent facts transactionally. W2 must declare the
verified-delivery fact reader. Ordinary request fields cannot select these
origins. Retain AuthorityRef for real credential ancestry and here awards.
This changes foreign C4/C2 contracts, so the brief requires stopping this piece
and reporting it. No synthetic session, capability or second admission writer
was added. Once declared, W1 routes both callers through admitRun and deletes
triggerIntake, triggerRows and the remaining raw insertion path.

Other retained integration gaps: enrolled concurrency and accepted renewal
observations for full runner inventory, remaining legacy-schema read/write
callers, and W2's end-to-end controller composition. In particular the declared
MetricWriter's WriteMetric/WriteUsage adapters are still missing. The existing
AddNodeMetricSample fix does not claim to supply those authority-bearing
transactions. W1b's billing, blob/log persistence and closed materialization
implementation is unchanged.

Cloud metering, ordered placement, rotation grace and launcher store fencing
remain implemented and pass their selected checks. The moved store acceptance
harness and deleted dead W1 contract tables remain removed. Lead-owned handoffs
and amendments are unchanged.

## Shared files and friction

Outside store, production edits are limited to `internal/controller/handlers.go`
(the dead source-upload branch). `internal/store/event_accounting.go` changes only
the caller migration after deletion of the unused context-fence helpers.
W1b credit/output fixtures change only their removed Store API calls.

Shared test files touched:

- `internal/backend/`: `store_backend_test.go`.
- `internal/web/`: `logpretty_test.go`, `server_test.go`.
- `internal/controller/`: `bootstrap_store_test.go`, `browser_pages_test.go`, `claim_dispatch_internal_test.go`, `compute_limits_test.go`, `concurrency_team_test.go`, `credits_test.go`, `crons_test.go`, `crons_tick_test.go`, `cycle_detection_test.go`, `egress_test.go`, `fixture_isolation_test.go`, `flood_test.go`, `github_app_test.go`, `github_checks_test.go`, `log_segments_internal_test.go`, `node_claim_fixture_test.go`, `node_output_internal_test.go`, `principal_limit_test.go`, `profiles_aggregation_test.go`, `profiles_coverage_test.go`, `profiles_test.go`, `prom_internal_test.go`, `reaped_run_conflict_test.go`, `retry_root_team_test.go`, `retry_test.go`, `secret_args_display_test.go`, `server_test.go`, `team_boundary_test.go`, `trends_test.go`, `trigger_test.go`.

C1, the port inventory, CHANGELOG, the unreleased migration, their embedded
mirrors and .apidiff/pkg_store.txt are updated. The new property is
internal/store/metrics_scope_runtime_test.go. The old artifact_manifest_test.go is
deleted. No foreign Go contract signature changed. No mechanism was added.

Friction: the broader suite still contains raw-schema fixtures. The changelog
check warns that rename detection exceeded its limit but exits successfully.
Initial tests set GOTMPDIR but not TMPDIR. Final checks pin both. The interrupted
suite may have left test temporary files outside W1 scratch. No unidentified
shared temporary directories were deleted. Scratch was removed after all checks.
The shared build cache is retained. Git staging and committing remain lead work.

STATUS: CONTINUE
