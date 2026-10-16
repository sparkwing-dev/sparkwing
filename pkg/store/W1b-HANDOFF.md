# W1b decision 112 verification

The W1b store implementation is complete to the declared contracts. Decision
112 still needs W2's production `BillingWriter` before customer checkout, card
setup, pay-now and budget mutations work end to end. No production behavior or
policy number changed in this session. No Git writes were made.

The remaining controller collection proof is now covered.
`TestBillingModelControllerPendingCollectionAndDeclineLetRunningWorkFinish`
creates debt through a real Cloud reservation, runs the controller collection
worker against a provider transport fake and delivers a signed decline webhook
twice. Both pending collection and Held standing prevent queued admissions while
the current holder renews. The job completes successfully after the decline.
The verifier, collection association, ledger, claims and transitions are real.
Only the external payment transport is faked. This test passed the existing
production code. No new production fix or failing regression is claimed.

The existing billing implementation retains the single standing/rung table,
postpaid ladder, first-week and monthly collection, pending-admission pause,
decline downgrade, immediate prepaid standing, Cloud deadlines, no automatic
free credits and the launch signup gate. Dollar caps and their production
support were deleted in earlier sessions. Owner budgets and operator grants
remain. `docs/billing.md` and `docs/migrations/_unreleased.md` already describe
that behavior. Their policy and embedded-mirror checks pass.

Production Go delta this session is **0 added, 0 deleted, net 0**. The remaining
unused `CreditLimitCents` field is deleted from the old controller test fixture
(one field line). A shared signed-webhook test helper replaces the inline copy.
Combined W1+W1b store production Go against main remains **21,490 added, 25,800
deleted, net -4,310**, within the **-4,250** budget by 60 lines. Counts exclude
tests, testdata, acceptance harnesses and contract declarations.

## Verification

All commands use `GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache`,
`GOMAXPROCS=4` and `TMPDIR`/`GOTMPDIR=/tmp/sw-astra-W1b`. Go test uses at most
`-p 4`. The worktree is `/home/bombuntu/.xwing/envs/1088/sparkwing`.

- Repository-wide `go build ./...` passes.
- `go vet ./pkg/store ./pkg/controller` passes.
- All 22 `TestBillingModel` tests pass across store and controller.
- Collection binding, payments, cards, metered lifecycle, store billing and
  billing-page contract-fake suites pass with the one socket fixture excluded.
- Race checks cover the billing model, collection binding and billing-page
  authority/replay properties in both packages.
- Docs policy, mirror and migration tests pass. Commentcheck against main and
  `git diff --check` pass.

Reproduce the focused billing sweep with:

```sh
go -C /home/bombuntu/.xwing/envs/1088/sparkwing test -p 4 ./pkg/store ./pkg/controller -run '^TestBillingModel|^TestCollectionBinding|^TestPayment|^TestCard|^TestMetered|^TestStoreBilling|^TestBillingPageHTTPAuthorityAndReplayProperty' -skip '^TestCardBilling_OwnerSetsABudget$' -count=1
```

The race command uses `-race -p 2` and selects
`^TestBillingModel|^TestCollectionBinding|^TestBillingPageHTTPAuthorityAndReplayProperty`.
Docs select `^TestPkgDocsContentMatchesDocsRoot$|^TestEmbeddedChangelogMatchesRoot$|^TestMigrations`.
Commentcheck is `go run ./internal/commentcheck -base main .`.

The existing SQLite acceptance proofs `TestStoreBillingStartAwardVersusStart`
and `TestStoreBillingRetention` pass in the focused sweep. No native acceptance
cell is promoted. No additional old test is ported. The proof inventory records
the new controller coverage. `TestPortDestinations` still fails on three
inherited W1 references already present at HEAD: missing
`acceptance_actions_enrollment_test.go:TestStoreActionsEnrollment`,
`acceptance_enrollment_test.go:TestStoreEnrolledClaims` and
`acceptance_launcher_sync_test.go:TestStoreLauncherSync`. Each also lacks a
matching scenario group. These references are outside decision 112 and were
not changed.

## Open integration and limits

No foreign contract signature change or product decision is needed. W2 must
implement its existing `BillingWriter`: `PrepareAuthorizedBillingPage`,
`CompleteAuthorizedBillingPage`, `RecordAuthorizedBillingPageFailure` and
`SetAuthorizedSpendBudget`. Only the controller contract fake implements it in
this checkout. Production page mutations still return `policy_missing`.
Contract-fake proofs pass. Live checkout completion is not claimed.

The broader controller selection fails at
`TestCardBilling_OwnerSetsABudget` because its `httptest` listener is refused
with `socket: operation not permitted`. The complete docs suite reaches the
same restriction in `TestWebClient_Versions_HappyPath`. Focused socket-free
selections pass. Live Stripe, PostgreSQL, Kubernetes and browser execution
remain lead-owned evidence. The inherited unrelated
`FuzzStorageStandingTenantInventory` failure from the previous session was not
rerun or changed.

Shared files touched are `pkg/controller/billing_model_internal_test.go`,
`pkg/controller/card_billing_test.go` and
`design/rewrite/contracts/t1-port-inventory.md`. No production caller migration
is needed. Lead-owned handoffs and amendments are unchanged. No setting,
service, interface, schema or customer restriction was added. Scratch
`/tmp/sw-astra-W1b` is removed after checks. `/tmp/sw-gocache` is retained.

STATUS: CONTINUE
