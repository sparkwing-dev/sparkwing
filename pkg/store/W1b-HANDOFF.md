# W1b decision 112 continuation

W1b's billing model is implemented. Decision 112 still needs W2's production
billing-page writer before the customer checkout, card setup, pay-now and budget
mutation paths work end to end. No Git writes were made.

This session fixes two remaining billing behaviors. Launcher queue eligibility
passes over teams at their standing capacity and jobs larger than their tier,
so they cannot block another team's eligible work. It reads the same capacity
rule used by issuance without taking the ledger lock ahead of claim locks.
Issuance still rechecks and reserves under that lock. Lost disputes reverse the
payment and retain Held standing until an operator releases the hold. Won and
warning-closed disputes release their own hold.

The existing decision-112 implementation remains: one standing/rung table,
collection ladder, first-week and monthly collection, pending-admission pause,
decline downgrade, prepaid standing, Cloud deadlines, no automatic free credits
and the launch signup gate. Dollar caps and their supporting production APIs,
overrides and shared-card buckets were deleted in the preceding session. Owner
budgets and operator per-account grants remain. The billing policy numbers are
unchanged. Source billing docs, migration notes and their embedded copies now
also describe queue behavior and chargeback holds.

This session's production Go delta is **31 added, 11 deleted, net +20**. The
replaced lines belong to eligibility error handling. No legacy billing path was
restored. Combined W1+W1b store production Go against main is **21,490 added,
25,800 deleted, net -4,310**, within the **-4,250** budget by 60 lines. These counts
exclude tests, testdata, acceptance harnesses and contract declaration files.

## Evidence

- The new controller queue test failed with 409 for a full team and 400 for an
  oversized job ahead of eligible work. Both cases pass after the fix.
- The new generated dispute-order proof failed because a lost dispute restored
  New standing. It now covers lost, won and warning-closed outcomes, duplicate
  events and open/close reordering, payment conservation and operator release.
- A signed-webhook controller proof drives prepay through the real verifier,
  checkout settlement and Cloud admission. It then drives a lost dispute through
  settlement to Held, refused new queued work, successful running renewal and
  successful completion. It uses no payment or ledger fake.
- The prior excluded collection-binding property had an invalid setup fixture.
  Its conflicting setup association now satisfies the schema's shape constraint,
  so it reaches the real binding transaction. All generated cases pass.
- All 21 billing-model tests and focused collection-binding, provider, payment,
  metered lifecycle, Cloud billing and store billing tests pass. Documentation
  policy/mirror checks, repository-wide build, scoped vet, commentcheck against
  main and diff whitespace checks pass.
- Scoped race tests pass for the billing model and collection-binding proofs in
  both store and controller packages. The full store sweep passes with only
  `FuzzStorageStandingTenantInventory` excluded. Billing-page contract-fake
  authority and replay proofs pass.

Checks use `GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache`,
`GOMAXPROCS=4`, and `TMPDIR`/`GOTMPDIR=/tmp/sw-astra-W1b`. Go test uses at most
`-p 4`. Reproduce the model proofs with:

```sh
go -C /home/bombuntu/.xwing/envs/1088/sparkwing test -p 4 ./pkg/store ./pkg/controller -run '^TestBillingModel' -count=1
```

The race command adds `-race -p 2` and selects
`^TestBillingModel|^TestCollectionBinding`. The full store command uses
`test -p 4 ./pkg/store -skip '^FuzzStorageStandingTenantInventory$' -count=1`.

No native acceptance cell is newly promoted. SQLite/controller handler proofs do
not establish live Stripe, PostgreSQL, Kubernetes or browser execution. Those
remain for the lead. No old acceptance test is ported this session. The current
proof inventory is updated in `t1-port-inventory.md`.

## Remaining integration and shared files

No foreign-area contract signature change is needed. W2 must implement the
existing `BillingWriter` contract: `PrepareAuthorizedBillingPage`,
`CompleteAuthorizedBillingPage`, `RecordAuthorizedBillingPageFailure` and
`SetAuthorizedSpendBudget`. Only the controller test fake implements it in this
checkout. Production page-opening mutations still return `policy_missing`.
Existing contract-fake tests remain the evidence for that unimplemented seam.

The sole production caller migration in W1's files is `unit_claim_rows.go`,
which consumes the private eligibility helper's `(bool, error)` result and
passes Cloud placement. Other shared changes are the controller billing-model
tests, `c1-data.md`, `t1-port-inventory.md`, the billing docs, migration notes and
their embedded mirrors. Lead-owned handoffs and amendments are unchanged.

The inherited `FuzzStorageStandingTenantInventory` seeds still fail: recorded log
inventory is 3 bytes while the fixture expects `own+1`. This unrelated storage
fixture remains outside decision 112. The broad store sweep excludes only it.
The legacy external billing-route fixture cannot run: its `httptest` listener
is refused with `socket: operation not permitted`. The new controller proofs use
handlers directly. Native provider/service evidence remains with the lead. No
new setting, service, interface, schema or customer policy was added. Scratch
`/tmp/sw-astra-W1b` is removed after checks. `/tmp/sw-gocache` is retained.

STATUS: CONTINUE
