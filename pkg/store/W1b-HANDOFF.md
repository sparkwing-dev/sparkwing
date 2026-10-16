# W1b decision 112 verification

A declined retry now restores Held after an operator releases a previous decline
hold. Decline holds identify the collection attempt instead of the charge. The
existing successful-collection transaction releases all decline holds together.
The conflict-ignore clause is removed because the locked attempt transition
already admits each failure once.

Invariant: every distinct declined collection attempt creates its own hold, and
replaying an earlier decline never reinstates its released hold. No interface,
setting, service, schema column or customer restriction was added. No policy
number changed. The ladder and standing table remains in `billing_policy.go`.
Dollar caps and their support remain deleted. Owner budgets and operator grants
remain. No production caller migration was needed.

## Tests first

Before the production change, both regressions failed with New standing on the
second declined attempt:

- `TestBillingModelEveryDeclinedAttemptHoldsUntilReleasedProperty` generates
  retry counts and duplicate deliveries through real store settlement and named
  operator release. Every distinct decline holds the team. Replays leave a
  released hold unchanged.
- `TestBillingModelControllerPendingCollectionAndDeclineLetRunningWorkFinish`
  now releases the first hold, replays its signed webhook and declines a newly
  bound retry. It proves paused admissions, Held standing, continued renewal
  and successful completion through the real controller and store. Only the
  provider transport is faked.

Both pass after the fix. The existing successful-collection test now retains
holds from two declined attempts and proves collection clears both. The shared
webhook helper captures the event timestamp once so duplicate deliveries retain
identical verified facts.

`docs/billing.md`, the unreleased migration notes and both embedded mirrors now
explain decline retries and release replay. The C1 implementation note and port
inventory record the same invariant. No foreign contract signature changed.

W1b production Go delta this session is **1 added, 1 deleted, net 0**. Before
concurrent billing-writer edits appeared, combined W1+W1b store production Go
against main was **21,490 added, 25,800 deleted, net -4,310**, within the
**-4,250** budget by 60 lines. This W1b fix leaves that baseline unchanged.
The separate writer's new production code is not counted as W1b work here.
Counts exclude tests, testdata, acceptance harnesses and contract declarations.
No legacy API or production file was deleted by W1b this session.

## Verification

The worktree is `/home/bombuntu/.xwing/envs/1088/sparkwing`. Commands use
`GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache GOMAXPROCS=4` and
`TMPDIR=/tmp/sw-astra-W1b GOTMPDIR=/tmp/sw-astra-W1b`. Go test uses at most `-p 4`.

- Repository-wide `go build ./...` passes.
- `go vet -p 4 ./pkg/store ./pkg/controller` passes.
- All 23 W1b `TestBillingModel` tests pass across store and controller.
- Focused collection, provider settlement, payment, card, metered lifecycle,
  billing acceptance and billing-page contract-fake tests pass.
- Race checks over the billing model, collection binding and billing-page
  authority/replay properties pass in both packages.
- Docs mirror, embedded changelog and migration checks pass. The billing policy
  drift test passes. Commentcheck against main and `git diff --check` pass.

Reproduce the focused sweep with the environment above:

```sh
go -C /home/bombuntu/.xwing/envs/1088/sparkwing test -p 4 ./pkg/store ./pkg/controller -run '^TestBillingModel|^TestCollectionBinding|^TestPayment|^TestProvider|^TestCard|^TestMetered|^TestStoreBilling|^TestBillingPageHTTPAuthorityAndReplayProperty|^TestHoldsAndRevocation$|^TestMonthEndBillsASmallDebt$|^TestSettlementChecksTheAttemptAndTheFrozenAmount$|^TestBillingWritersShareOneLockOrder$' -skip '^TestCardBilling_OwnerSetsABudget$|^TestBillingModelProductionPagesAndBudget$' -count=1
```

Race checks use `-race -p 2` with
`^TestBillingModel|^TestCollectionBinding|^TestBillingPageHTTPAuthorityAndReplayProperty`.
Exclude the separately authored production-page probe when reproducing on the
shared checkout. Docs select
`^TestPkgDocsContentMatchesDocsRoot$|^TestEmbeddedChangelogMatchesRoot$|^TestMigrations`.
Commentcheck is `go run ./internal/commentcheck -base main .`.

The existing SQLite proofs `TestStoreBillingStartAwardVersusStart` and
`TestStoreBillingRetention` pass. No native acceptance cell is newly
promoted and no additional legacy test is ported. `TestPortDestinations` still
fails on the same three inherited W1 references:
`acceptance_actions_enrollment_test.go:TestStoreActionsEnrollment`,
`acceptance_enrollment_test.go:TestStoreEnrolledClaims` and
`acceptance_launcher_sync_test.go:TestStoreLauncherSync`. Each file and matching
scenario group is missing. These references are outside decision 112.

## Open integration and limits

Decision 112's production page integration changed concurrently while W1b
finished its report. A separate session added `pkg/store/billing_pages.go` with
`PrepareAuthorizedBillingPage`, `CompleteAuthorizedBillingPage`,
`RecordAuthorizedBillingPageFailure` and `SetAuthorizedSpendBudget`, plus
billing-page schema and controller wiring edits. These files were absent at the
start of W1b verification. They are not W1b changes and their final integration
has not been verified here. The former missing-writer report is superseded by
that work in progress. No foreign signature change or product decision is
requested by this fix.

The separately authored `pkg/controller/billing_native_test.go` was left
untouched. Its `TestBillingModelProductionPagesAndBudget` initially failed
before billing mutation with HTTP 401, `session is unavailable`. Its fixture
and production implementation then changed in the shared checkout. That
initial result is not a verdict on the later version. The final focused W1b
sweep excludes this probe. Its owner must verify real checkout, card setup,
pay-now, budgets and authority/replay behavior after the writer is complete.
Store settlement and signed webhooks are real in the passing W1b proofs.

The existing `TestCardBilling_OwnerSetsABudget` socket fixture is also excluded
because the sandbox refuses listeners. Live Stripe, PostgreSQL, Kubernetes and
browser execution remain lead-owned integration evidence. Build, vet, race and
docs checks above passed before the concurrent writer changes settled. They
certify W1b's one-line fix, not the final shared billing-writer implementation.

Shared files touched: `pkg/controller/billing_model_internal_test.go`,
`docs/billing.md`, `docs/migrations/_unreleased.md`, both corresponding
`pkg/docs/mirror` files, `design/rewrite/contracts/c1-data.md` and
`design/rewrite/contracts/t1-port-inventory.md`. Lead-owned handoffs and
amendments are unchanged. No Git writes were made. Scratch
`/tmp/sw-astra-W1b` is removed after checks. `/tmp/sw-gocache` is retained.

STATUS: CONTINUE
