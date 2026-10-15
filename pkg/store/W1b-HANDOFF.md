# W1b handoff

Continues aa81d99d2 and W1b-session1792069416.out. W1b remains incomplete.
The lead owns review, commits, pipelines and native integration.

## Merge resolution

Resolved the conflict bodies in .apidiff/pkg_store.txt,
docs/migrations/_unreleased.md and its pkg/docs mirror. The migration guide
keeps both branches' entries and removes duplicate sections. The API snapshot
was regenerated from the merged implementation. Obsolete StorageCommit stays
removed. No contract conflict required a shape change.

The Git index still marks those three paths unmerged because the brief forbids
Git writes. The lead must stage the resolved paths and complete the merge.
No merge, staging or commit command ran here.

## Completed in this session

Removed nine raw Store methods without aliases. Their implementation now belongs
to Operator in operator_card_billing.go:

- DueCardCharges, RecordAttemptIntent and SettleCardPayment.
- RecordPaymentWarning, FailCardAttempt and DropCardAttempt.
- DueCardRefunds, MarkCardRefundMade and ReportCardRefund.

The private applyCardRefund owner moved with its public callers. Controller
collection and refund workers select Operator before using these methods.
The method bodies match the merged implementation after receiver normalization.
Ledger locking, persisted payer attribution, provider payloads, replay, retry
and refund behavior are preserved. Tenant setup, budgets and recovery checkout
keep their existing handles. This adds no provider verification contract.

The C1 contract and migration guide record this handle cut. No foreign contract,
schema, price, limit, setting, wrapper or compatibility alias was added.

Production Go removes 392 lines and adds 373, including the new file, for a net
reduction of 19 lines. Most of that diff moves the existing implementation.
Six old test functions, totaling 240 lines, are removed from card_billing_test.go
and ported to final-schema fixtures in operator_card_billing_test.go.

## Invariant and test ports

Invariant: a card payment uses its attempt's persisted payer, and replay grants
credit once.

Ported these existing tests to the final schema:

- TestADebtAtTheRungIsChargedOnce.
- TestConcurrentDebtChecksOpenOneCardCharge.
- TestADeclineHoldsTheTeamUntilPaid.
- TestAnAttemptPastTheKeyLifeIsReplaced.
- TestAFailedQueuedRefundIsRetried.
- TestARefundOutcomeBeforeItsBindIsKept.

The concurrent collection test now uses two independent SQLite clients.
TestCardCollectionPaymentKeepsPersistedPayerProperty generates 48 cases and runs
15 explicit adversarial cases. It varies team, charge and attempt selectors,
checks refusal before grants, settles against the stored payer, reopens selected
stores and checks replay plus both tenants' balances. Provider responses are
trusted contract inputs. No provider or native transport is exercised.

One port initially queried the old fixture team name. That test failed, its
query was corrected, and the complete final selection passed.

No new acceptance cell was added or unskipped. No additional archived T1
source-function identity is claimed closed.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE, GOTMPDIR and TMPDIR were under /tmp/sw-astra-W1b/.

- Whole-repository go build -p 4 ./... passes.
- Vet passes for pkg/store/..., pkg/controller/... and both acceptance trees.
- Store selections pass 230 top-level tests / 669 cases, with no skips.
- The existing TestBilling and TestStorage acceptance selection passes 287
  cases. It skips 562 native/platform cells. Skips prove no integration.
- Fourteen SQLite cells pass, listed below.
- Commentcheck against main passes across 1,830 Go files.
- API snapshot, docs mirror, embedded changelog and changelog checks pass.
- gofmt and diff whitespace checks pass.

Billing selection:

`^(TestADebtAtTheRungIsChargedOnce|TestConcurrentDebtChecksOpenOneCardCharge|TestADeclineHoldsTheTeamUntilPaid|TestAnAttemptPastTheKeyLifeIsReplaced|TestAFailedQueuedRefundIsRetried|TestARefundOutcomeBeforeItsBindIsKept|TestCardCollection|TestPayment|TestMeteredUnit|TestMeteredAccounting|TestStoreData|TestATeamStaysFrozen|TestReleaseCreditFreezes|TestADispute|TestAReversalMayNotUse|TestFrozenTeams|TestTenantSQLScope)`

Data selection:

`^(TestBlob|TestLogPersistence|TestLogPublication|TestLogMaintenance|TestRetainedStorage|TestTenantWriters|TestTenantTwins|TestGrantCredits|TestCreditGrantRequest|TestCreditsBalance|TestCreditReversal)`

