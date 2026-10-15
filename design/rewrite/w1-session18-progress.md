# W1 continuation 18

W1 remains incomplete. This continuation joins the final unit lifecycle to
W1b's accounting helpers for amendment A1-27. W1b implementation files are
unchanged. The lead owns Git writes, independent review and pipelines.

## Merge resolution

The sole conflict was handoffs.md. Both entries are retained and all markers
are removed. Git still marks the file unmerged because the brief forbids Git
writes. The lead must stage that resolved file. The two staged integration test
changes in internal/limits and sparkwing/sparkwingtest are untouched.

## Implemented and deleted

Claim issuance now records the immutable payer, executor, reservation and price
through W1b's recordBillingAwardTx. Pricing reads sealed request or resolved job
resources. The existing minute reservation remains separate from the final
compute minimum. A generation-zero here capability authorizes a future claim
and opens no compute award. Claiming takes the reservation in the same transaction
as its unit binding. A failed reservation returns no bearer or partial claim.

Queued matching passes over work that cannot fund its reservation. The actual
award repeats W1b's funding checks under the ledger lock. A final deadline check
rolls back an award if authority or the eval owner expires during accounting.

Job starts and Cloud job renewal open compute intervals. Renewal alone consumes
no attempt. Owner Executing reports current compute, including false during
customer-runner waits. Pauses close intervals and resumes open fresh keys.
Retries retain one reservation and record separate intervals. Renewal checkpoints
follow interval creation even when the clock advances inside a transaction.

Release, plan acceptance, expansion acceptance, FinishRunAndUnit and reaping
settle the same award. Reaping passes the persisted lease lapse. Eval settlement
derives its independent lease generation from persisted authority, so omitting
Actor.Owner cannot bypass billing. Result, interval, ledger and event changes
roll back together when settlement fails. Bootstrap exchange retains the original
reservation and ledger attribution.

Removed **22 source lines** from authority_issue.go: the unavailable-metering
refusal, the raw unmetered-only award insertion and duplicated billing-unit
mapping. The shared mapping and W1b ledger owner replace them. No legacy public
store API is removed in this continuation. AcceptPlan's old implementation and
validator, StateStoreOld, legacy claim/trigger writers and readiness code remain.

## Tests and acceptance

Invariants: a stale report cannot settle a replacement award, and issuance cannot
outlive its claim authority. Two generated properties cover 24 replacement-award
cases and 48 advancing-clock expiry cases. Refusal preserves the replacement
claim or rolls back the new reservation and charge.

The unit-billing tests cover atomic unfunded refusal, the last-reservation race,
queue fairness across teams, pricing after settings changes and exchange,
unstarted refunds, owner wait exclusion, independent eval/source generations,
Cloud renewal without an attempt, plan reservations without compute, retries,
lease-lapse settlement and failed-settlement rollback. They use W1b's real ledger
helpers and a C2 calculator fake.

Retained ports include credits_test.go's
TestMeteredClaimIsRefusedWhenTheBalanceCannotCoverTheReservation,
TestClaimReservationLetsOnlyOneRunnerClaimTheLastMinute,
TestFinalizeNodeCreditsRefundsAReservationWhenExecutionNeverStarts and
TestFinalizeNodeCreditsConsumesTheMinimum. No additional archived T1 source
identity is claimed closed. The old billing tests remain with their legacy APIs.

The new passing cell is
TestBilling/billing-start-award-versus-start/store-sqlite. It exercises real
award, exchange and start transactions, immutable rates and reopening. It does
not claim production calculator, HTTP, native runner or provider coverage.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and caches,
temporary directories and test scratch beneath /tmp/sw-astra-W1. Tests use -p 4.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet passes for pkg/store/..., internal/acceptance/... and tests/acceptance/....
- The W1 file selection passes 383 top-level tests / 1,059 cases with no skips.
  Extract Test names from w1-session18-tests.txt, omit TestPostgres names and pass
  their anchored alternation to go test -json -p 4 ./pkg/store. The added
  TestMeteredUnitApplicationRetryKeepsOneReservation passes separately.
