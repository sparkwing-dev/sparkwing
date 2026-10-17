# W2 machine registration

<!-- design:begin -->

Implemented the approved 2026-10-17 09:55 decision end to end.

Invariant: members enroll only their own personal machines when team policy
permits it; shared-runner enrollment, rotation and revocation, and policy
changes, require current team administration.

`members_can_add_machines` defaults to true. All member roles can enroll a
personal machine through an independent account session. When false, issuance
requires administration and its refusal names the setting. Issuance checks the
setting again under the same team lock used by settings changes. Existing
personal machines retain their owner-only claims after enrollment is disabled.
Shared runners always require current administration, including after their
creator is demoted. The management check applies to unbound machine credentials;
awarded work credentials retain their existing revocation rules.

GET/PUT `/api/v1/team/settings` share the native tenant and client types. PUT
requires an explicit boolean, rechecks current administration, and commits
`team.settings_changed` with the actor, store time and old/new values. A failed
audit rolls back the setting. Repeating the saved value creates no event.

The console exposes the setting and personal/shared enrollment. CLI
`team settings show/set --profile NAME` uses the same routes. Personal
`runners add --personal --session-stdin` succeeds or refuses through the real
controller and store. No new issuance interface or policy adapter was added.

## Tests and evidence

Tests preceded implementation. The HTTP test first failed on missing GET
`/api/v1/team/settings`. The new store tests first failed to compile because
the setting API was absent. The CLI settings case first failed because the
command was unimplemented. The shared-runner test reproduced revocation by a
demoted creator before its committing check was added.

Passing commands use `GOWORK=off GOFLAGS=-buildvcs=false`, shared
`GOCACHE=/tmp/sw-gocache`, scratch TMPDIR/GOTMPDIR under `/tmp/sw-astra-W2`,
and `go test -p 4`:

- Whole-repository `go build -p 4 ./...`.
- Focused vet: store, controller, client, CLI, capability, controllerwire,
  clisurface and apispec.
- Store/controller/CLI selection: `TestMachine`, `TestTeamSetting`,
  `TestSharedMachine`, `TestBurstPersonal`, `TestPersonal`, `TestRunnersPersonal`,
  `TestFinalCredential`, `TestCredentialRoute`, `TestTeamWrites`,
  `TestRootProvision`, `TestServing`, `TestCommandReference`,
  `TestCapabilityInventory`, `TestCapabilityRotation`, `TestAuthority.*Revo`.
  Store 11.202s, controller 0.685s, CLI 0.364s.
- New setting, authority and ownership proofs under race: store 61.301s,
  controller 4.119s. Selection: `TestMachineEnrollmentTeamSetting`,
  `TestTeamSetting`, `TestSharedMachine`, `TestMachineTeamSettingsHTTP`,
  `TestBurstPersonalMachineOwnership`, `TestBurstPersonalEnrollment`.
- The audit proof also checks exact store time and rollback on an unavailable
  audit sink. Its final rerun passes, 0.033s.
- Credential/authority/deployment/engine-authority/local-node/root-provisioning,
  session and runner acceptance selection, plus
  `FuzzStoreQueueClaimAuthorityW8` seeds: 5.913s. The first dependent run found
  a fixture revoking a shared root with its own credential. Both affected W8
  fixtures now revoke under team administration and retain their assertions.
- Full capability, apierr, clisurface, apispec, apiref and doccheck tests.
- Frontend teams tests: 42 pass. Executed directly with
  `node --import tsx src/lib/teams.test.ts` because this machine's `node --test`
  output reports only the containing file. TypeScript no-emit and focused
  ESLint pass.
- Generated command-reference drift, API references/OpenAPI and exported API
  snapshots refreshed. A temporary test invoked the existing command renderer
  and was removed. No Sparkwing candidate binary ran.
- Commentcheck against main is clean across 2,812 Go files. Documentation
  mirror check and `git diff --check` pass.

No historical test ports or native acceptance cells were newly completed.
No whole-controller, whole-store, PostgreSQL or browser execution pass is claimed.
The new machine-setting proofs run through real SQLite, HTTP recorders and the
public CLI dispatch. Existing ownership proofs cover both named and queue claims.

