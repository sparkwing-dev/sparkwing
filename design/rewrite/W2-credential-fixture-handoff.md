# W2 credential fixture continuation

<!-- design:begin -->

Started from cd3993233. The lead owns commits. Lead-owned amendments and handoffs
are unchanged. This continues W2-runner-inventory-handoff.md.

## Implemented and removed

RunnerSnapshot, RunnerObservation and RunnerHeadroom now live in
internal/capability/runners.go. RunnerInventoryReader returns
capability.RunnerSnapshot. W1 can implement it without importing controller,
which would create a store/controller cycle. Team is a string in the shared
observation. Tenant selection still uses store.Team. Every caller is migrated.
This is a local C2 contract correction. No alias or duplicate declaration remains.

The eleven member/machine token tests and five runner-claim tests are ported to
final issuance, delegation, inventory, enrollment, secret and claim boundaries.
The five GitHub identity tests are ported to final OAuth and link boundaries.
The old test functions are removed. Token/claim helper declarations remain
because other unported fixtures still use them. The unused runner fixture
constructor is removed. The GitHub test file is deleted outright.

Removed 776 legacy test/import lines from the three files. Production adds 43
and removes 37 lines in this session, net +6, because the relocated shared types
need their own package/import block. Against main, W2 adds 13196 and removes
16858 production lines, net -3662, within the -250 budget. The starting HEAD
measures -3668 under the lead's script rules, two lines different from the prior
report's -3670. Tests, testdata, acceptance and contract paths are excluded.

## Invariants and evidence

Invariant: a member can issue or delegate only grants available to its current
role and authenticated authority. CredentialMemberHTTPRoleCeilingProperty varies
roles and grants against real SQLite issuance. MembershipAndSessionLifecycle
proves independent member roots survive minting-session revocation, removal ends
authority, and demotion retains read authority while refusing write delegation.
The final contract permits narrow delegation and an owner's team.admin grant.
The old terminal-token/no-delegation and legacy-format expectations retire.

Invariant: a claim's live authority comes from its committed ancestry and fence,
never caller headers. CredentialClaimHTTPAncestryAndFenceProperty obtains real
node and trigger awards and generates revocation, replacement, terminal and
forged-header cases through Server HTTP. Positive run reads and root/claim grant
refusals use the real SQLite authentication reader. StartedClaimHTTPConsumesContractAttempt
uses an explicit current-attempt snapshot fake and real annotation transactions.
It proves positive delivery, sibling refusal and committing revocation despite
previously accepted authentication. It does not prove the real post-start
snapshot integration described below.

Invariant: a GitHub sign-in requires a primary verified provider email and its
identity is the numeric subject, independent of display names. OAuthHTTPGitHubPrimaryEmailProperty
generates profile, unverified-primary and verified-secondary values and proves
no new session. NumericIdentityProperty reads two real committed SQLite identities
with arbitrary display names. The shared OAuth fixture checks the exact
read:user/user:email scope. Provider I/O is fake. Existing OAuth/link tests retain
proof, replay, safe diagnostics and one-use transport behavior.

Port mappings and remaining persistence assertions are recorded in
contracts/t1-port-inventory.md. No native cell, scenario catalog, skeleton,
Harness or boundary registry changed. The requested 09:40 item 1 was already
committed: the independent expected table is in credential_route_expectations_test.go
beside credential_routes_test.go, and internal/acceptance/credential_routes.go
is absent. Its declaration, drift and ordering tests pass.

## Lead decisions 07:40 confirmed

- Item 2: PaymentEvent includes card setup, off-session success/failure, refunds,
  warnings and dispute open/close. The configured-key verifier and provider
  handoff tests pass. W1b still supplies real SettlePayment integration.
- Item 3: EnsureBootstrapAdminToken forwards only a valid seeded bootstrap
  ProvisionSpec to BootstrapAdminIssuer. Startup calls it. The gated-seed and
  missing-transaction tests pass. W1 still supplies the once-only issuing
  transaction and live deployment-admin read. No alternate issuer is added.
- Item 4: C2 accepts rotation authority until its RevokedAt deadline. The HTTP
  grace test passes with a fake manager. W1 readAuthorityTx still rejects every
  non-nil RevokedAt, so persistence grace remains open.
- Item 5: Server.HTTP returns wingd.LocalHTTP with shared API/dashboard handlers.
  LocalControllerHTTP and peer-policy tests pass using in-memory connections.
  W8 owns serving, drain and cancellation. C1 peer mutation authority remains open.
- Item 7: the legacy runner-permission, token-metering and credits.grant routes
  remain deleted. Dashboard, docs and migration guide use the final routes.
  Root provisioning, console policy and payment hard-cut tests pass.
- Item 10: EngineController.SyncLauncher is implemented through LauncherSyncReader,
  forwards authenticated root and immutable observation fences, and removes
  bearer material from returned awards. HTTP and fuzz seeds pass. W8/W9's wire
  now carries award kind/id/holder/generation and Job namespace/name/UID. W1
  still supplies ReadLauncherSync on Operator. No stub controller is claimed.

