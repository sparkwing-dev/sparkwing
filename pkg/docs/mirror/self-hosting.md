# Self-hosting Sparkwing

For the use cases that the former Compose example covered, choose one of these
supported paths:

- Run pipelines and the dashboard directly on one machine when you do not need
  a shared controller.
- Deploy the complete controller, dashboard, and runner stack to Kubernetes
  with the `sparkwing-full` Helm chart.

The former Docker Compose example was not a released or acceptance-tested
distribution. It depended on private image coordinates and stale controller
settings, so Sparkwing no longer presents it as an install path.

## Direct local execution

Run a pipeline on the machine where you invoke Sparkwing:

```bash
sparkwing run build
sparkwing serve start
```

This path needs no controller or Kubernetes cluster. Runs, logs, and cache
metadata stay under `~/.sparkwing/` unless the selected profile routes a
backend elsewhere. See [Local execution](local-execution.md) for profiles,
remote backends, and the distinction between running and triggering a
pipeline.

## Shared controller and dashboard

Use the [`sparkwing-full` Helm chart](../charts/sparkwing-full/README.md) for a
shared controller, dashboard, cache, logs service, and Kubernetes runner. The
chart requires Kubernetes and an explicitly compatible image set; its README
lists the required values and install command.

The repository's opt-in `k8s-e2e` pipeline exercises this deployment against
an explicit cluster and caller-supplied images. It does not create or delete a
cluster.

## Migrating from the Docker Compose example

Treat the Helm installation as a new deployment. Sparkwing provides no
in-place conversion or automatic import for the old Compose volumes. Keep
`controller-data`, `gitcache-data`, and `logs-data` until you decide whether
their history must be retained, then:

1. Deploy `sparkwing-full` with one compatible image set.
2. Recreate controller tokens and update remote profiles with the new URL.
3. Point workstation runners and webhooks at the new controller.
4. Retire the Compose stack only after the new path completes a real pipeline.

Pipelines that do not need shared history can move to direct local execution
instead.

## Add workstation capacity

After a controller is running, a Linux or macOS workstation can run
`sparkwing-runner` as a user service. With `sparkwing-runner` on PATH and a
profile naming the controller, one command does the whole enrollment:

```bash
go install github.com/sparkwing-dev/sparkwing/cmd/sparkwing-runner@latest
sparkwing cluster runners add --profile prod --name dev-laptop
```

It mints a runner token scoped to `nodes.claim`, `triggers.claim`,
`runs.state`, `secrets.read` and `logs.write`, writes
`~/.config/sparkwing/agent.yaml` at mode `0600`, installs the user service, and
prints the token prefix with the command that revokes it. The config and the
machine are checked before the mint, so a host with no `sparkwing-runner` or no
user service session fails before a credential exists; a failure after the mint
prints the live token and its revoke command. `--max-concurrent`,
`--contribution` and `--labels` set the same fields the interactive installer
asks for, `--no-service` writes the config for a machine you supervise
yourself, and an existing config is replaced only with `--force`. Retire the
machine with `sparkwing cluster runners remove --profile prod`, which stops the
service and then revokes the token.

The command needs an admin credential on the profile, because minting a token
is an admin route. A machine whose operator holds no admin token uses the
interactive installer with a token an administrator minted for them:

```bash
bash install/service-install.sh
```

GitHub Releases also contain `sparkwing-runner` binaries for amd64 and arm64
on macOS, Linux, and Windows (Windows asset names end in `.exe`). Before
registering or restarting a downloaded runner, inspect its embedded identity
without contacting the network:

```bash
sparkwing-runner version -o json --offline
```

The record names the runner binary, release version, target platform, and any
VCS provenance embedded by the Go build. The runner does not yet download or
replace its own executable; update the service binary through the same installer
or service manager you used to install it.

The installer asks for the controller URL, logs URL, API token, runner name,
and maximum concurrent jobs. It writes the existing unenrolled agent format,
which uses legacy FIFO claims with local admission enabled. Its default CPU
and memory contribution ceiling is 50%. On macOS it installs a LaunchAgent under
`~/Library/LaunchAgents/`. On Linux it installs a systemd user unit under
`~/.config/systemd/user/`.

The agent defaults `gitcache` to the controller's claim-scoped proxy. Set
`SPARKWING_GITCACHE_URL` and `SPARKWING_CACHE_TOKEN` only for a direct cache on
a trusted LAN, VPN, or tailnet. The same values are stored in the mode-0600
agent configuration.

For unattended installation, supply the same values as environment variables:

```bash
SPARKWING_CONTROLLER=https://api-sparkwing.example.com \
SPARKWING_LOGS=https://logs-sparkwing.example.com \
SPARKWING_API_TOKEN="$MY_TOKEN" \
RUNNER_NAME=dev-laptop \
MAX_CONCURRENT=2 \
SPARKWING_CONTRIBUTION='4,8gb' \
SPARKWING_LOCAL_RESERVE='1,2gb' \
bash install/service-install.sh --yes
```

