# W1 continuation: Cloud metering, attempt retirement and payment schema

W1 remains incomplete. Session HEAD is `f0898248e`. The pending integration merge
is `9e151731d`. Every conflict has a working-tree resolution and no conflict
markers remain. Deleted files stay deleted, including files deleted on either
side. C1 and the port inventory retain both areas' compatible additions.
The lead must stage the resolutions and finish the merge. No Git write ran.

The first post-resolution build exposed the merged result-reader mismatch.
The reader and node transition now use W1b's final output/manifest columns and
its existing selector writer. The redundant `result_records` writer and check
are gone. This is merge adaptation only. W1b's materialization implementation
is unchanged, and all six real-byte materialization cases pass.

## Implemented and deleted

Cloud placement now always meters its award. One helper supplies both issuance
and queued eligibility. Customer-runner placement keeps the enrolled root's
policy. Before the fix, `TestMeteredRootPolicyCannotRewriteAwardProperty` failed
in all eight Cloud/unmetered-root cases. It now passes, including committed
charges and exact settlement replay.

`TestPlacementMeteringUsesEnrolledOriginProperty` exercises all 32 combinations
of real machine/service provisioning, root metering, direct/delegated claimant,
named/queued claim and funded/unfunded payer. An unfunded metered placement
creates no award, interval, charge or event. Successful claims retain the same
metering in authority and billing persistence. Provisioning, delegation, matching
and reservation are real. The separate settlement property still uses its
existing calculator fake.

Removed Store/Tenant `AcknowledgeNodeExecutionStart` and
`FinishNodeExecutionAttempt`, their start/finish request types, and every local
and trigger implementation behind them. No production caller remained.
`Tenant.TransitionNode` owns their retained behavior. Removed the unused
coordinator reader/helper and the GitHub credential-push reader, scope type,
context helpers, name matcher, SQL matcher and principal-prefix constant.

Removed the Store attempt-history reader. Its tenant replacement reads current
`node_attempts` in one snapshot instead of the deleted legacy attempts table.
The two production callers in `internal/store/store.go` now use the tenant reader.
The old lineage test is ported to 24 generated cases using real claim, start and
finish transactions. Foreign tenant/run selectors reveal no history and leave
the owner's history unchanged.

Installed C1's declared non-checkout payment receipt and correlation schema.
Receipts accept the declared event kinds and require non-checkout object/fact/
settlement data. Only warnings can have no payer. Correlations retain mode,
provider object, payer, customer and collection identity. Purge removes a team's
bound receipts/correlations and preserves unbound warnings. The existing purge
property now exercises those rows across hostile tenant names and repeated
purges. Provider tests use the final tables rather than replacing them.

The session removes **972 production Go lines** and adds **30**, for **-942**
against the staged integration contents. The conflicting reader uses the
integration version as its baseline. These counts exclude tests, acceptance,
testdata and contract-named files. Combined W1+W1b against main is **-2,145**
(17,006 added, 19,151 removed), still **2,105 lines over** the -4,250 target.
There is no budget exception claim.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR/TMPDIR under /tmp/sw-astra-W1 and package parallelism -p 4.

- Whole-repository `go build -p 4 ./...` passes.
- Store/controller/nodehost compilation and scoped vet pass.
- All 21 W1 acceptance functions in `internal/store/acceptance_*_test.go` pass in
  9.448s. No native acceptance cell is newly claimed.
- Provider/payment, final-schema, SQL-scope, purge, placement and attempt-history
  selections pass in 17.852s.
- Current unit-billing, bootstrap seed, rotation grace, registered runners,
  console metering and launcher snapshot checks pass in 7.534s / 0.151s.
- Focused race checks pass in 93.132s. They cover placement billing, immutable
  root policy, tenant attempt history, purge, launcher snapshots and named claims.
- Commentcheck against main passes across 2,260 Go files.
- API snapshot, whitespace, embedded docs/changelog drift, changelog check and
  focused embedded documentation tests pass.

