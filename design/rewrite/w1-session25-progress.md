# W1 continuation 25

W1 remains incomplete. This continuation resolves the merge bodies and implements
new credential/session handoffs. W1b implementation files are untouched.
HEAD is a80c408b1. No Git write ran.

## Merge

Resolved all three conflicted file bodies first. docs/auth.md takes the incoming
rewrite's scope descriptions. Its mirror is regenerated from that source.
pkg/controller/server.go retains W1's GET node route and excludes the incoming
POST node-dependencies route because W1 already removed that API and handler.
The whole-repository build confirms the join.

The index still lists docs/auth.md, pkg/docs/mirror/auth.md and
pkg/controller/server.go as unmerged. The brief prohibits Git writes, and the
Git metadata is read-only. The lead must stage these resolved paths and finish
the merge. No conflict markers remain in them. Amendments and handoffs received
no edits.

## Implemented and deleted

- ReadAuthentication projects C2 BindingState.CancelReason. A committed job
  request precedes the run request, including an empty job reason. Owner
  authentication carries the run intent. Existing state transitions retain
  ownership of the first request and the holder's result.
- LookupSession tags authority-chain infrastructure and integrity errors, and
  RowsAffected errors, with ErrSessionBackend. Typed unauthenticated refusals
  remain authentication errors.
- Provider-linked sessions can have no selected team. Their persisted team is
  NULL, with an account/no-parent CHECK. SchemaTables classifies sessions as
  operator-owned identity rows. Tenant SQL still carries team predicates.
  Creation checks the linked provider identity. Lookup, renewal and C2's real
  authentication snapshot retain no team or grants. Membership creation grants
  nothing to that session until an explicit checked team switch.
- Identity-link and GitHub-connect consumption use the existing flow_nonces
  table, distinct kinds, store time and nanoseconds. ON CONFLICT arbitrates
  concurrent consumers without poisoning a PostgreSQL transaction. Requests no
  longer delete spent rows. Leased retention already owns expiry cleanup.
  Replay and the sealed identity-link signing key survive reopening.
- Authority resolution accepts a future revocation through its grace period
  and refuses the exact deadline. Ancestor expiry/revocation is checked again
  after the chain is locked.
- Tenant.RotateCredentials implements C2's default account/team-admin adapter.
  It reloads current ancestry and membership, serially visits one team's
  secret and git rows and commits both value columns together. It rechecks
  authority before commit. Errors return zero counts and roll back both tables.
  Other fields remain unchanged. Verified local peer integration remains open.
- Tenant.SourceReleaseAudits reads final capability identities and nanosecond
  times, newest first, within one tenant and the existing 200-row bound. Its
  integration test uses real C2 AuthorizeSourceRelease and real store commits.
  Enrollment remains a contract fake.
- Secret and git-credential timestamp writers/readers now preserve nanoseconds.
  No migration or compatibility decoder is added to the disposable final schema.

Removed Store.RotateSecretValues, Store.RotateGitCredentialSecrets, their global
row readers and their two SQL exemptions. No production caller remained after
W2's incoming route cut. Removed six superseded tests: three global secret
rotation tests, one global git rotation test and the two old flow-state tests.
Tenant rotation and generated nonce coverage preserve their requirements under
the final contracts. Legacy ClaimToken source release/audit APIs still have
callers and are not claimed removed.

The Go diff deletes **323 lines** and adds **809**, including replaced lines,
tests and acceptance support. Production Go deletes **154** and adds **197**,
a net increase of **43 lines**. Counts exclude merge bodies, incoming staged
integration changes, Markdown, SQL and generated snapshots.

## Invariants and acceptance

Generated tests check that authentication preserves the job's first cancellation
intent, grace deadlines fence every descendant, roleless sessions never gain
team grants without a checked switch, flow nonces consume once per kind, and
rotation changes only its authenticated tenant's value columns. Rotation tests
cover transform failures in both tables, authority expiry during a callback,
demotion, foreign tenant selection, wrong route alternatives and empty tables.
Schema checks reject a roleless row without an account or with a capability
parent. No test sleeps.

The existing credentials-session-digest-csrf/store-sqlite cell now also covers
roleless creation, renewal, authentication, a refused team switch and durable
link-state consumption. No native cell is relabelled or unskipped.

Two new HTTP tests use the real default store adapters and C2 verification:
credential maintenance rotates both bound credential kinds, reads audits and
refuses a demoted member; roleless sessions renew, refuse team writes and log
out. The latter does not claim the default account metadata route works.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE, GOTMPDIR and TMPDIR are under /tmp/sw-astra-W1.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet passes for pkg/store/..., pkg/controller/..., internal/acceptance/...
  and tests/acceptance/....
- W1's selection passes **418 top-level tests / 1,267 cases**, without skips.
  Extract Test names from the files in w1-session25-tests.txt, exclude Postgres
  names, and use the anchored alternation with ./pkg/store -count=1 -timeout=180s.
  The final nonce/key extension also passes separately after this selection.
- **Seventeen SQLite cells** pass with ./internal/acceptance ./tests/acceptance
  -run '^(TestLifecycle|TestRecovery|TestPlacement|TestCredentials|TestBilling|TestSurface)$/[^/]+/store-sqlite$'
  -count=1. These comprise three lifecycle, eight recovery, two placement,
  two credential, one billing-start and one schedule cell. internal/acceptance
  has no matching test entry point; tests/acceptance runs the cells.
