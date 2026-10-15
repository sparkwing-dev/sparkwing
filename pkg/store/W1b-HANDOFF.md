# W1b handoff

Continues b0cc55c27. W1b remains incomplete. The lead owns commits,
independent review, pipelines and native integration.

## Implemented and deleted

Eight raw Store methods are gone. Deployment billing settings now belong to
Operator: CreditSettings, SetCreditSettings, SetOperatorCreditSettings,
CreditRateTable and WarmCPUClassCores. Their existing transaction owners,
validation, defaults and update rules are unchanged. The implementation lives
in operator_credit_settings.go. There are no forwarding methods.

Store.GrantCredits, Store.ListCreditCharges and Store.AppendEventOnce are
removed. Their retained Tenant methods require callers to select a team
explicitly. Every Go caller is migrated. The paid-grant refusal property now
probes the retained tenant APIs. Its deleted callback named a removed method.

The shared billing helper no longer calls C2's calculator for unmetered awards.
Invariant: unmetered provenance produces zero cost without consulting a price
calculator or writing a financial row. Checkpoint, final settlement and replay
retain the reservation identity. Final settlement closes the award. Claimant
pruning cannot change the result. Metered computation still uses C2.

The new regression failed in all eight unit/start combinations before the
change. It now exercises trigger, plan, eval and job under at_award and at_start,
including checkpoints, final settlement and replay through the production helper.
It asserts no charge, billable interval, team spend or card spend. The calculator
fake fails if invoked. Final settlement uses the real wrapper whose C2 default
is still a panic stub, so that path also proves the bypass.

Production Go changes remove 132 existing lines and add 97, including the new
operator file, for a net reduction of 35 lines. Five methods moved and three
wrappers retired. No storage table, setting, price, policy limit, environment
variable, service or queue changed. No legacy module is claimed fully retired.

## Verification and acceptance

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE, GOTMPDIR and TMPDIR are beneath /tmp/sw-astra-W1b/.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet passes for store, controller and both acceptance trees.
- The inherited store-data selection plus the existing grant/reversal checks
  passes 182 top-level tests / 585 cases without skips.
- The affected billing selection passes 10 top-level tests / 130 cases after
  the helper change, including the one new top-level test / nine cases.
  The union is 183 top-level tests / 594 cases.
- The raw paid-grant fuzz property's three seed cases pass after deleting its
  removed-API callback.
- Both store and controller test packages compile with -run '^$'.
- The five existing controller egress/event regressions pass.
- TestStorage and TestBilling pass 43 cases, including all 12 existing SQLite
  storage/billing proofs. No new native cell is unskipped. The selection retains
  563 native/platform skips, which are not claimed as evidence.
- commentcheck -base main, sleepcheck -base HEAD, gofmt, git diff --check,
  sync-docs.sh --check and check-changelog.sh pass.
- The regenerated store API snapshot matches. Full snapshot comparison still
  finds the inherited pkg_runner.txt snapshot for a removed package.

Store selection:
^(TestBlob|TestLogPersistence|TestLogPublication|TestLogMaintenance|TestStoreData|TestRetainedStorage|TestPaymentSettlement|TestMeteredAccounting|TestTenantSQLScope|TestTenantWriters|TestTenantTwins|TestGrantCredits|TestCreditGrantRequest|TestCreditsBalance|TestCreditReversal)

Post-change billing selection: ^(TestStoreDataMetered|TestStoreDataUnmetered).
Controller selection: ^(TestControllerEgress.*|TestControllerEventsUseTenantAndReplica)$.
Acceptance selection: ^(TestStorage|TestBilling)$ on both acceptance packages.

No new archived T1 source row is claimed complete. Existing final-schema settings
ports now run through Operator. They preserve defaults, validation, corruption
refusal, restart, failed-batch rollback and serialized table authority.

No full legacy store/controller suite, PostgreSQL, race, provider, physical
backend or native-placement pass is claimed. The predecessor's old-schema
fixture failures were not rerun or waived as passes.

## Assignments and real integration

The approved record-mutation owner and the two retired output catalog entries
remain complete. The lead's legacy-cutover priority is still in progress.

- A1-24 remains implemented at the store boundary. W2's real DataAuthority and
  BindData adapters remain open.