## Contracts and shared files

Contract changes made: C1 adds the checked team column and native methods,
advancing the disposable baseline to 7. C2 records the enrollment/management
boundary and audited routes. C3 records the shared settings wire/client. C4
records CLI and console behavior. Lead-owned handoffs and amendments are unchanged.
There is no new contract change request or design question.

Shared production files outside W2:

- Store: `pkg/store/authority_provision.go`, `capabilities_contract.go`,
  `capability_rotation.go`, `enrollment_runtime.go`, `schema.sql`, `store.go`,
  and new `team_settings.go`.
- CLI/wire: `cmd/sparkwing/main.go`, `help_registry.go`, new `team_settings.go`,
  `internal/clisurface/registry.go`, new `internal/controllerwire/team_settings.go`,
  new `pkg/controller/client/team_settings.go`, and `internal/apispec/wire.go`.
- Console: `web/src/app/team/page.tsx`, `web/src/app/team/machines/page.tsx`,
  and `web/src/lib/teams.ts`.
- Tests: `pkg/store/team_settings_test.go`, `pkg/store/burst_placement_test.go`,
  `cmd/sparkwing/runners_personal_test.go`, `web/src/lib/teams.test.ts`,
  `tests/acceptance/runner_store_w8_test.go`, and `store_queue_w8_test.go`.
  The last two migrate shared-root revocation callers to current administration.
- Docs: `docs/auth.md`, `local-execution.md`, generated `api-reference.md`,
  `cli-reference.md`, `cli-runners.md`, new `cli-team.md`, their embedded mirrors,
  `api/openapi.yaml`, `.apidiff/pkg_controller_client.txt`,
  `.apidiff/pkg_store.txt`, `CHANGELOG.md`, `pkg/docs/changelog.md`,
  `perf/budgets.json`, and the four contract files above.

The hand-maintained route expectation also now includes the already implemented
deployment-admin alternative for GET `/api/v1/operator/teams/{team}`. Its old
expectation failed the existing declaration tests before this reconciliation.

## Volume and remaining integration

Using the lead's production filter, this session is +208/-5 Go lines, net +203,
across all areas. Five production Go lines were replaced. The implementation
reuses root issuance, team administration, business events, claim ownership and
authenticated transport. No legacy module is replaced by this setting.
W2 itself is +40/-1, net +39 this session, and +17,655/-18,198, net -543 against
main, within its -250 budget. Counts include untracked implementation files and
exclude tests, testdata, acceptance and paths containing `contract`.

The authorized mechanism added is the team setting and its CLI/API/console
surface. A CLI-only or handler-only gate would miss direct store issuance and
current membership. Removing the column, methods and two routes undoes it.

The lead's `W2-machines-native-latest.md` is absent, checked at the start and
after verification. Completion remains for the harness after commit and native
tests. Native sockets, PostgreSQL and browser behavior remain unverified here.
The new performance route entry records unmeasured authorized latency rather
than inventing a baseline. The previous C1 QueuePriorityWriter persistence
handoff remains open; no new machine-registration integration uses a stub.

Scratch is removed at handoff and `/tmp/sw-gocache` is retained. No Git write,
service start, release, harness memory write or lead-owned contract edit occurred.

## Native-failure reconciliation continuation

The lead's session-1 native receipt is now present. Its shared-runner revocation
failures reproduce through the store and HTTP recorders. The fixtures tried to
revoke shared roots with those roots' own credentials. Updated them to use
current team administration through the real issuer and revocation transaction.
Production authorization is unchanged. Seven controller call sites share one
test helper. The store tests reuse revokeClaimRoot and authorityMember.

The ownership property now generates personal/shared enrollment, creator
ownership, current administration and forged query selectors. It checks both
HTTP status and persisted credential liveness. A demoted creator can revoke
their own personal machine, but shared runners require current administration.

