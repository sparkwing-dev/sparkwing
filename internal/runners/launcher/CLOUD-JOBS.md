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

## Admission enforcement

Cloud Jobs run in a dedicated job namespace, `<release>-cloud-jobs` by default,
which the chart owns. Two ValidatingAdmissionPolicies built by
`AdmissionPolicies` bind with `validationActions: [Deny]`, `failurePolicy: Fail`
and a namespace selector on `kubernetes.io/metadata.name` of that namespace
only. The Pod policy covers Pod CREATE and UPDATE, `pods/ephemeralcontainers`,
`pods/resize` and CONNECT on `pods/exec`, `pods/attach` and `pods/portforward`;
the Job policy covers Job CREATE and UPDATE. Validating admission runs after
every mutating webhook, so the rules judge the final object. They take no
params: the namespace, the launcher's username, the runner and Docker digests
and the probe commands are CEL literals.

- Only the launcher creates Jobs, capacity requests (`sw-capacity-`) and
  boundary probes (`sw-boundary-`). Only
  `system:serviceaccount:kube-system:job-controller` creates Pods, each
  controlled by one `batch/v1` Job named by its `sparkwing.dev/job` label. Every
  other Pod and creator is refused. The policy names the launcher as
  `system:serviceaccount:<release namespace>:<launcher ServiceAccount>`; an
  externally managed launcher must run under exactly that identity.
- Job Pods run exactly `docker` (Docker image), `fetch-source` and `run-node`
  (runner image). Capacity requests and probes run one runner-image container,
  and each probe container runs its pinned listener, witness or measurement
  command.
- Environment names are `HOME`, `DOCKER_HOST` and the EKS region values, never
  `valueFrom` or `envFrom`. Capacity requests and probes carry none. AWS role,
  web-identity and container-credential variables are refused, as is any
  projected token volume.
- Volumes are only the Job's `scratch` emptyDir and its own bootstrap Secret.
  No hostPath, hostNetwork, hostPID, hostIPC or hostPort. No container in the
  namespace runs privileged.
- Job Pods set `hostUsers: false` and run every container as UID 0 of the
  Pod's user namespace, which maps to a high, unprivileged host UID. That keeps
  the contracted root for Cloud Jobs (`apt-get install`, the Docker socket)
  without node root. The Pod uses the RuntimeDefault seccomp profile, and the
  workload containers add no capability and set `allowPrivilegeEscalation:
  false`. Only the `docker` sidecar differs: it adds exactly the namespaced
  capabilities `SYS_ADMIN`, `NET_ADMIN` and `SYS_PTRACE`, uses the Localhost
  seccomp profile `profiles/sparkwing-dind.json`, Unconfined AppArmor and an
  Unmasked `/proc`.
- Admission compares each shape's Pod and container security contexts field
  by field with the launcher's canonical values (`exactly` in
  `admission_security.go`): any other field, including `seLinuxOptions` (no
  `super_t` or any type), `windowsOptions`, sysctls, `fsGroup` or a field a
  later API adds, is refused. Capacity requests and probes run as UID 65534
  with every capability dropped. `runtimeClassName`, `shareProcessNamespace`,
  resource claims and probe `host` fields must stay unset, as must
  `dnsConfig` and `hostAliases`; the scheduler is the default one, service
  links are off and no `system-` priority class may be named. The fields come
  from the vendored k8s.io/api, so a field a newer API server adds stays
  unpinned until that dependency moves.
- `fetch-source` and `run-node` run `sparkwing-runner` with the launcher's
  flags and fixed paths in fixed positions; only the controller, run,
  generation and job values vary, and `fetch-source` cannot become a sidecar.
- The sidecar's profile allows `unshare`, `clone` and `setns` without an
  argument filter, so a nested container can create its own user namespace,
  as Docker's default profile allows with `CAP_SYS_ADMIN`. Blocking
  `CLONE_NEWUSER` would need a real-node check that BuildKit and nested runc
  work without it; until then it stays allowed, and the nested namespace
  holds no more than the Job's own.
- The service account is `sparkwing-customer-job` with
  `automountServiceAccountToken: false`.
- Every Pod selects `node-restriction.kubernetes.io/sparkwing-team-node` equal
  to its `sparkwing.dev/team` label and tolerates that team taint. Job and probe
  Pods are bound to a node; capacity requests are not.
