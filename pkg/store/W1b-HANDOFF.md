# W1b handoff

Continues 39da03cfe and W1b-session1792068276.out after integration merge
0d2a4eda0. W1b remains incomplete. The lead owns review, commits, pipelines
and native integration. The checkout has no merge conflicts.

## Completed in this session

Removed five raw Store exports without aliases:

- ReversePayment and PaymentTeam now belong to Operator.
- HoldTeamForDispute becomes Tenant.HoldForDispute.
- ReleaseCreditFreezes belongs to Tenant and takes no team argument.
- PaidGrantTeam is deleted. PaymentTeam uses the existing transaction-local
  provider-to-grant resolver directly.

The private hold and release owners now belong to Tenant too. They derive their
team from the handle. Console HTTP and break-glass callers use the tenant they
already selected. Refunds retain committed checkout attribution, configured
payment mode, ledger locking and idempotent reversal behavior.

The local C1 contract records this handle cut. No foreign contract changes,
schema changes, new policy limits or mechanisms were added.

Deleted the unused 11-line PaidGrantTeam wrapper. Across production Go, 62 lines
are deleted and 36 added, for a net reduction of 26 lines. Three legacy test
functions are replaced by final-schema tests in payment_disputes_test.go.

## Invariant and test ports

Invariant: a tenant hold or release cannot affect another tenant, and a dispute
ID stays bound to its first team and payment after release.

- TestFrozenTeamsMeteredClaimsAreRefused now uses ClaimUnit, persisted capability
  roots and the real C2 SettleBilling calculator. A frozen metered award writes
  neither reservation nor award. Unmetered work remains available. Releasing
  the hold permits the metered award. Enrollment/authority fixture inputs remain
  contract fakes.
- TestADisputeIsBoundToOnePayment now settles two real committed checkouts and
  generates 96 cross-tenant cases. It checks foreign holds, refunds and releases,
  immutable dispute identity, release replay and unchanged balances.
- TestAReversalMayNotUseTheUnappliedPrefix now uses verified-payment settlement
  facts and the final refund owner. Reserved references leave the balance intact.

The existing TestOperatorHTTPCommitsAuthorizedActions now releases the exact
hold returned by the freeze route. API and dashboard variants reach the real
console handler and tenant persistence. Authentication/CSRF boundaries are fakes.

No new acceptance cell is added or unskipped. No additional archived T1
source-function identity is claimed closed.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE, GOTMPDIR and TMPDIR are under /tmp/sw-astra-W1b/.

- Whole-repository go build -p 4 ./... passes.
- Vet passes for store, controller, sparkwing-controller and both acceptance
  package trees.
- Store selections pass 223 top-level tests / 660 cases, with no skips.
- The existing billing/storage/operator HTTP acceptance selection passes.
  Thirteen existing SQLite cells pass. There are 562 skipped native/platform
  cases. These skips are not integration evidence.
- The modified named-hold HTTP test passes for API and dashboard handlers.
- TestAdminNeedsATeamAndAReason and
  TestAdminFreezesAndReleasesWithTheConsolesRecords pass.
- The socket-free TestOperatorSettingsHTTP selection passes.
- Commentcheck against main, generated API snapshots, docs mirrors, changelog,
  gofmt and diff whitespace checks pass.

Store selections:

1. ^(TestPayment|TestMeteredUnit|TestMeteredAccounting|TestStoreData|TestATeamStaysFrozen|TestReleaseCreditFreezes|TestADispute|TestAReversalMayNotUse|TestFrozenTeams)
2. ^(TestBlob|TestLogPersistence|TestLogPublication|TestLogMaintenance|TestRetainedStorage|TestTenantSQLScope|TestTenantWriters|TestTenantTwins|TestGrantCredits|TestCreditGrantRequest|TestCreditsBalance|TestCreditReversal)

Acceptance selection: ^(TestBilling|TestStorage|TestOperator.*HTTP).

Passing SQLite cells:

- billing-start-award-versus-start/store-sqlite
- billing-ledger-survives-retention/store-data-sqlite
- storage-cas-integrity/store-data-sqlite
- storage-replica-safe-accounting/store-data-sqlite
- storage-quota-settlement-crash-replay/store-data-sqlite
- storage-log-segments-and-fallback/store-data-sqlite
- storage-hasmany-read-set/store-data-sqlite
- storage-hasmany-large-budget/store-data-sqlite
- storage-reachability-and-retention-tails/store-data-sqlite
- storage-memo-and-depcache-scope/store-data-sqlite
- storage-reference-edge-laundering/store-data-sqlite
- storage-publish-one-way/store-data-sqlite
- storage-owner-receipts-and-stream-verification/store-data-sqlite

