# W2 run-resource existence privacy

<!-- design:begin -->

Implemented lead decision 1 from 2026-10-17 04:00. Invariant: an authenticated
caller cannot distinguish a foreign run from a missing run through status,
error body or decoder diagnostics.

Every run route now uses the existing post-authentication tenant selector
boundary. Decoders retain only path selectors and body evidence. Removed
EngineController.runTarget and its global Operator.RunTeam lookup. The router
resolves the target before returning decoder errors, including malformed named
claims, child intake and failure-generation selectors. Expansion source-node
reads moved to post-authentication resolution in the same tenant. Result-body
digests, child lineage selectors and stored source-generation fencing survive.

Production changes are +69/-105 lines, net -36. W2 production against main is
+17,124/-18,016, net -892, within the -250 budget. The count excludes client,
tests, testdata, acceptance harnesses and contract declaration paths, matching
the lead's area filter. No compatibility API or new mechanism was added.

## Tests first and verification

Both required tests failed before the change and pass unchanged afterward:

- TestTeamBoundaryOutputHidesRunExistenceProperty
- TestTeamBoundary_EveryRunRouteHidesOtherTeamsResources

The first reproduced foreign 403/team versus absent 404. The second reproduced
the same class across private, child, result and reporting operations. No test
file or assertion changed, and no historical test was ported in this session.

Passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR/TMPDIR under /tmp/sw-astra-W2 and at most four package workers:

- Whole-repository go build -p 4 ./....
- Focused go vet for controller, capability and apierr.
- Full capability and apierr tests.
- Credential, authority, deployment, engine-authority and local-node acceptance
  selection, 3.610s. No native topology cell was unskipped.
- Controller recorder selection, 40.860s. It includes both unchanged privacy
  tests, policy/credential/serving properties, engine/private routes, terminal
  result replay, owner handoff, expansion source fencing and child binding.
- Both privacy tests, terminal result replay, owner handoff, expansion fencing
  and child binding under -race, 15.177s.
- Commentcheck against main across 2,686 Go files, gofmt and git diff --check.

The broad controller attempt failed only at the socket restriction in
TestMetricsAddr_MovesMetricsOffTheAPIListener,
TestMetricsAddr_FailsStartupWhenTheMetricsPortIsTaken and
TestSourceCredential_AppTokenReadsTheVerifiedRepository. The recorder selection
omits socket families and skips
TestTeamBoundary_ResolveWaiterDoesNotReadAnotherTeamsLeader, whose real client
also requires a listener. PostgreSQL remains unexercised without its test URL.
No full controller or repository test pass is claimed.

## Contracts and remaining integration

C2 now records the approved target-resolution boundary and evidence preservation.
No Go signature, schema or other area's contract changed. Lead-owned amendments
and handoffs remain untouched. Earlier W2 serving, bootstrap and operator-team
work remains committed. C1's QueuePriorityWriter transaction remains the prior
separate integration point, with its existing fake-backed HTTP contract.

The latest native receipt passes store and capability tests. Three controller
tests stop at GitHub App connect-start returning 500. Completion requires the
lead harness to commit the fix below and replace that receipt with a run
containing no FAIL. There is no new design question or contract request.

The committed implementation's production files belong to W2: authority_routes.go,
child_routes.go, engine_claim_routes.go, engine_job_control.go, engine_routes.go,
log_submission_routes.go, metrics.go, missing_operation_routes.go,
policy_router.go, private_host.go, run_selector.go and source_controller.go,
all under pkg/controller. No caller outside W2 required migration. The only
contract edit is design/rewrite/contracts/c2-credentials.md.

## Fixture and guard continuation

The native panic address 0x1828a68 resolves to ConsoleFixtureRequest in the
controller test binary. newAppFixture left identityFixture.srv nil while a
shadowing appFixture.srv held the server. Removed the shadow field and populated
the embedded fixture. newGHFixture had the same missing initialization and now
populates it too. Existing security assertions remain unchanged.

Removed pkg/controller/handlers.go from declaredRepoReaders because that file
no longer reads the declared repository. The guard failed before this edit and
passes afterward. The shared file touched is
pkg/store/declared_repo_guard_test.go. The other edits are
pkg/controller/github_app_test.go and pkg/controller/github_runners_test.go.
No production code, API, contract or historical test port changed. This session
deletes five test lines and adds three, net -2. Production delta is zero, and
W2 remains net -892 production lines against main.

Both required privacy tests pass unchanged. Additional passing evidence:

- Whole-repository go build -p 4 ./....
- go vet -p 4 ./pkg/controller ./pkg/store ./internal/capability ./internal/apierr.
- Full internal/capability and internal/apierr tests.
- TestDeclaredRepoGuard_OnlyReviewedFilesReadTheField in pkg/store, both required
  controller privacy tests and TestGitHubBrowserConnection* (controller 1.260s,
  store 0.291s).