- Nineteen HTTP top-level tests pass with ./tests/acceptance
  -run '^(TestSessionFlowHTTP|TestCredentialRotationHTTP|TestCredentialMaintenanceHTTP|TestCredentialAuditHTTP)'
  -count=1. Existing tests use their documented fakes. The two new tests above
  exercise the real default SQLite adapters without opening a network listener.
- commentcheck -base main . passes across 1,487 files. git diff --check,
  sync-docs.sh --check and check-changelog.sh pass.
- The store API snapshot is regenerated. Changes are the two removed global
  APIs, the two new Tenant methods and the sessions ownership entry.
- Git has no local user.name, user.email or core.worktree override.

The new cancellation, backend classification, roleless and grace tests first
failed on their corresponding gaps. The generated nonce test first failed on
the absent identity_link_states table. The rotation property first caught
second-resolution timestamps. The broad selection caught the missing schema
ownership update. Those failures are corrected.

A draft HTTP test incorrectly sealed the git fixture under an incomplete row
binding. Its fixture was corrected without weakening production decryption.
One test invocation had an invalid -run expression and was replaced by the
recorded anchored selection. No result from that invocation is counted.

No native listener, api.sock, PostgreSQL, provider, pod, process execution,
full legacy suite, race suite or pipeline pass is claimed. The lead owns
independent review, gates and landing.

## Amendment audit and remaining W1 work

Inherited store implementations for A1-07/08/11, A1-13 through 18, A1-20 through
27, A1-40, A1-44/45 and A1-55 remain covered by the W1 selection. This continuation
closes the new A1-21 cancellation projection and roleless/backend handoffs,
implements the A1-24 credential-maintenance and source-audit handoffs for account
authority, and advances A1-51 with the global-rotation hard cut.

A1-28/29/31 through 35 remain in W1b's billing/blob/log split, including the
logical-record selection-authority request. A1-53 remains native integration
work for the lead and owning areas. No W1b accounting, storage or log mechanism
was copied or changed.

The incoming C2 now implements ValidateDelegation and SettleBilling. Earlier
reports' panic-stub blockers for these functions are obsolete. The new source
audit test also uses C2's implemented source-release policy. Enrollment, native
execution and lifecycle-wide real integration remain distinct evidence.

Unfinished W1 handoffs and cuts:

- Implement GitHubRunnerRegistry transactions, repository identity checks and
  atomic revocation against Actions provisioning.
- Implement console session representation/authentication and RunnerMeteringWriter.
  Existing awards must retain their immutable metering provenance.
- Supply production machine/service enrollment policy, registration uniqueness
  and ClaimProfileResolver integration. Resolve bootstrap raw-credential delivery
  with W2/C4 before replacing the old startup token seeding path.
- Implement CapabilityManager inventories and rotation through Store.Issue.
  Future-revocation liveness is fixed, but the management methods remain absent.
- Implement CredentialWriter and SecretReader with transaction-time authority,
  selected route alternative, secret declaration/pool checks and committed audits.
  Default write/read adapters still refuse rather than using old unfenced methods.
- Finish verified local peer authority integration for credential transactions.
  The implemented rotation adapter accepts persisted account authority, not an
  invented peer principal or a caller-provided team-admin assertion.
- Retire StateStoreOld, CreatePlanNode/bootstrap, UpdatePlanSnapshot, raw readiness,
  raw run/node lifecycle writes and old claim/token/trigger paths, migrating or
  deleting every remaining caller as the lead's scope clarification permits.

Real integration and foreign contract points:

- Default GET /api/v1/me still returns 401 missing bearer token for a valid
  session cookie, including a roleless session. This ran through the real server.
  Store/C2 account-policy tests pass, but W2 must finish the default account-route
  registration. The passing roleless HTTP test covers session endpoints instead.
- W9 JobTransitionRequest still has no SourceAttempt. W9 must transport it and
  W3 must send the captured ordinal and consume Applied.Attempt.
- LauncherObservation still lacks claim generation and Kubernetes Job-instance
  identity. W2 has a LauncherSyncReader transport seam, but W1 has no safe store
  reader under this shape. UnitID alone cannot distinguish re-awards.
- Verified card/refund/dispute event shapes remain W2/W1b work. Native local
  readiness/drain and host/source execution remain W8/W3/lead integration.

No foreign contract signature was changed. C1's local schema/ownership amendment
for roleless sessions is recorded above and in c1-data.md. There is no new
setting, environment variable, service, customer restriction or billing policy.

## Shared files and cleanup

Outside pkg/store, implementation work touches only acceptance support/tests:
internal/acceptance/store_sessions.go,
tests/acceptance/credential_maintenance_http_test.go and
tests/acceptance/session_flow_http_test.go. No production foreign caller needed
migration for the two removed global rotation APIs. pkg/controller/server.go
has only the merge resolution described above.

Shared prose is c1-data.md, docs/auth.md, docs/migrations/_unreleased.md and
CHANGELOG.md. Their embedded mirrors are regenerated. .apidiff/pkg_store.txt
is regenerated. Lead-owned amendments/handoffs and W1b files are unchanged.
The exact inherited/extended test file selection is w1-session25-tests.txt.

Friction is the read-only merge index, the default account-route gap, the
foreign wire shapes and the remaining legacy caller migrations. Scratch is
deleted at the end. No service, Sparkwing binary, Git write, release or harness
memory write is part of this continuation.

STATUS: CONTINUE