- internal/acceptance and tests/acceptance with -skip '^TestProtocol$' pass
  19 top-level tests / 199 cases. There are 2,422 native/platform skips.
- Required commentcheck -base main, sleepcheck -base HEAD, embedded migration
  and changelog checks, sync-docs --check and git diff --check pass.

The broad claim-name selection also reached two inherited legacy fixture failures:
TestAwardScannedNodeHonoursTheQueueRequirement and
TestAward_RefusesARunWithACancelRequest still call CreateRun with runs.args_json.
No full legacy suite, PostgreSQL, race, performance, native platform, provider,
candidate or pipeline pass is claimed. Git identity and worktree configuration
are unchanged. Scratch is deleted after the final checks.

## Amendment audit and real integrations

| Entries | State and remaining work |
| --- | --- |
| A1-07, A1-11, A1-44, A1-45 | Prior sealing, settings, source and secret-classification implementation remains. Trusted production intake/checkout providers still need installation. |
| A1-08, A1-18, A1-55 | Matching, push ancestry, ordinals, here reclaim and source-attempt fencing remain. This continuation adds funded eligibility and the final issuance deadline fence. W9/W3 still need SourceAttempt transport and consumption. |
| A1-13 through A1-17 | Reconciliation, failure, cancellation and expansion persistence remain. W9 still needs failure stage, GuardRefusal, materialized-result identity and generation-zero expansion transport. W1b owns transactional record copy/attachment. |
| A1-20, A1-25, A1-26 | Provisioning, secret validation and enrollment interfaces remain. W2 owns production origin/profile providers and installation. |
| A1-21 through A1-24 | Session, digest/CSRF, authority, binding and audited source release remain. C2 BindingState still lacks cancellation reason. Exact operation/digest transport, concurrency selectors and teamless-session/origin adapters remain foreign work. |
| A1-27 | W1's lifecycle join is implemented and tested with a calculator fake. C2 SettleBilling is still a panic stub. W3 must report current compute on owner renewal. W1b still owns spend-exhaustion/grace policy and the remaining legacy accounting cut. Production billing is incomplete. |
| A1-28 | Committed-checkout settlement remains. W2 owns the verified provider handoff and old grant-route cut. |
| A1-29, A1-31 through A1-35 | W1b retains blob, upload, log, accounting and retention ownership. No W1b implementation or fixture was changed. |
| A1-38 | W8/W9 must supply immutable award-generation and Kubernetes Job-instance correlation. SyncLauncher remains blocked on that foreign contract gap. |
| A1-40, A1-53 | W8/T1/lead retain readiness, HTTP/api.sock, native platform/provider/Postgres and real-repository evidence. |
| A1-51 | Core legacy store API and caller retirement remains W1 work. Earlier schedule, reaper and concurrency cuts remain complete. |

No new foreign type change is requested. The existing cancellation, source-attempt
and launcher-correlation contract gaps remain. The completed session, reaper,
concurrency and schedule handoffs are retained.

## Shared files, assumptions and friction

Shared code changes are internal/acceptance/store_unit_billing.go,
internal/acceptance/store_recovery.go and tests/acceptance/billing_test.go.
The recovery fixture now expires an award after its creation time. Its old
lease_expires_at=1 fixture made two real re-award cells violate the billing
time range. The seed helper now retains service identity and nullable deployment
team. Production changes stay in W1's store files. There are no cross-package
production caller migrations in this continuation.

Shared prose is c1-data.md, handoffs.md, t1-port-inventory.md, the unreleased
migration note and changelog with generated mirrors. No schema, public setting,
environment variable, service or release changed.

The private per-Store calculator seam substitutes C2 in tests because its
production contract is a panic stub. Production still calls C2.SettleBilling.
The smaller direct call cannot test the completed transaction joins until C2
lands. The seam adds no public setter or alternate billing policy. Removing the
field and injection points is its undo.

Friction included cold compilation, the two invalid recovery timestamps, and
using a constant test clock where a moving clock was needed. The moving-clock
renewal and expiry properties ran red before their fixes. No deployed state,
Sparkwing binary, Git write or harness-memory write was used.

STATUS: CONTINUE