Acceptance selection: `^(TestBilling|TestStorage)`.

Passing SQLite cells:

- billing-start-award-versus-start/store-sqlite.
- billing-ledger-survives-retention/store-data-sqlite.
- storage-cas-integrity/store-data-sqlite.
- storage-replica-safe-accounting/store-data-sqlite.
- storage-quota-settlement-crash-replay/store-data-sqlite.
- storage-log-segments-and-fallback/store-data-sqlite.
- storage-hasmany-read-set/store-data-sqlite.
- storage-hasmany-large-budget/store-data-sqlite.
- storage-reachability-and-retention-tails/store-data-sqlite.
- storage-memo-and-depcache-scope/store-data-sqlite.
- storage-reference-edge-laundering/store-data-sqlite.
- storage-publish-one-way/store-data-sqlite.
- storage-owner-receipts-and-stream-verification/store-data-sqlite.
- storage-local-remote-cache-opt-in/store-data-sqlite.

No full store/controller suite, PostgreSQL, provider, native placement or race
pass is claimed. The lead still owns the pipeline checks and independent review.

## Assignment audit and unfinished work

- The approved record mutation owner, output catalog removals, retired
  expired-claim helper and earlier refund/dispute handle cut remain done.
- A1-24: store authority, binding and read-set adapters remain implemented.
  DataController still needs production ResolveAuthority, ReadBlob and
  ChargeDownload composition. Its default record binding also needs the
  physical manifest reader. NewDataClient remains a declaration with a panic.
- A1-27: the merged claim/start/renewal/retry/finish/release/reaper billing join
  remains implemented. C2's production SettleBilling calculator is present.
  Spend-exhaustion/grace enforcement and legacy claim/trigger credit windows
  remain unfinished. W1's later continuation reports removing its duplicate
  unmetered calculator override. Merge that continuation instead of repeating it.
- A1-28: verified checkout settlement, raw paid-grant refusal and the earlier
  refund/dispute tenant cut remain done. The deployment card-worker handle cut
  is now done. Provider verification and controller delivery remain integrations.
- A1-29 through A1-35: records, members, read sets, exact uploader receipts,
  immutable tickets, memo/depcache pointers, tombstones and tagged log persistence
  remain implemented. Signed-store composition, executable binary policy,
  authenticated log transport and physical/native proof remain integration work.
- A1-53: native, provider and PostgreSQL evidence remains unverified here.

Priority remaining cuts are ReserveUpload/CommitUpload, ReleaseStorage, raw
output objects, old node-log transport and legacy claim/trigger credit windows.
Their legacy controller, client and lifecycle callers remain. The final tickets,
records and segments provide their persistence replacements. None of those
remaining paths is declared retired by this session.

Spend/grace still needs a durable exhaustion anchor per award and cancellation
in the same renewal reply. accountBillingAwardTx currently returns only C2's
BillingSettlement, and W1's renewal helpers fill LeaseBeat before accounting.
W1 owns those renewal files. Coordinate that join rather than adding a raw
terminal writer or enforcing against a caller clock. No spend/grace change was
implemented in this session.

The free-slot question remains open. Final tickets have no server-verified
intended use before signing, while legacy small outputs can bypass a full slot
pool. Lead/C1/W7 must supply intended-use authority or an explicit policy choice.
Allowance-driven physical eviction remains open.

## Shared files and friction

The only production caller migration outside pkg/store is
pkg/controller/card_billing.go. Store fixture migrations are
card_billing_test.go, card_payment_replay_boundary_test.go,
payment_refunds_test.go and warning_hold_team_test.go.

The W1-owned tenant_sql_scope_guard_test.go has seven mechanical Store-to-Operator
method-key renames required by the moved SQL owners. No W1 production file changed.

Shared documentation/generated files are design/rewrite/contracts/c1-data.md,
CHANGELOG.md, docs/migrations/_unreleased.md, pkg/docs/changelog.md,
pkg/docs/mirror/migrations/_unreleased.md and .apidiff/pkg_store.txt.
Lead-owned handoffs, amendments and the T1 inventory are untouched.

The lead must stage the three conflict resolutions. The sandbox and brief permit
no Git writes. The shared Git configuration has no local identity or worktree
override. No candidate install, Sparkwing binary, pipeline, service, deployment,
release, subagent or harness-memory write ran. The scratch directory is deleted
after verification.

STATUS: CONTINUE
