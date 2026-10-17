# Cloud Jobs

`NewLauncher` composes the shared controller client, Kubernetes and the enforced
Cloud boundary. Its client always selects `EngineOptions.Launcher`, independent
of the requested unit kinds. `Hosting` and `BootstrapHost` keep the caller's
HTTP transport when opening award or exchange clients. Each uses its own bearer.
The launcher component uses the default engine client, shared HTTP handlers and
real store. Its transport invokes the handlers without sockets. The embedded
local queue has no launcher adapter. The controller uses the transactional launcher sync reader.

`BuildJob` builds plan, eval and work Jobs from an awarded binding. It checks
images, resources, deadlines and engine identity before reserving an exclusive
team node. `CloudBoundary` must return matching placement and network evidence.
A failed boundary check releases its reservation and returns no runnable object.
Plan awards have no job or expansion source. Eval awards identify the terminal
source job and generation, while work awards have no expansion source. The
builder and private award reader reject other shapes before source acquisition
or node reservation. `FuzzCloudAwardUnitBindingW8` checks this boundary.

Retry awards retain the original request and accepted seals. Their request run
ID can differ from the execution run ID. Claim and bootstrap checks compare
accepted source seals with the original request, while the capability binding
must name the execution run. `FuzzControllerRetryAwardPreservesSealsAndFence`
checks both boundaries through claim and exchange. The sync-generation property
uses retained request seals when reconciling current and stale Jobs.

Requested CPU and memory above the operator ceilings fail with both values in
the error. The builder never clamps an award. CPU uses Kubernetes decimal
quantities and rounds up to a millicore. If that quantity would exceed the
ceiling, the builder refuses it before reserving a node. Exact values such as
2.007 cores stay exact through conversion. The pod shares its awarded CPU and
memory across the host and Docker sidecar.

The Job retains its requested template before API defaulting. The launcher
verifies Pods against that template after activation
and on each reconciliation. Activation polls for up to two seconds for exactly
one owned Pod. An active, unsuspended Job with zero or multiple Pods is rejected.
Pod-list and PriorityClass API failures retry on reconciliation without failing
the award. Only a typed spec mismatch starts rejection.

Allowed EKS changes are region environment values, the VPC controller's unit
Pod ENI resource and validated ENI annotation, and load-balancer readiness gates.
Customer Pods cannot receive IRSA or Pod Identity credentials, projected tokens
or token mounts. Their ServiceAccount must have no role annotation or Pod Identity
association. Infrastructure probes use the same account and restriction.

The explicit defaults are DNSClusterFirst, default-scheduler, a 30-second
termination grace, the serviceAccount alias, PreemptLowerPriority, priority zero,
container termination-message path/policy, startup-probe timeout/success threshold,
and the standard 300-second not-ready/unreachable NoExecute tolerations.
The cluster's globalDefault PriorityClass supplies its name, value and preemption
policy instead of priority zero. Multiple defaults select the lowest value, as
Kubernetes does. Other spec differences remain mismatches. Clusters changing the
standard toleration/grace defaults need matching configuration and native QA.
All other changes, including missing authored security fields, reject the Pod.
The launcher logs rejection, UID-fences Job deletion and tells the controller to
fail that exact plan, eval or work award. A rejection annotation and cleanup
finalizer retain ownership until the controller acknowledges failure and every
container stops. Rejection first cordons the reserved node and UID-fences deletion
of its Karpenter NodeClaim. A failed retirement retains cleanup ownership and
cannot release capacity. Restart reconciliation repeats pending rejection reports.
Malformed ownership, deadline or bootstrap annotations trigger UID-fenced Job
deletion. Garbage collection owns its Secrets. The node's persisted reservation
still requires current award retirement and container vacancy before cleanup.

RBAC grants Pod/Job creation only to the launcher. Operators must keep other
bindings from granting those rights. Customer ServiceAccounts receive no API
credential or role. Team nodes carry a team-specific NoSchedule taint, which
only launcher Job Pods tolerate. Kubernetes administrators remain trusted.
The launcher requires no Kubernetes admission policy API or webhook exclusions.