- Labels are limited to the launcher's keys for each shape, and Job and capacity
  Pods carry the labels that select the default-deny egress policy. Job and
  probe Pods may also carry `topology.kubernetes.io/region` and
  `topology.kubernetes.io/zone` with any value, because the API server's
  PodTopologyLabels admission copies them from the Node onto every Pod created
  with `spec.nodeName`. Job templates and capacity requests may not.
- Only AWS load-balancer readiness gates are allowed. VPC resource controller
  `vpc.amazonaws.com/pod-eni` requests and annotations pass because neither rule
  inspects them.
- On UPDATE a Pod's spec and labels are immutable, which also refuses ephemeral
  containers and in-place resize, and only the Job controller and garbage
  collector change its owner references. A Job's template never changes, only
  the launcher may change its spec, and it always runs one Pod without retries
  or a failure or success policy. Shape rules apply at CREATE, so a chart
  upgrade that changes the pinned images cannot block finalizer or annotation
  updates on running Pods.
- Exec, attach and port-forward are refused for everyone. `nodes/proxy` reaches
  the kubelet directly and bypasses admission, so grant it to no identity that
  is not a cluster administrator.

`Run` first reads its own username through a SelfSubjectReview, then refuses to
start unless both policies and bindings exist with exactly the specs
`AdmissionPolicies` builds for that username, its job namespace and images. It
repeats the check before every sync. On a mismatch it logs an error, keeps
reconciling existing Jobs and claims no work until the policies match again.
The launcher can read only those four objects.

The job namespace must hold no other NetworkPolicies, since an additive policy
would widen Job egress or ingress, and no Pod Security labels besides the
chart's privileged level.

## Award backstop

Admission cannot know an award. `LaunchOne` checks every create and patch
response for the Job and its Secret; the activated template must equal the one
built from the award, including `demandFor(award)` resources. After activation,
and for each live Job on every sync, the launcher compares the Job template's
team label, node selector, node, container set and resources with those built
from the Job's binding and reservation, and each Pod controlled by that Job UID
with the template, including the pod-level CPU and memory. Sync takes the
expected size from the template itself: the binding carries no resources, the
template matched the award at creation and admission keeps it unchanged. A Pod
the Job controller has not created yet is not an error. VPC Pod ENI resources
are ignored.

After a chart upgrade changes the pinned digests, admission refuses new Pods for
Jobs whose templates still name the old images. Sync fails such a Job's award
as soon as it sees the template on other images with no owned Pod, without
retiring the node, which never ran it. Jobs whose Pods already run continue.

A mismatch is the only rejection. Pod-list and other API failures retry on the
next sync. Rejection records `sparkwing.dev/pod-rejected` on the Job, cordons
the node and UID-fences deletion of its Karpenter NodeClaim and of the Job, then
tells the controller to fail that exact award. An unrecorded rejection deletes
nothing. The annotation and cleanup finalizer retain ownership until the
controller acknowledges failure, retirement succeeds and every container stops,
so a failed retirement cannot release capacity. Every sync repeats a pending
rejection, including after restart. Malformed ownership, deadline or bootstrap
annotations trigger UID-fenced Job deletion. Garbage collection owns its
Secrets. The node's persisted reservation still requires current award
retirement and container vacancy before cleanup.

Customer Pods cannot receive IRSA or Pod Identity credentials. Their
ServiceAccount must have no role annotation or Pod Identity association.

RBAC grants Pod and Job creation in the job namespace only to the launcher, and
admission refuses any other creator. Customer ServiceAccounts receive no API
credential or role. Team nodes carry a team-specific NoSchedule taint. Among
workload Pods only Job Pods, that team's capacity requests and boundary probes
tolerate it, but cluster DaemonSets such as `aws-node`, `ebs-csi-node` and
`kube-proxy` tolerate every taint and run privileged on team nodes. They and
Kubernetes administrators remain trusted.

## Node faults