`SPARKWING_CONTRIBUTION` and `SPARKWING_LOCAL_RESERVE` use the same machine
budget grammar as local admission. The contribution defaults to `50%,50%`;
the reserve defaults to empty because the contribution already retains half
the machine for other work.

A runner warms its Go module cache at startup from `--warm-modules` (or
`SPARKWING_WARM_MODULES`), a comma-separated list of module paths with an
optional `@version`; it defaults to the SDK at the runner's own version, and
`off` disables it. The warm runs in the background and a failed download only
logs, so a cold cache never blocks a claim.

The installer writes the token to `~/.config/sparkwing/agent.yaml` with mode
`0600`. The service uses that file rather than embedding the token in its
launchd plist or systemd unit. The contribution caps reported capacity; the
reserve constrains local admission. Neither enables the reservation-backed
assisted offer protocol.

Keep the config in that claim-mode shape. Adding `name` or `coordinators`
selects enrolled mode, which the controller refuses on both the claim route and
the offer route, so the agent refuses to start and names the state.
`sparkwing-runner agent --allow-enrolled-preview` starts it anyway, against the
unfinished enrolled path. See [local-execution.md](local-execution.md) for the
enrolled design.

The native Windows runner uses the same YAML and `sparkwing-runner.exe agent
--config <path>` command, but neither installer creates a Windows service. On
Windows `sparkwing cluster runners add` mints the token, writes the config, and
prints those manual steps. Supervise the agent with the service manager you
already use, or run the Linux installer inside WSL when systemd user services
are enabled.

### Operate the service

On macOS:

```bash
tail -f ~/.sparkwing/runner.log
launchctl list | grep sparkwing
launchctl unload ~/Library/LaunchAgents/com.sparkwing.runner.plist
launchctl load ~/Library/LaunchAgents/com.sparkwing.runner.plist
```

On Linux:

```bash
journalctl --user -u sparkwing-runner -f
systemctl --user status sparkwing-runner
systemctl --user stop sparkwing-runner
systemctl --user start sparkwing-runner
```

Enable lingering with `loginctl enable-linger $USER` if the Linux runner must
remain active after logout.

### Troubleshooting

**The runner returns `401 unauthorized`.** Confirm that `token:` in
`~/.config/sparkwing/agent.yaml` is a valid controller token.

**The runner does not claim work.** Confirm that the pipeline was remotely
triggered and that the runner can reach the configured controller and logs
URLs.

**A private repository cannot be cloned.** Confirm that the controller has a
cache URL, that the agent's claim is still live, and that the cache service can
reach the repository with its configured Git credentials.

**A Docker step fails.** Docker is a pipeline dependency, not a Sparkwing
service requirement. Install and start Docker only on machines assigned jobs
that invoke it.

## Bound storage growth

A controller keeps every event, every per-node metric sample and every backup
forever until an operator sets a window. Retention is off on an existing
install and stays off after an upgrade; a cloud-provisioned controller turns it
on.

Read the current policy with `GET /api/v1/storage` and write it with
`PUT /api/v1/storage/settings` (scope `admin`):

```bash
curl -sS -X PUT "$CONTROLLER/api/v1/storage/settings" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"event_retention_days":30,"node_metric_retention_days":14,
       "backup_retention_days":30,"database_alarm_bytes":8589934592,
       "default_tier":"free"}'
```

Every window is a whole number of days and zero is unbounded. The controller
compacts rows past their window on an hourly timer, never on a request.
`backup_retention_days` records the window the bucket's own lifecycle rule
enforces; set the same number on the S3 lifecycle rule for the backup prefix,
because the controller does not write or expire backup objects itself.

`database_alarm_bytes` and `backup_alarm_bytes` set the sizes worth a warning.
Passing one logs a warning and sets `database.alarm` on `GET /api/v1/health`;
neither degrades the reported status, so an alarm is a signal to read rather
than a controller that stopped working. The size comes from a timer sample:
SQLite reports its page count and Postgres the sum of `pg_total_relation_size`
over its relations, with the largest tables named in the field.

### Per-team storage quotas

`default_tier` holds every principal without a quota row of its own to a
tier's limits, and an empty default leaves them unlimited, which is what an
install that never enabled quotas reads. The free tier allows fourteen days of
retention, ten megabytes per run, a gigabyte per month, and a thousand objects
per run; the paid tier allows ninety days, a gigabyte per run, a hundred
gigabytes per month, and a hundred thousand objects per run.

Hold one team to a tier, or to limits of your own, with
`PUT /api/v1/storage/quotas/{principal}` (scope `admin`):

```bash
curl -sS -X PUT "$CONTROLLER/api/v1/storage/quotas/acme" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"tier":"paid"}'
```

Log appends, run events, and published artifact manifests count against the
team the calling token names. A write past a limit is refused with
`413` and a reason naming the limit, the team, what it has already stored, and
what the write asked for, which is what the CLI prints. `GET /api/v1/storage`
shows a caller its own quota and the month's usage, and shows an admin the
database sample and the largest teams of the month.
