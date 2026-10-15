# W8 local controller continuation

Continues `f5281682a` and `W8-REFRESH-RESET-CONTINUATION.md` under the second
14:40 lead decision. The first-priority slice remains incomplete. No
`internal/orchestrator` file changed.

The sole initial merge conflict was the modify/delete conflict in
`internal/opsview/doctor_rejections_live_test.go`. The integration branch removed
the surrounding doctor daemon tests and their fixtures. This continuation
accepts that deletion, removing 129 test lines. No amended contract or lead-owned
handoff was edited. Git still records the unmerged path because the swarm brief
forbids git writes. The lead must stage its deletion and finish the merge.

## Implemented

`openLocalController` now invokes `localcontroller.Config.Open`. Its binder
constructs concrete state projection, shared `Server.HTTP()` API/UI handlers,
store-bound local secret encryption, signed runner/engine policies, local
claimant delegation, enrolled machine observations and native host services.
The existing owner still supplies admission RunStore, elected recovery,
maintenance, joined workers and store closure.

`NewState` reads activity from one default-team snapshot, excludes publication
history and terminal nodes, watches changed counts and opens TYPESAFE_API_KEY
through the shared bound cipher. Startup uses the existing fenced reaper.
Expired started local owners fail, expired remote owners requeue and current
owners remain live. No forced invalidation of a current lease was added.

Invariant: ambient runner names and credentials never select the local
account. `NewClaimant` captures the kernel account, authenticates through the
shared peer policy and delegates queue permissions through Store.Issue. Its
profile resolver accepts only that account's authenticated default-team
capability. Cleanup revokes its queue credential and preserves cleanup errors.
Generated tests cover identity substitution, tenant, audience and authority
substitution, cancellation and closure.

Independent review found that the initial binder omitted RequestCapacity,
which made matching reject positive CPU requests. The binder now supplies the
machine CPU count. The regression uses the production binding and actual
`match.Fits` to admit a fractional CPU request. Memory remains unknown under
the existing match contract. Admission still owns compute arbitration.

The local-run resolver now attaches to the elected API and captures the local
environment. Detached preparation uses shared source acquisition, verified
engine selection and SDK planning, and retains pipeline alias/entrypoint,
machine settings and the captured snapshot. It does not open another store.
Foreground execution refuses before intake because its remote host state
adapter is missing.

`NativeServices` binds the owned tenant to W3's native host configuration,
lease keeper, source, captured environment, state following, process authority
registry and policy router. It checks current authority before allocation,
masks private JSONL logs and joins listener, registry and subscription cleanup.
It does not supply success for missing owner or storage policy.

The W8-owned StoreBinding contract now carries a Refresh callback. StoreHosting
deep-captures the award and constructs that callback through its existing
Authenticator/Authorize path. Native services require and use it on each
authority refresh. ValidateAuthority alone checks ancestry but omits unit
liveness. The callback applies the shared C2 policy instead of duplicating it
or adding another bearer field. No foreign contract shape changed.

The schedule factory now constructs the same home-bound lifecycle and native
timer install/remove operations. It opens no store. Registration, pin and tick
callbacks remain unbound pending the contracts below.

## Real integration points left open

1. **W2/W1 local credentials.** Store.ReadAuthentication accepts only bearer
   and session presentations. CredentialPeer currently returns unauthenticated.
   The queue therefore cannot sustain production startup. W2's existing
   14:40 assignment must supply the persisted local authority and its issuance
   relation. The W8 enrollment resolver is now bound. The constructor test does
   not run workers and does not establish sustained readiness.
2. **W2/W9/W1 local schedules.** ScheduleRepositoryManager carries a clone URL,
   branch, SHA and declarations. Its controller rejects a local filesystem path.
   Neither ScheduleManager nor the local control transport has pin/unpin or tick.
   A contract must carry absolute checkout, replace/upsert scope, captured
   environment, follow/proof settings, pin changes and dry-run tick to the
   elected owner. It must also define durable snapshot association with a
   schedule/repository. `crons.Service` already supplies the underlying local
   operations. Opening a second state.db or inventing foreign HTTP routes was
   not used to bridge this gap.
3. **W3 foreground hosting and owner policies.** NewNativeHost has the declared
   NativeHostStore interface, but no shared HTTP-backed implementation for the
   CLI-hosted owner. OwnerHost.Layer has no concrete exported policy binder.
   Dispatch currently refuses absent owner layers. Node-award adoption into
   execution delegation also remains unbound.
4. **W7/W3 native storage.** Authorized byte/record/output clients and their
   binding into NativeServices remain missing. The output callback returns a
   policy error. Private host logs also need the controller read projection.
5. **W3/W1 process relations.** ProcessAuthorities.CheckBinding forwards the
   narrowed ephemeral NodeHost principal to its relation checker.
   Store.CheckBinding resolves persisted authority and requires Controller
   audience. Passing the owned store cannot authorize those process routes.
   The relation adapter must preserve the child's narrowed authority while
   validating committed parent relations. Substituting the parent wholesale
   could widen read-set authority and was not added.

