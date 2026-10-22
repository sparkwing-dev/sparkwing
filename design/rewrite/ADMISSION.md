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
evaluation fence. Generator expansion cannot borrow approval permissions. The
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
