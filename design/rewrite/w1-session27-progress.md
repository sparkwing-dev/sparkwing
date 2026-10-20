# W1 continuation 27

W1 remains incomplete. This continuation implements the GitHub enrollment
transaction handoff and removes its raw add/remove writers. W1b implementation
files and the lead-owned amendment and handoff ledgers are unchanged.

## Merge

Both conflicts were in docs/migrations/_unreleased.md and its embedded mirror.
Their entries describe separate API cuts, so the resolution preserves both
blocks. The files contain no conflict markers and their contents match.
The index still lists both as unmerged because the brief prohibits Git writes.
The lead must stage the resolutions and finish the merge.

## Implementation and ports

Tenant implements C2 GitHubRunnerRegistry through RegisterGitHubRunner,
RevokeGitHubRunner and the existing readers. Registration rechecks the current
account, ancestry, membership, selected team-admin alternative and tenant in
the committing transaction. It derives CreatedBy from persisted authority,
stores gh:<numeric repository ID>, and uses nanosecond store time. A duplicate
returns ErrAlreadyBound without changing consent. The existing twenty-binding
cap still includes funded teams and exempts DefaultTeam.

Actions provisioning locks the team before taking the later shared team lock.
This avoids concurrent issuers upgrading shared team locks. After the trusted
origin policy returns, issuance reads the live enrollment and checks numeric
repository/owner, canonical repository key, github:<repository>:<owner>
registration, workflow subject, exact claim grants, absence of pools/data/
metering and the existing one-hour lifetime. The existing twenty-live-root cap
is serialized under that same team lock. Expired and revoked roots release
issuance capacity. Leased retention still owns physical credential deletion.

Removal deletes consent and revokes the matching push-bound roots through the
existing envelope and generation writer. Descendant authorization ends through
its retained ancestry. Concurrent issuance either commits first and is revoked,
or sees the missing enrollment and returns forbidden with no bearer. An error
rolls back consent and all authority writes. Re-enrollment never revives a root.
Readers now decode the stored nanosecond timestamp.

Invariant: removing enrollment makes every credential rooted in it unusable,
and no concurrent Actions issuance can escape that removal. Generated cases
vary tenant, route alternative, grants, membership, revocation, registration,
subject, repository, pools, data scope, metering and expiry. Concurrent tests
cover both caps and issuance/removal. Transaction-local trigger tests cover
lost authority and failed revocation. None uses sleeps.

Deleted 96 production lines implementing AddGitHubRunnerBinding,
RemoveGitHubRunnerBinding and the token-prefix revocation query. Deleted 135
lines across the superseded team-scope, token-revocation, stale-binding mint
and binding-cap tests. Their retained assertions move to github_enrollment_test.go:
identity, invalid inputs, foreign tenant, duplicate consent, unknown removal,
live authority, descendant invalidation, stale issuance, funded caps and the
DefaultTeam exemption. Existing raw-mint tests and execution attribution remain
legacy fixtures. Their setup now calls RegisterGitHubRunner through a small
fixture that seeds a current owner session. They are not claimed as final ports.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W1.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet passes for internal/store/..., internal/controller/..., internal/acceptance/...
  and tests/acceptance/....
- The inherited selection plus enrollment, roleless and grace tests passes
  436 top-level tests / 1,352 cases with no skips. The later invalid-metadata
  test and stronger concurrent-error assertion pass in the focused selection.
  w1-session27-tests.txt lists the files. Extract Test names, exclude
  Postgres/Pg names and pass their anchored alternation to ./internal/store.
- Focused enrollment race tests pass with -race. These use SQLite, not Postgres.
- Eighteen store-sqlite acceptance cells pass with no skips. Select
  ^(TestLifecycle|TestRecovery|TestPlacement|TestCredentials|TestBilling|TestSurface)$/.*/^store-sqlite$.
  The new cell is placement-github-actions-exchange/store-sqlite. It proves
  consent, root/child authority and revocation through two reopenings.
