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
| `profiles` | Named controller connections containing `controller.url` and an optional `controller.token`. |
| `repos` | Registered checkout paths, `fallback_paths`, `auto_register` and `include_worktrees`. |
| `admission` | Machine admission `mode`, `budget`, `custom` and `jev` policy. |
| `runner` | Controller URL, `credentials_dir`, permitted `repos`, labels and local capacity preferences. |
| `cache` | Local binary-cache `max_bytes` and `max_entries`. Explicit zero disables a bound. |
| `machine` | An optional `box_id` for box-scoped concurrency. |
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
  local_admission: true
cache:
  max_bytes: 4GiB
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
values. Planning, installation and execution use them, and values of at least
12 characters are masked before processes start. Public run views show only
names and an environment digest.

On a machine that runs other people's work, the same `run.submit_env_allow`
is the operator's list of submitted names it accepts. A submitted value under
any other name is dropped before a process starts; an empty list accepts none.
Cloud Jobs and here-hosted runs accept every submittable name. Go toolchain and
network base names cannot be submitted at all.

`runner.trust` selects the runs a shared runner takes work from. Unset, the
controller decides: a runner that holds a pool with a secret reserved to it
takes only verified pushes to the repository's default branch, and any other
runner takes any run. `verified-default` and `any` override that default.

Connections come only from this machine file. A project can name a profile but
cannot define one. A controller observation uses a profile only when selected
with `--profile`. See [Controller connections](backends.md).

Enrollment saves the machine's display name in `runner.holder_prefix`.
`sparkwing-runner start --config PATH` accepts this saved metadata. The Cloud
controller supplies the runner identity and claim holder for each award.

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
