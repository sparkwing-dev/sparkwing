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

Re-running on an already-set-up laptop re-applies 0700 to
~/.config/sparkwing/ and reports each config file's mode, naming any
that group or other users can read. --dry-run skips both the mkdir
and the permission fix so the command pure-probes.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `--dry-run` | Probe + report without creating or tightening ~/.config/sparkwing/ |

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

Every human-driven client command (tokens, users, runs
retry/cancel/prune/logs, gc) reads connection info from the
selected profile via --profile NAME. No --controller/--token flags
exist on other commands; profiles are the only config surface.

### Subcommands

- `add` -- Register a new connection profile
- `list` -- Print every registered profile
- `show` -- Print one profile's full config
- `remove` -- Delete a profile
- `duplicate` -- Copy one profile's config into another
- `set` -- Update fields on an existing profile
- `test` -- Probe controller/auth/logs/gitcache for one profile

## `sparkwing configure profiles add`

Register a new connection profile

Creates a new entry in profiles.yaml. --name and --controller
are required; the token is optional. --token-stdin reads the
token from stdin and prompts without echo when stdin is a
terminal; prefer it over --token, which is visible to other
processes in the process list and recorded in shell history.
Configure storage and service backends by editing profiles.yaml.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Profile name (unique per profiles.yaml) (required) |
| `--controller URL` | Controller base URL (required) |
| `--token TOKEN` | Bearer token, visible to other processes and shell history (omit for local/unauthed stacks) |
| `--token-stdin` | Read the bearer token from stdin, prompting without echo on a terminal |

### Examples

```sh
# Add a prod profile, prompting for the token
sparkwing configure profiles add --name prod --controller https://api.sparkwing.example --token-stdin

# Add a prod profile from a piped token
printf %s "$TOKEN" | sparkwing configure profiles add --name prod --controller https://api.sparkwing.example --token-stdin

# Add a local profile without auth
sparkwing configure profiles add --name local --controller http://127.0.0.1:4344
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

Prints a table of profile name, controller URL, logs URL, and
token. JSON is one profile per line; the token is redacted in
every mode.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |

### Examples

```sh
# List profiles
sparkwing configure profiles list

# Agent-readable record
sparkwing configure profiles list -o json
```

## `sparkwing configure profiles remove`

Delete a profile

Removes the named entry from profiles.yaml.

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
clears the token (empty value, not an omitted flag), and
--token-stdin with empty input clears it too. --token-stdin
reads the token from stdin and prompts without echo when stdin
is a terminal; prefer it over --token, which is visible to
other processes in the process list and recorded in shell
history. Use --show-token on 'profiles show' afterward to
confirm.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Profile name to mutate (required) |
| `--controller URL` | New controller URL |
| `--token TOKEN` | New bearer token, visible to other processes and shell history (empty string clears) |
| `--token-stdin` | Read the new bearer token from stdin, prompting without echo on a terminal |

### Examples

```sh
# Rotate a profile's token
sparkwing configure profiles set --name prod --token-stdin

# Change a profile's controller
sparkwing configure profiles set --name prod --controller https://api.sparkwing.example
```

## `sparkwing configure profiles show`

Print one profile's full config

Prints all fields of the profile named by --name. Token is
redacted unless --show-token is passed.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Profile name (required) |
| `--show-token` | Print the raw token (redacted by default) |

### Examples

```sh
# Show a named profile
sparkwing configure profiles show --name prod

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
| `--profile NAME` | Profile name (required) |
| `-o, --output FMT` | Output format (json\|table) |

### Examples

```sh
# Probe a named profile
sparkwing configure profiles test --profile prod

# JSON for scripting
sparkwing configure profiles test --profile prod -o json
```

## `sparkwing configure xrepo`

Manage the laptop-local repo registry

The registry maps pipeline names to local checkouts so
cross-repo RunAndAwait calls resolve without hardcoded WithFreshRepo
annotations. Auto-populated when you run 'sparkwing run <pipeline>'
in a .sparkwing/-bearing repo (set SPARKWING_NO_AUTO_REGISTER=1 to
disable).

### Subcommands

- `list` -- List registered checkouts and their pipelines
- `add` -- Register a checkout
- `remove` -- Remove a registered checkout
- `prune` -- Remove checkouts whose pipeline directory is gone

### Examples

```sh
# Register the current checkout
sparkwing configure xrepo add

# Show the fleet the registry reaches
sparkwing configure xrepo list

# Drop entries whose checkout is gone
sparkwing configure xrepo prune
```

## `sparkwing configure xrepo add`

Register a checkout

Registers a checkout explicitly. The path defaults to the current directory.

### Arguments

- `[path]` (optional) -- Checkout path; defaults to the current directory

### Examples

```sh
# Register the current checkout
sparkwing configure xrepo add

# Register another checkout
sparkwing configure xrepo add ../service
```

## `sparkwing configure xrepo list`

List registered checkouts and their pipelines

Shows each registered checkout, its status, and the pipelines it provides.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: json \| table |
| `--pipelines` | Include pipeline names (default: true) |

### Examples

```sh
# List registered checkouts
sparkwing configure xrepo list

# Emit one JSON record per checkout
sparkwing configure xrepo list -o json

# Skip pipeline discovery
sparkwing configure xrepo list --pipelines=false
```

## `sparkwing configure xrepo prune`

Remove checkouts whose pipeline directory is gone

Removes registered checkouts that no longer contain a .sparkwing directory.

### Examples

```sh
# Remove stale registry entries
sparkwing configure xrepo prune
```

## `sparkwing configure xrepo remove`

Remove a registered checkout

Removes every registry entry matching a path or basename.

### Arguments

- `<path-or-basename>` (required) -- Registered path or basename to remove

### Examples

```sh
# Remove a checkout by basename
sparkwing configure xrepo remove service
```