- TestGitHubEnrollmentHTTPUsesDefaultTransactions passes through C2's real
  session verifier, default Tenant registry, signed test OIDC proof, sole issuer
  and root/child revocation. Provider transport and trusted origin policy are
  fakes. Existing Actions issuance, mutation, maintenance and secret-read HTTP
  selections also pass.
- commentcheck -base main . passes across 1,854 files. git diff --check,
  sync-docs.sh --check and check-changelog.sh pass. The store API snapshot is
  regenerated. Git has no local user.name, user.email or core.worktree override.

The first tests failed compilation on the absent registry methods. The inherited
Actions provisioning fixture then required a real canonical enrollment. The
DefaultTeam exemption fixture required its team row. Those setup failures were
corrected. No Sparkwing binary, listener, pipeline, provider, pod, PostgreSQL or
native placement ran. The new SQLite cell does not complete the native Actions
acceptance scenario. Scratch is deleted after verification.

## Contracts and remaining work

No foreign signature changed for enrollment. Production still needs trusted
ProvisionPolicy composition and ClaimProfileResolver. There are no production
call sites installing either policy in this tree. The signed test proof does
not substitute for that composition or for native claim/execution evidence.

The merged c1-data.md identifies a materialization integration point. W1's
private materializationRecords interface receives the caller's storeTx, but
W1b's public RecordStore.CopyMemo opens and commits its own transaction. There
is no transaction-bound record copy contract for W1 to call. W1b must supply
that seam for memo and terminal-result materialization, including an unexecuted
destination identity, before W1 can bind the production copier. W1 has not
copied W1b's persistence logic or added a separately committed copy. Existing
materialization and reconciliation tests still use the declared fake interface.

Previously completed A1-07/08/11, 13-18, 20-27, 40, 44/45 and 55 store work
remains covered by the inherited selection. This pass advances A1-24/26/51
and the GitHub enrollment handoff. A1-28/29/31-35 stay within W1b's split.
A1-53 native placement evidence remains with the lead and owning areas.
Roleless sessions, identity-link nonces, backend-error tagging, cancellation
projection and future-revocation grace are already present in the integration
branch. This continuation does not redo them.

Remaining W1 work is production machine/service enrollment and claim profiles,
console session persistence and RunnerMeteringWriter, CapabilityManager and
rotation through Store.Issue, local-peer credential transactions, production
materialization binding and the remaining StateStoreOld, lifecycle/readiness,
snapshot, claim/token, trigger and credential API cuts. Existing-award billing
must remain immutable when root metering changes.

Foreign integration gaps remain: C2 sameFence still equates claim-wide attempt
zero with the current execution ordinal. JobTransitionRequest still lacks
SourceAttempt. LauncherObservation still lacks the claim generation and Job
instance identity needed for safe cleanup. Startup bootstrap raw-credential
delivery still needs agreement with W2/C4. Verified financial events remain
W2/W1b work. These are not relaxed by the enrollment implementation.

## Shared files and friction

Shared Go files are new internal/acceptance/store_actions_enrollment.go,
tests/acceptance/placement_test.go and new github_enrollment_http_test.go.
The minimal old-caller migrations are internal/store/github_runner_bindings_test.go
and execution_attribution_test.go, using github_enrollment_fixture_test.go.
All other implementation changes are W1-owned store files.

Shared prose is c1-data.md, docs/github-actions-runners.md,
docs/migrations/_unreleased.md and CHANGELOG.md. Embedded mirrors and
.apidiff/pkg_store.txt are regenerated. No amendment or handoff ledger changed.

The new production mechanism is the declared tenant enrollment transaction.
It reuses team locking, sole issuance, authority envelopes and revocation
generations. No schema, service, setting, environment variable or customer
restriction was added. Friction is the read-only merge index and the missing
transaction-bound record seam. The lead owns review, gates, staging and landing.

STATUS: CONTINUE