`Launcher.LaunchOne` persists a suspended Job and checks the admitted response
before creating the bootstrap Secret. Kubernetes defaults may fill unset fields.
Changes to the authored placement, containers, private volumes, resource budget,
execution policy or teardown ownership refuse activation. An injected failure
policy cannot turn an OOM failure into another pod execution.

The admitted bootstrap Secret must retain its exact bytes, immutable flag, type
and Job owner before activation. Extra data or altered ownership refuses launch
and starts teardown without reading Secrets or exposing credential bytes. A
created Secret's name and UID are recorded on the suspended Job before teardown.
`Sync` retries its UID-fenced deletion after a failure or launcher restart and
retains the reservation and finalizer until cleanup succeeds. The record binds
to the Job UID and must survive the admitted update. A cleanup record injected
at Job creation is refused. Added server metadata does not change the bootstrap
contract.

Secret creation and the cleanup record are separate Kubernetes writes. If the
record fails, the launcher attempts immediate UID-fenced deletion, retains the
Job and reservation, and reports the target name and UID. If that deletion also
fails, an operator must remove the named Secret. A crash between creation and
recording has the same residual when admission both renames the Secret and
removes its Job owner. Normal bootstrap Secrets retain their owner reference.

After publishing the bootstrap, the launcher confirms that the controller still
recognizes the exact awarded binding before activating the Job. Cancellation,
an unavailable controller or a mismatched decision leaves no runnable Job and
starts teardown.

`Sync` reconstructs ownership from persisted Job and
node metadata. It adopts live Jobs and retires cancelled, expired or terminal
Jobs. One controller call reconciles all live observations. Decisions may arrive
in any order; each must match exactly one observed binding before any live Job
is retired. Duplicate, changed, extra or missing decisions preserve live Jobs
and return an error. Expired or locally terminal Jobs can still be cleaned up
when the controller is unavailable.
Teardown deletes only the recorded bootstrap UID and retains the node reservation
while a Job container remains. `KubernetesBoundary.Release` also requires pod
removal and reservation-scoped cleanup before another Job can reserve the node.
Before Secret creation succeeds, including an AlreadyExists response, teardown
deletes no Secret. Job garbage collection handles uncertain creation through its
owner reference. If Job deletion fails, teardown still deletes its bootstrap after confirming
that the persisted Job has the original UID. An unknown or replacement Job
retains its bootstrap and reservation until ownership can be established.

A suspended Job follows the same authority check as an active Job because
another launcher may still be publishing its bootstrap. Suspension alone never
authorizes teardown. Cancelled or expired awards still retire suspended Jobs.
Controller errors preserve ownership and reach the caller.

The builder uses a readiness-gated native DinD sidecar, fixed aggregate 20 GiB
scratch, a separate customer service account and no API-token automount. Its
bootstrap mount is absent from Docker. Pipeline execution starts after source
acquisition. Plan resolves the exact SDK pin. Eval and work use the accepted
engine identity.

The immutable Secret contains `bootstrap` and `award.json`. The metadata carries
the awarded engine, complete source projection, binding, CPU and memory as
`engine`, `source`, `binding`, `cpu_cores` and `memory_bytes`. The bootstrap is
excluded from metadata. Source locations and bootstrap bytes stay out of Job
metadata, arguments and environment. Both files use the existing private mount,
which is absent from DinD. Admission verifies both files before activation.

`ReadBootstrapAward` reads both files beside the absolute bootstrap path through
the shared credential reader, including Kubernetes projection symlinks. It
rejects duplicate, unknown, incorrectly cased or malformed metadata and returns
no partial award on failure. Source metadata must be present and non-null.
The shared source package validates and acquires its contents.
`cluster.BootstrapRuntime` restores this award and passes it to
`BootstrapHost.Run` for one-time exchange. Its `Run` method parses the built
container arguments and compares their fence with the private award before
constructing either source or host services. Fetch-source can read the award without
exchanging it. The existing Kubernetes JSON decoder supplies strict decoding
because it checks nested duplicates without another parser implementation.
Unit kind, attempt, eval-source fence and engine now travel in that file. The
builder emits only the declared bootstrap arguments. Eval and work commands
pass the real shared parser. Plan commands omit `--job`. Eval and work name
their job, checked against the private award before service construction.

