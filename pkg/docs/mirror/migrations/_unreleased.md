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
The removed wire message names are `admission_request`, `cancel`,
`cancel_lease`, `cancel_lease_ack`, `drain_ack`, `drain_request`, `evicted`,
`grant`, `hello`, `hello_ack`, `liveness_ack`, `liveness_probe`, `local_control`,
`local_control_status`, `local_dashboard`, `local_dashboard_ack`, `queue_state`,
`queued`, `reattach`, `release`, `set_priority`, `set_priority_ack`, `stats_reset`,
`stats_reset_ack` and `unsupported`, including all their fields.
The protocol floor rises from 1 to 4, with newest major 4.

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

## Upgrade the store in place to schema baseline 32

Schema 32 adds `local_run_secrets` to the recognized schema 31 baseline in one
transaction. Older recognized baselines run the earlier upgrades first. The
local controller encrypts requester values under the existing local secrets
key before acknowledging intake and deletes them at terminal run state.
Hosted controller secret handling is unchanged.

The local filesystem object URL signing key now persists encrypted in store
settings, so outstanding upload and download URLs survive controller restarts
until their original expiry. Preserve the existing local secrets key alongside
your database backup. Run-scoped secrets in older backups remain encrypted
after live cleanup; see [backup retention](../backup-restore.md#local-restart-secrets-and-backups).

No CLI, HTTP route, award credential or SDK surface is removed by this step.
This step alone does not make embedded job execution survive a controller
restart; the runner separation is a later change.
