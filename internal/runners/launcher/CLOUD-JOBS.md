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

`Launcher.LaunchOne` persists a suspended Job and checks the admitted response
before creating the bootstrap Secret. Kubernetes defaults may fill unset fields.
Changes to the authored placement, containers, private volumes, resource budget,
execution policy or teardown ownership refuse activation. An injected failure
policy cannot turn an OOM failure into another pod execution.

The admitted bootstrap Secret must retain its exact bytes, immutable flag, type
and Job owner before activation. Extra data or altered ownership refuses launch
and starts teardown without reading Secrets or exposing credential bytes. A
renamed Secret's name and UID are recorded on the suspended Job before teardown.
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
Jobs. Teardown deletes the bootstrap Secret and retains the node reservation
while a Job container remains. `KubernetesBoundary.Release` also requires pod
removal and reservation-scoped cleanup before another Job can reserve the node.
If Job deletion fails, teardown still deletes its bootstrap after confirming
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

`NewControllerControl` binds the launcher's claim and sync operations to the
shared controller wire interface. Its client must use `EngineOptions.Launcher`.
It captures observed labels/capacity and requests the configured Cloud unit
kinds in one call. The server supplies team, holder, generation, resources and
the one-time bootstrap. The launcher has no caller-selected holder override.
Transport failures never trigger a second claim in the adapter.

The adapter projects plan source through `source.ForRequest`, and eval/work
through `source.ForExecution` using the accepted spec and matching engine.
Each award must include resolved CPU and memory in `ClaimedUnit.Execution`.
For eval, `Binding.Generation` fences the terminal source and
`Binding.Expansion` identifies the eval's own live lease. Both survive dispatch.

The Job's binding retains the controller unit ID. Sync sends that ID, the Job
name and reserved node UID reconstructed from Kubernetes metadata. It accepts
one live or removal decision per observed Job. Changed fences, duplicate or
missing decisions and returned credentials fail before activation or teardown.
No in-memory award map or replacement bootstrap is needed after restart.

`BootstrapHost` creates a separate controller client whose bearer callback holds
only the Job's bootstrap. It calls exchange once. Before dispatch, it checks the
direct award against the original unit, team, run, job, live lease, attempt,
source generation, deadlines, engine and resources. Missing authority, a repeated
bootstrap or changed execution inputs fail before the unit host runs. Cancellation
before or after exchange also prevents dispatch. The unit host receives the complete
exchanged claim and owns renewal, source acquisition and execution.

<!-- design:begin -->

The HTTP client, source projections, deployment network probes and node cleanup
must supply their declared interfaces. Component tests use a fake controller,
source projections and Kubernetes API. They do not prove that a deployed cluster enforces egress,
node isolation, native sidecar shutdown or scratch accounting. Those checks
also need real images, an explicit Kubernetes context and bootstrap/host
composition from the owning areas.

The bootstrap component consumes the original CloudJobAward in memory. The
container still needs a declared transport for that award and its source input.
The shared CLI registry also rejects the builder's unit/attempt/engine/source-fence
flags and requires a job for plan units. Those grammar and handoff contracts
remain open. Component exchange tests do not establish a working pod entrypoint.

<!-- design:end -->
