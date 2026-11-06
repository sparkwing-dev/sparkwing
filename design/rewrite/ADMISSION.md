# One durable admission owner

The controller store, or local SQLite store, owns logical capacity. Owners propose
releases. The store commits plan admission with release permission. Hosts call
`MayStart(job, award)` for admit, wait, skip or fail. Accepted declarations and the
authenticated award supply keys, policies, weights and host scope. Plan and job
namespaces stay separate. Plans cannot declare box scope.

`admission_work` identifies reservations by run and job. Each level's complete
group set grants atomically with one durable arrival. A queued set holds nothing;
policy effects roll back if another group waits. Hosts retry the same operation
to promote. Handoff changes fences and permits, never reservation identity or
arrival. Expired reservations start a new arrival. Queue timeouts apply only while
waiting. The ledger and policy engine remain in `internal/store/concurrency.go`.
Every acquisition creates and locks the group entry before locking holders.

Every executable job needs an award-bound permit, including jobs without groups.
The common start transition and process delegation require it. SkipIf and memo
run first on the executing host after logical admission and compute, under that
award (decision 42). A skip settles and releases. Admission skip/fail commits the
terminal outcome through the existing state machine and verdict owner. Completion
returns the committed verdict, including cancellation after the owner's snapshot.

Approvals cannot declare concurrency groups. Acceptance points at the job after
the approval. An approval's SkipIf runs on the coordinator before requesting
approval, holding no capacity (decision 51). Both placements preserve declared
dependency, secret and reference reads through one accepted-job rule and the exact
evaluation fence. Pipeline references carry only the referenced pipeline and job,
separate from the caller's run binding. Generator expansion cannot borrow approval
permissions. The
store never runs SDK code. One fenced verdict boundary commits skip, failure or
approval opening. A true predicate stays skipped. Expansions share the release
boundary.

Acquisition, renewal and precedence require claim **or** owner liveness, bounded
by the capacity lease and cancellation deadline. Maintenance only reaps complete
sets. Recovery reconciles immediately. Lapsed plan capacity queues indefinitely
after first release; only new releases wait (decision 38). Released jobs and
evaluations settle without reacquiring the plan. Queue views name the group.

Burst awards provisionally, requests logical admission, then tries compute without
waiting. Busy compute relinquishes the award, permit and job reservations while
preserving the readiness-ranking window. Compute releases after cleanup joins.
Terminal transitions release logical reservations. `cancel_others` replaces
capacity immediately and requests cooperative cancellation. Disconnected work can
overlap until cleanup or the five-minute drain deadline. No timeout knob remains.

Memo absence permits execution; authorization and fence refusals remain errors.
Publication stages a producer-owned record. Only committed success makes it
reusable. Failed or rolled-back settlement cannot publish reusable results.
Successful records survive producer retention. Retired coalesce fields are gone.

Real-store simulation models claim, owner and capacity leases independently per
work and level. Every step checks expected complete sets, including absent sets,
capacity, FIFO, permits, host scope and settlement. Claim-only loss cannot waive
arrival checks. Native recovery couples ready work, approvals, expansions and
contention. Mutations reproduce the ws12j/k failures. Production `MayStart` tests
replace the shadow allocator on both dialects. Concurrent admission tests race
execution, settlement and reaping. Architectural lint restricts ledger writers;
behavioral tests prove permit enforcement and complete-set grants.

## Durable machine admission

Logical concurrency above and local machine capacity are separate admission
layers. `internal/admission/ledger.go` owns the machine policy engine: priority
and class FIFO, guarded backfill, skip/fail, supersession and atomic semaphore
grants. The controller persists its complete snapshot, including waiters and
arrival counters, with request receipts in `local_machine_admission` in
`state.db`. `internal/store/machine_admission.go` commits the complete payload in
one transaction. Startup restores it before admission decisions.

Machine requests use HTTP on the controller API socket. Only the verified local
socket peer can call these routes. Browser cookies supply no authority. A
client request key identifies a retry and returns the same ticket or grant.
Ticket reads can long poll for about 20 seconds. A lost poll retries the same
ticket and retains its arrival. Release and cancel are idempotent. Lease watches
return committed eviction, orphan cancellation and run cancellation immediately.
In-memory wake channels only prompt another read of the committed ticket.
Active tickets remain until released or reaped. Mutations prune terminal retry
receipts older than one minute and retain at most 256. Pruning removes receipts
and their request keys together, including refusal and cancellation tombstones.
Startup applies the same bounds before serving requests. Receipts with recorded
execution roots remain until the controller observes those sessions empty.
`Store.SetRunCancellationObserver` forwards cancellation after the SQL transaction
commits, including cancellation from recovery, deletion and credit enforcement.
Rolled-back transactions do not notify. Replayed cancellation retries failed
notification without treating a committed transaction as a SQL conflict.

Every ticket records its holder's root PID and `procgroup.ProcessBirth` start
identity. The controller verifies process ownership against the socket peer.
Each decision and a periodic process check release capacity for dead holders or
reused PIDs. A holder with a recorded job root keeps its reservation until orphan
cleanup observes the execution session empty. Inspection errors keep the reservation. Connections, renewals,
expiry and timeout settings do not determine machine authority.
Release and cancelled-acquisition cleanup retry for at most one second. When the
controller cannot answer, cleanup reports the error and PID liveness recovers
capacity after the holder exits. During shutdown the controller refuses new
admission, joins embedded hosts while the API remains reachable, then closes and
joins the API before closing the store and releasing election ownership. Drain
completes shutdown even if its final admission save fails.

The host registers each spawned job's PID and start identity with
`POST /api/v1/local/admission/requests/{id}/process` before its SDK handshake.
This measurement root is separate from the holder root that determines lease
liveness. The controller verifies its owner against the socket peer and persists
it with the ticket. The existing sampler exposes current, peak and average
process-tree CPU and resident memory through the queue API. A dead or reused
measurement PID withholds readings without ending the holder's reservation.
Job measurements keep CPU baselines separate from stall detection, so a scan of
shared runner roots cannot shorten a job's measurement interval or alter its average.
Jobs sharing a runner PID have no individual reading until their job processes
are registered. Host-root CPU attribution for admission remains unchanged.
Existing host processes still own job execution and logical claim renewal.

The liveness worker checks recorded execution roots outside the admission lock.
On Unix, confirmed holder death or reuse triggers cleanup of the job's dedicated
session using its root PID and start identity. The session check refuses live
members behind a reused root identity. Empty-session acknowledgement is persisted,
so a controller restart or failed acknowledgement repeats cleanup safely. Live
holders keep running. Cancelled jobs with registered roots retain their machine
charge until the host releases after joining them. On Windows, runner-owned
kill-on-close Job Objects terminate jobs when their runner dies. The controller
retains the charge until it observes the root gone.

`TestAdmissionOrphanCleanupSurvivesControllerRestart` covers a real Linux holder
process, an orphaned child and a reopened SQLite store. Additional process tests
cover failed cleanup persistence, live holders, root reuse and receipt retention.
`TestMachineLeaseWatchMeasuresCancelToJobProcessExit` measures cancellation over
the verified API socket through nodehost to an executing protocol child, without
an award-renewal loop.

Store round trips and restart ordering are guarded by
`internal/store/machine_admission_test.go`. The ledger's property and invariant
tests continue to guard policy independently of transport and persistence.
