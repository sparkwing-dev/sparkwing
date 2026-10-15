# W2 identity fixture and signer continuation

<!-- design:begin -->

Started from a3696c4a6. This continues W2-credential-fixture-handoff.md.
The lead owns commits and the shared amendments/handoffs. Neither lead-owned
file changed. No foreign production package changed.

## Implemented and deleted

Eight new HTTP tests replace 35 tests from the three legacy identity suites.
The Google property verifies signed ID tokens through GoogleAuth, the registered
OAuth routes and real SQLite sessions. It varies display names, distinct subjects
sharing an email, unverified email and unrelated PKCE verifiers. The license test
checks public capabilities and both provider starts with seven license states.
The link properties check address-independent account selection, committing
identity conflicts, last-method refusal and recent-sign-in refusal. Link and
account mutation writers are contract fakes.

The roleless property keeps account-only reads and refuses services, members
and runners despite caller team headers. Its authentication snapshot is fake.
The membership property uses real SQLite authentication and binding checks for
owner/editor/reader roles, then a fake committing writer. It checks the exact
hashed session authority forwarded to that writer. Team/invitation guards check
canonical errors and unchanged real session state after refusal.

Invariants: provider email and display text never select another account.
Only current membership grants authorize another member's mutation. A roleless
session gains no team authority from request headers. Linking targets the
authenticated account even when the provider's verified address differs.
The four generated properties cover these boundaries.

Deleted identity_signin_test.go, identity_team_test.go and identity_link_test.go,
1,019 lines. Moved 65 lines of account-response and team fixture declarations
into identity_fixture_test.go because retained integration fixtures use them.
Removed its unused 16-line license splitter.

Deleted the unused controller S3 presigner, its private presign client and the
duplicate log-limit constants. The deletion delay uses limits.LogUploadURLTTL.
The retained legacy log fixtures use shared byte limits. Two obsolete signer
tests are replaced by the real W7 signer/uploader and controller transfer tests.
The log-test change removes 75 lines and adds six. Final signed uploads use
immutable slots followed by stored SHA-256/size verification, rather than the
retired future-payload MD5 signer. Retained dashboard log readers still use
their existing storage adapter.

Production change: +7/-50, net -43. W2 versus main: +13,190/-16,895,
net **-3,705**, within the -250 budget. Test changes total +459/-1,110,
including the 388-line new test files. Port mappings and the remaining
persistence assertions are in contracts/t1-port-inventory.md.

## Lead decisions confirmed

| Decision | Confirmed C2 implementation | Remaining integration |
| --- | --- | --- |
| 07:40 item 2 | PaymentEvent and configured-key verification cover card setup, card success/failure, refunds, warnings and dispute open/close. Payment and provider-handoff tests pass. | W1b SettlePayment for the expanded events. |
| 07:40 item 3 | Startup forwards only the fixed seeded ProvisionBootstrap request through BootstrapAdminIssuer. Gated-seed and missing-adapter tests pass. | W1 once-only bootstrap transaction and deployment-admin read. |
| 07:40 item 4 | C2 accepts rotation authority until RevokedAt. CapabilityManagement HTTP rotation tests pass. | W1 readAuthorityTx still rejects every non-nil RevokedAt. |
| 07:40 item 5 | Server.HTTP returns wingd.LocalHTTP with shared policy API/dashboard handlers and verified peer context. LocalControllerHTTP tests pass. | W8 serving/drain and W1 local authority transactions. |
| 07:40 item 7 | Legacy runner-permission, token-table metering and credits.grant routes remain deleted. Final root, console, payment and retired-route checks pass. | Native composition and lead ratification remain with the lead. |
| 07:40 item 10 | SyncLauncher forwards authenticated authority and immutable observation fences, then removes bearer material. Engine sync tests pass. | W1 Operator.ReadLauncherSync and W8 native A1-38 evidence. |
| 09:40 item 1 | The independent expected table is in pkg/controller/credential_route_expectations_test.go beside the route tests. internal/acceptance/credential_routes.go is absent. All three drift/order/declaration tests pass. | None in W2. |

The existing C2 portions of A1-01/03/05/08/09/11/14/15/16/19/20/21/22/23,
A1-24 through A1-35, A1-38/39/40/49/55 remain implemented against their
declared boundaries. This session changes no contract interface. The real
persistence/runtime obligations recorded in the prior report remain open.
A1-53 native evidence belongs to T1/lead. No native acceptance cell was unskipped
or claimed complete. The executable account, identity, OAuth, credential, data,
payment and engine component selections pass.

