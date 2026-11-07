# Migrating to the next release

## Parse command output with Capture

Hosts on Linux and macOS now run SDK commands themselves. A command run with `Run`
writes its output straight to the masked job log, and its result keeps only the last
64 KiB of stdout and of stderr, enough for error messages. Code that reads
`Run`'s `Stdout` or `stdout` must switch to `Capture`, which returns every byte and
writes nothing to the log:

```go
res, err := sparkwing.Exec(ctx, "go", "list", "./...").Capture()
```

```ts
const res = await ctx.exec(["go", "list", "./..."]).capture();
```

`String`, `Lines`, `JSON` and `MustBeEmpty` (`string()`, `lines()`, `json()` and
`mustBeEmpty()`) already capture and need no change.

## Local stores are copied before schema upgrades

A binary that upgrades a local SQLite store first copies it beside the store as
`state.db.schema-<N>-<YYYYMMDDTHHMMSSZ>.bak`, where N is the schema version it
upgrades from. Delete old copies yourself when you no longer need to return to
an older binary; they hold the same encrypted secrets as the store. A failed
copy refuses the upgrade and leaves the store unchanged. PostgreSQL stores still
need an operator snapshot before upgrading. See
[Schema compatibility](../backup-restore.md#schema-compatibility).

## Upgrade the store in place to schema baseline 34

Schema 34 adds the partial index `idx_runs_live_children` on pending and
running child runs to the recognized schema 33 baseline in one transaction. The
controller's recovery sweep reads it to cancel children whose parent run or
creating job has ended, without scanning retained run history. Earlier
recognized baselines run their existing upgrades first. Older binaries refuse
the upgraded store. Retain a backup before upgrading if you need to return to
an older binary.

No CLI, HTTP route, wire field or SDK symbol changes.

## Upgrade the store in place to schema baseline 33

Schema 33 adds `local_runner_instances` to the recognized schema 32 baseline in
one transaction. It records each runner's claim credential prefix, PID, process
birth, version, executable SHA-256 and registered, active, draining or stopped
state. Earlier recognized baselines run their existing upgrades first. Older
binaries refuse the upgraded store. The upgrade copies a SQLite store first;
see [Local stores are copied before schema upgrades](#local-stores-are-copied-before-schema-upgrades).

Verified socket peers use `POST /api/v1/local/runners` to register, and
`GET /api/v1/local/runners/{id}` to read an instance. The optional
`wait_state=active` query waits up to 20 seconds for a change. `POST` actions at
`/api/v1/local/runners/{id}/activate`, `/api/v1/local/runners/{id}/drain` and
`/api/v1/local/runners/{id}/stop` control its lifecycle. These are private socket
routes. Browser cookies and remote credentials cannot call them.

A registered successor cannot claim until activation. Activation drains the
previous instance and enables the successor under the same store lock as claim
arbitration. Draining preserves the old instance's held awards through uploads
and terminal acknowledgement. The machine placement identity remains stable
across process instances. A separate watch drains a runner even when all host
slots are occupied. Stopped records retire with their ended credentials.
New local awards retain the nearest registered runner in their credential
ancestry. Draining fences owner self-claims as well as queue polls; held owners
keep awaiting children while a successor claims newly ready dependencies.
Held renewals, reports, logs and child intake remain available. Earlier
root-parented awards retain their existing ancestry.

No existing HTTP route, wire field or SDK symbol is removed. Linux and macOS
queues run in an independent runner. The controller reuses matching live process
birth, version and executable digest, or starts a successor and atomically drains
the previous instance. Each runner uses a digest-pinned executable and independent
stdio. Systemd hosts require a working user manager; a missing manager is an
error. Confirmed no-systemd Linux hosts use a separate session. macOS uses a
per-instance launchd agent. No drain deadline kills held work. Dependencies
keep the exact accepted development executable from retained builds, with native
platform, embedded version and complete digest checked again before use. Windows queued
hosting retains its existing process lifetime.

`GET /api/v1/local/runners?state=active` returns the verified socket account's
current active instance, or `not_found` when none is active. A successor must
verify that the recorded process birth and build still match before reuse.
No existing route or wire symbol is removed by this discovery addition.

### Foreground local execution

Foreground local runs without publication execute in the local runner. The CLI
submits and observes persisted logs and the terminal outcome over HTTP. Its
captured environment and exact selected development engine remain the execution
inputs. Status and followed logs reconnect through controller read outages,
retaining the log cursor. Permission, protocol and integrity refusals remain
errors. First interrupt requests durable cancellation; a second stops observation.
An interrupt during successful intake keeps that same cancellation sequence.

Foreground publication retains CLI hosting so its existing publication deadline
and visible failure warning complete before return. `--sw-allow-unadmitted`
retains its owned in-process controller and CLI host. Remote `--runner here`
also stays CLI-hosted. No flag, route or wire symbol is removed by this piece;
`LocalForeground` and `local_foreground` still reserve these CLI-hosted runs.

## Local queue claims use HTTP

The local queue obtains its credential through the authenticated API socket and
uses the shared HTTP claim client. Verified socket peers may request exactly
`triggers.claim` and `nodes.claim` from `POST /api/v1/tokens`. Other member-token
requests remain refused. That claim token expires within seven days: an omitted
`expires_at` defaults to seven days and a later one is refused with a bad-request
error. A runner drains after one day and the supervisor starts a fresh instance
with a new token, so a job running longer than about six days on a local runner
loses its claim authority. The queue revokes its delegate after hosts join.
Detached runner cleanup uses the API independently of the controller's shutdown
lifetime. Captured account and label policy holds no store handle or credential.

No HTTP route, wire field or SDK symbol is removed. Detached queue supervision
is described in the schema 33 section above. Foreground/here execution transfer
remains separate work.

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
On the wire these are the `admission_request`, `grant`, `evicted`, `cancel`,
`cancel_lease`, `cancel_lease_ack`, `reattach`, `liveness_probe`, `liveness_ack`,
`queue_state`, `set_priority`, `set_priority_ack`, `drain_request`, `drain_ack`,
`hello`, `hello_ack`, `local_control`, `local_control_status`, `local_dashboard`,
`local_dashboard_ack`, `stats_reset`, `stats_reset_ack` and `unsupported` messages.
The daemon protocol floor raised 1 -> 4 (newest major 4), so a CLI that speaks
only an older major is refused with upgrade guidance.
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

An enrolled `sparkwing-runner` with `runner.local_admission: true` uses the same
machine admission, so it must match the controller's build too. An older runner
fails each claimed job with an error naming both versions; restart its service
after updating so it moves to the latest release. When the CLI runs an
unreleased build that no runner release matches, set `local_admission: false`
under `runner:` in the runner's config and restart the runner until a matching
release ships.

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

## Local controllers remain running while idle

The local controller starts on demand and remains running while idle. Use
`sparkwing daemon stop` to stop it. Managed login services retain their existing
restart-on-failure policy.

Remove `--stay` from `sparkwing dashboard start` and `sparkwing wingd run`
invocations. Remove it from existing systemd user and launchd unit files before
reinstalling or updating Sparkwing, including units generated by an older
Sparkwing release. Unit management refuses the removed argument. Windows users can run
`sparkwing wingd run --home <absolute-home>` in the foreground.

The hard cut removes `POST /api/v1/local/controller/stay`, `LocalControl.stay`,
`LocalControlStatus.stay`, `LocalControlStatus.idle_since` and daemon status
`stay`. Clients should remove stay mutations and stop reading those status
fields. The four activity counts remain status
observations. Dashboard activation and its remembered address retain their
existing behavior.

## Observed job exit time

Award-bound node transitions accept the optional `X-Sparkwing-Exited-At`
header, formatted as RFC3339Nano, on `report` and `retry` commands. Runners record
it after joining the execution process and before final log acknowledgement,
cleanup and artifact/cache publication. The controller stores it as the job and
attempt finish time even when reporting resumes after a restart.

It clamps observations to report receipt and, when clocks permit, the attempt
start to handle clock skew. Reports that omit the header retain receipt-time
closure. Older controllers ignore the header and still accept the unchanged
command body. Exit observations do not change result replay identity. Event
times, lease checks and other authority decisions still use the controller
clock. No route, credential, SDK surface or existing payload field is removed.

## Local job process cleanup

The local controller retains recorded job roots until their execution sessions
are empty, including roots on released or cancelled admission tickets. On Unix,
confirmed holder death triggers identity-checked session cleanup. A controller
restart retries cleanup from the stored root PID and process start time.
Inspection failures retain the root and its reservation for another check.

Cancelling a machine lease with a registered job root wakes its watch immediately
and retains its capacity while the host stops and joins the job. The host's
explicit release frees that capacity. Foreground cancellation keeps waiting for
the run's terminal result.

## Route an invocation directly to Cloud

`--sw-route cloud --profile NAME` on `sparkwing run` is additive. It uses the
existing controller-dispatched run and source-upload contracts. No wire field,
API endpoint or SDK symbol is removed or renamed. `--profile` remains refused
without `--sw-route cloud`. `--sw-route local` overrides machine routing for one
run. `--allow-secret-file PATH` also requires `--sw-route cloud`. Repository
local restrictions remain binding.

Explicit Cloud runs can use `--sw-detached` and need no attached here host.
They never fall back locally. Interrupting log observation leaves them running.
They use team Cloud credits rather than machine burst overflow allowances.
Requester secrets keep the existing volatile controller-memory lifetime, but
there is no here host to resupply them after a controller restart. Submit a new
run if those inputs are lost. Machine-configured burst routing keeps its
foreground owner, preference holds and pre-intake local fallback.

This repository's land pipeline accepts `-- --cloud-profile NAME` to wait for a
full Cloud gate, including race tests. Each job fetches the preconditions
comparison commit through this repository's `gate --base SHA` input, so policy
and lint judge the branch diff despite the parentless uploaded snapshot. Review and push remain local. Its
`gate_run` output names the Cloud gate's run rather than the local parent when
that option is used.

## GitHub trigger repository identity

`POST /api/v1/repositories/resolve` accepts `{"remote_url":"git@github.com:owner/repo.git"}`
and returns the canonical repository `key` and `display`. It requires `runs.read`
or `runs.write` and respects the credential's repository scope. The controller
resolves GitHub identities through the authenticated team's App installation or
configured source credentials. App credentials remain on the controller.

For GitHub identity lookup without an explicit local GitHub token, upgrade the
controller before the CLI. This applies to pipeline triggers, Cloud-routed runs
and profile cache identity. Explicit `GH_TOKEN` or `GITHUB_TOKEN` still supports identity
lookup when the controller cannot resolve it, including older controllers and
repository-scoped HTTPS source credentials. Without an explicit token, the CLI
returns the controller's error and makes no anonymous GitHub request.
No route, wire field or SDK surface is removed.
