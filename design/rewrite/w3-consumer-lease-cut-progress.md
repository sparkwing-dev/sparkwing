# W3 consumer lease cut

<!-- design:begin -->

This session removes **270 net production lines** (57 added, 327 deleted).
Against main, W3 is **+4,832** (12,262 added, 7,430 deleted), **7,132 over**
its -2,300 budget. This is unfinished deletion, not a justified overage.

The local consumer now uses `lease.Keeper`. Its separate ticker, retry loop,
heartbeat callback and predispatch cancellation helper are deleted. Renewal
carries the claimed generation, confirms authority before environment consumption
and dispatch, and continues through settlement. The keeper joins before return.
Observed lease loss or a full lease of silence suppresses completion writes.
Operator cancellation keeps its cancellation verdict before dispatch too.

Deleted unused warm-runner GC, its isolated tests, the process-metrics context
helper and `EngineNodeLog`. These account for 216 production lines. No production
caller references the deleted APIs. The consumer change removes another 54 net
production lines. No compatibility wrapper was added.

## Verification

The full nodehost, lease and source suites pass. Repository build, scoped vet,
focused keeper tests under race, the default-team caller audit, commentcheck
against main, sleepcheck against HEAD and diff checks pass. All Go checks use
GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache and at most -p 4.

The keeper adapter has refusal, cancellation and joining tests, plus a generated
property over 128 identities and generations. An independent Astra review found
a cancellation-classification edge. The predispatch check now returns Err()
while retaining the keeper's typed cause for settlement. Renewal stays live
through settlement. The prior worktree hold and cleanup order is retained.

The retained predispatch cancellation test now calls `runClaimedTrigger` and
checks both no dispatch and terminal state. It fails during fixture creation
because the final triggers table has no legacy pipeline column. Other old
orchestrator tests still encounter removed args_json columns or sandbox sockets.
These are failures or unavailable evidence, not passes. No new native acceptance
cell or completed archived-test port is claimed.

## Remaining work and handoffs

`RunHosted`, `HostTriggerRun` and dispatched-pod assembly still use the legacy
scheduler, `Server` and layers. Their native cut remains first priority. W8's
Hosting callbacks do not yet replace these production callers.

The existing cross-area contract requests remain open. Controller.ReadSnapshot
returns ScheduleSnapshot, while native execution needs the sealed request,
accepted spec and plan, owner state, complete execution rows and expansions.
The authenticated run-start transition, capacity/adoption, owner/process
delegation, state following and matched-engine host invocation also need their
real adapters. No foreign contract was changed or bypassed. Legacy settlement
checks generation without full live-lease authorization, so the keeper change
does not establish the full native completion invariant.

Lead decisions 09:40 items 1-3 remain done. No host_* file remains in acceptance,
scripted host fakes remain consolidated, and each keeper check has one test root.
Lead decision 07:40 item 9 remains done. The expansion concurrency test passes
and refuses capacity, scope and on_limit mismatches. Amendment obligations remain
as recorded in the earlier W3 audit and continuation reports. This session closes
no whole native-placement amendment.

Shared edits are C3, this report and removal of the deleted helper's stale entry
in pkg/store/default_team_callers_test.go. No foreign production caller changed.
Lead-owned handoffs and amendments are unchanged. No git write, Sparkwing binary,
pipeline, install or release ran. Scratch is removed after all checks join. The
shared build cache remains.

<!-- design:end -->

STATUS: CONTINUE