`cluster.BootstrapRuntime` reads the private award once and compares its run,
job and generation with argv before constructing services. It sends fetch-source
to `SourceInit` and run-node to `BootstrapHost`. A partial host binding closes
on failure, and normal cleanup follows exchange and host execution. The runtime
sets the exchange URL from the parsed controller argument. `MainWithRuntime`
supplies one set of runner, launcher and bootstrap bindings to the executable.
All commands share the process's SIGINT/SIGTERM context.

`SourceInit` captures source and deadline fields before binding live source
access. It uses `source.PrepareCheckout` because bootstrap authority permits
checkout before exchange, while SDK execution belongs to the resulting work
claim. Remote repository identity and the exact SHA are required. Eval/work
source must retain the accepted engine. Checkout uses a private staging directory
beside the destination. Publication moves only its matching checkout into the
requested source directory. Existing destinations are refused. Temporary Git
credentials and staging directories are removed before return. Source-access
cleanup and cancellation causes remain errors. The initializer never exchanges
the bootstrap or installs an SDK.

`NewControllerControl` binds the launcher's claim and sync operations to the
shared controller wire interface. Its client must use `EngineOptions.Launcher`.
It captures observed labels/capacity and requests the configured Cloud unit
kinds in one call. The server supplies team, holder, generation, resources and
the one-time bootstrap. The launcher has no caller-selected holder override.
Transport failures never trigger a second claim in the adapter.

`sparkwing-runner launch` uses the shared runner grammar. Both `--image` and
`--docker-image` require digest-pinned operator images. Namespace defaults to
`sparkwing-jobs`. Deadlines come from the award rather than a launcher flag.
`sparkwing-runner launch --poll <duration>` sets the interval between claim and
reconciliation cycles. The duration must be positive. Omitting it keeps the
runtime's configured interval, which defaults to five seconds.

The adapter projects plan source through `source.ForRequest`, and eval/work
through `source.ForExecution` using the accepted spec and matching engine.
Claim publication, source initialization and bootstrap exchange capture private
award values before calling service bindings. The claim adapter uses the shared
source projections directly.
Each award must include resolved CPU and memory in `ClaimedUnit.Execution`.
For eval, `Binding.Generation` fences the terminal source and
`Binding.Expansion` identifies the eval's own live lease. Both survive dispatch.

The Job's binding retains the controller unit ID, kind, holder and awarded
generation. Sync sends that fence with the actual Job namespace, name and UID
and the reserved node UID reconstructed from Kubernetes metadata. Different
generations of one unit can coexist during teardown. Sync correlates each live
award by its complete lease fence and each removal by its observed Job instance.
Deletion retains that UID as a Kubernetes precondition. Changed fences,
duplicate or missing decisions and returned credentials fail before activation
or teardown. No in-memory award map or replacement bootstrap is needed after
restart.

The adapter retains observed Job identities separately from the request passed
to the transport, so request mutation cannot select a different cleanup target.
The launcher-side award fence and Job-instance correlation are implemented.
The controller and store must check those observations against transactional
ownership before returning decisions.