- A1-27: W1's session-18 lifecycle join is implemented on its parallel worktree,
  but absent here. This branch still refuses metered issuance in authority_issue.go
  and has no unit_billing.go. W1 files were not copied or reimplemented. The
  shared unmetered settlement fix is ready for that join. W1 can then delete its
  duplicate unmetered-calculator override. C2 SettleBilling remains a panic stub.
  W3 still must report owner compute to exclude customer-runner waits. The
  legacy claim-credit cut and spend/grace enforcement remain W1b work.
- A1-28's committed-checkout settlement and raw paid-grant refusal remain done.
  W2 still owns provider verification, VerifiedPaymentEvent and the HTTP adapter.
- A1-29 through A1-35 retain the implemented store contracts for tagged uploads,
  receipts, provenance, records/members, memo/depcache, read sets, tombstones and
  tagged logs. W7 RecordNamespace and AuthorizeEdge remain panic stubs here.
  Physical verification/deletion, manifest-reader setup, GC, signed transfers,
  binary execution policy and proxy egress remain real integration points.
  Allowance-driven physical eviction remains open.
- Principal event and egress cuts remain complete. Legacy raw storage
  reservations, output objects, direct uploads, claim-credit and node-log
  transports still need consumer cuts. Capability data/event transport,
  materialization and imported owner-log identity remain open.
- A1-53 remains with the lead and integrating areas for PostgreSQL, provider,
  physical-backend and native-placement evidence.

No new foreign contract change or product decision is requested. The local C1
contract records the Operator/tenant hard cut and unmetered settlement behavior.

## Shared files

The only shared production caller changes are pkg/controller/credits.go and
internal/acceptance/store_billing.go. The controller selects Operator for its
already-authorized deployment-settings handlers. The acceptance fixture selects
Operator for storage pricing. W1-owned store.go changes only two callsites in
warmClassFilter, adding AsOperator() to the existing rate and class reads.
No admission, claim, transition, reconciliation or reaping behavior changed.

Shared Go caller and fixture files:
- internal/acceptance/store_billing.go
- pkg/controller/compute_limits_test.go
- pkg/controller/credits.go
- pkg/controller/credits_test.go
- pkg/controller/direct_source_test.go
- pkg/controller/metering_license_test.go
- pkg/controller/node_claim_named_test.go
- pkg/controller/prom_internal_test.go
- pkg/controller/storage_allowance_test.go
- pkg/controller/storage_inventory_internal_test.go

Store test callers changed only to name the retained API owner, except the
new billing regression and the deleted paid-grant callback described above:
- pkg/store/backup_restore_drill_test.go
- pkg/store/card_billing_test.go
- pkg/store/claim_attention_test.go
- pkg/store/claim_team_boundary_test.go
- pkg/store/compute_limits_test.go
- pkg/store/credit_billing_start_test.go
- pkg/store/credit_rates_test.go
- pkg/store/credit_settings_runtime_test.go
- pkg/store/credits_per_team_test.go
- pkg/store/credits_test.go
- pkg/store/event_policy_runtime_test.go
- pkg/store/execution_attribution_test.go
- pkg/store/execution_start_contention_test.go
- pkg/store/finalize_event_deadlock_test.go
- pkg/store/free_tier_test.go
- pkg/store/metered_disruption_test.go
- pkg/store/node_claim_named_test.go
- pkg/store/payment_authority_test.go
- pkg/store/queue_state_counts_test.go
- pkg/store/retained_storage_billing_test.go
- pkg/store/storage_credits_test.go
- pkg/store/team_storage_test.go
- pkg/store/terminal_parent_claim_test.go
- pkg/store/terminal_parent_race_test.go
- pkg/store/trigger_claim_credits_test.go

Shared prose and generated files: contracts/c1-data.md, handoffs.md,
t1-port-inventory.md, CHANGELOG.md, docs/migrations/_unreleased.md,
.apidiff/pkg_store.txt, pkg/docs/changelog.md and its migration mirror.
STORE-DATA.md records the persistence behavior.

## Friction and cleanup

The W1 join is not merged into this branch, and C2/W7 production seams are still
stubs. Contract fakes continue to exercise metered persistence without treating
those dependencies as implementation. The stale runner snapshot prevents a
clean whole-directory API comparison. Native/socket/provider checks remain
with the lead. No new operational blocker or product question is raised.

The shared Git config has no local user identity or core.worktree override.
No Git write, Sparkwing binary, candidate, pipeline, service, deployment,
release or harness-memory write ran. Scratch is deleted after the final checks.
