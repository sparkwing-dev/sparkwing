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
sparkwing dashboard start
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
`sparkwing-runner` as a user service. From a Sparkwing source checkout:

```bash
go install github.com/sparkwing-dev/sparkwing/cmd/sparkwing-runner@latest
bash install/install.sh
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
bash install/install.sh --yes
```

`SPARKWING_CONTRIBUTION` and `SPARKWING_LOCAL_RESERVE` use the same machine
budget grammar as local admission. The contribution defaults to `50%,50%`;
the reserve defaults to empty because the contribution already retains half
the machine for other work.

The installer writes the token to `~/.config/sparkwing/agent.yaml` with mode
`0600`. The service uses that file rather than embedding the token in its
launchd plist or systemd unit. The contribution caps reported capacity; the
reserve constrains local admission. Neither enables the reservation-backed
assisted offer protocol.

The native Windows runner uses the same YAML and `sparkwing-runner.exe agent
--config <path>` command, but the bundled installer does not create a Windows
service. Supervise it with the service manager you already use, or run the
Linux installer inside WSL when systemd user services are enabled.

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
