# W2 readiness verification

<!-- design:begin -->

W2 is ready for lead review at 2acf1cd91. The worktree started clean and the
whole-repository build passed before investigation. The interrupted work and
merge recovery are committed. The remaining implementation requires foreign
store or wire contracts, as listed below.

This continuation adds no production code, tests or fixture ports. It verifies
the committed implementation rather than repeating completed work. The sole
changed file is this report. Production added/deleted this session: 0/0. W2's
production delta against main remains +13,196/-16,909, net **-3,713**, within
the -250 budget. Counts use the lead's ownership and exclusion rules.

| Decision | Confirmed W2 behavior | Remaining integration |
| --- | --- | --- |
| 07:40 item 2 | Verified payment events include card setup, card payments, refunds, warnings and disputes. Provider and HTTP handoff tests pass. | W1b settlement of the expanded event set. |
| 07:40 item 3 | Startup sends the fixed seeded bootstrap request through BootstrapAdminIssuer. Seed and refusal properties pass. | W1 EnsureBootstrapCapability and HasDeploymentAdministrator. |
| 07:40 item 4 | C2 accepts authority until the rotation RevokedAt deadline. Capability-management and authentication tests pass. | W1 readAuthorityTx still rejects every non-nil RevokedAt. |
| 07:40 item 5 | Server.HTTP returns LocalHTTP with shared API/dashboard handlers and verified peer context. Local HTTP, console and peer tests pass. | W1 peer authentication and console committing transactions. W8 owns serving and drain. |
| 07:40 item 7 | Legacy GitHub permission, token-metering and credits.grant registrations remain deleted. Final runner routes, docs and migration notes remain in place. | Native composition and lead ratification. |
| 07:40 item 10 | SyncLauncher forwards authenticated authority and immutable observations to LauncherSyncReader and strips returned bearers. HTTP and fuzz seeds pass. | W1 Operator.ReadLauncherSync and W8 native award/Job correlation evidence. |
| 09:40 item 1 | The independent expected table remains in internal/controller/credential_route_expectations_test.go. internal/acceptance/credential_routes.go is absent. Declaration, drift and ordering tests pass. | None in W2. |

The W2 portions of A1-01/03/05/08/09/11/14/15/16/19/20/21/22/23,
A1-24 through A1-35, and A1-38/39/40/49/55 remain implemented against the
declared interfaces. Existing fake-backed and real-store checks pass. No
lead-owned handoff or amendment changed. Native A1-53 evidence remains with
T1/lead. No native credential-matrix cell is newly completed.

Remaining real integrations and contract changes needed:

- W1 authentication_binding.go still never assigns BindingState.CancelReason.
  The C2 field and request-only cancellation/renewal path are implemented.
  W1 must project committed intent, with job intent preceding run intent.
  W2 adds no SQL reconstruction of another area's snapshot.
- W1 must align held-job authentication with its current committed attempt
  while preserving exact process-attempt fencing. The explicit current-attempt
  fake remains in the post-start HTTP property. The prior real-snapshot failure
  is recorded in W2-merge-recovery-handoff.md and was not rerun unchanged.
- W1 must supply the authority-bearing credential, account, team, App,
  enrollment, identity-link, session, console, metering, billing, secret and
  source transactions documented in the preceding W2 handoffs. Missing
  adapters continue to return policy_missing. Peer authentication and durable
  roleless sessions remain foreign implementation work.
- W1 Tenant.ReadRunners and Operator.ReadLauncherSync remain absent. W2's
  RunnerSnapshot and reader interfaces already support contract fakes.
- W8/W9 must declare queue-depth ingestion. ClaimRequest still carries only
  available cores/memory. W2 preserves queue depth in the read model and adds
  no private wire field.
- W1/W9/lead must declare child/ref selection and claim-input transport before
  the remaining cross-run fixtures can be ported. The legacy ClaimToken methods
  cannot satisfy the final authority-bearing transaction boundary.
- W1/W7/W9/lead must declare final log mutation transport and authorized signing
  facts, including persisted retention and the exact writer fence. Existing
  signer and byte-verifier fakes do not supply those facts. The remaining log
  fixtures wait on that contract. Native metric/usage transport also remains
  outside the final C3 Controller interface.

Verification passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR under /tmp/sw-astra-W2.
Go commands use the absolute worktree with go -C. Tests use -p 4,
-count=1 and -timeout=180s.

- Whole-repository go build -p 4 ./... and scoped vet for capability, apierr,
  controller, controller/client and tests/acceptance.
- Full capability, apierr and controller/client suites.
- Controller W2 selection: engine, runner inventory, queue, policy,
  credentials, browser, console, bootstrap, local HTTP, reporting, billing,
  metrics, schedule and authorization tests, plus FuzzEngine seeds. Runtime:
  53.508s. StreamsOutlive and ReadsStayInside are excluded because they need
  sockets.
- Executable W2 HTTP acceptance selection: account, authority, credential,
  capability management, concurrency, data, secret, source, session, console,
  login, OAuth, OIDC, identity, operator, deployment, discovery, payment, root
  provisioning, GitHub runner administration, metering, credit, queue, sizing,
  self-service and engine tests, plus Credential/Data fuzz seeds. Runtime:
  58.182s. Existing native skips remain.
- Native TypeScript: go test -p 4 ./sdk/typescript/test/nativehost
  -run '^TestNative' -count=1 -timeout=180s. Runtime: 6.735s. Setup works.
- go run ./internal/commentcheck -base main . is clean across 2,106 files.
  git diff --check passes. Git identity still comes from Korey's global config,
  with no core.worktree override.

No new contract change or product decision is proposed. No production caller
outside W2 changed. No Sparkwing binary, pipeline, socket server, provider
network or whole-repository test suite ran. No Git writes were performed.
Friction remains the foreign integrations above. The lead owns review, commits
and native acceptance. Scratch is removed at handoff and the shared cache is
retained.

<!-- design:end -->

STATUS: COMPLETE
