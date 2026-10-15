# W1b store-data continuation

Session base: 94764599e. The integration merge is committed and the starting
worktree was clean. No unmerged paths or conflict markers remain. No Git write
ran in this session.

## Implemented and deleted

The 12:15 materialization priority is closed through memoization. Its existing
real-store proof passes again without changing that implementation. It selects
a memo, copies output and manifest selectors through the controller wire and
host, deletes the source run and memo, and reads the exact output and artifact
bytes through persisted record authorization and filesystem signing. Missing
or corrupt output, manifest or member bytes become executable misses. The proof
also checks download charging, reader substitution and revocation after binding.
The lead can release W7 to its legacy output/log cut.

Removed the unused production methods Store.CancelNodeForExhaustedCredits and
Store.CancelNodeForComputeLimit. Their retained result behavior is ported to
TestMeteredFailureReportSettlesAndReleasesClaimProperty, using real claim
issuance and Tenant.TransitionNode. It covers runner and Cloud claims, started
and unstarted work, failure reasons, claim release, closed compute, immutable
award settlement and exact versus changed replay. Job and attempt finish times
come from the store clock and cannot precede execution. An unstarted Cloud
failure refunds its reservation. This does not implement exhaustion detection
or renewal-time grace policy.

Also removed the unused Store.AccountEmail query. The three API cuts remove 26 production lines.

The tenant SQL scope guard exposed two inherited payment queries. Collection
now reads its exact attempt under the payer already resolved from the committed
correlation. Global payer discovery is consolidated in the existing reviewed
paymentWarningTeamTx owner, removing providerPaymentOwnerTx. Its mode-scoped
association must agree with retained grant and attempt identity. W1's exception
catalog is unchanged. TestProviderPaymentRejectsConflictingPersistedPayerProperty
replaces the correlation's payer or mode across all six payment-linked outcomes
and requires unchanged finances. No forwarding alias or production mechanism
was added. Source documentation and the API snapshot reflect the cuts.

## Acceptance and test ports

Three retired tests now run through the current transition and billing owners:

- TestCancelNodeForExhaustedCreditsFailsAndReleasesTheNode.
- TestCancelNodeForComputeLimitFailsTheNodeAndReleasesTheClaim.
- TestStaleCancellationSettlesAtTheExecutionStart.

Their replacement runs eight explicit combinations and 24 generated cases.
Current transitions accept no caller-selected finish time, removing the stale
cancellation timestamp mechanism. The port inventory records that distinction.
The unpriced-class raw writer is retained because the current transition table
has no owner edge that fails an unclaimed job for that refusal. Its intended
replacement needs W1's state-machine contract; this worker did not invent one.

09:40 item 1 remains closed for W1b. Its nine store proofs already live in
pkg/store tests. The remaining internal/acceptance/store_* files cover W1's
claims, plans, credentials, schedules, capacity, transitions and recovery. No
Harness, scenario catalog or boundary registry was extended.

No new native acceptance cell is claimed. The passing package proofs include
materialization, provider settlement, blob records and authorization, logs,
retention, billing, presence, CAS, cache scope and reference edges.

## Required integration and contract handoffs

07:40 item 2 has all seven non-checkout outcomes implemented in
payment_provider_settlement.go, using the expanded C2 verified-event proof.
The existing schema fake tests pass. The production schema still cannot persist
these receipts, so production payment readiness is not claimed. W1 must apply
the existing local C1 requirements in design/rewrite/contracts/c1-data.md:

- Extend payment_events with the seven kinds, nullable non-checkout checkout,
  warning-only nullable payer, object_id, event_json and settlement_json. Keep
  checkout foreign keys and required checkout associations. Index mode/object.
- Add immutable mode/provider payment_correlations for setup or collection,
  bound to the exact payer, customer and internal attempt/charge.
- Trusted billing-page completion and collection writers create those
  associations from their committed reservations. Bind the exact attempt's
  payment intent. Signed provider metadata never chooses the payer.
- Complete mode isolation for retained legacy card fingerprints, warnings and
  collection rows. Raw legacy rows are not replacement provider authority.

