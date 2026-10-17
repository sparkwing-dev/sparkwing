# W8 Cloud scale from zero (2026-10-17)

## Problem

The launcher reserves only nodes that already exist, are Ready and carry the
team's protected label `node-restriction.kubernetes.io/sparkwing-team-node`.
Karpenter adds a node only for a Pending pod, and job pods are created with
`spec.nodeName` already set, so they never pend. A team with no running node
can never start a Cloud job. The kikd QA on 2026-10-17 worked around this with a
hand-made pause pod.

## Mechanism

When `Reserve` finds no reservable node for an award, the launcher creates one
capacity-request pod for the awarded unit and leaves the award unstarted, as
before. Karpenter sees a Pending pod that only the team's NodePool can satisfy
and adds a node. Each launcher sync deletes a request pod once the node it is
bound to is Ready. The unit's next award then reserves that node through the
existing path, which pre-binds the job pod and verifies it after creation.

- **Name.** `sw-capacity-` plus a hash of team, run, job, kind and unit ID. A
  re-award of the same unit, or a second launcher replica, gets AlreadyExists
  instead of a second node. Generation and holder change on every re-award, so
  the name leaves them out.
- **Placement.** The node selector requires the team's protected label value,
  and the tolerations are exactly the job pod's: the team taint and the
  `sparkwing.dev/cpu-band` taint. Only a NodePool whose template sets that
  label can satisfy it, so only the team's own pool can grow.
- **Size.** The container requests the award's CPU and memory and the job's
  scratch ephemeral storage, so Karpenter picks a node that fits the job.
  Requests sit on the container, not the pod, because Karpenter sizes nodes
  from container requests.
- **Contents.** No volumes, no environment, no service account token, no
  service links. It runs `sparkwing-runner version` from the digest-pinned
  runner image as UID 65534 with every capability dropped. This also pre-pulls
  the job image onto the new node. The award's bootstrap credential never
  reaches the pod or its manifest.
- **Lifetime.** Sync deletes the pod, UID-fenced, when its node is Ready or when
  the award's own execution deadline has passed. No new timeout is introduced.
  Until it is deleted, the pod makes its node non-vacant, so no job is placed
  next to it. Deletion is graceful, so kubelet has released the pod's
  resources before the next reservation sees the node as empty.

## Invariants kept

- A node never moves between teams. Reservation still checks the node's
  NodeClaim, its NodePool UID and the template label, and records first use in
  the protected used-team label. The request pod changes none of that.
- Team isolation. A request pod can only land on a node carrying its team's
  protected label, and kubelets cannot set that label on themselves.
- No secrets in the request pod: tested by a property over random bootstrap
  bytes and team names.
- Job pods are still pre-bound and still verified after creation. The request
  pod is not a job pod and is never adopted as one.
- Cost stays bounded by the operator's NodePool `limits`. At most one node is
  requested per waiting unit.

## Needs an owner decision

1. **Consolidation window.** The award that triggered the request is not held
   while the node boots. It lapses after the award lease (`DefaultLease`,
   3 minutes) and is re-awarded. A NodePool that consolidates empty nodes
   sooner than that can delete the new node before the re-award reserves it.
   The kikd pool uses `consolidateAfter: Never`, which never scales back to
   zero. Operators need guidance: `WhenEmpty` with a `consolidateAfter` above
   the lease plus the launcher poll, for example 10 minutes. The chart example
   is unchanged until that value is chosen.
2. **First-job latency.** For the same reason, a team's first Cloud job starts
   about one lease after the node is Ready, not when it is Ready. Holding the
   unplaced award and retrying its reservation would remove that wait, at the
   cost of a held-award set in the launcher. This is not built.