The earlier source/build contract requests remain open: effective resolved-mod
replacement hashing, SDK cache-entry/lease ownership, and uploaded working-tree
versus committed-bundle identity. Native Cloud isolation, aggregate scratch,
teardown and non-Linux platform evidence remain unprovided.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and GOTMPDIR=/tmp/sw-astra-W8. Tests used at most -p 4.
Early tests used Go's default /tmp for t.TempDir, whose directories the test
framework cleaned. Final checks also set TMPDIR to the permitted scratch path.

- Whole repository build passes.
- Full localcontroller, runners, launcher, runner services and engine tests pass.
- Focused production CLI constructor, local run and native composition tests pass.
- Scoped vet and race selections pass. The final CPU-capacity fix also passes
  the production constructor selection under race.
- W8/LauncherCloud acceptance selection passes. No new native acceptance cell
  is claimed closed. No archived test was newly ported.
- Generated local profile testing passes 300 executions. Activity and bound
  secret fuzzing pass 36 and 21 executions. Native log authority/masking fuzzing
  passes 539 executions. Local identity ambient-input seeds pass under race.
- The final lost-claim-fence property passes all six mutation seeds through the
  common C2 authenticator. It covers generation, attempt, holder, lease expiry,
  finished unit and finished run, and verifies unchanged log bytes after refusal.
  Its bounded fuzz run passes 823 executions.
- Commentcheck is clean and git diff --check passes.

The unchanged W9 rewrite_integration probe now passes its local-controller
subtest. On the final resolver, local run reaches the private socket transport
and fails with `connect: operation not permitted` in the sandbox. An earlier
preparation-first version reached real source pin validation and rejected this
checkout's unpublished SDK replacement. Local schedules now reaches the
specific missing checkout/environment/pin registration binding, rather than
the previous missing service factory. The overall probe remains red.

## Remaining assigned work and volume

The 12:35 shared retry validator and its Hosting, launcher Claim and Cloud
Exchange entrypoint tests remain green. A1-38 is still closed on W8's side.
The four dead tables KeepAliveReasons, LocalPlatformPolicies, CloudUnitPolicies
and CloudPodInvariants remain deleted. No second implementation was added.

| Amendment group | Current W8 disposition |
| --- | --- |
| A1-08/09/18/19/26/27/36/39/48/51 | Shared claim/host constructors remain implemented. Concrete local state, enrollment and native configuration now bind. Peer credentials, owner policies, process relations and storage still prevent full execution. |
| A1-10/11/12/43/45/46/50/52/55 | Engine/source/environment/retry work remains implemented. Detached preparation reaches real acquisition/planning. Foreground hosting, build-cache contracts and native evidence remain open. |
| A1-20/49 | Private credential loading and repository fences remain implemented. Local queue now consumes shared peer authentication/delegation. W2's peer persistence and enrollment/bootstrap composition remain open. |
| A1-38 | W8 immutable award fences, claim generations and Job-instance correlation remain complete. W2 owns production SyncLauncher reader composition. |
| A1-40/41 | Production constructor, RunStore, state, recovery, activity, HTTP/UI and cleanup now bind. Sustained worker startup, foreground CLI and schedule operation transport remain incomplete. Managed units and reset regressions remain green. |
| A1-42/53 | Cloud and platform native acceptance evidence remains blocked. |

W8-owned production Go adds **452 lines this session** and deletes **3**,
for **net +449**. The
integration merge contributes another **72** net production lines in engine
pin files. Against main, W8 adds **10,047** and deletes **6,892** lines, including
untracked implementations: **net +3,155**, or **5,455 above the -2,300 budget**.
These counts exclude tests, testdata, acceptance and contract declarations and
disable rename detection. Budget cuts were not reached because the lead's
first-priority production composition remains incomplete. The earlier package
justifications remain in W8-REFRESH-RESET-CONTINUATION.md. The added state,
claimant and native binding responsibilities were previously unbound, not
legacy code that could be deleted alongside them. This is not a budget waiver.

Outside W8, production command composition adds **266** and deletes **18**
lines: **net +248**. Shared files touched are
`cmd/sparkwing/local_services.go`, `local_services_test.go`, `run_host.go`,
`schedules_local.go`, and the new `local_run.go` and `local_host.go`.
The only additional shared edit is the 129-line conflicting test deletion.
No foreign production API was removed and no other caller migration was needed.

Friction: Overwing reports a stale installed binary. Sockets are sandbox-blocked.
The sandbox and swarm brief leave git index resolution and commit to the lead.
No Sparkwing binary, pipeline, deployment or release ran. Scratch is removed at
handoff. The shared build cache is retained.

STATUS: CONTINUE
