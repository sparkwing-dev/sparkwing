# W1 continuation: named job claims and node renewal

W1 remains incomplete. The whole-repository build passes. This session starts
from `b6f7d2f3c` and makes no git writes.

Production Go delta this session is **-516 lines** across the repository.
There are 523 deleted lines and seven changed-in lines. The store loses
**258 net lines**, and its controller callers lose 258. Against main, the
combined W1/W1b store delta is **+1,607**, against **-4,250**, or **5,857 over**.
Remaining legacy paths need deletion. This is not a budget exception.

## Changes and test ports

- Removed `Store.ClaimNamedNode`, `NamedClaimOptions` and the named/unready
  branch of the legacy award helper. Current named job awards use `ClaimUnit`.
  Owner self-claims use `ReconcileNode`.
- Removed `HeartbeatNodeClaim` and `HeartbeatNodeClaimWithCredits`. Current
  awarded capability authority renews through `RenewUnit`.
- Removed the two old node claim/heartbeat HTTP registrations and handlers,
  their request and response types, the caller-selected profile decoder,
  placement-context helper and headroom writer that became unused.
- Removed the old named-claim, award-helper and spec-hash fixture files. Ported
  their retained requirements to current transactions. Removed five old node
  heartbeat tests, the old claim-token renewal test and obsolete named/tool
  assertions after porting their requirements. The port inventory maps them.
- Updated C1, the port inventory, changelog, migration guide, embedded mirrors
  and the store API snapshot. No compatibility alias or new mechanism is added.

Invariant: naming a job cannot bypass readiness or current authority, and a
refused claim changes none of the job's persisted state.

`TestNamedJobClaimCannotBypassReadinessOrAuthorityProperty` uses 12 generated
jobs, labels and resource demands through real intake, plan acceptance, identity
sessions, enrollment, claiming, start, report and owner completion. It checks
absent, unready, held and terminal work, insufficient capacity, forged identity
labels and a real foreign tenant's capability. Refusal preserves the complete
snapshot and award/event counts. The accepted job spec hash stays unchanged.

Invariant: renewal cannot mutate a job under stale or foreign award authority.

`TestRenewUnitCannotMutateAStaleJobProperty` uses 48 generated root, revoked,
expired, superseded, wrong-holder and forged cases. Both execution observations
are refused without changing the job, its billing intervals or its attempts.
Existing named-award billing tests retain atomic reservation, insufficient
funds, concurrent spend and lifecycle settlement evidence. The current engine
HTTP test also checks that both removed routes refuse requests.

## Verification

All Go checks use `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache` and `GOTMPDIR` under `/tmp/sw-astra-W1`.
Final checks also set `TMPDIR` to that scratch directory.

- `go build ./...` passed at startup and after the changes.
- All 21 W1 plain acceptance tests passed in 6.673 seconds. These cover seals,
  transitions, generated dependencies, owner recovery, scheduling, provisioning,
  inventory, sessions, Actions enrollment, claims, queue order, matching,
  launcher sync and all eight recovery cases. No native catalog cell is newly
  activated.
- Current claim, renewal, metered-unit and engine HTTP tests
  passed in 13.088 seconds for store and 0.273 seconds for controller.
- Both new properties passed with `-race` in 46.776 seconds. The named-job
  property passed again in 18.005 seconds after adding real foreign-tenant
  enrollment.
- Rotation-grace, launcher-snapshot, tenant SQL scope and default-team guards
  passed in 4.620 seconds.
- Scoped `go vet -p 4` and compilation of every test in store, controller and
  nodehost passed. Commentcheck, API snapshot comparison, documentation mirror
  check and `git diff --check` passed. Focused embedded-doc, migration and
  changelog tests passed in 0.088 seconds.

The full legacy suites remain unverified. A focused reproduction of
`TestStoreFinishRefusesAnotherTeamsNode` fails during `CreateRun` because the
legacy writer references the removed `runs.args_json` column. Remaining finance
and old host fixtures need current-schema authority setup. Their callsite
migrations compile and are not presented as passing runtime evidence. In
particular, the old unauthenticated host fixtures still have no capability
prefix to supply. Current store and engine HTTP proofs use real capabilities.

## Shared files and remaining work

Outside pkg/store, the caller changes are `pkg/controller/handlers.go`,
`server.go`, `engine_claim_http_test.go` and `agents_test.go`, plus
`internal/nodehost/dispatch_test.go` and `rehydrate_resume_test.go`.

The lead's W1b caller-migration exception is used for `card_billing_test.go`,
`credit_billing_start_test.go`, `credit_rates_test.go` and `credits_test.go`.
Financial assertions remain, apart from the removed sizes-to-class input test,
whose retained placement boundary is covered by current matching tests. W1b's
accounting, blob, log and materialization implementations are unchanged.

- **C2 contract change still needed:** `ProvisionSpec` has no seeded bootstrap
  presentation field. Implement its one-use Store.Issue input when declared.
- W1 still owns the StateStoreOld cut, raw lifecycle/readiness, node queue and
  trigger claim/writer retirement, token/deletion/account authority and GitHub
  automation admission migrations.
- Console sessions, verified-local authority, console service issuance,
  SetRunnerMetered, host/source observations and persisted pool/repository
  management remain open.
- Rotation grace, the SyncLauncher fence, credit-method ownership, harness
  moves and six dead contract tables remain complete. They were not rebuilt.
- Native PostgreSQL named-award locking, final transport/placement composition
  and independent review remain with the lead and area owners. The retained
  legacy PostgreSQL race test now covers only its queue branch. Production
  billing composition still needs W2's calculator and W1b's final legacy cut.

No foreign contract signature or lead-owned handoff/amendment file changes.
HEAD is unchanged, and Git configuration has no local identity or core.worktree
override.
Friction remains the legacy fixture schema and authority gap above. Assigned
scratch is deleted at the end, and the shared build cache is retained.

STATUS: CONTINUE
