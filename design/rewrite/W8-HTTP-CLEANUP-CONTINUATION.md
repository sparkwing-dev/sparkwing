# W8 HTTP cleanup continuation

Continues `573d75fb0` and `W8-LOCAL-CONTROLLER-CONTINUATION.md`.
The local-controller priority remains incomplete. No git writes occurred.
No `internal/orchestrator` or lead-owned contract file changed.

## Implemented and deleted

Native process HTTP now uses the API/dashboard serving lifecycle. Host cleanup
cancels requests, closes ordinary and hijacked connections, joins handlers,
then closes process authority. Concurrent or repeated cleanup preserves the
same error. `ServeLocalHTTP` is the existing W8 function made available to its
localcontroller subpackage. There is no second HTTP shutdown implementation.
The native binding retains its lazy start and uses one joined close function.

Invariant: a process handler cannot outlive the authority registry's cleanup.
`TestNativeHTTPJoinsBeforeReleasingAuthority` exercises ordinary and hijacked
handlers that remain blocked after cancellation. Authority cleanup waits for
their release. `FuzzNativeHTTPStartCloseOrdering` varies repeated starts and
closes, including close before start, and checks one authority release and a
closed listener. Tests use in-memory connections and no sleeps.

Deleted `PeerSockets` and its private `socketAlive` helper because their only
callers were the retired cross-home discovery tests. Removed those tests and
their socket fixtures. Deleted `ProbeQueue`, whose only callers were two
absent-daemon tests. Those tests now exercise the retained `Probe` API.
`Query` continues to own queue reads. No foreign production caller needed a
migration. Production deletes total **94 lines**, including the superseded
native HTTP cleanup. Removed test lines total **177**.

## First-priority integration status

The unchanged W9 `TestReviewProductionExecutionBindings` probe produces:

| Probe | Result |
| --- | --- |
| local controller | Passes the production constructor. |
| local run | Reaches the actual private transport and fails with `connect: operation not permitted`. |
| local schedules | Fails with `local schedules require checkout, environment and pin registration`. |

The controller factory still binds its one RunStore, recovery, state/activity,
Jev secret lookup, shared HTTP/UI handlers and queue. Its workers, maintenance
and HTTP lifecycle are covered with fake shared services where the foreign
implementation is missing. Constructor evidence does not establish sustained
production readiness. The foreground CLI still refuses its missing remote
host binding before intake. Native process cleanup now joins its handlers.

## Contract changes needed in other areas

These gaps were checked in the merged code. No foreign contract was changed.

- **W2/W1 peer authority:** `Store.ReadAuthentication` still accepts persisted
  bearer and session authority only. `CredentialPeer` has no persisted
  authentication authority. `NewClaimant` already consumes the shared peer
  authentication/delegation interface and supplies the local machine profile.
  W2/W1 must bind the verified OS subject to its persisted local authority so
  the actual queue can authenticate.
- **W2/W9 local schedule transport:** `ScheduleRepoRequest` has a clone URL,
  branch, SHA, follow flag and declarations. Its handler validates a clone URL.
  It cannot carry an absolute checkout, captured environment, or proof choice.
  `ScheduleManager` exposes neither pin/unpin nor dry-run tick. Add those
  operations through the elected controller's authenticated local transport.
  The local request needs replace/upsert scope, checkout, declarations,
  captured snapshot, follow and proof settings. W8 can then bind the existing
  factory callbacks to that interface without another store opener.
- **W1 schedule persistence:** `CronSchedule` still has no captured-environment
  association. Define the durable snapshot relation used at installation and
  scheduled intake. A daemon's startup environment cannot substitute for it.
- **W3 foreground state:** `RemoteOwnerStore` implements reconciliation,
  expansion and a scheduling projection. It still does not implement
  `NativeHostStore`. Supply the shared HTTP adapter for that declared interface
  so the CLI can host through `NewNativeHost` without opening state.db.