`BootstrapHost` creates a separate controller client whose bearer callback holds
only the Job's bootstrap. It calls exchange once. Before dispatch, it checks the
direct award against the original unit, team, run, job, live lease, attempt,
source generation, deadlines, engine and resources. Missing authority, a repeated
bootstrap or changed execution inputs fail before the unit host runs. Cancellation
before or after exchange also prevents dispatch. The unit host receives the complete
exchanged claim and owns renewal, source acquisition and execution.
Job construction, source initialization and bootstrap execution share one award
deadline calculation. It uses `limits.JobDeadline`, constrained by an optional
parent capability expiry. A service root without expiry does not add a deadline, including at node reservation.
The node stores the reservation's award fence and binding. Job construction
and restart reconstruction require that same award to match the Job binding. Sync submits reservations
without a Job through the same controller transport. The store proves the original
award belongs to that launcher and checks current unit authority. A stale decision
carries the exact award LeaseRef, NodeUID and ReservationID. The launcher verifies
all three, proves Job absence again, and requires container vacancy and matching
node ownership before release. Parent credential lifetime cannot keep an expired
unit's vacant reservation or authorize release of another award's reservation.
The resulting Cloud execution deadline must remain bounded. Exchange and host dispatch run under that deadline and any earlier caller
deadline. Expired awards fail before transport construction. Expiry during
exchange prevents dispatch, and expiry during hosting cancels and joins the host.
The exchanged source must match the private projection used by fetch-source,
including repository, remote, commit, pipeline directory, default branch, bundle,
checkout policy and accepted engine. A matching run and generation alone cannot
authorize another source.

For work, `runners.Hosting` captures the awarded runner, engine identity,
claim fence, accepted seals and job settings before renewal or preparation.
The node-input constructor owns those values for every preparation path and
copies nested settings. Later award mutations cannot change executor inputs,
and executor edits cannot change the award through those inputs.
Missing or inconsistent seals and missing job settings fail before renewal.
The constructor uses the acquired
entry's engine only when it matches the award and accepted spec. It supplies
the awarded runner, job, generation and attempt, including ordinal zero.
Preparation supplies the source entry, storage, delegated routes
and generator replay. `NodeHost` owns policy checks at each process start.

<!-- design:begin -->

`TestStoreLauncherReservationW8` drives the real store, controller handlers,
shared client, launcher and KubernetesBoundary with an unexpiring service root.
It checks live reservation retention, expired-unit cleanup, Job absence,
container vacancy and cleanup replay. `FuzzStoreLauncherReservationFenceW8`
checks fabricated fences, foreign launchers, newer generations and cancellation.
`FuzzControllerReservationRetirementMatchesObservation` checks exact cleanup
correlation for plan, eval and work observations. FuzzJobAndReservationShareAward
checks that a changed Job binding cannot release another award's reservation.

`TestStoreLauncherW8` constructs the launcher against the typed HTTP client,
controller handlers, SQLite issuance and persisted cleanup reader. It publishes
and activates a Job, exchanges its bootstrap, retains the Job through a running
attempt, and retires it after committed completion. Kubernetes and the Cloud
boundary remain fixtures in that test. `NewProductionBoundary` supplies live
Kubernetes canaries and reservation-scoped cleanup for the installed launcher. `FuzzStoreLauncherAuthorityW8` checks
changed, narrowed and revoked credentials, route selection and provider failure
across claims and synchronization. Refusals leave work unclaimed. Other
components retain their fake controller boundaries. Deployed egress, node
isolation, native sidecar shutdown and scratch accounting remain unproven. They
also need real images, an explicit Kubernetes context and bootstrap/host
composition from the owning areas.

The private files carry the original CloudJobAward and its source input to the
containers. All three builder argument shapes pass the shared CLI parser.
The default executable still needs the source-access, native host, runner and
launcher bindings supplied to `MainWithRuntime`. Source acquisition tests use
real Git with local fixture remotes and a fake credential release. DNS and
remote authorization are outside that proof. The native host must consume and
revalidate the initialized checkout, acquire its selected engine and install
its SDK after exchange. Plan/eval host composition remains open. Component
exchange tests do not establish a working pod entrypoint.

<!-- design:end -->

## Production boundary

The installed launcher loads the standard Kubernetes client configuration and
composes its award transport with `NewProductionBoundary`. The launcher account
can create, read and delete probe Pods, and list, create and delete NetworkPolicies
in the jobs namespace. It retains create/delete-only Secret access.

Before work starts, the boundary requires a default-deny egress policy selecting
the exact Job labels. It inventories declared DaemonSet ports and observed
kubelet endpoints. A temporary listener Pod has an explicit ingress allow rule. A separate witness
Pod must connect through a rule allowing only that listener before a denied
connection can establish egress enforcement. A separate root,
privileged canary uses the workload's labels. It checks controller connectivity,
then denies the listener and observed cluster API, DaemonSet TCP and kubelet
endpoints. It repeats the cluster endpoint checks from the node network namespace
with `nsenter`. DNS ports remain available. Declared UDP ports are inventoried,
while UDP denial needs protocol-specific native checks.