A kubelet can rewrite its own Node's annotations, so a fault read from them is
charged to that node alone. Sync quarantines a node whose reservation in this
job namespace is malformed, disagrees with its binding, names a team other than
the node's protected team label, or carries an award the controller refuses
outright: a binding refusal, a bad request or a changed binding. Each orphaned
reservation goes to the controller in its own call, so a refusal names one
node. Quarantine cordons a team node, adds the NoSchedule taint
`sparkwing.dev/quarantined`, which NodeRestriction stops a kubelet from
removing, and records the cause in the annotation of the same name. One
UID-fenced JSON patch writes all three. Reservation skips a node with the
taint or the annotation, even after it is uncordoned. No NodeClaim is deleted:
an operator inspects the node, then deletes it or removes the taint and
annotation.

One pass quarantines or keeps quarantined at most three faulted nodes. A
fourth means the controller, not a node, is likely at fault, so sync stops
claims with an error naming the node and asking an operator to check the
controller and the quarantined nodes.

A quarantine that has not persisted stops claims, so a 503 or any other failed
write blocks until a later pass writes it. A conflict is retried with backoff;
repeated conflicts, which a kubelet rewriting its own Node causes, become a
node fault and claiming continues, because placement already skips a node that
records a reservation other than the one being placed. Release retries
conflicts the same way and charges persistent ones to the node, which stays
reserved.

Release and Retire leave a node untouched when it was replaced or no longer
holds the expected reservation, because it may hold another award. The Job keeps
its cleanup finalizer and sync retries it. Rejection still fails the award.
These node faults are logged at error level on every pass while claiming
continues. Admission mismatch, Kubernetes and controller API failures and every
other reconciliation error still stop claims.

The launcher's ClusterRole grants `update` and `patch` on every Node, and RBAC cannot
narrow it. `resourceNames` needs names known at install, while Karpenter names
nodes at launch, and RBAC has no label selectors or field restrictions. A
ValidatingAdmissionPolicy on Node UPDATE by the launcher identity could confine
it to team nodes and the reservation keys, taint, used-team label and cordon.
That policy is not built.

Sync skips nodes reserved under another job namespace and logs each one. When
moving an install to a new job namespace, first let Jobs and reservations
recorded under the old namespace drain, or delete those Jobs and remove the
`sparkwing.dev/node-reservation`, `sparkwing.dev/reservation-namespace` and
`sparkwing.dev/award-binding` annotations and the `sparkwing.dev/cloud-reserved`
taint from their nodes by hand.

## Launch and reconciliation

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

The builder uses a native DinD sidecar, fixed aggregate 20 GiB scratch, a
separate customer service account and no API-token automount. The sidecar runs
the `docker:<version>-dind` entrypoint with `dockerd`, which adds no TCP
listener. dockerd owns the Pod's network namespace and, on nodes whose runc
mounts cgroups writable for user-namespaced Pods, the Pod's cgroup, so
container networks and `docker run --memory` work. `SYS_PTRACE` lets dockerd
join the network namespace of a container that runs as a non-root user. The
sidecar has no probe: `run-node`, as the workload's own identity, waits until
`/_ping` on `DOCKER_HOST` answers before it constructs the host, for at most
three minutes, so a socket
the workload cannot open never counts as ready. The bootstrap Secret is mode
0400. Node requirements are in the chart README. Its
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
in the jobs namespace. It retains create/delete-only Secret access. It can list
EndpointSlices in `default` to find the API server's addresses.

Before work starts, the boundary requires default-deny egress and ingress
policies selecting the exact Job labels. It inventories declared DaemonSet ports
and observed kubelet endpoints. Each probe role has its own name prefix, `sw-boundary-<role>-<random>`. The
EKS VPC CNI network-policy agent shares one policy map among Pods in a
namespace whose names match up to the last `-`, so probes named only
`sw-boundary-<random>` gave the peer the control's allow-all ingress and every
probe failed. Customer Pods in one namespace named `<prefix>-<suffix>` share
policy the same way; Job Pods are named after their Job, so no two share.

Two listener Pods run on the reserved node, each readiness-confirmed
before any connection is judged. The control listener has an explicit ingress
allow rule. The peer listener carries a Job's labels, so only the Job ingress
policy governs it; its readiness check runs on loopback, which no policy
filters. A witness Pod whose temporary policy allows egress to both must reach
the control, which proves its egress and the policy engine, and must fail to
reach the peer. Because the peer is listening and the witness may send to it,
that failure measures Job ingress denial apart from source egress.

