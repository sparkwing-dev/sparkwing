# W9 runner composition recheck

Base `fccd5ac4f`, environment `1087`. Runner composition remains incomplete.
The 2026-10-16 continuation changes only this report. No dependency implementation
has arrived since the preceding W9 session. All four contract requests below
remain open. The requested native self-hosted pipeline is not ready to run.

The existing `rewrite_integration` tests ran before any edit:

| Probe | Result |
| --- | --- |
| `TestProductionRunnerCommandsClaimAndHost`, start/warm trigger | Claims, renews and authenticates, then fails with `owner hosting requires canonical placement eligibility`. |
| `TestProductionRunnerOwnerReachesNativeHost` | Same placement refusal. |
| Start/warm node and `TestProductionRunnerNodeReachesNativeHost` | Reach native hosting's sandbox socket boundary. |
| `TestProductionCloudExchangeReachesAwardRenewal`, plan/eval | Exchange once, then fail with `controller awarded a unit this runner does not execute`, before renewal. |
| Cloud node exchange | Reaches awarded renewal. |
| `TestInstalledRunnerRuntimeComposition`, start/warm/fetch-source/run-node | Reach their expected production HTTP operations. |
| Installed launch | Fails with `launcher runtime is unavailable`. |
| `TestReviewOwnerEvaluationReportsBeforeWorkAward`, annotation/summary | Both return HTTP 409 with `holder_rights`, where the test requires 204. |
| Public numeric selectors, debug/resume selectors and numeric/team-isolation property seeds | Pass against the persisted controller. This work remains complete. |

## Contract changes needed

1. **W1/W2/W3 owner placement.** Invariant: owner self-execution uses the same
   persisted matching inputs as queued and named claims. `ReadOwnerSnapshot`
   returns no eligibility projection. `NativeConfig.Local` requires a
   synchronous per-node decision. C1's matching reader remains private and
   `ReconcileNode` returns an error on an ineligible self-claim, rather than
   a dispatch decision. W9 cannot construct the callback from the declared
   interfaces. Return canonical eligibility in the private owner snapshot,
   bound to generation and declaration hash, and retain the transactional
   matching recheck. An unconditional callback would either misplace work or
   strand a one-slot runner's owner while its node waits for that same slot.
2. **DESIGN: W1/W2/W3 owner/eval reporting.** Invariant: source evaluation
   retains its owner/eval lease and source identity independently of any work
   attempt. The committing result writer still requires a node holder and
   positive attempt. Latest-reference authorization still requires a node
   claim. Eval's fixed grants remain `expansion.submit` and `data.read`, while
   mask/cleanup routes require `node.report`. Define the source reporting and
   auxiliary-service relation together. The prior two-strikes request remains
   open. Fabricating an attempt or widening the frozen grant table would
   change another area's contract.
3. **W8 Cloud boundary.** `KubernetesBoundary.Probe` and `Clear` still have no
   production implementations. Supply a production `CloudBoundary` that
   measures enforcement and clears reservation-scoped state before reuse.
   W9 can then bind `ProductionRuntime.Launcher`. A failing factory or fake
   evidence would leave the shipped command unimplemented.
4. **W3/W8 plan/eval unit hosts.** `Hosting`, `nativeInputs` and `NewRunInput`
   still implement trigger/node hosting. The latter creates a trigger owner.
   Supply plan/eval hosts with their private-state access and independent lease
   handling behind `UnitHost`. Accepting the kinds in the validator alone
   would dispatch them to the wrong host.

The swarm brief says: "If it changes another area's contract, stop that piece
and put it under 'Contract changes needed' in your report."
The existing BootstrapRuntime, source/host binding and evaluation tests already
exercise declared interfaces with fakes. Those component adapters pass. No new
parallel abstraction or placeholder factory was added.

Resume when the integration branch supplies those four boundaries. Re-run the
existing probes before changing W9's composition. In particular, the self-hosted
trigger path needs the first boundary before it can reach the socket refusal.
Changing the fake snapshot or passing `Local: func(...) bool { return true }`
would bypass the missing placement decision and cannot satisfy that probe.

## Verification and scope

Whole-repository build and focused vet passed. Focused tests passed in
controller, cluster and localcontroller, including public numeric selectors,
debug/resume selectors and team-isolation seeds, BootstrapRuntime fence/cleanup,
private transport selection, awarded-credential property seeds and
owner-evaluation source-edge property seeds. The integration probes in the table
remain red or socket-skipped as stated. No acceptance cell was newly unskipped.
No archived test was ported and no new test was needed because the existing
probes already reproduce each unfinished integration point.

Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W9/gotmp`,
`TMPDIR=/tmp/sw-astra-W9` and at most four package workers.
Commentcheck against main passed across 2,573 files. Whitespace checks passed.

Production additions/deletions are **0/0**. W9-owned production Go versus main
is **+16,961/-23,817, net -6,856**, within the -2,900 budget. No legacy file,
shared source, caller, contract, lead-owned handoff or amendment changed.
Mechanisms added: none. The lead still owns native execution and integration
review. No Git write, candidate installation, operational Sparkwing command,
pipeline, deployment or release ran. Git identity/core.worktree overrides are
absent. Worker scratch is removed after checks join. The shared cache is kept.
Friction remains the four contract boundaries, sandbox sockets and Overwing's
reported installed-source lag.

STATUS: CONTINUE
