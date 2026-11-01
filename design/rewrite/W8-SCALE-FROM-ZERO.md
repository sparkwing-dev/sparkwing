# W8 Cloud scale from zero (2026-10-17)

## Problem

The launcher reserves nodes that already exist and are Ready.
Karpenter adds a node only for a Pending pod, and job pods are created with
`spec.nodeName` already set, so they never pend. A team with no running node
can never start a Cloud job. The kikd QA on 2026-10-17 worked around this with a
hand-made pause pod.

## Mechanism

When `Reserve` finds no reservable node for an award, the launcher keeps the
award and creates one capacity-request pod for that award. Karpenter sees a
Pending pod selecting the shared requested class and deployment and adds a node. Every
launcher pass retries the held awards. Reservation persists on the node first,
then consumes the launcher's requests on it and waits for their removal, and the
job pod is then pre-bound and checked as before.

- **Name.** `sw-capacity-` plus the Job name's award hash (unit, team, run,
  job, kind, generation, attempt and eval source). Only the same award gets
  AlreadyExists and adopts the request. A re-award creates its own request, and
  a request is never rebound, so cleanup of a stale generation cannot reach a
  newer one. Karpenter may briefly see both requests until the stale award's
  cleanup runs.
- **Record.** `sparkwing.dev/capacity-demand` holds the award's unit fence and
  size, never its bootstrap credential. It locates the award for observation;
  it is never the authority for the expected Pod.
- **Placement.** The node selector requires the shared protected requested
  class and launcher namespace, with the generic dedicated toleration.
  Never-used-node affinity keeps demand off nodes permanently bound to another
  run. The launcher binds the team only after proving shared-pool provenance.
- **Size.** The container requests the award's CPU and memory and the job's
  scratch storage. Karpenter sizes nodes from container requests.
- **Contents.** No volumes, environment, service account token or service
  links. It runs `sparkwing-runner version` from the digest-pinned runner image
  as UID 65534 with every capability dropped, which also pre-pulls the job image.

## Lifecycle

- **Verification.** The job namespace admission policy enforces the request's
  shape: launcher creator, no owner, one runner-image container, no volumes,
  environment or service account token, unbound, shared class/deployment selectors and dedicated toleration.
  The launcher compares only award fields (name, demand record, class/deployment placement,
  image and resources). The expected request comes from outside the Pod:
  `demandFor(award)` on create and adoption, the held award on every sync, and
  on reconciliation the controller's binding and committed allocation
  (`cloud_resources` on reservation observations). A mismatch or a missing
  allocation deletes the request, UID-fenced, and the held award asks again.
- **Liveness.** Sync observes each request through its recorded award, using
  the existing reservation-only observation shape with `capacity:<pod UID>` as
  the reservation ID and the pod UID as the node UID. Awards this launcher holds
  are renewed by the observation; others, such as after a restart, are only
  checked. Each request is reconciled separately. A definitive binding/fence
  refusal deletes only that Pod by UID and permits other requests and new claims
  to proceed. Transport, overload and service-credential errors retain the
  request for retry. A request also goes when the controller ends its award or
  the award deadline passes.
- **Handoff.** A request's container exits after `version`, and reservation
  removes the request once the reservation and its taint are persisted.
  `consolidateAfter` covers the empty gap before Job placement. Request removal
  is awaited so kubelet can release its resources before the Job starts.
- **Cadence.** Every check runs synchronously inside the existing sync pass.
  There is no separate verification loop: admission refuses a wrong shape
  before it exists, so a controller stall delays only the award backstop.

## Invariants kept

- A node never moves between teams or runs. Reservation verifies NodeClaim and
  NodePool identity and shared class/deployment provenance, then atomically
  binds the exact protected team label and permanent used-team/used-run fences.
- Jobs of the same run may share a node. Capacity requests choose never-used
  shared nodes and never establish execution authority.
- Requests carry no secrets. Admission refuses environment, volumes and tokens.
- Cost is bounded by NodePool `limits`, at most one request per live award.

## NodePool contract

Decision 75 replaces per-team templates with one shared NodePool per requested
hardware class and deployment. Use `disruption: {consolidationPolicy: WhenEmpty,
consolidateAfter: 10m}`. The delay must exceed the 3-minute award lease so a
node survives a launcher restart until its unit is re-awarded. The chart example
and its guard test enforce that relationship. The full shared-pool, permanent
binding and warm-placeholder contracts live in
[`CLOUD-JOBS.md`](../../internal/runners/launcher/CLOUD-JOBS.md).