Two broader controller selections stop on sandbox socket refusal in
TestOperatorPageServesOnlyTheOperatorsSession and
TestAdminTokenReachesNoOperatorPower. Their httptest.NewServer fixtures cannot
listen here. No socket-backed controller pass, full package pass, PostgreSQL,
physical backend, provider, native placement or race pass is claimed.
An import regroup edit briefly removed unrelated fixture imports. Those files
were restored byte-for-byte and final scoped vet and compilation pass.

## Assignment audit and unfinished work

- The approved record mutation owner, output catalog removals and retired
  expired-claim helper remain done. No W1 production file changed here.
- A1-24: store authority/binding/read-set adapters and logical selector preflight
  remain implemented. DataController still needs concrete ResolveAuthority,
  ReadBlob and ChargeDownload callback composition plus physical adapters.
- A1-27: W1's issuance/start/renewal/retry/finish/release/reaper join is present.
  This session's final claim/freeze port uses the real C2 calculator, as does
  the existing ledger/release regression. Spend-exhaustion/grace enforcement
  and legacy claim/trigger credit windows remain unfinished.
- A1-28: verified checkout settlement and raw paid-grant refusal remain done.
  Refund/dispute handle cut is now done. Provider verification and deployment
  evidence remain W2/T1 integrations.
- A1-29 through A1-35: records/members, read sets, tagged uploader receipts,
  immutable tickets, memo/depcache pointers, tombstones and tagged log persistence
  remain implemented. W7 namespace/edge rules, signed-store composition, binary
  execution policy, log transport and physical backends remain integration points.
- A1-53: native/provider/PostgreSQL/physical-backend cells remain unverified.

Priority remaining cuts are ReserveUpload/CommitUpload, ReleaseStorage, raw
output objects, old node-log transport and legacy claim/trigger credit windows.
Their controller, client and retained lifecycle callers still exist. Final
upload tickets, logical records and segment persistence supply replacements.
Allowance-driven physical eviction and final manifest-reader setup remain open.

Spend/grace must be joined to the renewal response as well as the ledger. The
current accountBillingAwardTx result has no cancellation output, and W1's
renewal helpers populate LeaseBeat before accounting. Owner-unit awards also
have no persisted per-award exhaustion anchor. Implement the policy and durable
clock in the billing owner, then let the same renewal transaction return the
persisted cancellation through LeaseBeat. Do not add raw status writers or
caller-clock enforcement. No such change is implemented or declared complete.

The earlier free-slot question remains open. Final tickets carry no
server-verified intended use before signing, while legacy small outputs can
bypass a full slot pool. Lead/C1/W7 must supply intended-use authority or an
explicit policy decision. This session adds no slot gate.

## Shared files and handoff

Production caller migrations outside pkg/store:

- cmd/sparkwing-controller/admin.go
- pkg/controller/operator.go

Shared test changes are mechanical caller migrations in
cmd/sparkwing-controller/admin_test.go, pkg/controller/operator_test.go and
pkg/controller/deletion_review_test.go. The named-hold acceptance test changes
in tests/acceptance/operator_authorization_test.go.

Other migrated store test fixtures are billing_trust_test.go,
business_events_test.go, card_billing_test.go,
card_payment_replay_boundary_test.go, credit_expired_claim_settle_test.go,
credit_freeze_standing_test.go, deletion_billing_test.go, deletion_test.go,
free_tier_test.go, payment_refunds_test.go, trigger_claim_credits_test.go and
warning_hold_team_test.go. W1 fixture changes are limited to calls required by
removed APIs. W1 implementation files are untouched.

Shared documentation/generated paths are design/rewrite/contracts/c1-data.md,
CHANGELOG.md, docs/migrations/_unreleased.md, pkg/docs/changelog.md,
pkg/docs/mirror/migrations/_unreleased.md and .apidiff/pkg_store.txt.
Lead-owned handoffs.md, amendments and T1 inventory are untouched.

No new foreign contract change is requested. No Git write, candidate install,
pipeline, service, deployment, release or harness-memory write ran. The shared
Git configuration has no local identity or worktree override. The session
scratch directory is deleted after verification.

STATUS: CONTINUE
