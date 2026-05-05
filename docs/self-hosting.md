# Self-hosting sparkwing without Kubernetes

Deploy sparkwing without Kubernetes. Two flavors:

1. **Server side** on a single host via `docker compose`. Runs the
   whole control plane (controller, logs, cache, web).
2. **Runner side** on a laptop via launchd (macOS) or systemd user
   service (Linux). Contributes compute to a shared controller.

These two together give you the "trusted team laptop fleet" deployment
target: one cheap VPS + team laptops.

> **Note:** the `docker-compose.yaml`, launchd plist template, systemd
> unit template, and `install.sh` referenced below ship as separate
> deployment assets. The paths in the snippets below assume you're
> working from a checkout of those assets alongside the `sparkwing`
> binary on PATH.

## Server side: docker-compose

```bash
cd install/docker-compose
cp .env.example .env
# edit .env — set SPARKWING_API_TOKEN, GITHUB_WEBHOOK_SECRET, and
# public hostnames
docker compose up -d
docker compose logs -f
```

You'll need a reverse proxy in front handling TLS. Caddy is recommended
for the simplest setup — see `install/docker-compose/Caddyfile.example`.
Traefik, nginx, or Cloudflare tunnels also work.

What runs:
- `controller` - queue, dispatcher, webhooks, pool management, state store.
- `sparkwing-logs` - streaming log store. Runners write, dashboard reads.
- `cache` - git server, artifact blob store, package registry proxy.
- `web` - dashboard.

All state persists in docker volumes. Backup `controller-data`,
`cache-data`, and `logs-data` to protect history.

### Where to host

Works anywhere docker-compose runs:
- **$5-10/mo VPS** (Hetzner, Vultr, Digital Ocean, Linode)
- **Fly.io** (with minor tweaks — Fly has its own TLS + networking)
- **Railway** (similar)
- **Home lab** (Raspberry Pi, old Mac, always-on desktop)
- **Tailscale + a laptop at the office** (no public IP needed)

Picking a host: you need docker, an always-on network connection,
and enough disk for log/git retention. 2GB RAM and 20GB disk cover a
small team comfortably.

## Runner side: launchd / systemd

```bash
# Make sure sparkwing-runner is on your PATH first:
go install github.com/sparkwing-dev/sparkwing/cmd/sparkwing-runner@latest

# Then run the installer:
bash install/install.sh
```

The script is interactive. It'll ask for:
- Controller URL (public URL of your team's sparkwing server)
- Logs URL (same)
- API token (from your team's sparkwing admin)
- Runner name (defaults to your hostname)
- Max concurrent jobs

On **macOS** it writes a LaunchAgent plist to `~/Library/LaunchAgents/`
and loads it. The runner starts at login and persists across sessions.

On **Linux** it writes a systemd user unit to
`~/.config/systemd/user/` and enables it. Note: if you want the
runner to keep running after you log out, enable lingering with
`loginctl enable-linger $USER` as root.

### Non-interactive install

For scripting or docs:

```bash
SPARKWING_CONTROLLER=https://api-sparkwing.example.com \
SPARKWING_LOGS=https://logs-sparkwing.example.com \
SPARKWING_API_TOKEN=$MY_TOKEN \
RUNNER_NAME=laptop-korey \
MAX_CONCURRENT=2 \
bash install/install.sh --yes
```

### Useful commands after install

**macOS:**

```bash
# view runner logs
tail -f ~/.sparkwing/runner.log

# check running state
launchctl list | grep sparkwing

# pause (runner stops claiming new jobs; existing jobs finish)
launchctl unload ~/Library/LaunchAgents/com.sparkwing.runner.plist

# resume
launchctl load ~/Library/LaunchAgents/com.sparkwing.runner.plist

# uninstall
launchctl unload ~/Library/LaunchAgents/com.sparkwing.runner.plist
rm ~/Library/LaunchAgents/com.sparkwing.runner.plist
```

**Linux:**

```bash
# view runner logs
journalctl --user -u sparkwing-runner -f

# check state
systemctl --user status sparkwing-runner

# pause
systemctl --user stop sparkwing-runner

# resume
systemctl --user start sparkwing-runner

# uninstall
systemctl --user disable --now sparkwing-runner
rm ~/.config/systemd/user/sparkwing-runner.service
```

## How the two pieces fit together

```
             GitHub webhook
                  │
                  ▼
         ┌────────────────┐
         │   (reverse     │
         │    proxy +     │     single-host deployment
         │    TLS)        │
         └────────┬───────┘
                  │
                  ▼
         sparkwing-controller
         (HMAC verification)
                  │
                  │ enqueue
                  ▼
          [pending job]
                                      │
                ┌─────────────────────┴──────────────────────┐
                │                                              │
                ▼                                              ▼
    long-polled by laptop 1                    long-polled by laptop 2
    running `sparkwing-runner                 running `sparkwing-runner
    serve --controller ...`                    serve --controller ...`
                │                                              │
                │ claims + runs                                │ claims + runs
                │                                              │
                ▼                                              ▼
    Docker Desktop on laptop 1              Docker Desktop on laptop 2
                │                                              │
                │ streams logs + status back                   │
                │ over HTTPS with bearer token                 │
                ▼                                              ▼
       sparkwing-logs  ◀──────────── sparkwing-controller ────────▶ dashboard
```

No Kubernetes. No helm. No operators. Just Go binaries on commodity
hardware.

## Troubleshooting

**Runner says "401 unauthorized" on poll.** Token mismatch. Verify the
value in `~/Library/LaunchAgents/com.sparkwing.runner.plist` (or the
systemd unit) matches what's set in the server's `.env`.

**Runner connects but never claims a job.** Check that you're actually
triggering jobs — `curl -X POST https://api.example.com/trigger?pipeline=foo`
with the bearer header. Also check `sparkwing.example.com` dashboard
for the job status.

**Pipeline build fails with "git: could not read Username for github.com".**
The runner needs SSH access to clone private repos. Make sure your
laptop has an SSH agent running with a key authorized on GitHub, and
that the LaunchAgent inherits it. On macOS this usually means running
`ssh-add ~/.ssh/id_ed25519` before the runner starts. For persistent
SSH agent across reboots, use [ssh-agent as a launchd service](https://apple.stackexchange.com/q/254468).

**Runner runs but Docker commands fail.** Ensure Docker Desktop (or
colima/rancher-desktop) is running before the runner claims a job. On
macOS, the LaunchAgent inherits the user's Docker socket.

**Logs stop arriving in the dashboard.** Check `sparkwing-logs` health
on the server and the logs hostname is reachable from the laptop. The
runner silently drops log writes on HTTP errors to avoid wedging the
job — use `curl https://logs.example.com/health` from the laptop to
confirm connectivity.