- Controller TestFinalCredential*, TestGitHubBrowserConnection*, TestReviewSetup*,
  TestOperatorCreateTeam*, TestBootstrapSeed*, TestEnsureBootstrap* and
  TestControllerRefusesAnonymous* (1.069s). Serving registrations pass in both
  metrics configurations.
- tests/acceptance TestCredential*, TestAuthority*, TestDeployment*,
  TestEngineAuthority* and TestLocalNode* (1.933s).
- go run ./internal/commentcheck -base main . and git diff --check.

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR/TMPDIR under /tmp/sw-astra-W2 and at most four package workers.
Run the test selections with go test -p 4 -run '<selection>' -count=1.

TestGitHubAppExistingPickerRefusesForeignIdentityAndBoundInstallation still
needs native verification: here its fixture fails first at googletest.New
because httptest cannot open a TCP socket. The native receipt's panic was
diagnosed from its address, not reproduced past that socket restriction.
No whole-controller, whole-store or PostgreSQL pass is claimed. No acceptance
topology cell was unskipped. The earlier QueuePriorityWriter persistence
integration remains with C1, as recorded above.

The lead-owned handoffs and amendments remain unchanged. No Git write,
candidate binary, service or pipeline ran. Scratch is deleted before handoff
and the shared cache is retained. The native receipt still contains FAIL, so
the lead must rerun it before completion.

## GitHub connection transaction continuation

The native connect-start failure was reproduced without sockets by
TestGitHubBrowserConnectionRealStore: selecting the production adapter returned
500 policy_missing. Tenant did not implement the existing GitHubConnectWriter
contract. Implemented its three methods in pkg/store/github_connect.go and
assigned the concrete tenant in default controller composition. The compiler
now checks conformance at that assignment.

Invariant: identity reads, nonce consumption and installation binding require
the same live direct session and current team administration, and installation
binding must match that account's current linked GitHub identity. The adapter
uses teamMutationTx and locks the account. Consumption uses the store clock
and existing nonce table. Binding shares the existing ownership guard in the
same authorization transaction and derives ConnectedBy from resolved authority.
No new interface, table, grant, setting or credential mechanism was added.

Deleted the unused Store.ConsumeGitHubAppConnectState API and migrated its
only caller, pkg/store/flow_nonce_runtime_test.go, to the shared internal nonce
owner. Extracted the nonce and installation helpers so both existing consumers
and the new adapter use the same implementations. Production changes including
shared files are +107/-23, net +84. W2 production against main remains
+17,124/-18,016, net -892. The 23 deleted lines include eight lines retiring the
global API. No historical test was ported and no acceptance cell was unskipped.

Tests added before implementation cover the real router/authenticator/store
connection and replay, generated adversarial session/identity changes, the
store-clock expiry boundary, forged binding facts and foreign installation
takeover. The missing-writer test now injects a missing writer explicitly,
because nil resolution selects the implemented production adapter.

Passing checks, using the environment and concurrency limits recorded above:

- Whole-repository go build -p 4 ./....
- go vet -p 4 ./pkg/controller ./pkg/store ./internal/capability ./internal/apierr.
- Controller TestGitHubBrowserConnection* and both unchanged privacy tests,
  1.638s.
- Store TestGitHubConnect*, TestFlowNonces*, TestGitHubAppMutations*,
  TestGitHubAppInstallationBelongsToOneTeam and Test*Guard*, 4.218s.
- The new real-store connection test, both privacy tests and new store
  connection tests under -race: controller 9.342s, store 29.010s.
- Focused capability/apierr tests and the credential, authority, deployment,
  engine-authority and local-node acceptance selection, 2.158s.
- Commentcheck against main across 2,688 Go files and git diff --check.

Shared files touched: pkg/store/github_app.go, github_connect.go,
github_connect_test.go, identity_link.go and flow_nonce_runtime_test.go.
W2 files touched: pkg/controller/browser_github_app_routes.go and its test,
plus this report and the C2 implementation note. Existing contract signatures
and lead-owned handoffs/amendments are unchanged. The prior QueuePriorityWriter
persistence integration remains with C1. Native GitHub/provider-listener tests
and PostgreSQL verification remain open for the lead.

The original native GitHub test still cannot start here because googletest.New
requires a TCP listener. A broad test selection also hit that socket refusal
in the controller's installation-ownership test. The recorder reproduction
passes, but no full native controller/store pass is claimed. The supplied
receipt still contains FAIL. No Git write, installed binary, service or pipeline
ran. Scratch is deleted at handoff and the shared cache is retained.

## Integration merge continuation

Resolved the contents of all 19 conflicted paths. The index remains unmerged
because the swarm brief forbids Git writes. The lead must stage these
resolutions and finish the merge. The integration-deleted
`pkg/controller/trigger_rows_internal_test.go` stays deleted. Removed the
14-line `triggerRunInvocation` helper and its 19-line retired test. Also
deleted the unused `runTrigger` and `triggerInTeam` helpers, 19 lines including
the separator, from `tenant_scope.go`. They were the last production
`Operator.RunTeam` caller in the controller.