- **W3/W7 native services:** shared owner-policy binding, scoped byte/record
  clients, output reads and the process-to-store binding relation remain open.
  The process relation must validate the narrowed child principal rather than
  substitute its parent. The local log read projection is also unbound.

**W8 work still open:** adopt independently awarded self-claimed nodes into
per-attempt process delegation, finish the foreground and schedule composition
against the completed foreign contracts, connect storage/owner policy, and
reduce the remaining volume. No fake integration is presented as production.
Native Cloud network/scratch/teardown and Windows/Darwin evidence remain open.

## Other assignments

The 12:35 retry validator remains shared by Hosting, launcher Claim and Cloud
Exchange. Their full package suites pass. A1-38 remains closed on W8's side:
immutable award fences, claim generations, exact observed decisions and Job
instance correlation retain their tests. The four tables `KeepAliveReasons`,
`LocalPlatformPolicies`, `CloudUnitPolicies` and `CloudPodInvariants` remain
absent. No completed implementation was rebuilt.

The earlier amendment disposition remains unchanged apart from joined native
HTTP cleanup: A1-40 now also covers the native service's HTTP handlers.
A1-08/09/18/19/26/27/36/39/48/51 still need the production credential, owner and
storage bindings above. A1-10/11/12/43/45/46/50/52/55 retain their engine,
source, environment and retry implementation, with native evidence and the
previous build-cache/source contract requests open. A1-20/49 still depend on
peer persistence and enrollment/bootstrap composition. A1-41's managed units
remain covered. A1-42/53 remain native acceptance work.

## Verification

Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W8 and
TMPDIR=/tmp/sw-astra-W8. Test package parallelism was at most four.

- Whole repository `go build ./...` passes.
- Full localcontroller, runners, launcher, runner service/unitfile and engine
  suites pass. This includes retry award and launcher correlation properties.
- Scoped vet passes for wingd, runners and the CLI.
- Focused lifecycle, HTTP, composition and local-control tests pass under race.
- Production CLI constructor, admission and schedule-factory selections pass.
- W8/LauncherCloud acceptance selection passes. No native cell was newly closed.
- Native HTTP fuzzing passes **23,996 executions** in three seconds.
- Commentcheck is clean across 2,218 files. `git diff --check` passes.

The migrated `TestProbeDoesNotInitializeAbsentDaemonState` remains blocked by
sandbox Unix-socket denial, which returns unreachable rather than ErrNoDaemon.
Its expected production behavior was not weakened. The W9 probe results above
also retain their failures. No archived test was newly ported. Sockets, native
service managers and deployment behavior need the lead's external execution.

## Volume and shared files

W8 production Go adds **28** and deletes **94** lines this session, **net -66**.
Against main it adds **10,054** and deletes **6,965**, **net +3,089**.
That is **5,389 above the -2,300 budget**. Counts exclude tests, testdata,
acceptance and contract declarations, with rename detection disabled.

| W8 path | Net production lines against main |
| --- | ---: |
| internal/cluster | -1,967 |
| internal/engine | +2,257 |
| internal/runners | +1,437 |
| internal/wingd | +1,362 |
| total | +3,089 |

The overage is not waived. Exact engine distribution/version policy/upgrade
adds 2,257 lines. The native service, machine service and local controller
compositions add substantial required behavior that was not bound in the old
code. Removing those behaviors would leave the frozen design unimplemented.
The dead API scan found two remaining unused legacy surfaces and removed them.
Further cuts need consolidation of active code rather than deletion of required
capabilities. Earlier package justifications remain in the prior W8 reports.

**Outside W8 production delta: 0.** No cmd/sparkwing composition file changed
this session. The previous report lists the cumulative command composition
edits already committed on this branch. The report itself is the only new file
outside W8's implementation paths.

Friction: Overwing's installed binary reports that it is ten commits behind
its source. The sandbox blocks sockets. The swarm brief forbids Sparkwing
binary execution, pipelines and git writes, so candidate/runtime and landing
checks remain with the lead. Scratch is removed at handoff. The shared build
cache is retained.

STATUS: CONTINUE
