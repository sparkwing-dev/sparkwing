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
runs admit host-independent plan groups before releasing ready work or expansion
evaluations. Their box-scoped plan groups are admitted by the awarded host,
because a controller has no execution-host identity.
Controller scheduling reserves no machine compute. A launcher award waits for
its authenticated activation before any box-scoped key can be derived.

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
not construction alone.

Two policy cases need a lead ruling. Plan and job groups share one namespace:
A holding plan X while waiting for job Y and B holding plan Y while waiting for
job X still forms a cycle. Both declarations are accepted today. A whole-demand
reservation would change plan lifetime; forbidding cross-role keys would add a
customer restriction. Also, after plan continuity lapses with work already
released, reacquisition can refuse but `RunPolicyRefused` requires unreleased
work. Waiting and cancelling have different policy meanings. The shared owner
preserves these existing semantics pending that ruling.
