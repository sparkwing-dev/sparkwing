# Machine settings: config.yaml

Machine settings live in `$XDG_CONFIG_HOME/sparkwing/config.yaml`, or
`~/.config/sparkwing/config.yaml` when `XDG_CONFIG_HOME` is absent.
`SPARKWING_CONFIG` selects another file. A present empty value is invalid.
`SPARKWING_HOME` selects runtime state and does not move machine settings.

Project declarations live in `.sparkwing/sparkwing.yaml` instead.
See [Config reference](config-reference.md).

## Sections

| Section | Holds |
| --- | --- |
| `profiles` | Named controller connections containing `controller.url`, an optional `controller.token`, and `default: true` to select an optional-profile CLI connection. |
| `repos` | Registered checkout paths, `fallback_paths`, `auto_register` and `include_worktrees`. |
| `admission` | Machine admission `mode`, `budget` and `custom` policy. |
| `dashboard` | The local dashboard's loopback `address` and `read_only` switch. |
| `runner` | Controller URL, `credentials_dir`, permitted `repos`, labels and local capacity preferences. |
| `cache` | Local binary-cache `max_bytes` and `max_entries`. Explicit zero disables a bound. |
| `machine` | An optional `box_id` for standalone machine admission. |
| `debug` | `pause_timeout`. |
| `run` | `submit_env_allow` and the opt-in `cache_from` controller profile. |

The shared decoder validates the complete file before a section is used.
Unknown keys, duplicate keys, explicit null and additional YAML documents fail
with file, key and line information. A reader never accepts another section's
invalid settings. Omission remains distinct from an explicit empty, false or
zero value where the setting permits it.

```yaml
profiles:
  prod:
    controller:
      url: https://api.sparkwing.example
      token: swk_...
repos:
  repos:
    - path: /srv/code/app
  fallback_paths:
    - ~/code
  include_worktrees: true
admission:
  mode: auto
  budget: 50%,ignore-external
runner:
  controller: https://api.sparkwing.example
  credentials_dir: /srv/sparkwing/credentials
  repos: [github.com/acme/app]
  max_concurrent: 2
  holder_prefix: desk
  labels: [host=desk, gpu]
  local_admission: true
cache:
  max_bytes: 4GiB
dashboard:
  address: 127.0.0.1:4343
machine:
  box_id: rack-3-host-7
run:
  submit_env_allow: [BUILD_REGION, BUILD_*]
```

`pipeline trigger` and Cloud-routed `run` capture only present variables matching
`run.submit_env_allow`. Empty values remain present. Literal names and terminal
`_*` prefixes are supported; a prefix needs at least two characters and must
start with a letter, so `_*` and `A_*` are refused as too broad. Credential-shaped and engine-owned names are
excluded. The controller encrypts the captured values separately from the run
spec and delivers them only to live execution awards. Retries keep the same
values. Planning and execution use them, and values of at least
12 characters are masked before processes start. Public run views show only
names and an environment digest.

On a machine that runs other people's work, the same `run.submit_env_allow`
is the operator's list of submitted names it accepts. A submitted value under
any other name is dropped before a process starts; an empty list accepts none.
Cloud Jobs, here-hosted runs and the requester's own personal machines accept every submittable name. Go toolchain and
network base names cannot be submitted at all.

Connections come only from this machine file. A project can name a profile but
cannot define one. A controller command uses an explicit `--profile` first, then a saved default
when its profile flag is optional, then the local controller. Use `--profile=`
to select local for one invocation. See [Controller connections](backends.md).

Enrollment saves the machine's display name in `runner.holder_prefix`.
`sparkwing-runner start --config PATH` accepts this saved metadata. The Cloud
controller supplies the runner identity and claim holder for each award.
Controller-backed concurrency uses this persisted machine identity for box
scope, independently of `machine.box_id` and `runner.local_admission`. A local
controller uses its persisted local peer identity in its private store.

## Admission budget

`admission.budget` limits the share of this machine that local admission
grants: a core count (`8`) or a percentage (`50%`), optionally followed by a memory
limit. By default admission grants
the whole machine less a 20% reserve and less the CPU and memory other
programs are using. Adding `ignore-external` stops subtracting other
programs' load, which suits a machine that runs only Sparkwing work. CPU used
by a job's own processes is never external; see [Local admission](admission.md#external-load). The
daemon reads the budget at start, so restart it after a change.

## Dashboard

