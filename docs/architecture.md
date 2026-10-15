# Architecture

Sparkwing's rewrite uses shared contracts for state, credentials, hosting, storage
and controller transport. The CLI already uses typed controller clients for run
observations, schedules, credentials and data operations. Native controller,
host, runner and storage composition still needs integration evidence. A passing
transport test does not establish a working deployment.

See [local dashboard integration](native-mode.md), [controller connections](backends.md)
and [self-hosting](self-hosting.md) for the related surfaces.

<!-- design:begin -->
## Execution model

The final component kinds are CLI, controller, runner, launcher and SDK process.
The host library is shared code. Local, triggered-local, runner, here, Actions
and launcher placements use the same run and job lifecycle.

The controller owns state, admission, the queue and the dashboard. A local
controller owns one home and one state store. Local runs stay hosted in the CLI.
Standalone CI uses the same runtime in process. A remote controller owns team
history and authorizes claims and data access.

A runner claims trigger and job units through one queue. A launcher turns
awarded plan, eval and work units into Kubernetes Jobs. The store owns lifecycle
transitions, claim fences, scheduling projections, billing and completion effects.
A host never implements another transition engine.

The SDK pin selects an exact verified engine before installation, describe,
planning or execution. The first seal captures intake. Plan acceptance stores
the second seal and validated plan together. Resume and retry consume persisted
seals. They do not reinterpret the caller's current settings.

## Storage and source

Source comes directly from its authorized Git host at the sealed SHA. An
optional source bundle can seed acquisition. Repository identity grants no fetch
authority. There is no cache service, Git mirror service or package proxy.

The controller authorizes logical records and short-lived signed `/data`
transfers. Physical bytes use filesystem or object storage. Every reference edge
is authorized. Pipeline binaries are built and cached on the executing machine.
An explicit cache profile permits only authorized verified-default-branch memo,
artifact and dependency-cache reads. Publication is a separate opt-in operation.

## Process and deployment boundaries

SDK processes negotiate protocol v2 once through hello. Engine identity, granted
runner identity and the delegated node-host bearer travel through private
protocol channels. Credentials and protocol handles do not travel in environment
variables. Process environments come from the shared process policy.

Cloud Jobs use root and privileged Docker-in-Docker on exclusive team-dedicated
nodes. Default-deny network enforcement and complete teardown require deployment
proof. Aggregate scratch includes DinD and remains bounded by the shared limit.

The supported controller topology is one replica. Persistent state and singleton
leases retain the planned replica-safe boundaries. Cross-replica live-log fan-out
and coordinated request limiting remain separate work.

## Version policy

Verified engine releases come from GitHub Releases. A signed version index carries
minimum and revocation policy. A cached verified index remains usable when a
refresh fails, with a notice. Twelve months of controller API compatibility allow
a fetched engine to keep using its matching node protocol. Age alone does not
revoke a release. See [repository upgrades](cli-upgrade.md).
<!-- design:end -->