A separate canary uses the workload's labels and runs as UID 65534 with every
capability dropped. It must reach the controller, and must be refused by the
control listener, the in-cluster API address, every address in the
`default/kubernetes` EndpointSlices, the instance metadata service
(169.254.169.254 and fd00:ec2::254), the Pod Identity agent (169.254.170.23),
DaemonSet TCP ports and kubelet endpoints. That proves Job egress denial,
including against an operator egress rule that drops the metadata exception.

All of it runs in the Pod network namespace. Job Pods hold no node root, so
Job code cannot reach the node network namespace, and the canaries need no
privilege or hostPID to measure what Job code can reach. The kubelet still
reaches the API from the node; node credentials and authorization protect it
there. DNS ports remain available. Declared UDP ports are inventoried, while
UDP denial needs protocol-specific native checks. The runner image needs `nc`.

Probe Pods carry the team label, node selector and team toleration admission
requires. Canary results require unchanged admitted commands, images, labels apart
from the two node topology labels, annotations, reservation and node. Both listener readiness and a successful measurement Pod are
required. Policy and endpoint snapshots must match after canary cleanup. Probe
Pods inherit the award deadline. Failures preserve the reservation until cleanup
can finish. A new launcher can remove only probe Pod and policy instances bound
to that exact reservation, using Kubernetes UID preconditions. No launcher
credential or service-account token enters a canary.

The known required destination is the controller. Source, blob and provider
origins depend on deployment and still need native verification. The checks do
not exhaust every network destination. A kernel escape from the user namespace
is outside them. The sidecar's seccomp profile stacks over every container the
customer starts, so it, not the customer's own `--security-opt`, bounds the
syscalls reachable as namespace root; nf_tables through `NET_ADMIN` remains
reachable, so kernel patch latency is the control there. Native enforcement, UDP services, image execution,
DinD shutdown and kubelet reclamation remain cluster acceptance requirements.

All authored workload state uses container writable layers and emptyDir storage.
Release waits for all non-DaemonSet Pods to disappear, then removes
reservation-owned probe state before permitting reuse. Kubernetes owns the
associated ephemeral-state reclamation. Job code holds no node root, so it cannot
change host state outside its Pod; node reuse within a team keeps that
assumption.

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
Karpenter add one. When reservation finds no vacant Ready team node, the
launcher keeps the award and creates `sw-capacity-<award hash>`: an unbound Pod
with the team node selector, the job tolerations, the award's CPU, memory and
scratch requests, the runner image running `sparkwing-runner version`, and no
volumes, environment or service account token. The Pod records its unit fence
and size in `sparkwing.dev/capacity-demand`, never the bootstrap credential.

- The hash covers the award generation, so a re-award creates its own request
  and never rebinds or deletes another generation's. Only the same award adopts
  an existing request.
- Admission enforces the request's shape. The launcher compares its award
  fields (name, demand record, team placement, image and resources) with demand
  taken from outside the Pod: the award on create and adoption, the held award
  on every sync, and the controller's binding and committed allocation on every
  reconciliation. A mismatch or an unknown allocation deletes it, UID-fenced,
  and a held award requests again.
- Each launcher pass retries held awards. Reservation may consume the team's
  own requests on a node: it persists the reservation and its taint first, then
  deletes them and waits for their removal. Boundary probe Pods then occupy
  the node during measurement and are removed before Job creation. The NodePool's
  consolidateAfter covers the empty gaps around those probes.
- Sync observes each request through its recorded award. Awards this launcher
  holds are renewed by that observation; others are only checked. A request
  is deleted when the controller ends its award or the award deadline passes.
  Requests are reconciled individually. A definitive binding or fence refusal
  deletes only the refused Pod by UID and lets other requests and claims proceed.
  Transport, overload and service-credential errors retain the request for retry.

Team NodePools need `disruption: {consolidationPolicy: WhenEmpty,
consolidateAfter: 10m}` or longer, above the award lease, so a node emptied by
a launcher restart survives until its unit is re-awarded.
[W8-SCALE-FROM-ZERO.md](../../../design/rewrite/W8-SCALE-FROM-ZERO.md) records
the rationale.

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
