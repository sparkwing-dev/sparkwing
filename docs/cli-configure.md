<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing configure

Every `sparkwing configure` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing configure`

Configure laptop-local settings

Configure this machine. 'init' prepares the configuration directory and
reports its contents. 'profiles' lists and edits controller connections;
'sparkwing cloud' adds, checks and removes them.
'xrepo' registers local repositories.

Manage controller users and tokens with 'sparkwing cluster'.
Manage secrets with 'sparkwing secrets'.

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

# Register the current repo with the cross-repo registry
sparkwing configure xrepo add
```

## `sparkwing configure init`

Set up ~/.config/sparkwing/ and report laptop-level config status

Idempotent setup + status command for laptop-level
sparkwing config. Creates ~/.config/sparkwing/ if it doesn't exist,
then reports which config files are present (config.yaml,
secrets.key), the running CLI + Go toolchain version,
and a curated list of next-step commands.

Pairs with the per-project flow: use this one on a fresh laptop
after install, then run 'sparkwing pipeline new --name <name>'
inside each project to scaffold .sparkwing/ + your first pipeline
in one step (no separate init needed).

Re-running on an already-set-up laptop re-applies 0700 to
~/.config/sparkwing/ and reports each config file's mode, naming any
that group or other users can read. --dry-run skips both the mkdir
and the permission fix so the command reports existing state.

Run inside a sparkwing project, it also reports whether this
checkout's declared git hooks fire, and names the command that
arms them. It installs nothing and changes no git configuration.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
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

Profiles are the profiles section of config.yaml:
$SPARKWING_CONFIG (if set), else $XDG_CONFIG_HOME/sparkwing/config.yaml,
else ~/.config/sparkwing/config.yaml. Permissions on save are 0600, and a
save rewrites only the profiles section.

SPARKWING_HOME does not move this file. It is the state, cache and
logs root; profiles are machine-wide connections that outlive any
one home. A write from a command running under a home of its own is
refused rather than sent to the machine's profiles: set
SPARKWING_CONFIG to a path inside that home to keep it there.

Every human-driven client command (tokens, users, runs
retry/cancel/prune/logs) reads connection info from the
selected profile via --profile NAME. No --controller/--token flags
exist on other commands; profiles are the only config surface.

Add a profile with 'sparkwing cloud connect', check it with
'sparkwing cloud status', and remove it with 'sparkwing cloud disconnect'.

### Subcommands

- `list` -- Print every registered profile
- `show` -- Print one profile's config, or the profile a command would select
- `set` -- Update fields on an existing profile

## `sparkwing configure profiles list`

Print every registered profile

Prints a table of profile name, controller URL, logs URL, and
token. JSON is one profile per line; the token is redacted in
every mode.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |

### Examples

```sh
# List profiles
sparkwing configure profiles list

# Agent-readable record
sparkwing configure profiles list -o json
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

Print one profile's config, or the profile a command would select

With --name, prints all fields of that config.yaml entry. The token is
redacted unless --show-token is passed.

Without --name, reports the profile a sparkwing command would resolve to and
the chain that picked it: --profile, then SPARKWING_PROFILE, then the
project's defaults.profile -- the resolver 'sparkwing run' and 'sparkwing
pipeline trigger' use, so the answer matches what they would do. --profile
NAME shows what adding that flag to your next command would select. That
report never prints tokens.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Profile name in config.yaml |
| `--profile NAME` | Without --name: the resolution --profile NAME would make |
| `--show-token` | Print the raw token (redacted by default) |
| `-o, --output FORMAT` | Output format for the resolution report: pretty\|json |

### Examples

```sh
# The profile a command would use, and why
sparkwing configure profiles show

# What --profile prod would pick
sparkwing configure profiles show --profile prod -o json

# Show a named profile
sparkwing configure profiles show --name prod

# Show a named profile with the raw token
sparkwing configure profiles show --name prod --show-token
```

## `sparkwing configure xrepo`

Manage the laptop-local repo registry

The registry maps pipeline names to local checkouts so
cross-repo RunAndAwait calls resolve without hardcoded WithFreshRepo
annotations. Auto-populated when you run 'sparkwing run <pipeline>'
in a .sparkwing/-bearing repo (set SPARKWING_NO_AUTO_REGISTER=1 to
disable).

The registry is the repos section of config.yaml: $SPARKWING_CONFIG
(if set), else $XDG_CONFIG_HOME/sparkwing/config.yaml, else
~/.config/sparkwing/config.yaml. SPARKWING_HOME does not move it; it
is the state, cache and logs root, and a registered checkout is a
machine-wide fact that outlives any one home. A write from a command
running under a home of its own is refused rather than sent to the
machine's registry: set SPARKWING_CONFIG to a path inside that home
to keep it there.

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
