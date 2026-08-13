<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing configure

Every `sparkwing configure` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing configure`

Configure laptop-local settings

Laptop-local setup commands. 'init' is the one-shot
"prepare ~/.config/sparkwing/ + report what's there" command;
'profiles' manages remote-cluster connection profiles. Future
laptop-level surfaces (aliases, default flags, per-repo config)
land here.

Controller-side state (users, tokens) lives under
'sparkwing cluster ...' since it writes to the remote
controller, not the local config. Secrets are top-level
('sparkwing secrets ...').

### Subcommands

- `init` -- Set up ~/.config/sparkwing/ and report laptop-level config status
- `profiles` -- Manage connection profiles for remote controllers
- `xrepo` -- Manage the laptop-local repo registry

### Examples

```sh
# First-time laptop setup
sparkwing configure init

# Status of laptop config
sparkwing configure init -o json

# List profiles
sparkwing configure profiles list

# Add a new profile
sparkwing configure profiles add --name prod --controller https://api.sparkwing.example --token $TOKEN

# Register the current repo with the cross-repo registry
sparkwing configure xrepo add
```

## `sparkwing configure init`

Set up ~/.config/sparkwing/ and report laptop-level config status

Idempotent setup + status command for laptop-level
sparkwing config. Creates ~/.config/sparkwing/ if it doesn't exist,
then reports which config files are present (profiles.yaml,
repos.yaml, secrets.env), the running CLI + Go toolchain version,
and a curated list of next-step commands.

Pairs with the per-project flow: use this one on a fresh laptop
after install, then run 'sparkwing pipeline new --name <name>'
inside each project to scaffold .sparkwing/ + your first pipeline
in one step (no separate init needed).

Re-running on an already-set-up laptop is a no-op status report.
--dry-run skips the mkdir so the command pure-probes.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `--dry-run` | Probe + report without creating ~/.config/sparkwing/ |

### Examples

```sh
# First-time laptop setup
sparkwing configure init

# Status of laptop config (agent-readable)
sparkwing configure init -o json

# Probe without writing anything
sparkwing configure init --dry-run
```

## `sparkwing configure profiles`

Manage connection profiles for remote controllers

Profile config lives at $SPARKWING_PROFILES (if set), else
$XDG_CONFIG_HOME/sparkwing/profiles.yaml, else
~/.config/sparkwing/profiles.yaml. Permissions on save are 0600.

Every human-driven client command (tokens, users, jobs
retry/cancel/prune/logs, gc) reads connection info from the
selected profile via --profile NAME. No --controller/--token flags
exist on other commands; profiles are the only config surface.

### Subcommands

- `add` -- Register a new connection profile
- `list` -- Print every registered profile
- `show` -- Print one profile's full config
- `use` -- Set the default profile
- `remove` -- Delete a profile
- `duplicate` -- Copy one profile's config into another
- `set` -- Update fields on an existing profile
- `test` -- Probe controller/auth/logs/gitcache for one profile

## `sparkwing configure profiles add`

Register a new connection profile

Creates a new entry in profiles.yaml. --name and --controller
are mandatory; the rest are optional. When this is the first
profile registered, it's auto-set as the default.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Profile name (unique per profiles.yaml) (required) |
| `--controller URL` | Controller base URL (required) |
| `--logs URL` | Logs-service base URL |
| `--token TOKEN` | Bearer token (omit for local/unauthed stacks) |
| `--gitcache URL` | gitcache URL (fleet-worker uses this) |
| `--default-runner NAME` | Runner name to pick when a job's Prefers don't match and several runners satisfy Requires (omit for local) |
| `--default` | Set this profile as the default |

### Examples

```sh
# Add a prod profile
sparkwing configure profiles add --name prod --controller https://api.sparkwing.example --token $TOKEN

# Add a local profile without auth
sparkwing configure profiles add --name local --controller http://127.0.0.1:4344

