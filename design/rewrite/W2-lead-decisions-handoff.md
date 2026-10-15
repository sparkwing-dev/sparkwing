# W2 lead-decision continuation

<!-- design:begin -->

Started from 3656dec979d4905658b833aacb252d3b9df3a80b. No Git writes.
Lead-owned handoffs.md and amendments-1.md are unchanged.

## Implemented and deleted

Lead decision 2: C2 now verifies card setup, card payment success/failure,
individual refund updates, early fraud warnings and opened/closed disputes.
PaymentEventPolicies contains fourteen provider types, including the original
four checkout types. PaymentEvent carries the verified provider identities,
minor-unit amounts, statuses and card facts. Provider metadata supplies no
team, credits, charge attempt or fingerprint authority. The existing callback
passes VerifiedPaymentEvent to SettlePayment and acknowledges only afterward.

Setup verification fetches the payment method and checks its exact identity,
customer, mode and card shape. Paid-card and warning verification fetch the
charge and check its identity, payment relation and mode. Provider reads use
the configured private API key, fixed HTTPS collections, path-safe IDs, no
redirects and the existing one-MiB JSON budget. Failed reads yield no proof and
safe errors. Test/live use the same implementation. No provider or financial
side effect was performed in this session. W1b still owns ledger settlement.
The duplicate checkout-reference decoder is replaced by the shared expandable
reference decoder used by these events.

Lead decision 3: removed the token-table startup issuer. The controller now
passes a validated swk_ seed through BootstrapAdminIssuer. C2 ProvisionSpec.Seed
is excluded from JSON in both directions. ValidateProvisionSeed confines a
supplied presentation to the bootstrap origin. The startup helper fixes the
unbound operator subject and the three operational administration grants.
It accepts no caller clock. Empty input provisions nothing. Missing seeded
transactions fail before issuance. W1 has not implemented the adapter, so a
configured bootstrap seed refuses startup.

EnableAuthFromStore no longer consults ListTokens or disables authentication
when that table is empty or unreadable. The remaining legacy private mux is
protected before any administrator exists. The --require-auth readiness check
uses the explicit capability read described below. Its token-table query and
unauthenticated-bootstrap advice are deleted. Public health checks database
connectivity directly rather than calling the legacy run-list query.

Lead decision 5: Server.HTTP was already implemented. Its existing in-process
peer/HTTP property passes. It supplies the shared API and dashboard handlers to
Wingd. No second adapter was added.

Lead decision 7: auth.md now explicitly records the three retired GitHub
runner-permission registrations, old token-metering path and eight credits.grant
service registrations. The migration guide already recorded those removals and
now describes verified provider facts, seeded startup and the removed open mode.
The embedded mirrors and API snapshot are regenerated.

Lead decision 10: EngineController.SyncLauncher was already implemented against
LauncherSyncReader. Existing HTTP and fuzz-seed tests pass. Its reader receives
the authenticated authority and cloned observations. The copied response strips
bearer material. The real store fence remains open. No legacy sync method was
restored or adapted.

The source diff deletes 116 production lines and 543 old test lines, excluding
docs and generated output. Three test files are deleted outright, 318 lines:
multi_team_auth_test.go, session_hash_test.go and session_lifetime_test.go.
The old bootstrap tests and auth-visibility tests are replaced by the final
properties and ports below. The old credential middleware and remaining
business registrations are still present. No additional route is removed in
this continuation.

## Invariants, ports and acceptance

Invariant: settlement receives only signature-verified provider facts and
checked provider relations, never metadata-derived team or credit authority.
TestPaymentProviderProofProperty generates metadata and adversarial signed
objects across all ten new provider types. Signature, mode, object, identity
and missing-reference refusals reach no provider read or settlement proof.
TestPaymentProviderRefusals covers provider identity/mode/customer mismatches,
redirects, oversized replies, malformed responses and private backend errors.
TestPaymentProviderOutcomes exercises all new types in test and live modes.
The provider transport is fake.

TestPaymentHTTPFinancialProviderHandoffProperty exercises the actual callback,
strict signature verifier and fake committing ledger for refunds, failed
payments and disputes in both modes. Generated metadata grants nothing, and
altered/stale proofs reach no settlement. Existing checkout and private-error
HTTP tests remain active. No financial persistence proof is claimed here.

Invariant: startup authentication cannot open private routes because a legacy
credential table is empty or unreadable. The real SQLite/HTTP property covers
single-team, license-before and license-after startup, generated tenant
selectors, both actual muxes, cookie absence and enabled health reporting.
It ports the removed multi-team and visibility tests. The single-team-open
expectation retires with that mode. TestCheckRequireAuthUsesCapabilityRead
ports the command's readiness check, including absent readers and backend errors.

Invariant: a supplied presentation can reach only the named bootstrap origin.
Generated seed properties check encoding, origin, JSON exclusion, fixed scope,
issuer result and missing-adapter refusal. The issuer is fake. Durable gate
races, digest persistence and restart/revocation remain W1 acceptance work.

The two deleted session tests move to TestLoginHTTPAccountCapabilitySession.
It uses the real account-capability login handler and SQLite. The session is
stored by digest, its expiry is seven days, and the schema has no plaintext
csrf_token column. An operator bootstrap credential is not an account login.

