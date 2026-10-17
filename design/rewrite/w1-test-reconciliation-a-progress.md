# W1 test reconciliation-a progress

Local final selection: **85/95 groups pass, ten fail, none skip**. All 72
GitHub App groups pass. This closes 39 previously open original groups and adds
two passing controller regressions. The native report still reflects the last
committed state and still contains FAIL. No native or acceptance-matrix cell is
newly claimed complete.

Verified App deliveries now call the existing sealed admission transaction.
The committing checks revalidate the exact installation, team and consent
generation. Shared settings, ceilings, rate limits, number allocation, source
identity, fork refusal, owner creation and completion intent remain in that
transaction. An ordinary capability or intake request cannot manufacture the
transaction-private origin. Failed effects leave no run, owner, replay-team
binding or spent number.

The first successful admission binds the signed body to its tenant in the
existing delivery record. Partial admission cannot move the body to another
team after the installation moves. Generated tests reproduce that failure when
the check is removed. Other generated cases cover subscription/automation
revocation, suspension, replacement, event tampering, team mismatch and forks.

Re-requests read immutable App/event/PR facts from seals. Tag, base-branch and
label filters and optional merge state survive. Discovery of today's default
branch cannot overwrite the default reported by the signed event. Full refs
retain exact default-branch storage trust. Tags, wrong SHAs and absent App facts
do not acquire that trust. OIDC event identity survives replay-ledger expiry.

Manual schedules carry resolved repository keys independently of App authority.
Manual/App conflicts use numeric identity. Unresolved GitHub URLs fail before
arming. Existing tenant, refusal and narrow GitHub token assertions remain.
No test group or security assertion is deleted, skipped or disabled.

Deleted the raw automation trigger insertion API, its generic insertion branch,
the old commit-trigger reader, the alias repository lookup and the App handler's
legacy trigger/run writer. Under the lead's production exclusions, this session
adds 233 lines and deletes 187, net **+46**. Store delta against main is **-2,482**,
which remains 1,768 over the -4,250 W1+W1b target. The budget is still open. The
new transaction implements the existing one-intake requirement. Reusing an
account credential would bind automatic work to a user's session lifetime, and
keeping the raw writer would preserve a second admission path.

## Contract changes made

C1, C2 and C4 record these integration amendments for lead review:

- GitHubAppAdmission, AdmitGitHubAppRun and VerifiedAppIntake attach trusted proof
  only to the shared admission transaction. The github_app reference is an
  accountability tag, not a new authenticatable credential or bearer.
- github_app_deliveries.admitted_team binds partially admitted signed bodies.
  This changes the disposable baseline schema. No migration is added.
- AppFacts.Event/Action and PullRequest.Label/Merged preserve typed event context.
  Arbitrary environment values remain outside the request snapshot.
- CronSchedule.RepositoryKey separates resolved identity from App consent.
- GitHubCommitRuns replaces GitHubCommitTriggers and reads seals in one query.

No package, setting, flag, environment variable, quota, compatibility alias or
second intake implementation is added. Lead-owned handoffs/amendments are
unchanged. No further contract change is requested in this session.

## Verification

Checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOMAXPROCS=4, -p 4 and scratch under /tmp/sw-astra-W1.

- Whole-repository go build ./... passes. Scoped vet passes for store, controller,
  runspec and githubapp. Commentcheck is clean. git diff --check passes.
- Final assigned HTTP selection: 85 pass, ten fail, zero skip, 19.895s.
- All App groups pass. The corrected signed-default regression also passes in
  isolation. Tag/base/label re-requests reproduce red before their projection fix.
- Focused store App/data/reporting race checks pass in 92.157s. Focused controller
  consent/replay/re-request race checks pass in 7.578s.
- Store App/data/reporting checks pass after the final rollback assertion in
  4.311s. Complete runspec and githubapp suites pass with memory HTTP transport
  in 2.575s and 0.958s. Normal HTTP listeners fail on sandbox socket restrictions.
- The local wingd sweep still reaches TCP/Unix socket restrictions. Native
  wingd evidence stays open. Unchanged bin/CLI suites were not repeated.

Temporary standard-library overlays replace only httptest listeners with
net.Pipe and route the default transport to them. Real handlers, provider fakes,
authorization and SQLite transactions execute. This is component evidence, not
native socket proof. The overlays and logs are removed with W1 scratch. The
shared build cache remains. No Sparkwing candidate or pipeline is run.

## Remaining work

- TestRunSourceCredential_MintsTheApprovedRepositoriesByID
- TestRunSourceCredential_NeedsTheGitHubApp
- TestGitHubRunnerCredentialClaimsOnlyItsRepository
- TestGitHubRunnerCredentialReachesNothingOutsideItsRoutes
- TestGitHubRunnerCredentialClaimsOnlyItsOwnPush
- TestSourceCredential_AppTokenReadsTheVerifiedRepository
- TestSourceCredential_TypedRepositoryNeedsASubscription
- TestSourceCredential_NamesTheRemedy
- TestSourceCredential_NeedsTheRunsOwnClaimToken
- TestSourceCredential_AppTokenReadsOnlyTheOwnersExtraRepos

Runner fixtures still write removed trigger columns and call retired claim
routes. Port them to current unit/run/job claims with sealed numeric repo and
push facts. Preserve named and queued tenant/repository/SHA exclusions.

Source fixtures still write removed repo/trigger columns and use legacy claim
bearers. Port them to current requests and claims. SourceController still handles
team git credentials without an App minting adapter. Connect verified App IDs,
owner-approved extras, live claim authorization, narrow token minting and audit
commit. Preserve every extra-repo, owner-consent and claim-boundary assertion.
Use contract fakes for runner/software-policy composition where needed and name
the real enrollment/runtime integration still left open.

The original scheduled-origin handoff remains open. Generic legacy trigger
insertion, triggerIntake and triggerRows cannot be deleted until schedules and
their remaining callers enter the one sealed intake. Completed placement,
metering, enrollment, rotation and connection work is not repeated.

The lead must run native tests and review the trusted-origin/schema amendments.
The native report must show no FAIL before completion can be claimed.

## Shared files touched

- design/rewrite/contracts/c1-data.md
- design/rewrite/contracts/c2-credentials.md
- design/rewrite/contracts/c4-surface-config.md
- design/rewrite/test-reconciliation.md
- internal/githubapp/githubapp.go
- internal/runspec/contracts.go
- pkg/controller/github_app_automation_test.go
- pkg/controller/github_app_check_events.go
- pkg/controller/github_app_crons_identity_test.go
- pkg/controller/github_app_test.go
- pkg/controller/github_app_webhook.go
- pkg/controller/github_checks_test.go
- pkg/controller/intake_policy.go

Store files changed: app_intake.go, app_intake_test.go, authority_oidc.go,
blob_persistence.go, capabilities_contract.go, crons.go, dialect.go, github_app.go,
github_app_automation.go, github_commit_reporting_test.go, schema.sql and
tenant_triggers.go. These are the minimal store, projection and caller changes
for App admission. The generic insertion API remains only for unmigrated callers.

Friction: sockets remain forbidden, and the native harness owns final evidence.
No Git writes, external messages, candidate runtime or harness-memory writes are
performed. The shared Git config has no user.name/core.worktree overrides.
W1 scratch is removed after checks. The shared Go cache is retained.

STATUS: CONTINUE