Canary results require unchanged admitted commands, images, labels, reservation
and node UID. Both listener readiness and a successful measurement Pod are
required. Policy and endpoint snapshots must match after canary cleanup. Probe
Pods inherit the award deadline. Failures preserve the reservation until cleanup
can finish. A new launcher can remove only probe Pod and policy instances bound
to that exact reservation, using Kubernetes UID preconditions. No launcher
credential or service-account token enters a canary.

The known required destination is the controller. Source, blob and provider
origins depend on deployment and still need native verification. The checks do
not exhaust every network destination or every node escape. Pod-level policy
alone cannot authorize a deployment where the escaped node network still reaches
control-plane services. Native enforcement, UDP services, image execution,
DinD shutdown and kubelet reclamation remain cluster acceptance requirements.

All authored workload state uses container writable layers and emptyDir storage.
Release waits for all non-DaemonSet Pods to disappear, then removes
reservation-owned probe state before permitting reuse. Kubernetes owns the
associated ephemeral-state reclamation. Privileged pipeline code can change
arbitrary host state under the accepted threat model. That state requires node
reprovisioning and is outside reservation-scoped Kubernetes cleanup.

## Preparation leases

Boundary probes, image startup and source checkout can take longer than a unit's
three-minute lease. The launcher keeps unexchanged awards alive using the existing
sync route's `preparing` fences and `prepared` acknowledgements. Its root credential
owns those requests. The bootstrap still grants only source read and one exchange.

`LaunchOne` checks preparation authority before reserving a node, then runs the
shared lease keeper through activation. Renewal also reconciles existing Jobs,
so a slow new reservation does not stop another Job's source checkout heartbeat.
Every return cancels and joins that keeper. Normal reconciliation keeps persisted
Jobs alive until exchange. Exchanged claims receive no launcher renewal because
the executing host owns their lease.

Preparation retains the existing award deadline, capacity and Cloud billing rules.
Shared financial functions open and checkpoint the held Cloud interval without
marking the unit running. Existing credit grace still applies. At grace expiry,
accounting commits and the original bootstrap lease ends, permitting cleanup and
refusing exchange. Finished, cancelled or expired own awards do not prevent
reconciliation of other live Jobs.

## Capacity requests

Job pods are pre-bound to a reserved node and never pend, so they cannot make
Karpenter add one. When reservation finds no vacant Ready team node, LaunchOne
creates `sw-capacity-<unit hash>`: an unbound Pod with the team node selector,
the job tolerations, the award's CPU, memory and scratch requests, the runner
image running `sparkwing-runner version`, and no volumes, environment or
service account token. Its name omits generation and holder, so re-awards and
other launcher replicas reuse it. Sync deletes it, UID-fenced, once its node is
Ready or the award deadline passes. Until then it keeps that node non-vacant.
[W8-SCALE-FROM-ZERO.md](../../../design/rewrite/W8-SCALE-FROM-ZERO.md) records
the rationale and the open consolidation and latency decisions.

## Team identity and credential rotation

`node-restriction.kubernetes.io/sparkwing-team-node` is the placement label and
team NoSchedule taint key. Enable Node authorizer and NodeRestriction. Reservation
checks the node's owning NodeClaim UID, status node name and provider ID, plus
that claim's owning NodePool UID and protected template label. Both must match
the award's team value. A second protected label,
`node-restriction.kubernetes.io/sparkwing-used-team`, records first use and is
never cleared. Neither reservation cleanup nor a NodePool template change can
move a used node to another team. No LimitRange read permission is needed.

The launcher rereads its private agent-token file for every controller request.
A missing, unreadable or empty rotated file fails the request. The launcher does
not reuse the previous token or need a restart. The file reader remains in the
transport and is never passed to Jobs.