The operator failures come from tests still refusing the deployment placement
read approved in C2. Updated both suites to admit that read and require exactly
the team ID and display name. The recorder acceptance test opens real SQLite
for successful reads rather than invoking the authorized handler with a nil
store. All console-action refusals remain. Names now describe that split:
TestOperatorConsoleKeepsActionsPrivateAndPlacementReadMinimal and
TestOperatorHTTPSeparatesConsoleActionsFromDeploymentReads.

### Unit and acceptance evidence

The reported failures ran red before edits. Their recorder/store selection now
passes: store 1.894s, controller 5.506s, acceptance 0.446s. The broader selection,
including machine enrollment, policy audit/rechecks, ownership, CLI settings and
operator team creation, passes: store 6.951s, controller 4.988s, CLI 0.607s,
acceptance 1.236s. Existing native topology cells remain for the lead.

Race checks pass: store 73.233s, controller 5.468s, acceptance 3.277s. Selection:
TestCapabilityClaimLivenessProperty, TestRenewUnitRefusesStaleAndRootAuthority,
TestNativeTokenOwnershipRelationProperty, TestMachineEnrollmentTeamSettingProperty,
TestTeamSetting, TestSharedMachine, TestMachineTeamSettingsHTTP and the operator
HTTP boundary test. The latter ran under its prior name before the rename.

### Commands and limits

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W2/go,
TMPDIR=/tmp/sw-astra-W2 and go -C with this worktree's absolute path.

- go build -p 4 ./... passes.
- go vet -p 4 ./pkg/store ./pkg/controller ./tests/acceptance passes.
- go test -p 4 ./pkg/store ./pkg/controller ./cmd/sparkwing ./tests/acceptance
  -run 'TestMachine|TestTeamSetting|TestSharedMachine|TestBurstPersonal|TestPersonal|TestRunnersPersonal|TestOperatorTeamsCreateCLI|TestOperatorHTTP|TestCapabilityClaimLivenessProperty|TestReleaseUnitRefusesForgedStartedAndCancelledClaims|TestRenewUnitRefusesStaleAndRootAuthority|TestCredentialClaimHTTPAncestryAndFenceProperty|TestCredentialStartedClaimHTTPConsumesContractAttempt|TestNativeLogReadEndsWithItsRoot|TestEngineClaimHTTPRefusalsDoNotAward|TestEngineClaimHTTPDeadCredentialProperty|TestTeamGitCredentialStaysWithTheTeamClaimAndHost|TestNativeTokenOwnershipRelationProperty|TestSourceControllerUsesStoreAuditAndBoundCipher|TestRootProvision|TestServing|TestCredentialRoute'
  -count=1 passes.
- go run ./internal/commentcheck -base main . is clean across 2,812 files.
- gofmt and git diff --check pass.

No whole-store/controller, PostgreSQL or socket-dependent native pass is claimed.
The changed console fixture needs the lead's native rerun. No product behavior
or public documentation changed, so there is no additional changelog impact.

### Scope and remaining work

Zero production lines added or deleted. W2 remains +17,655/-18,198 against
main, net -543, within its -250 budget. Existing test setup and expectations
were replaced. No historical test was newly ported and no old production API
was removed in this continuation.

Contract signatures and policies are unchanged. C2's evidence paragraph now
records the approved deployment read, and t1-port-inventory records the approved
personal-enrollment setting. No contract change request or design question is
new. The earlier C1 QueuePriorityWriter persistence handoff remains open and
uses its existing fake-backed controller seam. Machine enrollment uses no stub.

Shared files touched: pkg/store/claim_authentication_property_test.go,
pkg/store/unit_renewal_test.go, tests/acceptance/operator_authorization_test.go,
design/rewrite/contracts/t1-port-inventory.md, design/rewrite/test-reconciliation.md
and design/rewrite/test-reconciliation-sockets.md. Other changes are W2-owned
controller tests, C2 evidence and this report. Lead-owned handoffs/amendments
are unchanged.

The receipt still contains FAIL from the last committed state. Completion awaits
the harness's next commit and native run. No Git writes, installed Sparkwing
execution, pipeline, service or harness-memory writes occurred. Scratch is
deleted at handoff and /tmp/sw-gocache is retained.

<!-- design:end -->

STATUS: CONTINUE
