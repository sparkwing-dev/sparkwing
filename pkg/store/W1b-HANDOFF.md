# W1b decision 112

W1b's billing policy implementation is finished. The production billing-page
adapter owned by W2 is still absent, so decision 112 is not complete end to end.
No Git writes were made.

Implemented the shared standing/rung table, collection ladder, first-week and
monthly collection, pending-admission pause, decline downgrade and Held standing.
Cloud issuance serializes vCPU admission with reservation. Awards carry the tier
job deadline. Reclaiming a job retains its first Cloud admission clock. Existing
running work drains during collection and payment holds. Prepaid settlement
raises standing immediately. Customer-runner placement receives neither the
Cloud standing capacity limit nor its deadline. Owner budgets and operator
credit grants remain. The launch gate admits at most the automatic account
limit, reports its capacity state, and preserves explicit operator approval.

Deleted team/card spend ceilings, daily caps, purchase-window and balance caps,
cap overrides, shared-card spend buckets, obsolete error/API fields, purchase
window helpers and their tests. Dashboard callers now show standing and permit
Add funds with a saved card. The final rewrite schema is edited in place. No
intermediate schema version or migration was introduced.

The session production Go delta is **305 added, 618 deleted, net -313**. Store
production Go alone is **-283**. Production TypeScript is **net -225**. The combined
store delta against main, including new files and using the lead's exclusions,
is **-4,330**, within the **-4,250** budget by 80 lines.

## Evidence

- All 18 billing-model proof tests pass through the store, controller claim and
  read handlers, and controller sign-in flow. The identity provider and runner
  profile are external contract fakes. Prepay exercises committed checkout
  settlement. Generated and independent-client claims pin the capacity invariant.
- The final focused metering, lifecycle, billing retention, controller claim and
  billing-page contract tests pass. The reclaim regression failed with a renewed
  clock before the fix. Scoped race tests pass after that fix.
- A full store sweep passes with two inherited tests excluded. This sweep
  preceded the final reclaim fix. The final fix has the focused evidence above.
  The excluded tests are `TestCollectionBindingCannotChangePayerProperty` (its
  corruption fixture violates the setup/collection CHECK constraint) and
  `FuzzStorageStandingTenantInventory` (the inherited inventory assertion).
- Repository-wide `go build ./...`, scoped vet, commentcheck against main, docs
  mirror checks, TypeScript checking and changed-file ESLint pass. The billing
  and operator frontend unit files pass all 39 tests. The documentation drift
  test compares the policy table and ladder with `docs/billing.md`.
- Existing immutable-price, refund, warning, collection replay and retention
  proofs remain. Retired dollar-cap proofs were replaced, not restored. No
  native acceptance cell is newly claimed.

Checks use `GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache`, with
`TMPDIR` and `GOTMPDIR` under `/tmp/sw-astra-W1b`, and at most `-p 4`.
Reproduce the policy proofs with:

```sh
go -C /home/bombuntu/.xwing/envs/1088/sparkwing test -p 4 ./pkg/store ./pkg/controller -run '^TestBillingModel' -count=1
```

The broad store sweep adds the skip expression
`^TestCollectionBindingCannotChangePayerProperty$|^FuzzStorageStandingTenantInventory$`.

## Integration and shared files

No foreign-area contract signature change is needed. C1-local type/schema cuts
and the retired proofs are recorded in `c1-data.md`, `t1-port-inventory.md` and
the migration notes. Source docs and their embedded mirror are updated.

W2 still must provide the real `BillingWriter` adapter, including
`PrepareAuthorizedBillingPage`, `CompleteAuthorizedBillingPage`,
`RecordAuthorizedBillingPageFailure` and `SetAuthorizedSpendBudget`. Only a test
fake implements that interface in this checkout. Its absence leaves checkout,
card setup, pay-now and owner-budget mutations at `policy_missing` in production.
The existing billing-page contract fake tests pass. This is an implementation
handoff, not a request to change the interface.

Minimal caller migrations outside the store are
`pkg/controller/{billing_routes,billing_trust,operator,signup_gate,team_billing}.go`,
the operator/team billing test files, and
`web/src/{app/operator/page.tsx,app/team/billing/page.tsx,lib/billing.ts,lib/operator.ts}`
with their unit/browser fixtures. Public store/controller API snapshots changed.
Shared store integration files include `authority_issue.go`, `unit_billing.go`,
`compute_limits.go`, `console.go`, `free_tier.go`, `schema.go`, `schema.sql`,
`signup_gate.go`, `metered_accounting.go`, `storage_credits.go` and the maintenance
catalog. The lead-owned handoffs and amendments are unchanged.

Native Stripe, PostgreSQL and browser execution remain for the lead. The
sandbox refuses socket listeners, including the existing external signup-route
fixture. The new controller proof avoids listeners and still uses the real
sign-in/store path. Browser interaction/performance was not measured. Node's
test subprocess wrapper failed in the sandbox. Running each changed unit file
directly with Node and tsx passed. Scratch is removed after checks and the
shared build cache is retained.

STATUS: CONTINUE
