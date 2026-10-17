# W8 Cloud scale from zero (2026-10-17)

## Problem

The launcher reserves only nodes that already exist, are Ready and carry the
team's protected label `node-restriction.kubernetes.io/sparkwing-team-node`.
Karpenter adds a node only for a Pending pod, and job pods are created with
`spec.nodeName` already set, so they never pend. A team with no running node
can never start a Cloud job. The kikd QA on 2026-10-17 worked around this with a
hand-made pause pod.

## Mechanism

When `Reserve` finds no reservable node for an award, the launcher keeps the
award and creates one capacity-request pod for its unit. Karpenter sees a
Pending pod that only the team's NodePool can satisfy and adds a node. Every
launcher pass retries the held awards. Reservation persists on the node first,
then consumes the team's requests on it and waits for their removal, and the
job pod is then pre-bound and verified as before.

- **Name.** `sw-capacity-` plus a hash of team, run, job, kind and unit ID. A
  re-award of the same unit, or a second launcher replica, gets AlreadyExists
  and adopts the request. Generation and holder change on every re-award, so
  the name leaves them out.
- **Record.** `sparkwing.dev/capacity-demand` holds the award's unit fence and
  size, never its bootstrap credential. The expected pod is recomputed from it.
- **Placement.** The node selector requires the team's protected label value,
  and the tolerations are exactly the job pod's. Only a NodePool whose template
  sets that label can satisfy it.
- **Size.** The container requests the award's CPU and memory and the job's
  scratch storage. Karpenter sizes nodes from container requests.
- **Contents.** No volumes, environment, service account token or service
  links. It runs `sparkwing-runner version` from the digest-pinned runner image
  as UID 65534 with every capability dropped, which also pre-pulls the job image.

## Lifecycle

- **Verification.** Job pods and capacity requests pass one comparison,
  `verifyAdmittedPod`: the admitted object may differ from the request only by
  API defaults, the named EKS injections, owner labels and, for an unbound
  request, the scheduler's node. Requests are checked on create, on adoption
  and on every launcher pass; a mismatch deletes the request, UID-fenced, and
  the held award asks again.
- **Liveness.** Sync observes each request through its recorded award, using
  the existing reservation-only observation shape with `capacity:<pod UID>` as
  the reservation ID and the pod UID as the node UID. Awards this launcher holds
  are renewed by the observation; others, such as after a restart, are only
  checked. A request goes when the controller ends its award or the award
  deadline passes, so cancelled or reassigned work stops asking for nodes.
- **Handoff.** A request stays on its node until an award reserves that node.
  The reservation taint keeps further requests off it, and removal is graceful,
  so kubelet has released the request's resources before the job is placed.
- **Cadence.** Kubernetes-only verification of job pods and requests runs on
  every pass. A controller Retry-After pauses only controller requests.

## Invariants kept

- A node never moves between teams. Reservation still checks the NodeClaim,
  its NodePool UID and the template label, and records first use in the
  protected used-team label.
- A request can only land on a node carrying its team's protected label, and
  reservation consumes only requests in the launcher namespace that carry the
  same team's label and no owner.
- No secrets in a request: the property test covers the outbound object and
  admitted-object verification covers what Kubernetes stores.
- Cost is bounded by the NodePool `limits`, at most one request per live unit.

## NodePool contract

Team NodePools use `disruption: {consolidationPolicy: WhenEmpty,
consolidateAfter: 10m}`. `consolidateAfter` must exceed the 3-minute award
lease: after a launcher restart a held award is no longer renewed, its request
is removed when the award lapses, and the node may sit empty until the unit is
re-awarded. The owner is choosing the exact value; 10 minutes ships in the
example and is enforced by a chart test against `limits.DefaultLease`.
