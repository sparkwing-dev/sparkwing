# W1 session 63

The fresh starting suite has **15 failures and 1,200 passing top-level tests**
(201.372 seconds). The ending suite has **15 failures and 1,199 passing
top-level tests** (202.321 seconds). The failing-test set is
unchanged. One passing lease-window test retired with its unused API. The
supplied 214-failure inventory remains stale. The inherited failures are listed in
[w1-session63-store-failures.txt](w1-session63-store-failures.txt).

## Implemented and deleted

`Tenant.CreateRun` is deleted. Its only callers were test fixtures. Fifty-seven
calls across 23 controller/web test files now use `teststore.SeedRun`, which
writes the final sealed reporting schema. It adds no executable owner, job or
capability. The helper's real SQLite test checks redacted unclassified args,
round-trip metadata, duplicate refusal, missing-tenant refusal and empty
executable/credential tables. It replaces fixture setup, not admission policy.

`Operator.CountActiveRunners` and its lease-window test are deleted. The method
had no production caller. `ReadRunners` and its existing properties own current
runner observations and liveness. Unused `runTeamSQL`, `CompileLogNode`,
`ErrPlanAccepted`, `Token.IsValid`, `Token.HasScope`, `TokenEnded`,
`ErrTokenRevoked` and `ErrTokenExpired` are deleted. No raw token issuer or
W1b fixture is changed.

Profile aggregation still checked the retired `cached` outcome. Both retained
SQLite profile suites now seed `memoized`, and the folding reader excludes that
outcome. They pass with all their existing measurement and partial-evidence
assertions. This is the sole shared production change, in
`pkg/controller/profiles.go`.

The session removes **72 production Go lines** and adds 53 for **-19 net**
against starting HEAD under the lead's filter. The additions include 51 lines
in the shared reporting-fixture package. Runtime code alone is -70 net.
Combined store production is **-3,252 against main**, still **998 lines over**
the -4,250 budget. This is not a completion or budget-waiver claim.

## Verification

Whole-repository build, scoped store/controller/web vet, fixture tests,
web grep checks, profile aggregation and coverage tests, commentcheck against
main, API snapshot regeneration/comparison, source/embedded documentation
drift and whitespace checks pass. All Go commands disable workspace and VCS
stamping, share /tmp/sw-gocache and use -p 4 at most. The focused store selection
passes (43.269 seconds), covering claim matching,
ordered placement, launcher synchronization, plan seals, schedule boundaries,
transitions, runner inventory, rotation and recovery cells. The fixture/profile
race selection passes too. Acceptance and direct caller packages compile.

The first final store run also found a new declared-repository source-guard
entry needed by the reporting fixture. The entry now states its fixture-only
purpose. The focused guard check passes. The original failure is retained in
the session failure log.

The broader controller fixture probe reaches inherited raw-token failures and
the sandbox socket refusal. Its original output is summarized here rather than
claimed green. Reporting/profile checks were rerun after the demonstrated seed
corrections. Native listener, PostgreSQL, Kubernetes and pipeline execution
remain lead checks. Independent landing review remains with the lead under
the swarm brief. No new native matrix cell is claimed.

## Contracts and remaining integration

W1b's files are untouched. The starting fifteen failures remain in billing,
blob/storage accounting or their test fixtures and writer inventory. Green
store verification requires those owner changes to be integrated.

Trusted schedule/App intake still needs C4's tagged committing origin and W2's
transaction-readable verified delivery contract. C4 IntakeFacts and the first
seal currently carry only AuthorityRef. It cannot represent an arming account
or a verified delivery as a committing origin. The existing store implementation
uses the declared IntakePolicy and capability interfaces with fakes. No
credential is synthesized and no foreign contract is extended. The legacy
cronLauncher/startGitHubAppRun -> triggerIntake/triggerRows -> raw combined
insertion path remains open on that integration.

Ordered placement, Cloud metering, rotation grace and the SyncLauncher store
fence remain implemented. Remaining contracts include maximum-concurrency and
queue-depth observations, publisher metric binding, additive usage retry
identity and sealed GitHub completion facts. Raw trigger and token issuers and
W1b billing/blob/log retirements still prevent completion.

## Shared files and friction

The exact changed-file list is [w1-session63-files.txt](w1-session63-files.txt).
Shared changes are the profile reader, the 23 controller/web fixture files,
the teststore helper and test, the source-guard entry, C1, the port inventory,
migration guide, changelog, generated documentation copies and API snapshots. Lead-owned handoff
and amendment files are unchanged. No other area's contract signature changed.

No git write ran. HEAD remains 88b63dd9e. Local Git config has no identity or
core.worktree override. The scratch directory is removed after final verification. The shared cache
is retained.

STATUS: CONTINUE
