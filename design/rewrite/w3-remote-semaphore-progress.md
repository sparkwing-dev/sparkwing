# W3 remote semaphore continuation

<!-- design:begin -->

Priorities 1 and 2 remain green. The full nodehost suite passes against the
Tenant/Operator store, and the W4 concurrency tests retain mismatch refusal.
Priority 3 remains incomplete. Server, the legacy scheduler and layers, and
the local/trigger/pod production callers still exist.

## Implementation and deletion

Native admission now retains run/box semaphores around remote jobs without
reserving local CPU or memory. It acquires before readiness, follows persisted
completion and reacquires admission for ready or claimed jobs on owner resume.
Global groups continue through the real capacity store. Native construction
still discovers the tenant adapter and binds each invocation's owner.

Invariant: a remote job's semaphore remains held until its holder reports a
terminal result. A sublease eviction requests cancellation through the owner's
typed TransitionNode and then drains the holder under the live owner context.
It preserves the holder, generation and result rights. Independent persistence
and subscription failures remain errors. Parent ownership loss ends following.
Final run policy outcomes and supersession classification remain open below.

The private remote-admission flag and persisted follower are required because
the remote readiness callback returns before execution ends. A lease wrapped
around that callback alone releases capacity too early. They reuse the existing
state follower and cancellation transition. Removing them restores the previous
refusal of remote run/box groups. No public API, setting, limit or daemon is added.

Removed Run.Add, its request channel and scheduler branch, and Config.Execute.
Neither API had a production caller. Removed 85 unused legacy fixture lines.
Existing scheduling assertions already use the native owner and real store.
The legacy scheduler loses 49 production lines. The complete production diff
adds 93 and removes 69 lines, for **+24 net lines**. W3's measured production
delta against main is **+5,210**, **7,510 over** its -2,300 budget. The unfinished
legacy cut remains the reason for this overage. It is not a justified exception.

## Tests and acceptance

The new dispatch checks cover both local concurrency scopes with fresh,
already-ready and already-claimed jobs. They exercise real tenant admission,
claims and transitions. The admission daemon is the existing fake. The eviction
check requires cancellation intent while both leases remain live, then accepts
the holder's fenced terminal report before release. It does not prove final run
settlement or native remote process termination.

The retained parent-binding property now covers remote admission too.
FuzzRemoteAdmissionPreservesJobIdentity varies run, job, declaration hash,
generation, state sequence and terminal state. It permits completion only for
the original declaration with nonregressing fences. Two-second fuzz runs passed
9,752 remote-identity cases and 2,400 parent-binding cases.

The store fixture now broadcasts state notifications to each subscriber.
One shared consumable channel could lose wake-ups when the scheduler and a
remote follower wait together. Every subscriber still catches up from storage.

No additional retained test root was ported. No acceptance cell was added,
activated or marked complete. Existing nodehost component tests run with the
package suite. Native placement evidence remains open.

## Foreign contracts and remaining W3 work

W2/W9 must expose the authorized full execution snapshot needed by OwnerStore
and ClaimStore. Controller.ReadSnapshot still returns ScheduleSnapshot, and
ReadRun exposes display inputs. Neither supplies the accepted run, complete
execution rows and owner state. The remote RunStart, capacity/adoption and
owner/process-delegation adapters remain absent. These gaps block the live
trigger/pod cut. No foreign interface or raw legacy writer was substituted.

W1 still owns typed policy-failure and approval-SkipIf success edges, debug
persistence and cancellation classification at the snapshot boundary. The
remote eviction test proves holder cancellation and drain, then returns the
admission error. It does not close native owner failure settlement or preserve
superseded as the remote terminal outcome. Those policy outcomes need the
agreed store edges before production migration can be claimed complete.

W3 still owns native memo/retry selection, concrete service and record wiring,
local intake/host composition and the remaining legacy deletion. Matched-engine
host invocation, sealed selections, terminal output references and cross-process
mask propagation remain the integration points recorded in the earlier audit.
The amendment and handoff audit in w3-amendment-handoff.md remains in force.

## Verification and friction

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and scratch under /tmp/sw-astra-W3. Tests use at most -p 4.

Whole-repository build, scoped vet, full nodehost/nodeproto/lease/source tests,
commentcheck against main and sleepcheck against HEAD pass. Nodehost takes
27.6 seconds. Focused race validation **fails** on the previously reported W1
snapshot cancellation bug. The remote box/claimed case returns SQLite
`interrupted (9)`. It is not suppressed or retried by the host. The exact race
selector is:

```text
-run '^(TestDispatch|TestAdmission|TestLocalAdmission|TestV2MultipleLocalConcurrency|TestNativeHostAutomaticallyBindsRealCapacityPerRun|FuzzRemoteAdmissionPreservesJobIdentity|FuzzLocalAdmissionParentBinding|TestOwnerHostWaitRetainsFollowerCleanupFailure)' -race -count=1
```

An intermediate full run was interrupted after an overbroad remote-wait change
blocked fixtures without local admission. Following now applies only to remote
run/box leases. A resume-test observer was also overwritten by per-run admission
binding. The test now observes the existing fake daemon's acquisition boundary.
The initial eviction check reproduced missing cancellation intent. The first
implementation treated the cancellation cause as an independent follower error.
Final normal checks include these corrections. Failed runs are not evidence
of passing behavior.

Shared edits are the C3 implementation inventory, T1 port inventory and this
report. No production file outside nodehost changes. Lead-owned handoffs and
amendments remain unchanged. No Git writes, Sparkwing binaries, pipelines,
installs or releases ran. Scratch is deleted after checks join. The shared
build cache is retained.

No product docs impact. The removed APIs had no production caller, and the
native placement assembly remains incomplete. The C3 contract documents the
implemented library behavior. The product documentation mirror has no drift.

<!-- design:end -->

STATUS: CONTINUE
