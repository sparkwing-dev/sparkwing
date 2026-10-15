# W3 envelope consolidation

<!-- design:begin -->

W3 remains incomplete. This session removes **108 net production lines**
(58 added, 166 deleted). The lead's area metric against main is **+5,102**
(12,209 added, 7,107 deleted), **7,402 over** the -2,300 budget. Tests,
testdata, acceptance and contract declarations are excluded. The unfinished
legacy cut remains the reason for this overage. It is not a justified exception.

## Changed and deleted

The retained local, trigger and pod paths take layer order and ownership from
LayerTable. The separate local and dispatch lists and the pod's inline list
are deleted. Local and pod execution share their start/attempt wrapper.
The legacy layer implementations and lifecycle writers still exist.

Local Execute now uses the existing short-lived planner. Its separate
start/hello/describe/guard/secret/plan/shutdown loop and planSession helper are
deleted. A private callback records the run after preflight and before planning.
No new public API is added. The common planner's protocol failure at close now
fails local planning too. It cannot publish a successful plan from a process
that breaks the protocol while closing.

The legacy timeout layer uses the native executor's progress and nested-await
context. Its separate idleTimer and sentinel are deleted. Existing wall-budget
holds, retry scope, timeout reasons and final-result handling remain in that
layer. The native executor's execution deadline remains independent of awaits.

These changes reduce duplicated production paths. They do not migrate
RunHosted, HostTriggerRun or hostPodNode to OwnerHost/ClaimedNodeHost.
Session and StartSession were already deleted before this session. Server,
the graph scheduler, layers and their orchestrator callers still need the cut.

## Tests and acceptance

TestPlanSessionSeesTheDryRunSwitch is retained as
TestPlanningEnvironmentSeesTheDryRunSwitch against the shared environment builder.
No additional archived test root is claimed ported.

TestPlanningRefusalsPreserveTheRunRecordBoundary drives real protocol pipes.
Argument, secret and run-creation refusals stop before planning. Plan, snapshot
and protocol-at-close failures finish a recorded run as failed. Defaults reach
preflight and the run record before planning. A recording wrapper intercepts
three legacy persistence methods over a temporary store. This is caller-boundary
evidence, not final intake/claim or controller integration evidence.

The full nodehost suite, full nodeproto/lease/source suites and focused race
checks pass. The race selector covers planning refusals, failed describe,
legacy timeout/progress and native await behavior. Whole-repository build,
scoped vet, commentcheck against main, sleepcheck against HEAD and diff checks
pass. Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, scratch under /tmp/sw-astra-W3 and test parallelism at
most -p 4. The lead retains pipeline and full-repository test execution.

No acceptance cell is added or activated. Existing host component tests run
with the package suite. Native HTTP/api.sock, placement, platform and real-repo
cells are not newly established. The earlier SQLite cancellation race was not
retested by the focused race selector.

## Handoff audit and remaining work

Lead decisions 09:40 items 1-3 were completed in the earlier package-test
cleanup. No host_* file remains in internal/acceptance. The retained scripted
SDK, host-store, byte/record and remote-cache implementations remain in the
package tests. Keeper checks run in lease tests, and the redundant reconciliation
harness stays deleted. No new Check or scenario registry is added.

Lead decisions 07:40 item 9 was already completed. The current W4 expansion
test refuses capacity, scope and on_limit mismatches for plan and job membership,
allows a different cost and checks the field path. Its package suite passes.

The amendment audit in contracts/w3-amendment-handoff.md and later continuations
remains open where it names native placement integration. This consolidation
advances shared envelope/deadline integration. It closes no whole amendment.
Native memo/retry candidate selection, concrete service/record wiring, local
intake composition and the remaining legacy deletion are still W3 work.

Foreign contract gaps remain unchanged. W2/W9's Controller.ReadSnapshot returns
ScheduleSnapshot. Native hosts need the sealed request, accepted spec and plan,
owner state, complete execution rows and accepted expansions. Display reads
cannot replace that authorized execution snapshot. The remote equivalent of
Tenant.TransitionRun(RunStart), capacity/adoption, owner/process delegation and
state subscriptions still need their authenticated adapters. Matched-engine
host invocation, sealed step windows, terminal record references and remote mask
propagation remain the integration points from the earlier audit. No foreign
contract was changed or bypassed in this session.

## Shared files and friction

Only contracts/c3-protocol-sdk.md and this area report change outside nodehost.
No foreign production caller changes. Lead-owned handoffs and amendments remain
unchanged. No Git write, Sparkwing binary, pipeline, install or release ran.

No product-docs impact because no public command, flag, config, route or promised
behavior changes. Invalid protocol bytes already require refusal. The C3 source
document records the implementation change. No generated documentation changes.

The private planning callback preserves the existing record boundary because
moving record creation past planning would lose recorded planning failures.
Layer wrappers now use the existing table because independent lists could drift.
The existing native timer replaces the duplicate timer. No new setting, daemon,
retry loop, credential or customer limit is added.

Friction: the live legacy callers still lack native admitted inputs and complete
remote execution adapters. Their deletion remains larger than this consolidation.
Scratch is removed after checks join. The shared cache is retained.

<!-- design:end -->

STATUS: CONTINUE
