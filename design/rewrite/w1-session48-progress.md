# W1 continuation: sole claim queue and console metering

W1 remains incomplete. This session starts at `199d936ff`. The integration
merge's only conflict is the deleted legacy doctor test. Its working-tree
resolution takes the integration deletion because its facade no longer exists.
The lead must stage `internal/opsview/doctor_root_unix_test.go` and finish the
merge. The swarm's no-git-writes rule leaves that unmerged index entry intact.
The initial and final whole-repository builds pass.

## Implemented and deleted

Removed `ClaimNextReadyNode`, both named trigger claim methods,
`PrincipalHoldsTriggerClaim`, `TriggerClaimant`, `HeartbeatTrigger` and
`FinishTrigger`. Their second queue scanner, token-table tenant fallback,
matcher/placement path and claim-token minting helpers are gone. Three source
files are deleted. Current `ClaimUnit`, renewal, release and completion own
their lifecycle. Legacy host readers still use `ClaimToken` and its format
constant, so those declarations remain until their callers migrate.

Implemented C2's `RunnerMeteringWriter` on Operator. The transaction resolves
the current direct console session and controller policy, locks one enrolled
machine root by its public prefix, and rechecks both before writing the root's
metering column and authority envelope. Existing awards keep their billing
facts. The default controller route now reaches this writer. The real HTTP
test includes authentication and CSRF verification without a fake writer.

Fixed `ListRegisteredAgents` to retain a credential until its scheduled
revocation deadline. Generated cases vary grace times and check the exact
deadline. An overlay restoring the old query fails all 32 cases.

The session removes **1,140 production lines** and adds **88**, for a net
**-1,052**. The store alone is -1,055. Against main, combined store production
is **-574**, versus its -4,250 budget, leaving **3,676 over**. These counts
exclude tests, testdata, acceptance harnesses and contract-named files, and
include the new untracked production file. There is no budget exception claim.

Retired 130 legacy test functions and seven dependent helpers. Test changes
remove 5,053 lines and add 279, for -4,774. The port inventory names current
claim, placement, authority, recovery and immutable-billing replacements.
Accounting's old card-cap, fraud and warm-pool fixtures still need final-schema
ports. Their former APIs and schema assertions are not passing evidence.

## Verification

All commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR and TMPDIR under /tmp/sw-astra-W1, and Go package parallelism -p 4.

- Whole-repository `go build -p 4 ./...` passes.
- Store/controller/nodehost vet and compilation pass. Both acceptance packages
  compile.
- The focused store/controller run passes in 14.355s / 0.212s. It includes all
  21 existing W1 store acceptance functions, current named/queued claims,
  placement, registration, owner recovery, immutable unit billing, console
  metering and the SQL-scope guard.
- Scoped race tests pass in 128.144s / 2.684s. They cover metering authority and
  HTTP commits, rotation grace, placement and claim authentication.
- Commentcheck against main passes across 2,226 Go files. Whitespace, embedded
  docs/changelog drift and the generated store API snapshot pass. The focused
  embedded migration/changelog tests pass.

The broader docs name selection also selected `TestWebClient_MigrationFetch`.
That test cannot open a socket in this sandbox. It is recorded as unavailable;
the narrower embedded-doc selection passes. No native PostgreSQL, socket
service, whole-repository test suite, product execution, pipeline, release or
independent review is claimed. The lead owns those checks.

## Contract changes and assumptions

No foreign contract signature changed. The C1-local `SetConsolePolicy` passes
the existing controller predicate into the committing transaction because
persisting another operator list would duplicate its authorization owner.
Server.New supplies current account configuration, direct-session origin,
license and absolute lifetime. Remove that setter, its store field and the
three constructor lines to undo the addition. This introduces no customer
setting, environment variable, credential format, table or billing rule.

