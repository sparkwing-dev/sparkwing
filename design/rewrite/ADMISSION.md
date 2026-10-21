# One admission owner

`NativeAdmission` owns execution admission for local owners, independent claims,
burst self-claims, here, local peers, Actions and launcher awards. Placements
supply a tenant capacity store and optional machine compute client. Requesters
require compute; runner configuration controls compute only. Eligibility never
changes logical admission. `Tenant.AdmitCapacity` is its transactional logical
owner, also called directly by controller scheduling.

The global acquisition order is:

1. Accepted run/plan groups, ordered by their derived keys.
2. Accepted job groups, ordered by their derived keys.
3. Machine CPU and memory for the operation actually executing.

The store derives declarations, policies, lease ceilings and execution-host
identity from acceptance and the authenticated award. A node request first
validates its exact award, then admits the parent plan as a prerequisite. It
cannot choose a group or independently control the parent. Controller-owned
runs admit all plan groups before releasing ready work or expansion evaluations.
Persisted keys prefix the scoped key with `plan:` or `job:`, so the same name
at both levels identifies different budgets. The schema baseline is 16. Pipeline
acceptance refuses box-scoped plan groups with an error naming the group and
pointing at job-level box scope, because a run has no single execution host.
Controller scheduling reserves no machine compute. A launcher award waits for its authenticated activation before
any job-level box-scoped key can be derived.

An owner admits plan groups around scheduling, without a run-wide compute
reservation. Each job passes through the same admission operation under its
award before source preparation or execution. Burst self-claims use that same
operation before reserving compute for reconciliation. Reentrant execution
reuses its live reservation. Owner closure evaluation uses the same order;
independent expansion evaluations admit plan capacity under their expansion
lease, then compute. Their source job is already terminal, so its released job
groups are not revived. Planning before acceptance has no accepted groups to
acquire; its existing unit lease remains its authority.

Waiting never holds a later resource while acquiring an earlier one. A job
waiting for plan capacity holds neither job capacity nor compute. A job waiting
for job capacity holds no compute. Compute is acquired only after all declared
logical capacity is granted. Scheduling and state followers hold no compute,
so a remote claimant holding logical capacity can obtain machine resources.
Within each logical stage the existing sorted-key acquisition is reused.

Continuous holders and waiters survive ownership handoff. Admission adopts
only live persisted bindings and preserves waiter arrival order. Recovery first
revalidates plan capacity, then job capacity; an elapsed lease never revives a
reservation. Later job reservations are discarded if recovery must wait on the
earlier plan stage. Unit renewal keeps both stages live while execution waits.

Compute releases after process cleanup has joined. Job terminal transitions
release job holders and waiters atomically. Run completion releases plan
holders and waiters after child executions drain. Process loss leaves logical
capacity attached to durable work, bounded by its unit lease, for adoption or
expiry. No placement owns a second semaphore implementation.

Regressions exercise controller-owned plan contention, owner and independent
claim contention with a finite compute budget, compute enabled and disabled,
and recovery adoption. Tests inspect persisted holders and actual execution,
not construction alone. The production scheduling regression parks one job until
another has a persisted waiter, then verifies promotion and release for both
run-scoped and box-scoped job groups with compute enabled and disabled. Separate
plan/job namespace coverage lets A hold plan X and wait for job Y while B holds
plan Y and waits for job X; both runs make progress.

One policy case needs a lead ruling. After plan continuity lapses with work
already released, reacquisition can refuse but `RunPolicyRefused` requires
unreleased work. Waiting and cancelling have different policy meanings. The
shared owner preserves these existing semantics pending that ruling.