The C2 implementations for A1-01/03/05/08, A1-14 through A1-17,
A1-19 through A1-28, A1-29 through A1-35, A1-38/40/49/55 remain in place.
The existing credential/data/source/payment and engine selections pass.
A1-24/25/26/27/29/31/32/35/38 still require the owning area's persistence or
runtime composition. A1-53 native evidence remains with T1/lead. Existing
handoffs for roleless sessions, source/data/account/enrollment, console/peer,
usage replay identity and credential/business transactions are not closed by
fake-backed checks.

## Real integrations and contract changes needed

- W1 implements Tenant.ReadRunners(ctx) (capability.RunnerSnapshot, error), with
  the existing same-clock, live-ancestry, immutable-award and tenant invariants.
  Default reads still refuse the absent adapter. The import-cycle obstacle is fixed.
- W8/W9 must declare queue-depth ingestion. ClaimRequest still carries only
  available cores/memory. W2 preserves queue depth in reads and adds no private
  wire field.
- W1 still projects BindingState.CancelReason, with job intent before run intent.
  The separate C1 cancellation intent and C2 consumer already exist.
- DESIGN: W1/W2 current job-attempt authentication. Invariant: the live held
  job credential reports only its current committed attempt, and a process
  credential never crosses an attempt boundary. W1/W2 need to settle the projection. A real /runners/claim followed
  by /runs/run/jobs/job/transition with start/source_attempt=0 returns attempt=1.
  ReadAuthentication then returns capability.Binding.Attempt=0 and
  BindingState.Current.Attempt=1. C2 sameFence correctly refuses that inconsistent
  snapshot, so the next annotation is 401. The explicit contract snapshot with
  attempt=1 passes the same route and real store write. Preserve exact process
  attempt fencing while making the held job credential's authenticated projection
  match its committed attempt. Do not loosen sameFence for every credential.
  No foreign implementation or signature was changed here.
- W1 new-account admission is broken in the three targeted existing signup tests.
  ResolveSignIn fails with `identity: record admission: constraint failed:
  FOREIGN KEY constraint failed (787)` in SignInCreatesOnePersonalSpace,
  SecondSubjectFromTheSameProviderGetsItsOwnAccount and
  SignInRefusesANewProviderOnAnExistingAccountsAddress. Existing-identity OAuth
  succeeds. New-account/personal-space and provider-link persistence remain open.
- The legacy Tenant.ListRuns integration returns a private 500 through GET /runs
  on the final SQLite fixture. The member port exercises the real implemented
  delegation operation. W1 still owns the final list projection.

Remaining W2 work: port the cross-run claim, identity/sign-in/link/team and log
fixture families, then delete their shared legacy helpers. The retained socket
fixtures are not evidence for final APIs. Continue from the port inventory.
No runtime mechanism, flag, setting, environment variable or customer limit was added.
No new human decision is needed for the type relocation. The attempt projection
needs cross-area agreement before changing its security mechanism.

## Shared files and verification

Shared files changed outside W2 source: tests/acceptance/queue_reads_http_test.go
(type migration), oauth_http_test.go and oauth_identity_ports_test.go (ports),
C2's contract, t1-port-inventory.md, CHANGELOG.md, migration source/mirror,
embedded changelog and .apidiff/pkg_controller.txt. No W1/W7/W8/W9 production
file changed. No Git metadata was written.

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp, with go -C
and this worktree's absolute path. Tests use -p 4 and -timeout=180s.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Controller selection: Engine, RunnerInventory, QueueState, ClaimAttention,
  Policy, ServerPolicy, Credential, FinalCredential, Browser, OperatorPage,
  DeleteRunOnALocal, RequestPrincipal, CredentialLabel, Audit, EnsureBootstrap,
  LocalControllerHTTP and FuzzEngine seeds. It excludes StreamsOutlive and
  ReadsStayInside. Runtime was 7.327s.
- Executable W2 acceptance: Credential, CapabilityManagement, Data, Discovery,
  Payment, IdentityLink, IdentityHTTP, LoginHTTP, OAuth, Session, Source, Secret,
  OIDC, Account, RootProvision, QueueRead, Engine annotation/summary/step/reporting,
  plus Credential/Data fuzz seeds. Runtime was 15.555s. Native skips remain.
- Full capability/apierr tests and apidiff/apiref/apispec package tests.
- Scoped vet for controller, capability, apierr, executable acceptance and
  standalone controller startup.
- Documentation symbols, migrations, changelog/mirror tests. API snapshot
  regeneration, documentation sync/check, changelog check and git diff --check.
- Commentcheck -base main: clean across 2197 files.

The three targeted existing W1 signup tests failed as recorded above. This is
not a whole-store or whole-controller pass. No native sockets, provider network,
Sparkwing binary, pipeline, deployment or whole-repository test suite ran.
The lead owns those checks and review/landing. Scratch is removed at handoff.
The shared build cache is retained.

<!-- design:end -->

STATUS: CONTINUE