The approved privacy boundary was already committed. Both required test files
remain byte-for-byte identical to HEAD, and both named privacy tests pass.
`EngineController.runTarget` remains deleted. No decoder regains a global
run-team lookup. The existing result digests, expansion generation fences and
child bindings remain in the post-authentication resolution path.

Amended contracts win in the merge. C2's `ScheduledRunWriter` now takes
`store.CronSchedule`, matching C1's concrete `Tenant.AdmitScheduledRun`.
Production composition assigns the tenant directly, preserving compile-time
conformance and the injectable test seam. Queue activity records only a new
admission. Tick tests now reach the real committing schedule adapter. Their
generated property requires changed snapshots to be refused, the original
seal to stay unchanged and foreign admission to create no run. The missing
adapter test injects a missing adapter explicitly. Manual schedule HTTP tests
retain their existing fake boundary.

Kept W2's GitHub connection session/identity rechecks and the integration
helper name, account-type validation and nanosecond timestamp contract. Kept
both branches' independent connection tests. Updated the one W2 timestamp
assertion that expected seconds. The first run failed that assertion with
the preserved nanosecond value. The corrected connection tests pass.

Verification uses `GOWORK=off GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W2/go-tmp`,
`TMPDIR=/tmp/sw-astra-W2/tmp` and `-p 4`:

- Whole-repository `go build -p 4 ./...` passes.
- `go vet -p 4 ./pkg/controller ./pkg/store ./internal/capability ./internal/apierr` passes.
- Both unchanged privacy tests pass. The controller race selection also passes.
- Controller merge selection covering CronTick, CronLauncher, ScheduleIntake,
  ScheduleHTTP, FloodPolicy, ComputeLimits, Trigger and RunIntake passes, 5.167s.
  After simplifying the schedule fake to use the committed repository key,
  the schedule/tick selection passes again, 0.563s.
- Controller GitHubCompletion and GitHubBrowserConnection selection passes,
  12.192s. Store GitHubConnect, DeclaredRepoGuard, AppAdmission, AppFullRef and
  FlowNonces selection passes, 7.176s.
- Full internal/capability and internal/apierr tests pass, 0.301s and 0.020s.
- Commentcheck against main is clean across 2,698 Go files.
- `git diff --check` passes.

- Both required privacy tests and both branches' GitHub connection tests pass
  under `-race`: controller 9.446s, store 68.947s.

The broader acceptance attempt accidentally included `|.` in its filter and ran
the full package. It exited 1 after 258.244s. Its large output was truncated
before the failure names were retained. No full acceptance pass is claimed.
The corrected W2 selection passes, 2.013s:
`go test -p 4 ./tests/acceptance -run
'TestCredential|TestAuthority|TestDeployment|TestEngineAuthority|TestLocalNode'
-count=1`. These are credential, authority, deployment, engine-authority and
local-node component cells. Native topology cells remain for the lead.

After deleting the unused tenant-scope helpers, the whole-repository build
and commentcheck pass again. The required privacy tests plus GitHubCompletion
selection pass, 14.030s.

W2 production is +17,262/-18,167 against main, net -905, within the -250
budget. These counts exclude client, tests, testdata, acceptance and contract
declarations. This continuation is +171/-184 W2 production lines relative to HEAD,
net -13, including integration's changes. No historical test was
newly ported and no native acceptance cell was unskipped.

Contract changes made: C2 records the amended scheduled-intake signature.
No new store signature, schema or mechanism was added. Lead-owned handoffs
and amendment files remain untouched. Shared files resolved are
`design/rewrite/contracts/c1-data.md`, `design/rewrite/test-reconciliation.md`,
`pkg/store/github_app.go`, `pkg/store/github_connect.go`,
`pkg/store/github_connect_test.go` and `pkg/store/declared_repo_guard_test.go`.
The W2 changes are in `pkg/controller/crons.go`, `crons_tick_test.go` and
`schedule_routes_test.go`, and `tenant_scope.go`, alongside the other controller conflict resolutions
and C2's implementation note. No caller outside these paths needed a new
migration.

The native receipt still reports `TestGitHubAppRerequestedForkOrForeignCommitStartsNothing`
panicking at `CheckRunCalls()[0]`. Its local attempt stops earlier at
`googletest.New` with `socket: operation not permitted`. The integration merge
brings trusted App admission through sealed intake and its completion facts,
but the original native test still needs the lead's rerun. No native pass is
claimed. PostgreSQL remains unexercised. All checks have exited. Scratch
`/tmp/sw-astra-W2` is deleted and `/tmp/sw-gocache` is retained. C1's prior QueuePriorityWriter
persistence handoff remains open. There is no new contract request or design
question.

<!-- design:end -->

STATUS: CONTINUE
