# Migrating to the next release

## Upgrade the store in place to schema baseline 31

The local controller stores machine-admission requests, queue arrivals, leases
and retry receipts in `state.db`. Queued work keeps its place across controller
restarts. The former `wingd/state.json` sidecar no longer supplies admission
authority. Logical concurrency reservations remain a separate store layer.

The controller upgrades a recognized schema 30 baseline in one transaction on
open. Recognized schema 28 and 29 stores first run their existing forward
upgrades. The upgrade adds `local_machine_admission` and records schema version
31 with its baseline digest. A failed migration leaves the store unchanged.
An older binary refuses the upgraded store. Back up the store before upgrading
if you need to retain the option to restore it with an older release.

Machine admission now uses HTTP on the verified local controller API socket.
The former wingwire `Hello`, `HelloAck`, `AdmissionRequest`, `Grant`, `Queued`,
`Evicted`, `Cancel`, `CancelLease`, `CancelLeaseAck`, `Release`, `Reattach`,
`LivenessProbe`, `LivenessAck`, `QueueState`, `SetPriority`, `SetPriorityAck`,
`DrainRequest`, `DrainAck`, `LocalControl`, `LocalControlStatus`, `LocalDashboard`,
`LocalDashboardAck`, `StatsReset` and `StatsResetAck` frames are removed.
Upgrade the installed CLI and each
repository's pinned pipeline CLI together:

```sh
sparkwing pipeline upgrade
```

The former top-level `upgrade` command is now `pipeline upgrade`. The obsolete
admission-sidecar recovery subcommand is removed because admission now lives
in the store.

An incompatible pinned CLI receives an error naming its version, the controller
version and this upgrade command. There is no compatibility relay.

A ticket's holder PID and process start time determine liveness. Closing a
request or losing a long poll leaves its queue arrival or lease intact. The
controller releases capacity when it observes the holder process gone, including
PID reuse with another start time. Inspection errors retain capacity. There is
no machine-admission renewal, expiry or timeout setting. Explicit release and
cancellation remain idempotent.

Job execution remains with the existing hosts. This change does not transfer
running jobs to a separate local runner or change their logical claim renewals.
