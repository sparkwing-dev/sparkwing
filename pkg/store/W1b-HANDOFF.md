# W1b handoff

Continues f6eb26100 and W1b-session1792072784.out. W1b remains incomplete.
The worktree started clean. The lead retains review, commits and pipelines.

## Implemented

Fraud-warning persistence and its payer hold now share recordPaymentWarningTx.
A repeated warning ID cannot substitute its payment or card fingerprint. The
store checks persisted identity before applying any hold. Payer resolution
compares matching paid grants with every matching card attempt. Conflicting
team evidence fails instead of selecting a first row or trusting one source.
The warning, hold and business event roll back together on any failure.

Invariant: a warning ID affects only its persisted payment and card, and a
payment with conflicting payer evidence cannot freeze any tenant. The generated
identity regression failed against the previous implementation. It now passes
64 generated identity cases. Another property covers 64 ambiguous-payer cases.
Shared-card payments still hold their own teams. Exact replay after release and
reopening cannot restore a hold. Audit failure leaves no warning or hold.

Removed the provider-only legacy warning-hold identity lookup. Final holds use
only the tenant-scoped identity. No provider metadata or caller-selected team
was added as a replacement authority source.

Production Go deletes 29 existing lines and adds 59, including the extracted
37-line transaction owner, for a net increase of 30. Deleted the 220-line legacy
warning test file. Its supported assertions move to final-schema fixtures.
Tests net +73 lines. No W1 production file or foreign contract was edited.

## Tests and acceptance

Ported TestSharedFraudWarningHoldsEveryTeam to final card collection fixtures.
It retains warning-first, payment-first and concurrent payments, both balances,
shared-card holds, distinct tenant hold IDs, trust/headroom refusal, idempotent
refunds, release/replay and the absence of unsolicited refund work. Existing
TestFrozenTeamsMeteredClaimsAreRefused supplies the final claim-refusal proof.
The unrelated-dispute row from TestWarningHoldRetainsLegacyReleaseAndUnrelatedDispute
moves to TestPaymentWarningHoldDoesNotReplaceUnrelatedDispute. Its two legacy
identity rows are dropped with the compatibility lookup. No new archived T1
source-function identity is claimed complete. The spend test helper uses the
final capability_prefix column, fixing its stale token_prefix fixture.

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
-p 4 where applicable, and cache/temp directories under /tmp/sw-astra-W1b/.

- Whole-repository go build -p 4 ./... passes.
- go vet -p 4 ./pkg/store passes.
- Focused store regressions pass 84 top-level tests / 343 cases.
- TestBilling and TestStorage pass 287 cases and skip 562 native/platform cases.
- Commentcheck against main passes across 2,118 Go files.
- Documentation mirror and git diff --check pass.

Fourteen existing SQLite acceptance cells pass. No new cell was activated:

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
- storage-local-remote-cache-opt-in/store-data-sqlite

Reproduce the store selection with:

`go test -p 4 ./pkg/store -run '^(TestPayment|TestSharedFraudWarningHoldsEveryTeam|TestFrozenTeamsMeteredClaimsAreRefused|TestMeteredAccounting|TestMeteredUnit|TestStoreDataMetered|TestLogPersistence|TestLogPublication|TestLogMaintenance|TestCardCollection|TestADebt|TestConcurrentDebt|TestADeclineHoldsTheTeamUntilPaid|TestAnAttempt|TestAFailedQueued|TestARefundOutcome|TestAWarning|TestANonActionableWarning)' -count=1`

Acceptance: `go test -p 4 ./tests/acceptance ./internal/acceptance -run '^(TestBilling|TestStorage)' -count=1`.
No full-suite, PostgreSQL, provider, native placement, race or pipeline pass is claimed.

## Contract changes needed

Lead decision 07:40 item 2 remains open. C2 PaymentEvent still has only checkout
session/payment identity, amount, currency, mode and three checkout outcomes.
The W2 branch has the same shape. There is no declared expanded interface or
stub against which W1b can implement the missing verified-event dispatcher.
SettlePayment therefore remains checkout-only. The brief forbids inventing or
editing another area's contract. The warning transaction owner is ready for
that dispatcher and is exercised through the real Operator path.

W2 must declare verified setup/customer/method/fingerprint/completion facts,
collection attempt/payment correlation, refund identity/amount/status, warning
identity/payment/card/actionability and dispute identity/payment/outcome. The
provider handoff must retain those facts without accepting payer metadata.

W1 must extend payment_events as well. Its checkout_id is required, its foreign
key requires a credit_checkouts row, and its kind check permits only the three
checkout outcomes. Card setup has no persisted pending setup/customer-to-tenant
binding or commit API. Those correlations need a declared durable owner before
settlement can derive the first card's team. Broader card/refund event correlation
must retain the configured payment mode. No speculative schema was added.

W1 spend/grace integration remains open. billing_awards has no durable per-award
exhaustion anchor. LeaseBeat is filled before accounting. accountUnitBillingTx
returns only error. W1 must expose the committing renewal policy result for job,
host, plan and eval while preserving prepaid runway, customer-wait exclusions,
existing grace/spend policy and holder drain rights. W1b will supply the policy
once that join is declared. No raw terminal-state writer or caller clock was added.

## Remaining area work and amendment audit

- A1-24 and A1-29 through A1-35 remain implemented on the persistence side.
  C2 still needs live data resolution, ReadBlob, ChargeDownload and physical
  manifest composition. Source-before-run binding and verified small-output
  intended use remain open. Allowance-driven physical eviction remains open.
- A1-27 real claim/start/renewal/retry/finish/release/reaper settlement joins remain
  done. Spend/grace and the old claim-credit consumer cut remain open with W1.
- A1-28 and decision 07:40 item 2 remain limited by the contracts above.
- W1/W7 must define the unexecuted destination record identity/read edge before
  transactional materialization can copy memo/retry/coalesced records. Public
  CopyMemo requires a live positive attempt. Synthetic attempts are forbidden.
- W2/W3/W9 still need final blob.LogTransport composition. Legacy log readers
  use node_logs. LogSegmentUploader targets removed node writers. Old output
  clients/local persistence also remain. These cuts require their final callers'
  authority and subject bindings, rather than reintroducing removed APIs.
- A1-53 native/provider/PostgreSQL evidence remains with T1/lead. No native cell
  was unblocked or represented as passing. Lead-owned ledgers are unchanged.

## Shared files, assumptions and friction

Outside pkg/store, only design/rewrite/contracts/c1-data.md, CHANGELOG.md and
its generated pkg/docs/changelog.md mirror changed. W1 files, lead-owned
handoffs/amendments, T1 inventories and API declarations are unchanged.

The missing expanded C2 contract and checkout-only event schema block payment
completion. The missing W1 renewal result/anchor blocks spend/grace completion.
Legacy billing fixtures initially failed on token_prefix. Their shared helper
now uses capability_prefix and the affected warning checks pass. The broad
legacy suite is not represented as passing.

Mechanisms added: one private transaction-local warning owner, used by the
existing Operator path. It replaces the scattered write/effect sequence so the
verified-event dispatcher can use the same transaction. No configuration,
price, quota, grace, schema, compatibility alias or public write API was added.

No Git write, Sparkwing executable, candidate, pipeline, service, release,
subagent or harness-memory write ran. Scratch /tmp/sw-astra-W1b/ is deleted
after verification. Independent review remains with the lead.

STATUS: CONTINUE