An early broad TestStore selection also selected the legacy
`TestStoreFinishRefusesAnotherTeamsNode`. It fails because its raw CreateRun
fixture inserts the removed args_json column. That test was not changed into a
passing claim. Remaining raw lifecycle fixtures still need retirement/ports.
The initial API drift command lacked the prescribed cache environment and was
refused by the read-only default cache. The corrected command passes.
No whole-repository test suite, PostgreSQL, socket service, pipeline, release or
independent review is claimed. Those remain lead work.

## Test ports

Retired eleven tests tied to the removed attempt writers and name matcher,
plus five dependent helpers. The port inventory maps them to current attempt,
identity, billing, retry, replay, push-scope and authority properties. The old
attempt-history test is replaced by its real-transition property.
Spend-exhaustion/grace cases remain open rather than being counted as covered
by reservation/start tests. Conflict resolution also preserves W1b's removal of
eight superseded billing/token tests and W1's earlier legacy queue deletions.

## Contract and integration work left

No foreign signature changed. The seeded bootstrap input, readAuthorityTx grace,
launcher snapshot fence, Operator credit methods, configured placement, console
runner metering and deleted dead contract tables remain intact. The applicable
A1 lifecycle/authentication/sealing/claim amendments retain their existing
implementations. Blob, log, payment settlement and materialization requirements
remain W1b's implementation ownership.

- Trusted schedule and verified App intake remain open. IntakeRequest still
  requires capability/session authority. Stored schedules have ArmedBy as an
  account string, and verified App deliveries lack a declared committing
  authority. Declare the trusted origin reference and have W2 supply verified
  delivery facts through IntakePolicy. No production IntakePolicy exists yet.
  Then remove triggerIntake, triggerRows and the legacy insertion writers.
- Full ReadRunners still lacks declared immutable Cloud/here identity,
  enrollment concurrency and accepted-renewal persistence inputs. Queue-depth
  transport remains W8/W9 work. Poll timestamps must not stand in for renewals.
- W2 owns committing direct-session origin checks and general ConsoleWriter
  composition under the lead's 14:40 assignment. Only runner metering has W1's
  real console commit adapter.
- Payment setup correlation still needs W2's trusted billing-page writer.
  Production provider composition is separate. Mode isolation for retained
  fingerprints, warnings and collection state remains open. Its readers/writers
  are W1b-owned and were not changed here.
- Renewal-time spend exhaustion/grace still needs its all-unit policy boundary.
  W1b can now cut accounting helpers whose only callers were the removed raw
  execution writers. Its reserveNodeCreditsTx/reserveTriggerCreditsTx and legacy
  TokenMetered path also remain retirement candidates. No W1b production edit ran.
- Raw token constructors remain. Their only production store caller is the web
  browser fixture, plus 65 legacy/test or client-manager calls requiring triage.
  StateStoreOld and its local lifecycle consumers remain too.
- internal/acceptance/store_recovery.go now serves only W3 host_events.go.
  W1's recovery checks already live beside the store. Moving the host consumer
  and its shared fakes is the remaining harness handoff.

## Shared files and ownership

Production caller migration is confined to `internal/store/store.go`. Outside store,
the only new code edit is funding the existing Cloud award fixture in
`internal/controller/launcher_fixture_test.go`.

W1b test changes are caller/schema migrations only:
`internal/store/payment_provider_settlement_test.go` drops the replacement schema,
`operator_card_billing_test.go` drops its schema-helper call, and conflict files
retain retired APIs' caller cuts while adopting Operator methods. No billing,
blob, log or materialization production implementation was edited.

Other shared edits are C1, the port inventory, CHANGELOG.md, the unreleased
migration guide, their generated mirrors and generated API snapshots. Snapshot
regeneration also captures already merged client API removals. Lead-owned
amendments and handoffs are unchanged.

Friction: Git's index remains unmerged because the swarm forbids Git writes.
The working-tree resolutions are ready for the lead. HEAD and Git identity
remain unchanged. Scratch is removed after verification and the shared build
cache is retained.

STATUS: CONTINUE