# Add a profile that defaults to a cluster runner
sparkwing configure profiles add --name prod --controller https://api.sparkwing.example --token $TOKEN --default-runner cloud-linux
```

## `sparkwing configure profiles duplicate`

Copy one profile's config into another

Useful when you want to tweak a known-good profile (say, change the token for a staging rotation) without hand-editing yaml.

### Flags

| Flag | Description |
|---|---|
| `--src NAME` | Source profile name (required) |
| `--dst NAME` | Destination profile name (must not exist yet) (required) |

### Examples

```sh
# Branch prod into a staging-prod profile
sparkwing configure profiles duplicate --src prod --dst staging-prod
```

## `sparkwing configure profiles list`

Print every registered profile

Prints a table of profile name, controller URL, logs URL, token
(redacted), and gitcache URL. The default profile is marked with
a leading '*'.

### Examples

```sh
# List profiles
sparkwing configure profiles list
```

## `sparkwing configure profiles remove`

Delete a profile

Removes the entry from profiles.yaml. If the removed profile was the default, no new default is auto-picked -- operators must pass --profile on every call or set one via 'sparkwing configure profiles use --name <X>'.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Profile name to remove (required) |

### Examples

```sh
# Remove a stale profile
sparkwing configure profiles remove --name old-stage
```

## `sparkwing configure profiles set`

Update fields on an existing profile

Only flags you pass are overwritten. --token="" explicitly
clears the token (empty value, not an omitted flag). Use
--show-token on 'profiles show' afterward to confirm.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Profile name to mutate (required) |
| `--controller URL` | New controller URL |
| `--logs URL` | New logs-service URL |
| `--token TOKEN` | New bearer token (empty string clears) |
| `--gitcache URL` | New gitcache URL |
| `--default-runner NAME` | Runner name (empty clears, falls back to local) |

### Examples

```sh
# Rotate a profile's token
sparkwing configure profiles set --name prod --token $NEW_TOKEN

# Clear a stale logs URL
sparkwing configure profiles set --name prod --logs=""

# Point a profile at a different default runner
sparkwing configure profiles set --name prod --default-runner cloud-gpu
```

## `sparkwing configure profiles show`

Print one profile's full config

Prints all fields of the profile named by --name. Token is
redacted unless --show-token is passed. Omitting --name prints
the current default profile.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Profile name (default: current default) |
| `--show-token` | Print the raw token (redacted by default) |

### Examples

```sh
# Show the default profile
sparkwing configure profiles show

# Show a named profile with the raw token
sparkwing configure profiles show --name prod --show-token
```

## `sparkwing configure profiles test`

Probe controller/auth/logs/gitcache for one profile

Sequentially checks the profile's controller (/api/v1/health),
auth (/api/v1/runs?limit=1 + /api/v1/auth/whoami), logs
service (if configured), and gitcache (if configured). Each
probe prints ok / warn / fail along with latency and any
error detail.

Exit code is non-zero when any probe fails. Missing optional
services (logs, gitcache) count as warn, not fail, so a
minimally-configured laptop profile can still exit 0.

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile name (default: current default) |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `-o, --output FMT` | Output format (json\|table) |

### Examples

```sh
# Probe the default profile
sparkwing configure profiles test

# Probe a named profile
sparkwing configure profiles test --profile prod

# JSON for scripting
sparkwing configure profiles test --profile prod -o json
```

## `sparkwing configure profiles use`

Set the default profile

Updates profiles.yaml so commands run without --profile target this
profile. The previous default is untouched beyond losing its
default status.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Profile name to mark as default (required) |

### Examples

```sh
# Switch the default to prod
sparkwing configure profiles use --name prod
```

## `sparkwing configure xrepo`

Manage the laptop-local repo registry

The registry maps pipeline names to local checkouts so
cross-repo RunAndAwait calls resolve without hardcoded WithFreshRepo
annotations. Auto-populated when you run 'sparkwing run <pipeline>'
in a .sparkwing/-bearing repo (set SPARKWING_NO_AUTO_REGISTER=1 to
disable).

Subverbs: list (show every registered repo and the pipelines it
provides), add (register a checkout explicitly), remove (drop one by
path or basename), prune (drop repos whose .sparkwing/ is gone). Run
'sparkwing configure xrepo --help' for their flags.

### Examples

```sh
# Register the current checkout
sparkwing configure xrepo add

# Show the fleet the registry reaches
sparkwing configure xrepo list

# Drop entries whose checkout is gone
sparkwing configure xrepo prune
```