## Contract changes needed and real integrations

- W8/W9 must declare queue-depth ingestion. ClaimRequest still contains available
  cores/memory but no queue-depth observation. C2 retains queue depth in the
  RunnerSnapshot read model and adds no private wire field.
- W1 must implement Tenant.ReadRunners returning capability.RunnerSnapshot and
  Operator.ReadLauncherSync. The C2 types and readers already permit test fakes.
- W1 must align the held job's authenticated attempt with its committed attempt
  without weakening process-attempt fencing. A temporary Go overlay removed only
  the contract snapshot from TestCredentialStartedClaimHTTPConsumesContractAttempt.
  The first annotation after real claim/start returned 401 instead of 204.
  The committed test with the explicit snapshot fake still passes.
- W1 new-account admission still fails. TestIdentitySignInCreatesOnePersonalSpace,
  TestIdentitySecondSubjectFromTheSameProviderGetsItsOwnAccount and
  TestSignInRefusesANewProviderOnAnExistingAccountsAddress each fail in
  ResolveSignIn while recording admission with a foreign-key constraint.
  Existing-identity Google and GitHub sign-in pass through real SQLite.
- W1 still must supply roleless/console snapshots, CancelReason projection,
  rotation grace and the authority-bearing credential/account/enrollment
  transactions listed in W2-credential-fixture-handoff.md. Component fakes do
  not establish their atomicity, ownership, restart or race behavior.
- W1/W9/lead must finish the child/ref contracts before the remaining cross-run
  fixtures can be ported. The policy rows exist, but the final child/ref HTTP
  transport is absent. Existing POST /runs intake handles parent lineage, but
  C3 has no child read/output or claim-input transport. The retained store
  ResolveClaimInput/EnqueueChildRun use retired ClaimToken and caller clocks.
  They cannot serve as authority-bearing production adapters. Define the
  request/response handoff and authorized selection/commit boundaries. Keep
  team-scoped declared references, immutable child lineage, retry-source
  ownership and current attempt/generation checks in the transaction.
- W1/W9/lead must finish final log HTTP composition. W7 supplies LogTransport
  request types, BatchSigner and UploadVerifier. W1 supplies RecordSegments and
  ReadLogSegments. C3 has log reads but no signing/verification/commit transport.
  Signing also needs the persisted retention class and exact writer fence
  before a URL is issued. No existing C1 reader exposes that authorized fact.
  Do not infer retention from a request or issue URLs by calling an empty
  RecordSegments mutation. The remaining claim/log fixtures and the final
  controller handlers stay open pending that contract. They are not replaced
  by the component signer checks.

Cross-run and log fixture families therefore remain unfinished. STATUS remains
CONTINUE because their controller transport still needs implementation once the
cross-area contracts are supplied. No new security, billing or product decision
was made in this session.

## Shared files and verification

Shared files changed outside W2 source are the four tests/acceptance port files,
contracts/t1-port-inventory.md and contracts/t1-credential-routes.md. The latter
uses the catalog's existing rename disposition for the OIDC route. The inventory
also gains missing code delimiters around an existing concurrency destination.
The first catalog run found these two existing documentation errors. Its focused
rerun passes after those corrections. No scenario catalog, skeleton, Harness or
boundary registry changed. No other-package caller migration was needed.

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp, with the
absolute worktree supplied to go -C. Tests use -p 4 and -timeout=180s.

Passed: whole-repository go build; full capability/apierr tests; scoped
controller engine/credential/policy/session/bootstrap/local/runner tests;
executable acceptance account/identity/OAuth/session/login/root/authority/data/
credential checks; payment and capability-management checks; W7 signer,
upload-verification and tagged-filesystem checks; catalog and port destinations;
scoped vet; commentcheck -base main; git diff --check.

The final controller selection also covers data maintenance, signed log-key
immutability and CloudFront. The broad acceptance run passed in 27.197s.
The final controller selection passed in 18.958s. The log-owner selection passed
for blob and logseg. The later port/storage run failed only the two catalog
format checks described above. No broad rerun is needed after a docs-only fix.
The W1 probes fail as recorded above.

Friction: final child/ref and log wire/transaction declarations are missing,
and the real W1 integration failures persist. No Sparkwing binary, pipeline,
socket server, provider network, deployment or whole-repository test suite ran.
No Git metadata was written. The lead owns native checks, review and commits.
Scratch is removed at handoff. The shared cache is retained.

<!-- design:end -->

STATUS: CONTINUE