The W2 executable acceptance selection passes. No native matrix cell is newly
unskipped, and the remaining blocked cells are not counted as successes.
The port inventory records these limits.

## Foreign contracts and remaining work

- W1 must implement EnsureBootstrapCapability(ctx, capability.ProvisionSpec)
  (bool, error) on Store. It must reach Store.Issue in one locked, once-only
  transaction, validate Seed, retain only its digest/indexed prefix, and never
  revive a revoked seed. Issue must reject Seed on every other origin.
- W1 must implement HasDeploymentAdministrator(context.Context) (bool, error).
  The trusted startup read must use the store clock and complete live ancestry.
  An administrator is an unbound controller operator/service capability in the
  deployment scope with operator.tokens. No raw credential need leave the read.
  Missing readers refuse --require-auth startup. This is declared by the
  command's local read interface and documented in C2. No C1 signature or SQL
  implementation was changed here.
- W1b must extend SettlePayment for the seven new kinds against committed card
  setup, customer, payment, charge/attempt, refund, warning and dispute records.
  Preserve per-mode event deduplication, stale-setup refusal, partial/refund
  replay, warning-before-payment and dispute semantics. Several provider events
  for one payment/refund must never grant or reverse twice. Store still refuses
  these new kinds. The C2 contract supplies exact facts and provider mappings.
- W1 must supply ReadLauncherSync with the current launcher ancestry and immutable
  award/claim-generation/Job-instance fence. W8's launcher correlation remains
  its integration responsibility. W2's sync reader boundary is fake evidence.
- W1 still does not populate BindingState.CancelReason in
  authentication_binding.go. Existing C2 consumption and job-before-run intent
  precedence remain ready. W2 adds no second snapshot or SQL projection.
- The console/peer authority snapshots and committing transactions, roleless
  sessions, enrollment, credential transactions, data/read-set and source
  integrations from the earlier handoffs remain open. The remaining legacy
  business routes, auth.go verifier/cache and old HTTP clients still require
  final cutover and deletion. W2 is incomplete.

The older SourceAttempt warning is closed in this tree: the shared wire and
controller forward it, and both previously failing real-store lifecycle tests
now pass. This session makes no further change to that transport.

C2 contract extensions are local: PaymentEvent kinds/facts, ProvisionSpec.Seed,
ValidateProvisionSeed and BootstrapAdminIssuer. The startup read is a local
command interface. New mechanisms are the required provider-read adapter and
these issuing/read boundaries. Raw financial methods and token-table fallbacks
would bypass the frozen authority/ledger contracts, so they are not alternatives.
No environment variable, cache, timeout, customer limit, schema or compatibility
route is introduced. No new decision needs human ratification beyond the lead's
already recorded decisions.

## Shared files and verification

Shared source caller changes are cmd/sparkwing-controller/main.go and its
require_auth_test.go. Other shared edits are the two tests/acceptance files,
t1-port-inventory.md, auth/migration docs, CHANGELOG.md, embedded mirrors and
.apidiff/pkg_controller.txt. The remaining code changes belong to W2.

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W2/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp.
They select this worktree with go -C. Tests use -p 4.

- Whole-repository go build -p 4 ./... passes on the final code tree.
- Scoped go vet passes for capability, apierr, controller, controller/client,
  sparkwing-controller and executable acceptance.
- Final W2 acceptance selection passes in 13.724 seconds. Its regex is
  ^Test(Account|Authority|Credential|ConcurrencyHTTP|Data|Secret|Source|Session|Console|Login|OAuth|OIDC|IdentityLink|Operator|Deployment|Discovery|Payment|RootProvisioning|GitHubRunnerAdmin|RunnerMetering|Credit|Queue|Sizing|SelfService|Engine|ReportingClient).
- Final in-process policy, startup, local HTTP, launcher sync and fuzz-seed
  selections pass. The command readiness tests pass. Full capability, apierr,
  apiroutes, apiref and apispec suites pass. The final payment decoder was then
  rechecked through its focused verifier and actual HTTP tests.
- TestEngineJobLifecycleRenewsAndReplaysUnderStoreFences,
  TestEngineOwnerReconcilesExecutesAndFinishes,
  TestEngineCancelRechecksMembershipAndCommitsEffectsOnce and
  TestEngineCancellationIntentPreservesHolderProperty pass in 1.907 seconds.
- Native Linux TypeScript passes in 6.754 seconds with
  go test -p 4 ./sdk/typescript/test/nativehost -run '^TestNative' -count=1
  -timeout=180s. There is no TypeScript setup error.
- commentcheck -base main . is clean across 2133 Go files. API snapshot/spec,
  changelog, docs mirror/changelog, embedded scrub and git diff --check pass.

The first startup property exposed the legacy health run-list query's failure
against the final schema. Replacing that connectivity probe with PingContext
fixes the actual cause. Its final property passes with HTTP 200 and auth enabled.
No skip, status relaxation or fake health probe was added. A report-edit script
initially expected a marker absent from the port inventory. Its completed edits
were retained and the insertion used the actual heading. Neither issue remains.

No full socket-dependent controller suite, pipeline, candidate binary, service,
release or Git write ran. The lead owns landing and native integration. Git
configuration has no core.worktree override. All check commands have finished,
and /tmp/sw-astra-W2 is deleted at session end.

<!-- design:end -->

STATUS: CONTINUE