Origin verification and C2 billing calculation remain fakes in the store
metering lifecycle test. The HTTP commit test fakes only provisioning-origin
verification. Console authentication, account policy, CSRF and persistence are
real. The general ConsoleWriter mutation adapters remain unfinished.

Trusted schedule and App intake remain open. There is no production
IntakePolicy implementation. IntakeRequest requires capability/session
authority; schedules retain ArmedBy as an account string, and verified App
deliveries do not supply a declared committing authority. C1 needs a trusted
origin reference derived from the stored schedule/current membership or
verified delivery/consent, and W2 must supply verified delivery facts through
the existing intake policy boundary. No synthetic credential or bypass was
invented. `triggerIntake`, `triggerRows` and their raw writers remain until
that cutover.

The full ReadRunners adapter also remains open. C2 requires accepted renewal
times and immutable cloud/here runner identity, separate from headroom times.
Current persistence has polls and claim fences; the award's declared payload
does not store a RunnerIdentity, enrolled maximum concurrency is undeclared,
and the claim wire has no queue-depth observation. These need declared inputs
before the store can return those facts without inferring identity from holder
strings or presenting an invented observation. W8/W9 own queue-depth transport.

Bootstrap seeding, the readAuthorityTx rotation fix, SyncLauncher store fencing,
the five Operator credit methods, moved W1 acceptance cells and the six dead
contract tables remain intact. Current checks retain their evidence. Further
token constructors, legacy lifecycle/trigger writers, StateStoreOld callers,
pool/repository management and exact HTTP replay provenance remain unfinished.

W1b production, blobs, logs, settlement and materialization are untouched.
Its legacy fixture callers are retired under the lead's explicit caller-cut
exception in handoffs.md. The removed queues leave reserveNodeCreditsTx and
reserveTriggerCreditsTx without production callers; their credit-window helpers
remain in W1b's credits.go for that owner's cut. The residual
internal/acceptance/store_recovery.go fixture is still used by W1b unit-billing
and W3 host-event harnesses, so it was not deleted out from under those owners.

## Shared files and caller cuts

Outside internal/store, the implementation change is `internal/controller/server.go`.
`internal/controller/runner_metering_store_test.go` adds the real route/commit proof.
The deleted API callers are removed from these shared test files:

- `internal/nodehost/rehydrate_resume_test.go`
- `internal/controller/egress_test.go`
- `internal/controller/prom_internal_test.go`
- `internal/controller/server_test.go`

Store legacy caller cuts affect these test files. Each change removes functions
that call the deleted API or their dependent helper, plus unused imports:

```text
agent_loss_retry_test.go
card_billing_test.go
claim_attention_test.go
claim_helpers_test.go
claim_scope_internal_test.go
claim_team_boundary_test.go
compute_limits_test.go
credit_billing_start_test.go
credit_expired_claim_settle_test.go
credit_rates_test.go
credit_reversals_test.go
credits_per_team_test.go
credits_test.go
execution_attempts_local_test.go
execution_attribution_test.go
execution_start_contention_test.go
github_runner_bindings_test.go
lease_test.go
monitoring_test.go
node_claim_comma_or_test.go
node_claim_test.go
node_placement_internal_test.go
node_placement_query_test.go
node_placement_test.go
node_usage_bounds_test.go
postgres_test.go
queue_state_counts_test.go
repo_allowlist_claim_test.go
tenant_node_state_test.go
tenant_writer_test.go
terminal_parent_claim_test.go
terminal_parent_race_test.go
trigger_absorbing_test.go
trigger_claim_credits_test.go
trigger_claim_identity_test.go
trigger_team_test.go
warning_hold_team_test.go
```

Other shared edits are the store API snapshot, C1 and the test port inventory,
CHANGELOG.md, the unreleased migration guide and their generated mirrors.
Lead-owned handoffs and amendments are unchanged. Git HEAD and configuration
remain unchanged. Scratch is deleted after verification; the shared cache stays.

STATUS: CONTINUE
