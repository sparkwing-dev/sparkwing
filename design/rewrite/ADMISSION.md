# One durable admission owner

The controller store, or the local SQLite store, owns all logical capacity.
Owners propose ready, approval and expansion releases. The store commits plan
admission with permission to release. Hosts call `MayStart(job, award)` and receive
admit, wait, skip or fail. Accepted declarations and the authenticated award supply
every key, policy, weight and host. Plan and job namespaces stay separate. Plans
cannot declare box scope. Job box scope uses the award's sealed execution host.

`admission_work` identifies reservations by run and job. Each level's complete
group set grants in one transaction with one durable arrival sequence. A queued
set holds no group. Policy effects, including cancellation, roll back if another
group must wait. Promotion runs through the same complete-set operation. Handoff
changes authorization fences and start permits, never reservation identity or
arrival. An expired reservation starts a new arrival. Supersession cancels the current
job award even when the reservation predates its claim. The retained transactional
ledger and policy engine live in `internal/store/concurrency.go`.

Every executable job needs an award-bound permit, including jobs without groups.
The common start transition requires it, and process delegation reads its live
permit with the authenticated running attempt. SkipIf and memo run first on the
executing host after job admission and compute, under that award. A predicate skip
settles the job and releases its reservations. Admission skip or failure commits
the terminal outcome through the existing state machine and verdict owner.

Approval-only predicates have no execution host. Store admission releases a
fenced evaluation without job groups or compute. An owner evaluator, or an
awarded evaluation unit for controller-owned runs, executes the SDK callback.
The store commits skip, failure or opening the approval through one source-fenced
boundary. A true predicate stays skipped. Expansions use the same release boundary.

Acquisition, renewal and queue precedence use claim **or** owner liveness, bounded
by the capacity lease and cancellation deadline. Acquisition and maintenance discard holders and waiters when neither source
remains live. Maintenance only reaps. Hosts retry `MayStart` to promote a complete
set through the same admission operation. Recovery reconciles immediately.
Lapsed plan capacity queues indefinitely after the first release, regardless of
its initial policy. Only new releases wait. Released jobs and evaluations settle
without reacquiring the plan. Queue views name the group being reacquired.

Burst placement awards provisionally, requests logical admission, then tries
compute without waiting. Unavailable compute relinquishes the award, permit and
job reservations while preserving the original readiness ranking window. Compute
releases after cleanup joins. Terminal job and run transitions release their
logical reservations. `cancel_others` replaces capacity immediately and requests
cooperative cancellation. Disconnected work can overlap until cleanup or the
five-minute drain deadline. No cancellation timeout knob remains.

Memo absence permits execution. Authorization and fence refusals remain errors.
Publication stages a producer-owned record. Only committed success makes it
reusable. Failed, cancelled or rolled-back settlement cannot publish reusable
results. Successful cache records survive producer retention.

The real-store seeded simulation varies host and store interleavings and checks
complete grants, capacity, FIFO, award permits, host scope and settlement after
every operation. Native recovery tests couple ready work, approvals and expansions.
Focused mutation regressions cover recovery-entry gating, owner/claim horizon,
handoff arrival and partial grants. Both store dialects use the same fixtures.