W1's schema and issuance/admission/reaping files were not edited. W2 owns the
production route handoff under the lead's 14:40 decision. Its route adapters
still need to meet these persistence requirements.

Coalesced copies need a declared persisted cross-run read edge. Spec equality
and the legacy waiter table cannot grant it. Memoization satisfies the requested
12:15 byte-copy proof without that undeclared authority.

Renewal-time credit/spend exhaustion and grace remain open. The shared award,
compute, checkpoint and settlement helpers work with W1's lifecycle joins, and
all TestMeteredUnit tests pass. Legacy claim-credit and raw local output paths
still have W1/W3 callers in this checkout. Removing their backing code before
those caller migrations would break the build. W1's newer independent branch
reports more legacy cuts, but they are not in this worktree's integration base.

## Files

Production changes are in pkg/store/credits.go, card_billing.go,
payment_provider_settlement.go, payment_lookup.go and
payment_warning_settlement.go. Payment properties extend
payment_provider_settlement_test.go. Tests changed in credits_test.go,
compute_limits_test.go and credit_billing_start_test.go. The new port is
metered_failure_test.go. STORE-DATA.md corrects stale payment integration claims
and documents the raw writer cut. This report replaces the prior session note.

Shared files touched this session: design/rewrite/contracts/t1-port-inventory.md
and .apidiff/pkg_store.txt. No W1 production file or lead-owned handoffs/amendment
file was changed. No W3/W7/W9 implementation file was changed again.

The earlier materialization edits remain in these cross-area files:

- W9: internal/controllerwire/engine.go and pkg/controller/engine_snapshot.go.
- W3: internal/nodehost/output_records.go, owner_evaluation.go,
  owner_schedule.go, remote_contract.go, sdk_code.go and sdk_replay.go.
- W7: internal/blob/authority.go, keys.go, object_contract.go and objects.go,
  plus pkg/storage/manifest.go.
- Controller binding: pkg/controller/data.go preserves the default persisted
  authority resolver used by the real-store proof.

## Budget

Session net production Go delta is -28 lines: 36 added and 64 deleted, all
in pkg/store. Combined W1+W1b store production against main is +3,588:
14,796 added and 11,208 deleted, excluding tests, testdata, acceptance and contract declarations. This is 7,838
lines over the -4,250 budget. No waiver or COMPLETE claim is made. The remaining
legacy call chains and already documented unsized persistence requirements must
be resolved with the lead and W1. The payment edit consolidates existing owners
and needs no new mechanism or overage justification.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and TMPDIR/GOTMPDIR under /tmp/sw-astra-W1b. Test
parallelism is four or less.

- Final whole-repository go build -p 4 ./... passes.
- Scoped go vet passes for pkg/store, pkg/controller, internal/nodehost,
  internal/blob and tests/acceptance, including test compilation.
- The focused payment/blob/materialization/log/store-boundary selection passes
  in 52.185 seconds, including all six real-byte materialization cases and the
  provider-event proofs using their schema fake.
- The final provider/payment/tenant-scope selection passes in 8.810 seconds,
  including the new persisted-payer property.
- All TestMeteredUnit tests pass in 2.743 seconds.
- The new failure-report property passes in 1.021 seconds.
- commentcheck -base main . is clean across 2,143 Go files.
- Generated API output matches every checked-in snapshot. Diff whitespace is
  clean. Local Git configuration has no user identity or core.worktree override.

The first scope-guard run failed on the two inherited payment queries described
above. The final guard and provider properties pass after consolidation.

The first new-test compilation found another caller of the deleted compute
cancellation method in credit_billing_start_test.go. Its stale-time regression
was ported with the other two tests. No production regression or remaining
compilation failure was observed.

The lead still owns independent review, pipelines, PostgreSQL, native runtime
and provider-network evidence. No Sparkwing executable, socket-dependent
pipeline, service, deployment or release ran. Scratch is deleted at the end of
the session. The shared build cache is retained.

STATUS: CONTINUE
