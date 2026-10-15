# Cloud Jobs

`BuildJob` builds plan, eval and work Jobs from an awarded binding. It checks
images, resources, deadlines and engine identity before reserving an exclusive
team node. `CloudBoundary` must return matching placement and network evidence.
A failed boundary check releases its reservation and returns no runnable object.

Requested CPU and memory above the operator ceilings fail with both values in
the error. The builder never clamps an award. CPU uses Kubernetes decimal
quantities and rounds up to a millicore. If that quantity would exceed the
ceiling, the builder refuses it before reserving a node. Exact values such as
2.007 cores stay exact through conversion. The pod shares its awarded CPU and
memory across the host and Docker sidecar.

`Launcher.LaunchOne` persists a suspended Job before creating the bootstrap
Secret, then activates it. `Sync` reconstructs ownership from persisted Job and
node metadata. It adopts live Jobs and retires cancelled, expired or terminal
Jobs. Teardown deletes the bootstrap Secret and retains the node reservation
while a Job container remains. `KubernetesBoundary.Release` also requires pod
removal and reservation-scoped cleanup before another Job can reserve the node.

The builder uses a readiness-gated native DinD sidecar, fixed aggregate 20 GiB
scratch, a separate customer service account and no API-token automount. Its
bootstrap mount is absent from Docker. Pipeline execution starts after source
acquisition. Plan resolves the exact SDK pin. Eval and work use the accepted
engine identity.

<!-- design:begin -->

The controller claim/sync adapter, deployment network probes and node cleanup
must supply their declared interfaces. Component tests use a fake controller
and Kubernetes API. They do not prove that a deployed cluster enforces egress,
node isolation, native sidecar shutdown or scratch accounting. Those checks
also need real images, an explicit Kubernetes context and bootstrap/host
composition from the owning areas.

<!-- design:end -->