The shared local controller serves the dashboard whenever it runs.
`dashboard.address` is the loopback IP and port it binds, `127.0.0.1:4343` by
default. A non-loopback or host-name bind address is refused. A browser on
another machine can use an SSH port forward, for example
`ssh -N -L 4343:127.0.0.1:4343 user@host`, or Tailscale Serve as described below.
Port `0` picks a free port, which suits an isolated
`SPARKWING_HOME` that would otherwise contend for the default; `sparkwing
dashboard status` prints the bound address. `dashboard.read_only: true` refuses
browser writes while still allowing signed-in reads, including decrypted
secrets, and sign-out. The controller reads this section when it starts, so run
`sparkwing daemon restart --force` after a change.

### Tailscale Serve

Keep the dashboard bound to loopback and add the machine's exact Tailscale DNS
name to `dashboard.allowed_hosts`. Replace `dashboard.example.com` below with
that Tailscale DNS name:

```yaml
dashboard:
  address: 127.0.0.1:4343
  allowed_hosts: [dashboard.example.com]
```

Entries are DNS names without a scheme, port, path, trailing dot or wildcard.
Loopback names and IP addresses are refused. Restart the controller, then run
these commands on the machine serving the dashboard:

```bash
sparkwing daemon restart --force
tailscale serve --bg 4343
sparkwing dashboard open --host dashboard.example.com
```

Open the printed HTTPS link from a device on the tailnet. Serve terminates TLS
and preserves the browser's Host and Origin. Sparkwing derives HTTPS from the
configured host, ignores forwarded identity headers, and requires the socket-minted
sign-in code. Sessions stay bound to that exact origin, so a loopback session
does not authorize the Serve address. `dashboard.read_only` applies to both.
The signed-out static shell is visible to anyone who can reach the Serve URL;
API data still requires a session.

Use Serve rather than Funnel. Serve the dashboard alone at that HTTPS name on
port 443: any other app served under the same name, including another path, shares
the browser origin and can read the dashboard session. `tailscale serve --bg 4343`
takes over `/` on port 443, replacing an existing root mapping. List only names
you control, typically your MagicDNS name: whoever controls a listed name's DNS
and certificate controls its origin. See the
[Serve guide](https://tailscale.com/docs/features/tailscale-serve).

## Runner labels

`runner.labels` lists custom labels this machine reports for job placement,
such as `host=desk` or `gpu`. They apply to everything the machine runs: work
an enrolled runner claims from a controller, and the machine's own local
`sparkwing run` work. A job with `Requires("host=desk")` runs locally on a
machine whose configuration lists `host=desk`, and waits on any other machine
with a status detail naming the missing label.

Labels are matching preferences the machine reports about itself. They never
grant pools, reserved secrets, trust or repository scope; those come from the
machine's credentials and the team's access rules. A label that names a granted
fact (`name=`, `class=`, `location=`, `team=`, `pool=` or `local`) has no
effect: matching uses the granted value instead. The daemon logs a warning for
each one when it starts, and `sparkwing doctor` reports it with its file and
line. Validation refuses a blank label and the `sparkwing-` prefix reserved for
Sparkwing Cloud. `os=` and `arch=` labels are accepted but never override the
platform the machine observes.

The local controller reads `runner.labels` when it starts. After changing them,
run `sparkwing daemon restart --force` so local runs report the new labels.
`sparkwing runners list` shows the labels the running daemon read. An
enrolled runner reads them when its service starts.

## Permissions and writes

The file carries credentials, so reads require an owner-only regular file.
Unix uses mode `0600`. Windows checks the owning SID and DACL. Symlinks and
reparse points are refused.

Settings writers lock `config.yaml.lock`, reread the file, change their section
and atomically replace the file with an owner-only copy. Other sections and
their comments survive. A write that would leave a dangling YAML alias fails.
Profile mutations also preserve changes to other profiles made during a
controller probe or credential revocation.

A command running under a separate `SPARKWING_HOME` refuses to write the
machine's settings. Set `SPARKWING_CONFIG` to a file inside that home when
testing an isolated runtime.

## Local secrets

Secret values are separate from the machine settings file.
Use [Secrets](cli-secrets.md) for credential storage and rotation.

## Moving from the per-file settings

There is no automatic import or compatibility reader. Move settings into the
final sections above. Remove old `agent`, `logs`, `fleet`, `allow_repos`,
`dispatch_wait` and profile backend keys. Runner enrollment uses
`sparkwing runners add` and stores its credential in a private file under
`runner.credentials_dir`.

Enrollment leaves an omitted `--max-concurrent` unset in the file. The shared
runner settings resolver defaults it to one. An explicit positive value is
saved as supplied. Enrollment and removal validate the complete machine file.
Removal reads `agent-token` as an owner-only regular file and checks its
controller and runner selectors before stopping the service or revoking authority.

See [the migration guide](migrations/_unreleased.md#machine-connection-profiles)
for controller-profile changes.
