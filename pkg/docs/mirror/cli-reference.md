<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference

Complete listing of every `sparkwing` command, flag, and argument, generated from the CLI's own command registry. For the conceptual overview -- which binaries exist, the flag-naming rule, and what to reach for when -- see [cli.md](cli.md).

## `sparkwing`

sparkwing -- CI/CD pipelines written in Go

Sparkwing is a self-hosted pipeline runner. Pipelines are Go
programs in a repo's .sparkwing/ directory, triggered by git hooks,
webhooks, schedules, or manual invocation. Use 'sparkwing run
<pipeline>' to invoke one; 'sparkwing pipeline list' / 'describe'
for agent-facing discovery.

### Subcommands

- `info` -- What is sparkwing, what's in this repo, what to run next
- `pipeline` -- This repo's pipelines
- `run` -- Run a pipeline (shortcut for `pipeline run`)
- `runs` -- Inspect or manage runs
- `repos` -- The machine's fleet of sparkwing repos + SDK pins
- `queue` -- The truthful view of local admission: holders + waiters
- `profile` -- Show which profile sparkwing would use right now, and why
- `version` -- Show + update versions
- `update` -- Self-update the CLI binary
- `dashboard` -- Local dashboard server
- `doctor` -- Diagnose and repair provably-dead local state
- `cluster` -- Cluster ops
- `secrets` -- Manage secrets
- `configure` -- Laptop-local config
- `debug` -- Interactive run debugging
- `docs` -- Embedded user docs (offline)
- `commands` -- Full CLI surface as JSON (agent self-discovery)
- `completion` -- Shell completion script

### Examples

```sh
# Run a pipeline (positional shortcut)
sparkwing run build-test-deploy

# First command an agent should run
sparkwing info -o json

# List every invocable (agents)
sparkwing pipeline list -o json

# Inspect one pipeline's full metadata
sparkwing pipeline describe --name release -o json

# Bootstrap + scaffold your first pipeline in a fresh repo
sparkwing pipeline new --name release

# Start the local dashboard
sparkwing dashboard start
```

## `sparkwing cluster`

Operate and inspect the sparkwing cluster

Cluster-scoped operations and state. 'status' rolls up
controller health + fleet + queue state into one report;
individual verbs drill in (agents for fleet detail, users /
tokens for controller-stored config, image rollout
for deploys, webhooks for GitHub delivery debug).

Secrets used to live here; they're now top-level
('sparkwing secrets ...') since they straddle laptop dotenv
+ controller storage and are referenced constantly.

'worker' runs a laptop-side queue drainer against a remote
cluster. 'gc' sweeps stale warm-runner PVCs.

For the laptop-local dashboard server, see
'sparkwing dashboard start'.

Profiles (via --profile) pick which cluster these commands
address; set them up with 'sparkwing configure profiles'.

### Subcommands

- `status` -- Roll-up report: controller health + fleet + queue + recent runs
- `agents` -- Fleet-view detail (GET /api/v1/agents)
- `worker` -- Run a laptop-side worker against a remote cluster
- `gc` -- Sweep stale warm-PVC state
- `users` -- Create / list / delete dashboard login users
- `tokens` -- Create / list / revoke / rotate controller API tokens
- `image` -- Image rollout helpers for gitops-managed deployments
- `webhooks` -- Inspect / replay GitHub webhooks (wraps gh api)
- `concurrency` -- Inspect a concurrency namespace: holders + queue

### Examples

```sh
# Cluster health summary
sparkwing cluster status --profile prod

# List fleet agents
sparkwing cluster agents --profile prod
```

## `sparkwing cluster agents`

Inspect the controller's fleet view

Hits GET /api/v1/agents on the selected profile's controller.
Prints one row per agent seen claiming work in the last hour
(the controller infers agents from recent node claims; there
is no explicit registration table yet).

### Subcommands

- `list` -- Print agents (name, type, status, active jobs, last-seen, labels)

### Examples

```sh
# List prod agents
sparkwing cluster agents list --profile prod
```

## `sparkwing cluster agents list`

Print the controller's known agents

Fetches /api/v1/agents and renders a table of fleet members.
The controller infers agents from node claims over the last
hour, so idle agents without any recent claim activity won't
show up -- a known limitation until we add explicit heartbeats.

Use -q to print just names, one per line, for shell piping
(e.g. looping over agents with xargs).

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile name (required) |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `-o, --output FMT` | Output format (json\|table) |
| `-q, --quiet` | Print just agent names, one per line |

### Examples

```sh
# List agents on prod
sparkwing cluster agents list --profile prod

# Just agent names for piping
sparkwing cluster agents list --profile prod -q
```

## `sparkwing cluster concurrency`

Inspect a single concurrency namespace: holders + queue

Shows who currently holds a concurrency namespace's slots
and the queue of waiters behind it, each with its arrival-rank
position. Weighted admission can run a later fitting waiter before
an earlier non-fitting waiter, so position is not always run order.
Use it to tell whether a node is wedged or waiting for budget.

Hits GET /api/v1/concurrency/{namespace}/state on the
selected profile's controller.

For a controller's whole admission state -- every key, its holders and
waiters, and each registered runner's free capacity -- through the same
view as the local queue, use 'sparkwing queue --profile NAME'. This
command narrows to one namespace.

### Flags

| Flag | Description |
|---|---|
| `--namespace NAME` | Concurrency namespace to inspect (required) |
| `--profile NAME` | Profile selecting the controller (required) |
| `-o, --output FORMAT` | Output format (json\|table) |

### Examples

```sh
# Who holds and who's queued
sparkwing cluster concurrency --namespace deploy-prod --profile prod
```

## `sparkwing cluster gc`

Sweep stale warm-PVC state

Operator-facing manual invocation of the warm-PVC sweep.
Normally fires at 'sparkwing cluster worker' startup; exposed as a subcommand
so operators can trigger it against a running pod via kubectl
exec during incident response.

When --profile is omitted, the run-directory sweep is skipped; the
mtime-based git/ and tmp/ sweeps still run and free disk. Supply
--profile to enable the full sweep.

### Flags

| Flag | Description |
|---|---|
| `--root DIR` | Warm-PVC root (default: $SPARKWING_HOME resolution) |
| `--profile NAME` | Profile name; without it run-dir sweep is skipped |

### Examples

```sh
# mtime-only sweep in-pod (no controller)
sparkwing cluster gc

# Full sweep against prod controller
sparkwing cluster gc --profile prod

# Target a specific warm root
sparkwing cluster gc --root /var/lib/sparkwing --profile prod
```

## `sparkwing cluster image`

Rollout helpers for images referenced by a gitops repo

Composite verbs that operate on the images: block of a
kustomization.yaml plus the downstream ArgoCD / kubectl dance.
Building and pushing images stays with the consumer pipeline --
this subcommand only owns the "bump tag, commit, push, sync,
wait for rollout" path.

### Subcommands

- `rollout` -- Bump a kustomization newTag, commit+push, sync ArgoCD, wait for rollout

### Examples

```sh
# Bump sparkwing-runner to a new commit tag
sparkwing cluster image rollout --image sparkwing-runner --tag commit-abc123 --profile prod --wait
```

## `sparkwing cluster image rollout`

Bump a kustomization image tag, commit+push, sync ArgoCD, optionally wait

Rewrites the newTag: field for the image whose entry in the
gitops repo's kustomization.yaml matches --image (suffix match
against the ECR / registry URL), commits + pushes the change,
optionally triggers an ArgoCD sync, and optionally blocks on
kubectl rollout status.

Gitops repo resolution order:
  1. --gitops-repo PATH explicit flag
  2. ~/code/gitops (the author's default layout)

The command is idempotent: if the newTag already matches --tag
there is nothing to commit, and the pipeline continues to sync
+ wait without error. Use --dry-run to preview the plan without
writing, committing, pushing, syncing, or waiting.

Optional tools are skipped cleanly when absent from PATH:
  - argocd missing  -> sync is skipped with a one-line notice
  - kubectl missing -> --wait / --tail-logs error before side effects

This verb does NOT build or push the image itself. The consumer
pipeline that produced --tag is responsible for publishing the
image to the registry before calling rollout.

### Flags

| Flag | Description |
|---|---|
| `--image NAME` | Short image name (matches the suffix of the ECR URL) (required) |
| `--tag TAG` | New tag to write in kustomization.yaml (required) |
| `--profile NAME` | Profile name. Reserved for future per-profile gitops repo + argocd context discovery. (required) |
| `--gitops-repo PATH` | Gitops repo path (default: ~/code/gitops) |
| `--namespace NS` | Kubernetes namespace for rollout status + logs (default: sparkwing) |
| `--argocd-app NAME` | ArgoCD app name (default: derived from --image) |
| `--message MSG` | Commit message (default: 'chore: bump <image> to <tag>') |
| `--wait` | Block until 'kubectl rollout status deployment/<image>' returns |
| `--tail-logs` | After rollout, 'kubectl logs -f -l app=<image>' until ctrl-c |
| `--dry-run` | Print what would happen without writing, committing, pushing, or syncing |

### Examples

```sh
# Dry-run against the sparkwing-runner image
sparkwing cluster image rollout --image sparkwing-runner --tag commit-abc123 --profile prod --dry-run

# Bump and wait for the rollout
sparkwing cluster image rollout --image sparkwing-runner --tag commit-abc123 --profile prod --wait

# Bump, sync, wait, then tail pod logs
sparkwing cluster image rollout --image sparkwing --tag commit-abc123 --profile prod --wait --tail-logs
```

## `sparkwing cluster status`

Connectivity + fleet + queue health check against a remote cluster

Answers "is this cluster alive?" in one command. Runs the
connectivity / auth probes from 'profiles test' plus cluster-
state probes that hit /api/v1/agents, /api/v1/pool,
/api/v1/triggers (status=claimed), and /api/v1/runs?since=24h.

Sections:

  CONNECTIVITY  controller / auth / logs / gitcache
  FLEET         agents (connected vs stale) + warm-runner pool
  QUEUE         stuck triggers + recent-run success rate

Exit 0 when every probe is ok or warn; exit 1 when any probe
fails (auth reject, controller down, HTTP 5xx). Warnings are
informational -- low success rate, empty pool, stale agents --
and don't change the exit code so scripts can still condition
on "is the cluster reachable at all?".

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile name (default: current default) (required) |
| `-o, --output FMT` | Output format: pretty\|json |

### Examples

```sh
# Quick-check prod
sparkwing cluster status --profile prod

# Structured output for a status dashboard
sparkwing cluster status --profile prod -o json
```

## `sparkwing cluster tokens`

Manage controller API tokens

All subcommands resolve controller URL + admin bearer from the
named profile (or the default profile when --profile is omitted).
Token creation prints the raw value to stdout exactly ONCE --
stash it immediately.

### Subcommands

- `create` -- Mint a new token (prints raw value once)
- `list` -- List token prefixes + metadata (never prints raw)
- `revoke` -- Mark a token revoked so further requests 401
- `lookup` -- Print metadata for a single token by prefix
- `rotate` -- Mint a replacement token with a grace window

## `sparkwing cluster tokens create`

Mint a new API token

Creates a token of the given --type scoped to --principal.
Comma-separated --scope lists which API surfaces the token may
call. The raw token is printed to stdout exactly once; after
this command exits it cannot be recovered.

### Flags

| Flag | Description |
|---|---|
| `--type KIND` | Token type: user \| runner \| service (required) |
| `--principal NAME` | Free-form label identifying the token holder (required) |
| `--scope CSV` | Comma-separated scopes (e.g. jobs:read,jobs:write) |
| `--ttl DURATION` | Token lifetime (e.g. 30d, 720h). 0 = never expires |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Mint a service token with write scopes
sparkwing cluster tokens create --type service --principal deploy-bot --scope jobs:read,jobs:write

# Mint a user token that expires in 30 days
sparkwing cluster tokens create --type user --principal alice --scope admin --ttl 720h
```

## `sparkwing cluster tokens list`

List token prefixes + metadata

Prints the non-secret prefix + metadata (type, principal,
scopes, last-used) for every token. The raw token value is
never printed by this command.

The SCOPES column shows the comma-separated scope set granted
to each token. Tokens carrying the controller's "admin"
superset render as "*" since admin short-circuits every other
scope check. An empty scope set renders as "-".

Use -o json to get a structured array with explicit
scope arrays, suitable for piping into jq.

### Flags

| Flag | Description |
|---|---|
| `--type KIND` | Filter by token type |
| `--include-revoked` | Include revoked tokens in the output |
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# List all active tokens
sparkwing cluster tokens list

# Audit every revoked service token
sparkwing cluster tokens list --type service --include-revoked

# Inspect the warm-runner pool token's scopes as JSON
sparkwing cluster tokens list --profile prod -o json | jq '.[] | select(.principal=="agent:sparkwing-warm-runner") | .scopes'
```

## `sparkwing cluster tokens lookup`

Print metadata for a single token

Prints the JSON metadata for a token given its non-secret prefix. Useful for confirming principal + scopes before revoking or rotating.

### Flags

| Flag | Description |
|---|---|
| `--prefix PREFIX` | Non-secret token prefix (required) |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Inspect a token before revoking
sparkwing cluster tokens lookup --prefix a1b2c3d4
```

## `sparkwing cluster tokens revoke`

Mark a token revoked

Subsequent requests using the token receive HTTP 401. Revocation is immediate and irreversible.

### Flags

| Flag | Description |
|---|---|
| `--prefix PREFIX` | Non-secret token prefix (from 'tokens list') (required) |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Revoke a leaked token
sparkwing cluster tokens revoke --prefix a1b2c3d4
```

## `sparkwing cluster tokens rotate`

Mint a replacement token with a grace window

Creates a new token and schedules the old token for revocation
after --grace. During the grace window, both tokens work, which
lets callers cycle credentials without downtime.

### Flags

| Flag | Description |
|---|---|
| `--prefix PREFIX` | Non-secret prefix of the token to rotate (required) |
| `--grace DURATION` | Window during which the old token still authenticates (default: 24h) |
| `--ttl DURATION` | TTL of the new token (0 = preserve the old token's remaining TTL) |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Rotate a token with a 48h grace window
sparkwing cluster tokens rotate --prefix a1b2c3d4 --grace 48h
```

## `sparkwing cluster users`

Manage dashboard login users

Seeds admin credentials in the controller's users table, used
by the web pod's login flow. Connection info comes from the
selected profile; --profile overrides the default.

### Subcommands

- `add` -- Create a user with a password (prompts hidden on stdin)
- `list` -- Print every user + created_at + last_login_at
- `delete` -- Remove a user (active sessions stay until expiry)

## `sparkwing cluster users add`

Create a dashboard user

Prompts for a password on stdin with echo disabled when stdin
is a TTY (the password is not shown on-screen or recorded in
shell history). Passing --password skips the prompt -- useful
for CI seed flows but leaks via shell history if used
interactively.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Dashboard username (required) |
| `--password PASSWORD` | Password (omit to prompt interactively) |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Interactive add
sparkwing cluster users add --name alice

# Non-interactive add for CI
sparkwing users add --name ci-bot --password "$CI_BOT_PW"
```

## `sparkwing cluster users delete`

Remove a dashboard user

Deletes the user row. Any sessions that user holds remain
valid until their individual expiry -- sparkwing does not
proactively invalidate active cookies on delete.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Dashboard username to remove (required) |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Delete a user
sparkwing cluster users delete --name alice
```

## `sparkwing cluster users list`

Print every user

Prints name, created_at, and last_login_at for every user in
the controller's users table.

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# List users on the default profile
sparkwing cluster users list

# List users on prod
sparkwing cluster users list --profile prod
```

## `sparkwing cluster webhooks`

Inspect and replay GitHub webhooks

Sparkwing-aware wrapper over the GitHub hooks API. Shells out
to 'gh api' (inherits your gh auth); install gh from
https://cli.github.com if it isn't on PATH.

Value-add over 'gh api' alone: the deliveries view joins
GitHub's delivery log with sparkwing's trigger/run rows so
each delivery shows the run id it produced and the run's
terminal status -- without two separate lookups.

### Subcommands

- `list` -- List hooks on a repo + derived pipeline name
- `deliveries` -- Recent deliveries for one hook, joined with trigger state
- `replay` -- Queue a redelivery of a specific delivery UUID

### Examples

```sh
# List hooks on a repo
sparkwing cluster webhooks list --repo your-org/my-app --profile prod

# Recent deliveries for a hook
sparkwing cluster webhooks deliveries --repo your-org/my-app --hook 608819334 --since 1h --profile prod
```

## `sparkwing cluster webhooks deliveries`

List recent deliveries for a hook, joined with trigger state

Fetches recent deliveries via 'gh api' and, for each one,
looks up the matching sparkwing trigger by GITHUB_DELIVERY env
stamp. Surfaces TRIGGER_ID + RUN_STATUS columns so operators
see GitHub-side status alongside the run it produced.

--since filters deliveries client-side (GitHub's API does not
take a time filter). Default: 24h.

### Flags

| Flag | Description |
|---|---|
| `--repo OWNER/NAME` | GitHub repo (required) |
| `--hook N` | GitHub hook id from 'webhooks list' (required) |
| `--since DURATION` | Only deliveries newer than this (default: 24h) |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `-o, --output FMT` | Output format (json\|table) |
| `--profile NAME` | Profile name (used for trigger/run lookups) (required) |

### Examples

```sh
# Recent deliveries for a hook
sparkwing cluster webhooks deliveries --repo your-org/my-app --hook 608819334 --since 1h --profile prod
```

## `sparkwing cluster webhooks list`

List GitHub hooks configured on a repo

Calls 'gh api /repos/OWNER/NAME/hooks' and prints id, derived
pipeline, active flag, last-delivery status, and URL.

The PIPELINE column is parsed from the hook URL path
(/webhooks/github/<pipeline>). Hooks posting to the older
unscoped /webhooks/github endpoint render as "(unscoped)"
so operators can spot them for cleanup. Non-sparkwing hooks
render as "(non-sparkwing)".

### Flags

| Flag | Description |
|---|---|
| `--repo OWNER/NAME` | GitHub repo (owner can be omitted if gh has a default) (required) |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `-o, --output FMT` | Output format (json\|table) |
| `--profile NAME` | Profile name (reserved for symmetry; unused by list) |

### Examples

```sh
# List hooks on a repo
sparkwing cluster webhooks list --repo your-org/my-app --profile prod
```

## `sparkwing cluster webhooks replay`

Queue a redelivery of a specific delivery UUID

POSTs /repos/OWNER/NAME/hooks/HOOK/deliveries/DELIVERY/attempts
to GitHub. GitHub queues a fresh attempt; the new delivery
appears in the hook's delivery log within seconds.

### Flags

| Flag | Description |
|---|---|
| `--repo OWNER/NAME` | GitHub repo (required) |
| `--hook N` | GitHub hook id (required) |
| `--delivery UUID` | Delivery GUID to redeliver (required) |
| `--profile NAME` | Profile name (reserved; unused by replay) |

### Examples

```sh
# Redeliver a webhook attempt
sparkwing cluster webhooks replay --repo your-org/my-app --hook 608819334 --delivery 0ac55946-3e96-11f1-9de8-f33e32f0060f --profile prod
```

## `sparkwing cluster worker`

Claim triggers from a profile's controller and run them in-process

Polls the trigger queue at the selected profile's
controller and executes each claimed trigger in-process. Laptop-local:
no K8s, no warm pool, no image dispatch. For the cluster-mode worker
with --runner k8s|warm and image / service-account flags, use
sparkwing-runner.

Run against a remote controller via --profile prod (or whichever profile),
or against a local 'sparkwing dashboard start' via --profile local. With
--profile omitted, uses the default profile from profiles.yaml.

### Flags

| Flag | Description |
|---|---|
| `--profile PROFILE` | Profile name from profiles.yaml (default: default profile) |
| `--poll DUR` | Claim poll interval when the queue is empty (default: 1s) |
| `--heartbeat DUR` | Claim-lease heartbeat cadence (default: 5s) |

### Examples

```sh
# Run against the default profile
sparkwing cluster worker

# Run against a named profile
sparkwing cluster worker --profile local

# Faster polling for tight dev loops
sparkwing cluster worker --profile local --poll 250ms
```

## `sparkwing commands`

Emit the full CLI surface as structured data (agent self-discovery)

Returns every registered verb -- path, synopsis, description,
positional args, flags, examples, subcommand list -- as one
JSON document. Designed as the agent's "what is this CLI"
probe: one tool call replaces walking every -h page.

The same Command record powers --help on every verb; this just
emits all of them in one shot. Filter with --path PREFIX to
narrow to a subtree (e.g. --path "sparkwing pipeline"). Default
output is JSON because agents are the primary audience; -o
plain emits one path per line for shell consumption.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: json \| plain \| pretty (default: json) |
| `--path PREFIX` | Only emit commands whose Path starts with PREFIX |
| `--include-hidden` | Also emit Hidden:true commands (default: skip) |

### Examples

```sh
# Full CLI surface (agent self-discovery)
sparkwing commands

# Just the pipelines subtree
sparkwing commands --path "sparkwing pipeline"

# All paths, one per line
sparkwing commands -o plain
```

## `sparkwing completion`

Emit a shell completion script (bash|zsh|fish)

Prints a completion script for the selected shell. Source it
from your shell rc:

  \# bash
  source <(sparkwing completion --shell bash)

  \# zsh (add 'autoload -U compinit; compinit' once above)
  source <(sparkwing completion --shell zsh)

  \# fish
  sparkwing completion --shell fish | source

zsh and fish get per-item descriptions; bash is name-only because
compgen lacks the facility.

### Flags

| Flag | Description |
|---|---|
| `--shell NAME` | bash \| zsh \| fish (required) |

### Examples

```sh
# Wire completion for the current zsh session
source <(sparkwing completion --shell zsh)

# Install persistent completion for fish
sparkwing completion --shell fish > ~/.config/fish/completions/sparkwing.fish
```

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
- `xrepo` -- Cross-repo registry: list / add / remove / prune local checkouts

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

- `add` -- Register a new profile
- `list` -- Print every profile; * marks the default
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

Removes the entry from profiles.yaml. If the removed profile was the default, no new default is auto-picked -- operators must pass --profile on every call or set one via 'sparkwing profiles use --name <X>'.

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
sparkwing profiles set --name prod --logs=""

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

## `sparkwing dashboard`

Manage the local dashboard + API server

Background lifecycle for the laptop-local dashboard.
'start' spawns a detached server (writes PID + log under
$SPARKWING_HOME), 'kill' stops it, 'status' reports liveness.

The server is one Go process that hosts the embedded Next.js SPA,
the JSON API, the log endpoints, and the SQLite store on the same
port. There is no separate Node process. The dashboard is purely
for visualization -- everything it shows is reachable from the
CLI as well.

### Subcommands

- `start` -- Spawn the detached dashboard server (idempotent)
- `kill` -- Stop a running dashboard server
- `status` -- Report whether the dashboard is running

### Examples

```sh
# Start the dashboard
sparkwing dashboard start

# Check liveness
sparkwing dashboard status

# Stop the dashboard
sparkwing dashboard kill
```

## `sparkwing dashboard kill`

Stop a running dashboard server

Sends SIGTERM to the PID recorded in
$SPARKWING_HOME/dashboard.pid, polls for exit, escalates to SIGKILL
after 5s if necessary, and removes the PID file. No-op (exit 0)
when nothing is running.

### Flags

| Flag | Description |
|---|---|
| `--home DIR` | State directory (default: $SPARKWING_HOME or ~/.sparkwing) |

### Examples

```sh
# Stop the dashboard
sparkwing dashboard kill
```

## `sparkwing dashboard start`

Spawn the detached dashboard server (idempotent)

Detaches a child process that runs the in-process
dashboard + API + logs server (pkg/localws). PID is written to
$SPARKWING_HOME/dashboard.pid; stdout/stderr are appended to
$SPARKWING_HOME/dashboard.log. Returns once the listener is
accepting TCP connections so callers can immediately curl it.

Idempotent: if a live server is already on file, prints the URL
and returns 0 without spawning a duplicate.

### Flags

| Flag | Description |
|---|---|
| `--addr HOST:PORT` | Bind address (default: 127.0.0.1:4343) |
| `--home DIR` | State directory (default: $SPARKWING_HOME or ~/.sparkwing) |
| `--profile PROFILE` | Profile from ~/.config/sparkwing/profiles.yaml (uses its log_store + artifact_store) |
| `--log-store URL` | Pluggable log backend URL (fs:///abs/path, s3://bucket/prefix). Overrides --profile. |
| `--artifact-store URL` | Pluggable artifact backend URL (fs:///abs/path, s3://bucket/prefix). Overrides --profile. |
| `--read-only` | Reject writes on /api/v1/* (auth + webhooks remain open) |
| `--no-local-store` | Skip local SQLite; list runs from --artifact-store. Requires --log-store + --artifact-store. |

### Examples

```sh
# Start with defaults
sparkwing dashboard start

# Use an alternate port
sparkwing dashboard start --addr 127.0.0.1:5000

# Isolate state under a scratch dir
sparkwing dashboard start --home /tmp/sparkwing-x

# Tail CI runs from S3 (no SQLite)
sparkwing dashboard start --profile ci-smoke --no-local-store --read-only
```

## `sparkwing dashboard status`

Report whether the dashboard is running

Reads $SPARKWING_HOME/dashboard.pid, probes the PID
with kill(0), and reports running state + URL. Exit code 0 when
running, 1 when not.

### Flags

| Flag | Description |
|---|---|
| `--home DIR` | State directory (default: $SPARKWING_HOME or ~/.sparkwing) |

### Examples

```sh
# Check liveness
sparkwing dashboard status
```

## `sparkwing debug`

Interactive debugging for pipeline runs

Pause nodes at selected hook points, inspect the paused pod,
drop into a shell, or release the node. Every debug verb is
ephemeral -- pause directives live only on the run they launch,
never in pipeline source. Pipelines stay production-clean.

### Subcommands

- `run` -- Run a pipeline with --pause-before / --pause-after / --pause-on-failure
- `release` -- Resume a paused node
- `attach` -- kubectl exec into a paused node's pod (cluster mode)
- `env` -- Print a paused node's env + workdir + claim holder
- `rerun` -- Reproduce a node's dispatch frame and drop into a shell
- `replay` -- Headlessly re-execute a single node from a prior run

### Examples

```sh
# Pause before the tests node
sparkwing debug run build --pause-before tests

# Resume a paused node
sparkwing debug release --run run-X --node tests
```

## `sparkwing debug attach`

kubectl exec into a paused node's pod (cluster mode)

Looks up the pod holding the paused node's claim-lease from
the controller's node row, then shells out to kubectl exec -it
-- bash. Local mode prints a note that attach does not apply
(the process is already in your current shell's world) and
exits 0.

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the paused node (required) |
| `--node NAME` | Node ID to attach to (required) |
| `--profile NAME` | Profile name (cluster mode) |

### Examples

```sh
# Attach in prod
sparkwing debug attach --run run-X --node tests --profile prod
```

## `sparkwing debug env`

Print a paused node's env + workdir + claim holder

Inspection-only command: reads the stored node record (env map,
claim holder, current pause state) and prints them to stdout.
Does NOT spawn a shell. If the node is not paused, prints a
warning and exits 0 -- env info is captured at pause time, not
continuously.

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the node (required) |
| `--node NAME` | Node ID to inspect (required) |
| `--profile NAME` | Profile name (cluster mode) |

### Examples

```sh
# Inspect locally
sparkwing debug env --run run-X --node tests
```

## `sparkwing debug release`

Resume a paused node

Flips the pause row's released_at timestamp so the
orchestrator's poll loop wakes and continues dispatching past
the pause point. Local and cluster modes share this surface.

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the paused node (required) |
| `--node NAME` | Node ID to release (required) |
| `--profile NAME` | Profile name (cluster mode) |

### Examples

```sh
# Release locally
sparkwing debug release --run run-X --node tests

# Release in prod
sparkwing debug release --run run-X --node tests --profile prod
```

## `sparkwing debug replay`

Re-execute a single node headlessly using its dispatch snapshot

Mints a new run row linked to the original via replay_of_run_id /
replay_of_node_id, creates a single nodes row for the target, and
exec's the pipeline binary to execute that one node. The
node's input struct is reconstituted from the stored dispatch
snapshot; upstream Refs resolve against the original
run's outputs without re-executing them.

Replay is "what would this node do now, with the same args+env?":
secrets re-resolve fresh through sparkwing.Secret, BeforeRun hooks
re-fire, and any code drift in the registered job struct (renamed
type, removed field) aborts loud rather than silently producing
wrong results.

With --profile PROF, the original run + target node + dep outputs +
dispatch snapshot are first fetched from the named controller via
HTTP and side-loaded into the local store. Replay execution itself
always runs locally because the user's sparkwing binary owns the
registered pipeline factories.

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the original node (required) |
| `--node NAME` | Node ID to re-execute (required) |
| `--profile PROF` | Sideload from this profile's controller before replaying locally |

### Examples

```sh
# Replay a node locally
sparkwing debug replay --run run-X --node deploy

# Replay a prod run on your laptop
sparkwing debug replay --profile prod --run run-X --node deploy
```

## `sparkwing debug rerun`

Reproduce a node's dispatch frame in an interactive shell

Reads the dispatch snapshot for the given run/node and reproduces
the env + workdir the orchestrator saw at dispatch time. Local mode
exec's $SHELL with the snapshot env applied and writes upstream Ref
outputs to ~/.sparkwing/rerun/<run>/<node>/refs so they're cat-able
from the shell. Cluster mode shells out to 'kubectl run' against a
runner image (--image or $SPARKWING_RERUN_IMAGE) with the snapshot
env materialized as --env=K=V flags.

Replays do NOT freeze the rest of the cluster: secrets re-resolve
through the standard sparkwing.Secret API on demand, and the runner
image is whatever the cluster runs today. Replay is "what would this
node do now, with the args+env it had then?", not a frozen
reproduction.

Default --seq selects the most-recent attempt for the node; pass
--seq 0 (or another integer) to target a specific attempt index.

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the node (required) |
| `--node NAME` | Node ID to reproduce (required) |
| `--seq N` | Attempt index; -1 selects most recent |
| `--profile NAME` | Profile name (cluster mode) |
| `--image REF` | Runner image for cluster-mode debug pod (cluster mode) |

### Examples

```sh
# Rerun locally
sparkwing debug rerun --run run-X --node tests

# Rerun a specific attempt
sparkwing debug rerun --run run-X --node tests --seq 1

# Rerun in prod
sparkwing debug rerun --run run-X --node tests --profile prod --image ghcr.io/me/runner:v1
```

## `sparkwing debug run`

Run a pipeline with ephemeral pause directives

Runs the named pipeline exactly as 'sparkwing run <pipeline>' would, with
additional pause hooks the orchestrator honors before and after
each matching node. Directives travel as env vars to the
pipeline binary; they never land in git-tracked code.

--pause-before <node> holds the node BEFORE its Run is invoked.
--pause-after  <node> holds the node AFTER its Run returns
  (success or failure). Both flags are repeatable.
--pause-on-failure holds ANY node whose Run returns a non-nil
  error. Skipped / cancelled / OnFailure-recovered nodes do NOT
  pause -- only honest Run errors.

Paused nodes hold for 30 minutes by default; set
SPARKWING_PAUSE_TIMEOUT=<duration> to change. A timed-out pause
is released with reason 'timeout-released' and surfaces in the
run record.

See 'sparkwing debug release' to resume, 'sparkwing debug env'
to inspect, and 'sparkwing debug attach' (cluster mode) to shell
into the pod holding the paused node.

### Flags

| Flag | Description |
|---|---|
| `--pipeline NAME` | Pipeline (pipeline) name to run under debug supervision (required) |
| `--pause-before NODE` | Hold NODE before Run (repeatable) |
| `--pause-after NODE` | Hold NODE after Run (repeatable) |
| `--pause-on-failure` | Hold any node whose Run errors |

### Examples

```sh
# Pause before tests
sparkwing debug run --pipeline build --pause-before tests

# Pause on failure
sparkwing debug run --pipeline build --pause-on-failure
```

## `sparkwing docs`

Embedded user docs (offline)

The sparkwing docs are shipped inside the binary. `sparkwing docs read --topic getting-started` returns the
raw markdown to stdout; `sparkwing docs all` dumps every
doc in one shot for an agent that wants the full corpus in
context. The docs match the binary version exactly -- no risk of
the website explaining a flag your CLI doesn't have.

Discovery: `sparkwing docs list -o json` returns slug + title +
summary for every topic. `sparkwing docs search --query "warm pool"`
substring-matches across slug + title + body.

### Subcommands

- `list` -- Enumerate every doc topic (slug, title, summary)
- `read` -- Print one doc's markdown to stdout (--topic NAME)
- `all` -- Concatenate every doc to stdout (full corpus dump)
- `search` -- Substring search across docs (--query TEXT)
- `migrations` -- Per-version migration guides (list / read / between)
- `versions` -- List doc versions known to this CLI (and sparkwing.dev with --web)
- `cache` -- Inspect / clear the on-disk cache used by --web

### Examples

```sh
# List all topics (table)
sparkwing docs list

# List all topics (agent-readable)
sparkwing docs list -o json

# Read one topic
sparkwing docs read --topic pipelines

# Read one topic at a specific version (online)
sparkwing docs read --topic pipelines --version v0.3.0 --web

# Slurp the whole corpus into context
sparkwing docs all

# Find docs that mention warm pool
sparkwing docs search --query "warm pool"

# List migration guides this CLI knows
sparkwing docs migrations list

# Pipe every guide up to v0.4.0 into context
sparkwing docs migrations between --to v0.4.0

# List every version available online
sparkwing docs versions --web
```

## `sparkwing docs all`

Concatenate every doc to stdout (full corpus dump)

Prints every embedded doc to stdout, separated by short ASCII
headers. The "give me everything" path for an agent that wants
the full corpus in context with one Bash invocation.

### Examples

```sh
# Slurp every doc into context
sparkwing docs all
```

## `sparkwing docs cache`

Inspect or clear the on-disk cache used by --web

--web fetches are cached to $XDG_CACHE_HOME/sparkwing/web/ (or
~/.cache/sparkwing/web/). The cache mirrors the URL path, so you
can `cat` the cached files directly when debugging.

Use `cache info` to see size / counts; use `cache clear` to wipe it.

### Subcommands

- `info` -- Print cache dir, total size, per-resource breakdown
- `clear` -- Remove every cached file (refuses to escape the cache dir)

### Examples

```sh
# How big is the cache?
sparkwing docs cache info

# Force-refresh on next --web call
sparkwing docs cache clear
```

## `sparkwing docs cache clear`

Remove every cached file

Deletes every file under the cache directory. Safe: the
implementation refuses to remove paths that don't resolve inside
the cache dir, so a stray symlink in the cache can't escape.

Useful when a cached versions.json or index.json has gone stale
faster than the 24h TTL window, or when debugging --web behavior.

### Examples

```sh
# Wipe the cache
sparkwing docs cache clear
```

## `sparkwing docs cache info`

Print cache dir, total size, per-resource breakdown

Walks the cache and prints a summary: total size, file counts
broken down by doc / migration / index, and the freshness state of
the cached versions.json (24h TTL).

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Human-readable
sparkwing docs cache info

# Agent-readable
sparkwing docs cache info -o json
```

## `sparkwing docs list`

Enumerate every doc topic

Walks the docs corpus and prints one row per topic with its
slug, first-H1 title, and first-paragraph summary. By default reads
the binary's embedded copy (hermetic, version-locked); pass --web
to fetch from sparkwing.dev for another version.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `--web` | Fetch from sparkwing.dev instead of the embedded corpus |
| `--version vX.Y.Z` | Doc version (e.g. v0.4.0, 'latest'). Defaults to this CLI's embedded version. |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# Human-readable table
sparkwing docs list

# Agent-readable
sparkwing docs list -o json

# Slug-per-line for shell loops
sparkwing docs list -o plain

# List the v0.3.0 corpus from sparkwing.dev
sparkwing docs list --web --version v0.3.0
```

## `sparkwing docs migrations`

Per-version migration guides (agent-friendly)

Surface the migration guides shipped under docs/migrations/.
Each released sparkwing version that introduces breaking changes
gets a guide; `sparkwing docs migrations between` concatenates
every guide in a version range into one blob you can pipe straight
into an agent context.

The same files are also reachable as regular docs (e.g.
`sparkwing docs read --topic migrations/v0.4.0`); this
subcommand is the ergonomics layer with semver-aware filtering and
range output.

### Subcommands

- `list` -- Table of every migration guide this CLI knows about
- `read` -- Print one migration guide to stdout (--version vX.Y.Z)
- `between` -- Concatenate every guide in (--from, --to] into one blob

### Examples

```sh
# List embedded migration guides
sparkwing docs migrations list

# Read one guide
sparkwing docs migrations read --version v0.4.0

# Every guide upgrading from v0.3.0 to v0.4.0
sparkwing docs migrations between --from v0.3.0 --to v0.4.0

# Every guide this CLI knows (one-shot agent context)
sparkwing docs migrations between
```

## `sparkwing docs migrations between`

Concatenate every guide in a version range into one blob

Returns every migration guide whose version is in (--from, --to],
in ascending version order, separated by markdown horizontal rules.
The output starts with a "Migration: vA -> vB" header so an agent
knows the range up-front.

This is the agent-killer command: one invocation produces the full
migration context for an N-version jump in a form ready to pipe.

--from defaults to v0.0.0 (every guide up through --to).
--to defaults to the highest version this CLI knows about.

### Flags

| Flag | Description |
|---|---|
| `--from vX.Y.Z` | Exclusive lower bound (default v0.0.0) |
| `--to vA.B.C` | Inclusive upper bound (default = latest embedded version) |
| `-o, --output FORMAT` | Output format: markdown \| plain (default: markdown) |
| `--web` | Fetch every guide in the range from sparkwing.dev |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# Every guide for a v0.3.0 -> v0.4.0 jump
sparkwing docs migrations between --from v0.3.0 --to v0.4.0

# Every guide up to a target version
sparkwing docs migrations between --to v0.4.0

# Every guide this CLI knows (one-shot agent context)
sparkwing docs migrations between

# Full range from sparkwing.dev (includes versions not yet embedded)
sparkwing docs migrations between --web
```

## `sparkwing docs migrations list`

Table of every embedded migration guide

Lists each migration guide bundled with this binary in
descending semver order, with date and one-line summary parsed
from docs/migrations/README.md. Use --output json for an
agent-readable array of {version, date, summary, slug, bytes}.

When the CLI's own version is older than the newest embedded
guide a one-line stderr note suggests rebuilding.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `--web` | Fetch the index from sparkwing.dev/migrations/index.json instead of the embed |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# Human-readable table
sparkwing docs migrations list

# Agent-readable
sparkwing docs migrations list -o json

# Version-per-line for shell loops
sparkwing docs migrations list -o plain

# Online (every release on sparkwing.dev)
sparkwing docs migrations list --web
```

## `sparkwing docs migrations read`

Print one migration guide's markdown to stdout

Outputs the markdown body for a single migration guide. Default
output is the raw markdown so an agent can pipe straight into
its context. Cross-doc markdown links to other topics are
rewritten into `sparkwing docs read --topic <slug>` form
(same transform as `sparkwing docs read`).

### Arguments

- `[vX.Y.Z]` (optional) -- Migration guide version, when --version is not supplied

### Flags

| Flag | Description |
|---|---|
| `--version vX.Y.Z` | Migration guide version (e.g. v0.4.0). Positional fallback accepted. |
| `-o, --output FORMAT` | Output format: markdown \| plain (default: markdown) |
| `--web` | Fetch from sparkwing.dev instead of the embedded corpus |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# Read the v0.4.0 guide
sparkwing docs migrations read --version v0.4.0

# Positional shortcut
sparkwing docs migrations read v0.4.0

# Read v0.5.0 from sparkwing.dev (not yet embedded)
sparkwing docs migrations read --version v0.5.0 --web
```

## `sparkwing docs read`

Print one doc's raw markdown to stdout

Prints the raw markdown body for the named topic. The slug is
the filename under /docs/ minus .md (run `sparkwing docs list` to
see them all). Subdirs use slash-separated paths (e.g.
design/remote-retry).

Default source is the binary's embedded corpus. Use --web to fetch
from sparkwing.dev, optionally pinned to --version vX.Y.Z or
--version latest.

### Flags

| Flag | Description |
|---|---|
| `--topic NAME` | Doc slug (e.g. getting-started, pipelines, mcp) (required) |
| `--web` | Fetch from sparkwing.dev instead of the embedded corpus |
| `--version vX.Y.Z` | Doc version (e.g. v0.4.0, 'latest'). Defaults to this CLI's embedded version. |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# Read the getting-started page
sparkwing docs read --topic getting-started

# Pipe through a pager
sparkwing docs read --topic pipelines | less

# Read v0.3.0's pipelines page online
sparkwing docs read --topic pipelines --version v0.3.0 --web

# Always fetch the freshest version
sparkwing docs read --topic pipelines --version latest --web
```

## `sparkwing docs search`

Substring search across embedded docs

Returns every doc whose slug + title + body contains every
space-separated token in --query (case-insensitive). Hits in
title/slug rank above body-only matches. Output shape matches
`sparkwing docs list` so -o json composes the same way.

### Flags

| Flag | Description |
|---|---|
| `-q, --query TEXT` | Search terms (every token must match) (required) |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |

### Examples

```sh
# Find docs about the warm pool
sparkwing docs search --query "warm pool"

# JSON for agents
sparkwing docs search -q approval -o json
```

## `sparkwing docs versions`

List doc versions known to this CLI (and sparkwing.dev with --web)

Reports each doc version the source knows about. Default
output is hermetic: only the binary's embedded version (plus every
migration-guide version shipped in the embed) appears, with no
network calls.

With --web, fetches sparkwing.dev/versions.json and merges in every
release available online -- useful for discovering newer versions
this CLI can render via --web on the read / list verbs.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `--web` | Merge in sparkwing.dev/versions.json (network) |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# Embedded only (default)
sparkwing docs versions

# Every version available online
sparkwing docs versions --web

# Agent-readable JSON
sparkwing docs versions --web -o json
```

## `sparkwing doctor`

Diagnose and repair provably-dead local state

Checks the sparkwing home for state that is safe to
remove because the process behind it is provably gone, repairs what it
finds, and reports everything -- so it is safe to run at any time and a
healthy machine reports a clean bill. It never kills a process, never
touches the admission daemon's live state, and never touches
cluster-scoped (global) rows.

It cleans four things: local run rows still marked running whose process
is gone and which the daemon does not know about; leftover box-slot lock
files from older binaries (a file whose owner is still alive is reported,
never removed); local-scope concurrency rows whose run has ended; and
run directories on disk whose run row no longer exists.

If an older-pinned pipeline binary is still admitting outside the daemon
through a held box-slot lock, doctor reports it and points at the fix --
bump that repo's sparkwing pin -- rather than deleting live state.

Use --dry-run to report what it would repair without changing anything.

### Flags

| Flag | Description |
|---|---|
| `--dry-run` | Report what would be repaired without changing anything |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain |
| `--home DIR` | Sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing) |

### Examples

```sh
# Diagnose and repair now
sparkwing doctor

# Report without changing anything
sparkwing doctor --dry-run

# Agent-readable report
sparkwing doctor -o json
```

## `sparkwing info`

Self-describe sparkwing + the current project (agent entrypoint)

One command that answers "what is sparkwing, am I in a
project, what should I run next" without prior knowledge. Prints
the CLI version, whether the current directory is inside a
sparkwing project (and how many pipelines it has), whether the Go
toolchain is on PATH, a curated list of next-step commands, and
the docs URL.

This is the canonical first command an agent runs after install.
Use -o json for structured output that an agent can parse, or
-o plain to emit one next-step command per line for shell
pipelines (head -n1 yields the most-likely next command).

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `--for-agent` | Emit a paste-ready block for CLAUDE.md / AGENTS.md (no ANSI, no extras) |
| `--first-time` | Print the post-install onboarding card (used by install.sh; re-runnable any time) |

### Examples

```sh
# Human-readable card
sparkwing info

# Agent-readable record
sparkwing info -o json

# Paste into CLAUDE.md / AGENTS.md
sparkwing info --for-agent >> CLAUDE.md

# Reprint the post-install onboarding card
sparkwing info --first-time
```

## `sparkwing pipeline`

This repo's pipelines

Per-project namespace. Every verb here operates on the
nearest .sparkwing/ walking up from the current directory.

Discovery (list / describe / discover / explain) shows what
pipelines this repo defines. 'new' scaffolds a fresh pipeline
(auto-bootstraps .sparkwing/ on first use). 'run' invokes one
(positional name; same as 'sparkwing run <name>'). 'hooks' wires
pipelines to git pre-commit / pre-push / post-commit.
'sparks' manages reusable spark libraries declared in
.sparkwing/sparks.yaml.

The discovery verbs (list / describe / discover / templates)
support -o json so an agent can parse output directly rather
than scraping tab-complete.

To bump the pipeline SDK pin in .sparkwing/go.mod, use
'sparkwing version update --sdk'. To see the current pin, run
'sparkwing version' (composite card).

### Subcommands

- `list` -- Enumerate every pipeline with metadata
- `describe` -- Print one pipeline's full metadata
- `discover` -- Fuzzy search over names, descriptions, tags
- `new` -- Scaffold a new pipeline (auto-bootstraps .sparkwing/ if missing)
- `templates` -- List the sparks-core template registry (starters for `new --template`)
- `explain` -- Render the pipeline's Plan DAG without running
- `lint` -- Check pipeline source for idiomatic anti-patterns (enforced gate)
- `plan` -- Render the runtime-resolved DAG (would-run/would-skip) without running
- `run` -- Invoke a pipeline (canonical form of `sparkwing run <name>`)
- `trigger` -- Submit a pipeline to a profile's controller (remote execution)
- `hooks` -- Git pre-commit / pre-push / post-commit hooks: install / uninstall / status
- `sparks` -- Manage sparks libraries: list / add / remove / lint / resolve / update / warmup

### Examples

```sh
# Machine-readable catalog
sparkwing pipeline list -o json

# One pipeline's details
sparkwing pipeline describe --name release -o json

# Search by intent
sparkwing pipeline discover --query "tag a release"

# First pipeline in a fresh repo (auto-bootstraps)
sparkwing pipeline new --name release

# Inspect the DAG before running
sparkwing pipeline explain --name release-all

# Run a pipeline
sparkwing pipeline run release
```

## `sparkwing pipeline describe`

Print one pipeline's full metadata

Emits the full record for a single pipeline: kind, group,
description, typed args, examples, triggers, and (for scripts)
frontmatter-declared positional args and flags. Always resolves
hidden entries -- if you're asking for a name explicitly, the
hidden flag shouldn't surprise you.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Pipeline name to describe (required) |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |

### Examples

```sh
# Human-readable
sparkwing pipeline describe --name release

# Agent-readable
sparkwing pipeline describe --name release -o json
```

## `sparkwing pipeline discover`

Fuzzy search over pipeline names + descriptions + tags

Search the catalog by intent. Every token in --query
must match some haystack field (name / short / help / group /
tags / triggers); matches in the name score higher than matches
in prose so direct hits surface first.

-o json emits {name, kind, group, ..., score} records sorted by
score descending; agents should prefer -o json for consumption.

### Flags

| Flag | Description |
|---|---|
| `--query TEXT` | Search query (one or more tokens, all must hit some field) (required) |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |

### Examples

```sh
# Find release-related pipelines
sparkwing pipeline discover --query release

# Multi-token, all must hit
sparkwing pipeline discover --query "tag release"

# Agent-readable ranked hits
sparkwing pipeline discover --query deploy -o json
```

## `sparkwing pipeline explain`

Render the pipeline's Plan DAG without dispatching any jobs

Compiles the nearest .sparkwing/ binary, calls the named
pipeline's Plan method, and prints the resulting DAG (nodes,
dependencies, approval gates) without running a single job.

Any --flag value tokens that are NOT recognized by explain itself
(i.e. anything other than --name / --all / -o/--output / --help) are
forwarded to the pipeline so Plans that branch on --env / --version
/ etc. can be previewed under realistic inputs. Missing required
args are non-fatal here -- explain renders a best-effort plan so
the shape is visible before every flag is provided.

--all sweeps every pipeline in .sparkwing/sparkwing.yaml, runs
Plan() on each with no extra args, and exits non-zero if any
pipeline fails. Designed as a CI gate: a Plan-time validation
mismatch (sparkwing.RefTo[T] type drift, Produces[T] / SetResult
asymmetry, duplicate node ID, etc.) blocks merges before the
pipeline ever runs.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Pipeline to explain (one of --name or --all required) |
| `--all` | Validate every pipeline in this repo's sparkwing.yaml; non-zero exit on any failure |
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Inspect release-all's DAG
sparkwing pipeline explain --name release-all

# Preview with args (forwarded to the pipeline)
sparkwing pipeline explain --name example-release --env prod

# Agent-readable JSON
sparkwing pipeline explain --name release-all -o json

# Validate every pipeline (CI gate)
sparkwing pipeline explain --all
```

## `sparkwing pipeline hooks`

Install / uninstall git pre-commit + pre-push + post-commit hooks

Writes small git hook scripts into the repo's .git/hooks/
directory that call 'sparkwing run <pipeline>' for every pipeline that
declares pre_commit:, pre_push:, or post_commit: in its
.sparkwing/sparkwing.yaml triggers block.

The post-commit hook is non-blocking: the commit has already
landed, so it runs its pipelines, tolerates failures, and never
aborts. pre-commit and pre-push abort the git action on the first
failing pipeline.

Managed hooks carry a "Installed by sparkwing" marker so
uninstall and status can tell them apart from hand-written
hooks. Existing unmanaged hooks are left alone; install skips
them with a warning.

### Subcommands

- `install` -- Write pre-commit / pre-push / post-commit hooks for the enclosing repo
- `uninstall` -- Remove sparkwing-managed git hooks
- `status` -- Report which sparkwing hooks are installed

## `sparkwing pipeline hooks install`

Install pre-commit / pre-push / post-commit git hooks from sparkwing.yaml triggers

Discovers the enclosing .sparkwing/sparkwing.yaml, reads
pre_commit / pre_push / post_commit triggers, and writes one hook
file per hook name that fans out to the matching pipelines. Existing
non-sparkwing hooks are skipped so hand-written ones survive.

### Flags

| Flag | Description |
|---|---|
| `--repo DIR` | Repo directory (default: discovered via nearest .sparkwing/) |

### Examples

```sh
# Install in the current repo
sparkwing pipeline hooks install

# Install in a different repo
sparkwing pipeline hooks install --repo /path/to/repo
```

## `sparkwing pipeline hooks status`

Report which sparkwing hooks are installed

Lists every managed hook file under .git/hooks/ along with the pipelines it invokes. Prints a hint when nothing is installed.

### Flags

| Flag | Description |
|---|---|
| `--repo DIR` | Repo directory (default: discovered via nearest .sparkwing/) |

### Examples

```sh
# Show hook status
sparkwing pipeline hooks status
```

## `sparkwing pipeline hooks uninstall`

Remove sparkwing-managed git hooks

Deletes every file under .git/hooks/ that carries the "Installed by sparkwing" marker. Hand-written hooks are left alone.

### Flags

| Flag | Description |
|---|---|
| `--repo DIR` | Repo directory (default: discovered via nearest .sparkwing/) |

### Examples

```sh
# Uninstall in the current repo
sparkwing pipeline hooks uninstall
```

## `sparkwing pipeline lint`

Check pipeline source for idiomatic anti-patterns (enforced gate)

Statically analyzes pipeline source for the anti-patterns
that make a Plan() non-deterministic, impure, or misconfigured,
and exits non-zero on any violation. Unlike 'explain' (which
builds and runs Plan to validate the resulting DAG), 'lint' reads
the Go source with go/ast -- it never compiles or runs anything,
so it works against a pinned-SDK .sparkwing/ tree.

Only the Plan() body is inspected; code inside job/step closures
and SkipIf / BeforeRun bodies runs at dispatch, so I/O and
environment reads there are idiomatic and never flagged.

The rule set (see --rules for each rule's charter):
  plan-io              I/O (shell, exec, file, http) in Plan()
  plan-runtime-branch  os.Getenv / runtime.GOOS / IsLocal branching in Plan()
  runner-label         blank runner labels; Inline + Requires on one job
  unused-ref           a RefTo result discarded into _ or a bare statement
  guard-misuse         pipeline guards that can never be satisfied together

--all sweeps every pipeline in .sparkwing/sparkwing.yaml and exits
non-zero if any violates a rule -- designed as a CI gate alongside
'explain --all'. --name lints a single pipeline. Source defaults
to <.sparkwing>/jobs; override with --dir.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Pipeline to lint (one of --name or --all required) |
| `--all` | Lint every pipeline in this repo's sparkwing.yaml; non-zero exit on any violation |
| `--rules` | Print each rule's charter (what it forbids and why) and exit |
| `--dir DIR` | Directory of pipeline source to scan (default: <.sparkwing>/jobs) |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `-C, --sw-cd DIR` | Operate as if started in this directory (re-anchors the .sparkwing search) |

### Examples

```sh
# Lint one pipeline
sparkwing pipeline lint --name release

# Lint every pipeline (CI gate)
sparkwing pipeline lint --all

# Agent-readable findings
sparkwing pipeline lint --all -o json

# Show the rule set
sparkwing pipeline lint --rules
```

## `sparkwing pipeline list`

Enumerate every pipeline with metadata

Walks up from the current directory to locate .sparkwing/,
merges sparkwing.yaml entries with the describe cache's typed
metadata, and prints a grouped aligned table.

-o json emits structured records instead; agents should prefer
-o json since tab-complete / table output is for human reading.

--all includes entries marked 'hidden: true'. By default they're
omitted.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `--all` | Include entries marked hidden |

### Examples

```sh
# Human-readable table
sparkwing pipeline list

# Agent-readable catalog
sparkwing pipeline list -o json

# Include hidden entries
sparkwing pipeline list --all
```

## `sparkwing pipeline new`

Scaffold a new Go pipeline

Creates a stub pipeline in the nearest .sparkwing/:
jobs/<snake>.go plus a sparkwing.yaml entry. Auto-bootstraps
.sparkwing/ on first use, so a fresh repo's first scaffold sets
up the package skeleton too -- no separate init step, no
sample pipeline you didn't ask for.

Before building by hand, browse the ready-made starters:
'sparkwing pipeline templates' lists task-shaped registry templates
(Go CI hygiene, docker/static deploys for AWS+GCP, migrations, ...);
scaffold one with --template <name> [--param k=v ...].

Pass --sw-cd/-C to scaffold into a repo other than the current
directory (the .sparkwing search re-anchors there).

Built-in templates (registry templates are listed by 'pipeline templates'):
  - minimal (default): single-node Plan with a stubbed Run.
    Smallest viable shape; the editor's first move is replacing
    the placeholder Info() line with real logic.
  - build-test-deploy: three-node Plan (build -> test -> deploy)
    with echo Run bodies that print a placeholder line on each step.
    The canonical CI shape; first 'sparkwing run <name>' surfaces three
    exec banners + three echoed lines so the structure is
    visible end-to-end.
  - ci-pr-check: pull-request gate. lint and test run in parallel and
    a final gate job depends on both, so the pipeline is green only
    when every check passes. test Prefers a CI runner label.
  - release: linear version-bump -> changelog -> publish flow. The
    canonical release shape; publish Prefers a release runner label.
  - scheduled-report: fan-out report. One collect job seeds three
    parallel gatherers (metrics, errors, usage) and publish-report
    converges them. Prints the sparkwing.yaml 'on:' schedule trigger
    to add for cron runs.

Each built-in template scaffolds a pipeline that compiles, renders
clean under 'pipeline explain', and passes 'pipeline lint': pure
Plan(), runner-label preferences over host branching, echo Run bodies
so the first 'sparkwing run <name>' succeeds end-to-end.

Refuses to clobber: if the name already exists in sparkwing.yaml
the command fails before writing anything.

Supply --hidden to hide from default listings; --short to pre-fill
the description.

See also:
  If your pipeline is a single linear shell sequence with no DAG,
  retry, or cross-runner concerns, a plain shell-script runner
  (e.g. just / make / a wrapper over ./bin/*.sh) is probably a
  better fit -- it skips the compile cycle.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | New pipeline's kebab-case name (a-z, 0-9, -) (required) |
| `-C, --sw-cd DIR` | Scaffold as if started in this directory (re-anchors the .sparkwing search) |
| `--template KIND` | minimal \| build-test-deploy \| ci-pr-check \| release \| scheduled-report \| any registry name from `sparkwing pipeline templates` (default: minimal) |
| `--param K=V` | Registry template parameter (repeatable); see `sparkwing pipeline templates` |
| `--hidden` | Mark the entry hidden in default tab-complete menus |
| `--short TEXT` | Pre-fill the ShortHelp / desc line (built-in templates only) |

### Examples

```sh
# Single-node pipeline (default template)
sparkwing pipeline new --name release

# Build/test/deploy DAG (three-node)
sparkwing pipeline new --name release-all --template build-test-deploy

# Pull-request gate (lint + test -> gate)
sparkwing pipeline new --name pr-check --template ci-pr-check

# Scheduled fan-out report
sparkwing pipeline new --name daily-report --template scheduled-report

# From a registry template
sparkwing pipeline new --name deploy --template go-test-build-deploy-k8s --param image=myapp --param namespace=myapp --param app-name=myapp --param health-url=http://myapp.myapp.svc:8080/health
```

## `sparkwing pipeline plan`

Render the runtime-resolved DAG without dispatching any jobs

Compiles the nearest .sparkwing/ binary, calls the named
pipeline's Plan method, and prints the runtime-resolved DAG --
the same structure 'explain' shows plus a per-step decision
("would_run" / "would_skip <reason>") evaluated under the
supplied args and --start-at / --stop-at bounds. NO step bodies
execute.

Skip reasons surface their cause:
  - user_skipif    : a SkipIf predicate would match at run time
  - range_skip     : item is outside the --start-at..--stop-at window

For SpawnNodeForEach generators (dynamic fan-out), cardinality is
reported as "unresolved" with a pointer to the source item -- the
honest answer when the count depends on a runtime value.

State-loading caveat: if a step normally populates in-memory state
that downstream steps consume, --start-at past it leaves state
empty. The plan output reflects this honestly (downstream steps
show "would_run") but operators should design step bodies to
lazy-load when state isn't populated, so resume-from-step is safe.

Like 'explain', this is the read-only pre-flight surface; pair
with 'sparkwing run <name>' to actually dispatch.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Pipeline to plan |
| `--start-at STEP` | Skip every WorkStep upstream of STEP in the resulting plan |
| `--stop-at STEP` | Skip every WorkStep downstream of STEP in the resulting plan |
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Resolve cluster-up's DAG with current args
sparkwing pipeline plan --name cluster-up

# Preview a resume-from-step
sparkwing pipeline plan --name cluster-up --start-at install-argocd

# Agent-readable JSON for diff against expectations
sparkwing pipeline plan --name release-all -o json
```

## `sparkwing pipeline run`

Invoke a pipeline (canonical form of `sparkwing run <name>`)

Compiles the nearest .sparkwing/ binary and exec's it
with the named pipeline. Identical to the top-level shortcut
'sparkwing run <name>'.

The pipeline name is the only positional in the sparkwing
surface -- a deliberate exception, kept short because run is
typed many times a day.

Any flag not recognized by run itself is forwarded to the
pipeline binary, e.g. 'sparkwing pipeline run release
--version v1.2.3' passes --version through to the pipeline's
Args.

### Arguments

- `<pipeline>` (required) -- Pipeline name registered in .sparkwing/sparkwing.yaml

### Flags

| Flag | Description |
|---|---|
| `-C, --sw-cd PATH` | Run as if started in PATH |
| `--sw-ref REF` | Run the pipeline at REF (branch/tag/SHA) instead of the working tree |
| `-v, --sw-verbose` | Enable debug logging |
| `--sw-start-at STEP` | Start the run at STEP |
| `--sw-stop-at STEP` | Stop the run after STEP |
| `--sw-only GLOB` | Run only jobs whose ID matches GLOB (plus their Needs ancestors) |
| `--sw-no-cache` | Ignore cached per-node results (writes still happen) |
| `--sw-local-only` | Force local state, cache, and logs for this run; ignore any configured shared backends |
| `--sw-dry-run` | Run each step's dry-run probe instead of its real action |
| `--sw-allow LABEL[,LABEL...]` | Authorize risk-labeled steps (repeatable) |
| `--profile NAME` | Run / read against the named profile from ~/.config/sparkwing/profiles.yaml (default: laptop) |
| `--target TARGET` | Run against the named pipeline deployment target (e.g. dev, prod) |

### Examples

```sh
# Run with no flags
sparkwing pipeline run build-test-deploy

# Pass a typed pipeline arg
sparkwing pipeline run release --version v0.28.1

# Run from a different git ref
sparkwing pipeline run build-test-deploy --sw-ref feature/xyz

# Dispatch remotely
sparkwing pipeline trigger deploy --profile prod
```

## `sparkwing pipeline sparks`

Manage sparks libraries declared in .sparkwing/sparks.yaml

Sparks libraries are Go modules that add opinionated helpers
(Docker builds, GitOps deploys, ECR auth, language-specific
checks) on top of the unopinionated SDK. Consumers declare
which libraries they want live-tracked in
.sparkwing/sparks.yaml; the resolver writes an overlay modfile
at .sparkwing/.resolved.mod that the compile step uses via
'go build -modfile='. The consumer's git-tracked go.mod is
never modified.

See docs/sparks.md for the full spec (spark.json schema,
sparks.yaml shape, resolution rules, warmup).

### Subcommands

- `list` -- Show declared libraries and resolved versions
- `lint` -- Validate a spark.json library manifest
- `resolve` -- Resolve versions and materialize the overlay modfile
- `update` -- Re-resolve one or all libraries
- `add` -- Add a library to sparks.yaml
- `remove` -- Remove a library from sparks.yaml
- `warmup` -- Pre-compile pipeline binaries and upload to gitcache

### Examples

```sh
# List declared sparks libraries
sparkwing pipeline sparks list

# Validate a library's spark.json
sparkwing pipeline sparks lint ~/code/sparks-core

# Re-materialize the overlay modfile
sparkwing pipeline sparks resolve

# Add a library pinned to latest
sparkwing pipeline sparks add github.com/sparkwing-dev/sparks-core
```

## `sparkwing pipeline sparks add`

Add a library to sparks.yaml

Appends a new entry to .sparkwing/sparks.yaml. Defaults the
version to 'latest' when --version is omitted. Refuses to add
a duplicate (same source or same name).

### Flags

| Flag | Description |
|---|---|
| `--source PATH` | Go module path (e.g. github.com/user/sparks-lib) (required) |
| `--version VER` | Declared version ('latest', exact tag, or semver range) |
| `--name NAME` | Short library name (default: last path segment of --source) |
| `--sparkwing-dir DIR` | Path to .sparkwing/ (default: <cwd>/.sparkwing) |

### Examples

```sh
# Add a library pinned to latest
sparkwing pipeline sparks add --source github.com/sparkwing-dev/sparks-core

# Add with a semver range
sparkwing pipeline sparks add --source github.com/sparkwing-dev/sparks-core --version "^v0.10.0"
```

## `sparkwing pipeline sparks lint`

Validate a spark.json library manifest

Loads spark.json from the given path (or the current directory
if omitted) and checks: required fields (name, description,
author, packages), that each packages[] path exists as a
directory under the module root, stability values are valid,
and package paths are not duplicated. Unknown fields are a
soft warning, not an error. Exits non-zero on any hard
failure.

### Flags

| Flag | Description |
|---|---|
| `--path PATH` | Library directory or direct spark.json path (default: .) |

### Examples

```sh
# Lint the library in the current directory
sparkwing pipeline sparks lint

# Lint a sibling library by path
sparkwing pipeline sparks lint --path ~/code/sparks-core
```

## `sparkwing pipeline sparks list`

Show declared sparks libraries and their resolved versions

Reads .sparkwing/sparks.yaml and prints one row per declared
library with its declared constraint and the resolved tag
(found via the module proxy). Use --no-resolve to skip the
proxy calls when offline.

### Flags

| Flag | Description |
|---|---|
| `--sparkwing-dir DIR` | Path to .sparkwing/ (default: <cwd>/.sparkwing) |
| `-o, --output FMT` | Output format: pretty\|json\|plain |
| `--no-resolve` | Skip module-proxy lookups; print declared versions only |

### Examples

```sh
# Table output
sparkwing pipeline sparks list

# JSON for scripting
sparkwing pipeline sparks list -o json

# Offline (no proxy calls)
sparkwing pipeline sparks list --no-resolve
```

## `sparkwing pipeline sparks remove`

Remove a library from sparks.yaml

Removes the entry matching NAME (or matching its source path).

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Library name or source path to remove (required) |
| `--sparkwing-dir DIR` | Path to .sparkwing/ (default: <cwd>/.sparkwing) |

### Examples

```sh
# Remove by short name
sparkwing pipeline sparks remove --name sparks-core

# Remove by source path
sparkwing pipeline sparks remove --name github.com/sparkwing-dev/sparks-core
```

## `sparkwing pipeline sparks resolve`

Resolve versions and materialize the overlay modfile

Runs the same pipeline as 'sparkwing run <name>' takes before compile:
load sparks.yaml, resolve each entry against the module proxy,
and write .sparkwing/.resolved.mod + .resolved.sum. Idempotent
-- a second run with no upstream change is a fast no-op that
prints 'up-to-date'. Never modifies the git-tracked go.mod.

### Flags

| Flag | Description |
|---|---|
| `--sparkwing-dir DIR` | Path to .sparkwing/ (default: <cwd>/.sparkwing) |
| `-q, --quiet` | Suppress the 'up-to-date' message |

### Examples

```sh
# Resolve and write the overlay
sparkwing pipeline sparks resolve

# Quiet mode for scripts
sparkwing pipeline sparks resolve -q
```

## `sparkwing pipeline sparks update`

Re-resolve one or all libraries

Re-runs resolution for every declared library (or a single
named one) and re-materializes the overlay modfile. For a
range or 'latest' constraint this picks up any new tag from
the module proxy; for an exact pin it is a no-op.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Restrict update to one library (name or source); omit to update all |
| `--sparkwing-dir DIR` | Path to .sparkwing/ (default: <cwd>/.sparkwing) |

### Examples

```sh
# Update every declared library
sparkwing pipeline sparks update

# Update one by name
sparkwing pipeline sparks update --name sparks-core
```

## `sparkwing pipeline sparks warmup`

Pre-compile pipeline binaries after a sparks release

Post-release optimization: resolve the latest versions, compile
the pipeline binary for the current .sparkwing/ tree, and
upload to gitcache so the next 'sparkwing run' in-cluster or on a
fresh laptop gets a cache hit instead of paying the full
compile cost.

Uses the exact same build path as 'sparkwing run', so the cache key
matches. Warmup is optional -- pipelines always resolve on
build -- it just removes the first-run compile cost after a
new sparks version is published.

### Flags

| Flag | Description |
|---|---|
| `--sparkwing-dir DIR` | Path to .sparkwing/ (default: <cwd>/.sparkwing) |
| `--clear-cache` | Delete the local pipeline binary cache before compiling |

### Examples

```sh
# Warm up the current repo's pipelines
sparkwing pipeline sparks warmup

# Force a fresh compile
sparkwing pipeline sparks warmup --clear-cache
```

## `sparkwing pipeline templates`

List the sparks-core template registry

Lists the curated, parameterized pipeline starters in the
sparks-core/templates registry -- the values usable as
'sparkwing pipeline new --template <name>'. Each entry shows a
"when to use" signal and its required / optional parameters.

These are distinct from the two built-in stubs (minimal,
build-test-deploy) that ship in the CLI itself: the registry
templates are richer, real-world shapes (build-test-deploy to
k8s, static-site, migrate+deploy, ...).

-o json emits the manifests (name, description, whenToUse,
parameters, applicability) -- prefer it for agent consumption.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Browse the registry
sparkwing pipeline templates

# Agent-readable manifests
sparkwing pipeline templates -o json

# Scaffold from one
sparkwing pipeline new --name deploy --template go-test-build-deploy-k8s --param image=myapp
```

## `sparkwing pipeline trigger`

Submit a pipeline to a profile's controller (remote execution)

Submits a trigger to the controller defined by --profile and
follows the remote run until it reaches a terminal state.

When the profile defines a logs URL, the follow streams full log
output; otherwise it shows node-status updates from the
controller. --detach skips the follow and prints the run id once
the trigger is registered (the trigger POST itself always
completes before the command exits, so the run is guaranteed
queued).

Any flag not recognized here is forwarded to the pipeline as a
typed Arg, e.g. 'sparkwing pipeline trigger release --profile
prod --version v1.2.3' passes --version through to the trigger
payload -- same shape as 'sparkwing run'.

Requires a profile with controller: set. For local execution
against a profile's storage, use 'sparkwing run --profile X'.

### Arguments

- `<pipeline>` (required) -- Pipeline name registered on the controller

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile (from ~/.config/sparkwing/profiles.yaml) whose controller runs the pipeline (required) |
| `--detach` | Return once the trigger is registered (print the run id); don't follow |

### Examples

```sh
# Submit and follow
sparkwing pipeline trigger release --profile prod --version v1.2.3

# Fire-and-forget; print run id and exit
sparkwing pipeline trigger release --profile prod --detach
```

## `sparkwing profile`

Show which profile sparkwing would use right now, and why

Reports the profile a sparkwing command would resolve to and
the chain that picked it (flag > project hint > detect > default
\> builtin laptop), using the same resolver 'sparkwing run' and
'sparkwing pipeline trigger' use -- so the answer matches what
they would actually do.

With no flag it shows the active no-flag resolution. With
--profile NAME it shows the hypothetical: what adding that flag
to your next command would select. Tokens are never printed.

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Show the hypothetical resolution for `--profile NAME` |
| `-o, --output FORMAT` | Output format: pretty\|json (default: pretty) |

### Examples

```sh
# Active profile with no flag
sparkwing profile

# What would --profile prod pick
sparkwing profile --profile prod

# Machine-readable
sparkwing profile -o json
```

## `sparkwing queue`

The truthful view of local admission: holders, waiters, and why

Reads the local admission daemon and prints one honest picture of
where every run stands: each resource (host cores, memory, and every
named concurrency semaphore) with its capacity and how much is in use;
every run currently holding admission, with the repo it came from, how
long it has held, and what it is charged; and every waiter in arrival
order, with its position, its cost, and exactly what it is waiting on.
A child run attached to its parent's lease renders indented under that
parent. The header carries a one-line summary of the daemon's recent
admission outcomes -- runs granted, median wait, evictions, queue
timeouts -- so chronic patterns show up at a glance.

A holder that is alive but has burned near-zero CPU while runs queue
behind it is flagged as stalled, together with the exact command to
clear it -- 'sparkwing runs cancel --run <id>'. The queue never kills a
run for you and never points at a host-wide destructive verb.

Pretty on a terminal, JSON when piped (add -o json to force it), and
one tab-separated record per line with -o plain for shell pipelines.

When no daemon is running there is nothing to arbitrate: the command
reports an empty queue and exits 0 rather than erroring.

With --profile NAME the view switches to that profile's controller: the
same renderer prints the controller's admission state -- every
concurrency key, its holders and waiters, and each registered runner's
free capacity -- so one vocabulary reads local and cluster admission
alike.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain |
| `--home DIR` | Sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing) |
| `--profile NAME` | Inspect this profile's controller instead of the local daemon |

### Examples

```sh
# Show the current queue
sparkwing queue

# Agent-readable snapshot
sparkwing queue -o json

# One record per line for shell pipelines
sparkwing queue -o plain

# Inspect a controller's admission state
sparkwing queue --profile prod
```

## `sparkwing repos`

The machine's fleet of sparkwing repos and their SDK pins

Lists every repo on this machine that carries sparkwing
pipelines -- derived from the repos this laptop has run pipelines
for, unioned with the explicit repos.yaml registry. No manual
registration: a repo shows up once it has run a pipeline or been
added to repos.yaml.

Each row reports the repo, its .sparkwing SDK pin, the last run
observed, and how many migration guides sit between its pin and
the latest release. Linked git worktrees are folded into their
primary checkout; a worktree pinned differently from its primary
is reported as a detail line, not a separate repo.

Bare 'sparkwing repos' and 'sparkwing repos list' both print this
fleet. Use 'sparkwing repos info' for a single-repo deep dive, and
'sparkwing repos update' to bump the whole fleet in one sitting
with a compiled per-repo verdict.

### Subcommands

- `list` -- List the fleet (the same as bare 'sparkwing repos')
- `info` -- Deep dive on one repo: pin, guides, worktrees, schema, pipelines
- `update` -- Bump every repo's SDK pin with a compiled per-repo verdict

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |

### Examples

```sh
# List the fleet
sparkwing repos

# Agent-readable record
sparkwing repos -o json
```

## `sparkwing repos info`

Deep dive on one repo: pin, guides, worktrees, schema, pipelines

Reports everything worth knowing about one repo without
stitching it together from git, go.mod, and run history by hand. It
defaults to the repo containing the current directory; --repo names
another fleet member by name or checkout path.

It shows the .sparkwing SDK pin (or replace directive) against the
latest release, the migration guides in between with their titles
and summaries, linked worktrees and any that pin a different
version, the working tree's branch, commit, and clean/dirty state,
whether the pin can open the machine's shared state database (a
mismatch is caught here rather than when a run fails), and the
repo's pipelines with their last run time and status. When
something is off it prints one suggested next step.

Read-only: it never builds, bumps, or commits anything.

### Flags

| Flag | Description |
|---|---|
| `--repo NAME_OR_PATH` | Repo by name or checkout path. Default: the repo containing the current directory. |
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Deep dive on the current repo
sparkwing repos info

# Deep dive on a named repo
sparkwing repos info --repo my-app

# Agent-readable record
sparkwing repos info --repo my-app -o json
```

## `sparkwing repos list`

List the machine's fleet of sparkwing repos

Prints the fleet: every repo on this machine that carries
sparkwing pipelines, with its SDK pin, last run, and how many
migration guides sit between its pin and the latest release. This is
the same output as bare 'sparkwing repos'; the explicit verb exists
so the listing has a name alongside 'info' and 'update'.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |

### Examples

```sh
# List the fleet
sparkwing repos list

# Agent-readable record
sparkwing repos list -o json
```

## `sparkwing repos update`

Bump the fleet's SDK pins with a compiled per-repo verdict

Bumps every tracked repo's .sparkwing SDK pin to a target
release and reports a compiled verdict per repo. For each repo with
a clean working tree it bumps the pin, runs go mod tidy, and
plan-constructs every registered pipeline before and after the
bump:

  - clean: the bump compiled and every plan is byte-identical --
    a guaranteed no-behavior-change upgrade.
  - plan-differs: the bump compiled but a plan changed shape; the
    structured node/dep/step diff is shown.
  - broken: the bump failed to apply, compile, or verify; the
    actual error is shown with the crossed migration guides.

Dirty or missing repos are skipped and named rather than guessed
at. Dry-run by default: nothing is written. --apply commits the
bump per repo with a conventional message (no pushes). --verify
additionally runs each repo's pre-commit gate after the bump.
--repo scopes to one repo by name or path.

Because a shared state database refuses an older pin against a
migrated schema, the fleet is meant to move together; the report
leads with that when pins would diverge.

### Flags

| Flag | Description |
|---|---|
| `--version TAG` | Target SDK release (e.g. v0.16.0). Default: latest. |
| `--apply` | Write the bumps and commit per repo (default is a dry run) |
| `--verify` | Run each repo's pre-commit gate after the bump |
| `--repo NAME_OR_PATH` | Scope to a single repo by name or checkout path |
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Preview a fleet-wide bump to latest (dry run)
sparkwing repos update

# Preview a bump to a specific release
sparkwing repos update --version v0.16.0

# Apply the bump and commit per repo
sparkwing repos update --version v0.16.0 --apply

# Scope to one repo and run its gate
sparkwing repos update --repo my-app --verify
```

## `sparkwing run`

Invoke a pipeline

Compiles the nearest .sparkwing/ binary and exec's it
with the named pipeline.

The pipeline name is the only positional in the sparkwing
surface -- a deliberate exception, kept short because run is
typed many times a day. Every other input is a named flag.

Any flag not recognized by run itself is forwarded to the
pipeline binary, e.g. 'sparkwing run release --version
v1.2.3' passes --version through to the pipeline's Args.

For remote execution on a profile's controller, use
'sparkwing pipeline trigger <name> --profile PROF'.

Output: a human-readable per-node summary when stdout is a
terminal, line-delimited JSON otherwise (so piped/agent/CI
consumers get a stable JSONL stream). Force a format with
SPARKWING_LOG_FORMAT=pretty|json|quiet. quiet collapses the
run to a progress line plus a one-line pass/fail status with
the run id, surfacing the failing step only on failure; it is
the default for managed git hooks.

### Arguments

- `<pipeline>` (required) -- Pipeline name registered in .sparkwing/sparkwing.yaml

### Flags

| Flag | Description |
|---|---|
| `-C, --sw-cd PATH` | Run as if started in PATH |
| `--sw-ref REF` | Run the pipeline at REF (branch/tag/SHA) instead of the working tree |
| `-v, --sw-verbose` | Enable debug logging |
| `--sw-start-at STEP` | Start the run at STEP |
| `--sw-stop-at STEP` | Stop the run after STEP |
| `--sw-only GLOB` | Run only jobs whose ID matches GLOB (plus their Needs ancestors) |
| `--sw-no-cache` | Ignore cached per-node results (writes still happen) |
| `--sw-local-only` | Force local state, cache, and logs for this run; ignore any configured shared backends |
| `--sw-dry-run` | Run each step's dry-run probe instead of its real action |
| `--sw-allow LABEL[,LABEL...]` | Authorize risk-labeled steps (repeatable) |
| `--profile NAME` | Run / read against the named profile from ~/.config/sparkwing/profiles.yaml (default: laptop) |
| `--target TARGET` | Run against the named pipeline deployment target (e.g. dev, prod) |

### Examples

```sh
# Run with no flags
sparkwing run build-test-deploy

# Pass a typed pipeline arg
sparkwing run release --version v0.28.1

# Run from a different git ref
sparkwing run build-test-deploy --sw-ref feature/xyz

# Retry a failed run
sparkwing runs retry RUN_ID --failed

# Submit to a remote controller
sparkwing pipeline trigger deploy --profile prod
```

## `sparkwing run config`

Print the resolved Config struct + declared Secrets for a pipeline + target

Pure inspection: resolves the pipeline's typed Config
struct through the same layering `sparkwing run` uses
(struct defaults < sparkwing.yaml values.base < per-target values)
and prints each field's resolved value alongside which layer
contributed it. Also lists every declared Secret with its source
binding -- useful before driving destructive `--for prod`
runs to confirm you'd hit the right vault.

Honors --for (the target selection). No Plan() runs, nothing
dispatches, nothing mutates.

Invocation: `sparkwing run <pipeline> config --for <target>` --
the pipeline binary handles the subverb directly.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Inspect the staging config
sparkwing run release config --for staging

# Agent-readable form
sparkwing run release config --for prod -o json
```

## `sparkwing runs`

Inspect and control pipeline runs

Runs are the per-invocation records of pipeline execution.
Every 'sparkwing run <pipeline>' produces a run; cluster mode surfaces
the same runs remotely via the controller.

Local-mode subcommands (list, status, logs, errors) read from
~/.sparkwing/runs/. Controller-mode subcommands (cancel, retry,
prune) require a profile; 'jobs logs' supports both.

### Subcommands

- `list` -- List recent runs with filters (pipeline, status, branch, sha, search, etc.)
- `status` -- Show a single run's status (with per-step + approval state)
- `summary` -- Aggregated work view: groups, work items, modifiers, annotations
- `timeline` -- ASCII waterfall of a run's nodes (and steps)
- `wait` -- Block until a run reaches a terminal status
- `find` -- Find runs matching a git SHA / repo / pipeline filter
- `grep` -- Search log bodies across recent runs for a substring
- `logs` -- Print a run's logs (optionally --follow)
- `errors` -- Surface the error trail for a failed run
- `failures` -- List recent failed runs; optional clustering by step
- `stats` -- Aggregate stats (pass/fail, success %, avg/p95 duration)
- `last` -- Show the most recent run; --watch tails new runs
- `tree` -- ASCII tree of a run and every descendant run
- `get` -- Emit one run's raw JSON (run + nodes)
- `receipt` -- Emit a run's audit + cost receipt as JSON
- `annotations` -- Read or append persistent node + step annotations
- `approvals` -- List, approve, or deny approval gates
- `triggers` -- Inspect trigger envelopes that produced runs
- `retry` -- Trigger fresh runs copying pipeline + args from old ones
- `cancel` -- Request cancellation of in-flight runs
- `prune` -- Delete finished runs older than a threshold, or by id

## `sparkwing runs annotations`

Read or append persistent node + step annotations

Annotations are short summary strings that pipelines (via
sparkwing.Annotate) and agents append to a node or step during a
run. They show up on the dashboard alongside outcome. This verb
lets an agent read every annotation on a run or contribute one
without going through the SDK.

### Subcommands

- `list` -- List annotations on a run (optionally filtered to a node/step)
- `add` -- Append one annotation to a node or step

## `sparkwing runs annotations add`

Append an annotation to a node or step

Appends one message to the annotations list on a node, or on a
step when --step is given. Annotations are append-only; the same
message string can be added more than once and the order is
preserved as the dashboard renders them.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `--node NODE_ID` | Node identifier (required) |
| `--step STEP_ID` | Step identifier (annotates the step instead of the node) |
| `-m, --message TEXT` | Annotation text (required) |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Note something on a node
sparkwing runs annotations add --run run-... --node deploy -m 'agent: retried after 502'

# Note something on a step inside a node
sparkwing runs annotations add --run run-... --node deploy --step canary -m 'rolled out 5%'
```

## `sparkwing runs annotations list`

List annotations on a run

Prints node-level annotations by default. Pass --steps to also
include per-step annotations as separate rows; passing --step
implies step-scope and limits to the matching step.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `--node NODE_ID` | Limit to one node |
| `--step STEP_ID` | Limit to one step (implies step-scope reads) |
| `--steps` | Include per-step annotations |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Every node annotation on a run
sparkwing runs annotations list --run run-...

# Include per-step annotations
sparkwing runs annotations list --run run-... --steps

# One node's annotations as JSON
sparkwing runs annotations list --run run-... --node build -o json
```

## `sparkwing runs approvals`

List approval gates (pending and history)

Inspect approval gates. Without --run returns every pending
gate across all runs; with --run returns one run's full history
(pending + resolved).

### Subcommands

- `list` -- List pending approvals, or one run's history with --run (the default verb)
- `approve` -- Approve a pending gate: --run <id> --node <id> [--comment ...]
- `deny` -- Deny a pending gate: --run <id> --node <id> [--comment ...]

## `sparkwing runs approvals approve`

Approve a pending approval-gate node

Resolves the named approval gate as 'approved'. The gate's
downstream nodes begin dispatching on the next orchestrator
poll (roughly 500ms). The approver is recorded from the
authenticated principal when --profile is set, or from $USER in
local mode.

Exit code is 0 on success, non-zero if the gate doesn't exist
or was already resolved (409).

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the approval gate (required) |
| `--node ID` | Node ID of the approval gate (required) |
| `--comment STR` | Optional note recorded on the approval |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Approve a local gate
sparkwing runs approvals approve --run run-20260423-143012-abcd --node approve-prod

# Approve a prod gate with a comment
sparkwing runs approvals approve --run run-... --node approve-prod --profile prod --comment "release notes ok"
```

## `sparkwing runs approvals deny`

Deny a pending approval-gate node

Resolves the named approval gate as 'denied'. The gated node
fails; downstream nodes see the failure and propagate per
their ContinueOnError / Optional settings.

### Flags

| Flag | Description |
|---|---|
| `--run ID` | Run ID holding the approval gate (required) |
| `--node ID` | Node ID of the approval gate (required) |
| `--comment STR` | Optional note recorded on the approval |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Deny a local gate
sparkwing runs approvals deny --run run-20260423-143012-abcd --node approve-prod

# Deny a prod gate with a reason
sparkwing runs approvals deny --run run-... --node approve-prod --profile prod --comment "tests still red"
```

## `sparkwing runs approvals list`

List pending approvals (or one run's history)

Prints a table of approval rows. Without --run the list is the
cross-run pending queue; with --run it's every approval for that
run, both pending and resolved.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Restrict to one run's approvals |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Pending gates on the local store
sparkwing runs approvals list

# Pending gates on prod
sparkwing runs approvals list --profile prod

# Full history for one run
sparkwing runs approvals list --run run-...

# Emit JSON for an agent
sparkwing runs approvals list -o json
```

## `sparkwing runs cancel`

Request cancellation of in-flight runs

Sends a cancel request per run to the controller. Each run
transitions to 'cancelling' and then 'cancelled' once the runner
acknowledges. Already-finished runs surface a per-id error but
don't abort the batch.

Pass --run once per id (repeatable). Use --run - to read ids
from stdin, one per line.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run id to cancel (repeatable; use --run - to read ids from stdin) |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Cancel one run
sparkwing runs cancel --run run-... --profile prod

# Cancel every running prod run
sparkwing runs list --status running --profile prod -q | sparkwing runs cancel --run - --profile prod
```

## `sparkwing runs errors`

Surface the error trail for a failed run

Walks the run's node DAG and prints the error chain for any node that failed. Quicker than paging through full logs when you only care about the terminal failure. Reads from the local run store.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |

### Examples

```sh
# Inspect a local failure
sparkwing runs errors --run run-20260422-142501-abcd

# As JSON
sparkwing runs errors --run run-... -o json
```

## `sparkwing runs failures`

List recent failed runs, optionally clustered

Fetches recent runs with status=failed and extracts the first failing node's step + error message for each. --group-by clusters the output by step so a systemic failure surfaces as one row with a count.

### Flags

| Flag | Description |
|---|---|
| `--pipeline NAME` | Restrict to one pipeline |
| `--since DURATION` | Only failures newer than this (e.g. 24h, 7d) |
| `--limit N` | Max failures to analyze (default: 20) |
| `--group-by KEY` | Cluster by: step \| node |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Recent local failures
sparkwing runs failures --since 24h

# Prod failures clustered by step
sparkwing runs failures --profile prod --group-by step
```

## `sparkwing runs find`

Find runs by git SHA / repo / pipeline filter

Searches recent runs for a match. Use --git-sha to find
the run that was fired by a specific commit; add --pipeline to
disambiguate when multiple pipelines respond to the same push. --repo
matches the GITHUB_REPOSITORY env on the trigger (owner/name), useful
when one controller handles webhooks from multiple repos.

With --wait, blocks until at least one match appears, up to
--find-timeout. Pairs with 'jobs wait' for the push-and-follow loop:

  git push && \
  sparkwing runs find --git-sha $(git rev-parse HEAD) --pipeline X --wait --profile prod -q | \
    xargs -n1 -I{} sparkwing runs wait --run {} --profile prod

Exit code 0 on match, non-zero on timeout-without-match or
infrastructure error.

### Flags

| Flag | Description |
|---|---|
| `--git-sha SHA` | Match runs whose git SHA starts with this value (prefix match) |
| `--pipeline NAME` | Restrict to one pipeline |
| `--repo OWNER/NAME` | Match trigger's GITHUB_REPOSITORY env |
| `--since DURATION` | Lookback window (default: 1h) |
| `--limit N` | Max results (default: 20) |
| `--wait` | Block until at least one match appears |
| `--find-timeout DURATION` | Give up (nonzero exit) after this long when --wait is set (default: 2m) |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `-q, --quiet` | Print only run ids, one per line (or a JSON array of ids with -o json) |
| `--profile NAME` | Profile name (cluster mode). Omit to search the local SQLite store. |

### Examples

```sh
# Find a run by SHA + pipeline on prod
sparkwing runs find --git-sha $(git rev-parse HEAD) --pipeline build-test-deploy --profile prod

# Block until the matching run appears
sparkwing runs find --git-sha abc123 --pipeline X --wait --profile prod

# Pipe matching ids into jobs wait
sparkwing runs find --git-sha abc --wait -q --profile prod | xargs -n1 -I{} sparkwing runs wait --run {} --profile prod
```

## `sparkwing runs get`

Emit one run's raw JSON (run + nodes)

Prints a combined {run, nodes} JSON blob to stdout. Consumed by agents and scripts that need the full store shape rather than the summary 'status' command renders.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Fetch a local run as JSON
sparkwing runs get --run run-...

# Fetch a prod run
sparkwing runs get --run run-... --profile prod
```

## `sparkwing runs grep`

Search log bodies across recent runs for a substring

Walks the runs matching the filter set and substring-greps
every node's log. Reuses the same filter flags as `runs list` so
the candidate set is identical to what that verb would return.
In cluster mode the grep runs server-side per (run, node), so only
matching bytes come back over the wire.

Default output is a table of RUN / NODE / LINE / TEXT. -q
(quiet) prints just the unique matching run ids -- the usual
shape for piping into `runs logs` or `runs status`.

Exit code 0 even when there are no matches.

### Flags

| Flag | Description |
|---|---|
| `--pattern TEXT` | Substring to match (case-sensitive) (required) |
| `--pipeline NAME` | Restrict candidate runs to one pipeline (repeatable; `!` to exclude) |
| `--status STATUS` | Restrict by status (repeatable; `!` to exclude) |
| `--branch BRANCH` | Restrict by git branch (repeatable; `!` to exclude) |
| `--sha PREFIX` | Restrict by git sha prefix (repeatable; `!` to exclude) |
| `--since DURATION` | Only runs newer than this |
| `--started-after DATE` | Only runs whose StartedAt >= this |
| `--started-before DATE` | Only runs whose StartedAt <= this |
| `--limit N` | Max candidate runs to scan (default: 50) |
| `--max-matches M` | Per-node match cap (0 = no cap) (default: 5) |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain (default: pretty on TTY, json when piped) |
| `-q, --quiet` | Print only the unique matching run ids |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Find every run that hit a permission-denied line in the past week
sparkwing runs grep --pattern 'permission denied' --since 7d

# Pipe matching run ids into runs logs
sparkwing runs grep --pattern OOMKilled --since 24h -q | xargs -I{} sparkwing runs logs --run {}

# Search prod runs as JSON for an agent
sparkwing runs grep --pattern 'connection refused' --profile prod --since 24h -o json
```

## `sparkwing runs last`

Print the most recent run

Shorthand for 'jobs list --limit 1' with a compact one-line output. --watch tails for new runs, reprinting whenever a newer run ID appears.

### Flags

| Flag | Description |
|---|---|
| `--pipeline NAME` | Restrict to one pipeline |
| `-w, --watch` | Tail for new runs |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Local last run
sparkwing runs last

# Watch prod for new runs
sparkwing runs last --profile prod --watch
```

## `sparkwing runs list`

List recent pipeline runs

Without --profile, reads from the local run directory. With --profile NAME,
fetches from the named profile's controller. Filters compose with
AND semantics across flag types (pipeline=X AND status=Y), OR
semantics within a repeated flag (pipeline=X OR pipeline=Y).

With -q / --quiet the output is just run ids, one per line, for
shell piping:

  sparkwing runs list --pipeline X --limit 1 -q --profile prod \
      | xargs -I{} sparkwing runs logs --run {} --profile prod --follow

### Flags

| Flag | Description |
|---|---|
| `--pipeline NAME` | Filter by pipeline name (repeatable; prefix `!` to exclude) |
| `--status STATUS` | Filter by status: running\|success\|failed\|cancelled (repeatable; prefix `!` to exclude) |
| `--tag TAG` | Filter by sparkwing.yaml tag (repeatable) |
| `--branch BRANCH` | Filter by git branch (repeatable; prefix `!` to exclude) |
| `--sha PREFIX` | Filter by git sha prefix (repeatable; prefix `!` to exclude) |
| `--error SUBSTR` | Substring match against the persisted failure reason |
| `--search QUERY` | Free-text search across pipeline/branch/sha/id/error; prefix a term with `-` to exclude |
| `--since DURATION` | Only runs newer than this (e.g. 1h, 24h, 7d) |
| `--started-after DATE` | Only runs whose StartedAt >= this (today, yesterday, 24h, 7d, or a date) |
| `--started-before DATE` | Only runs whose StartedAt <= this |
| `--finished-after DATE` | Only runs whose FinishedAt >= this (excludes still-running) |
| `--finished-before DATE` | Only runs whose FinishedAt <= this (excludes still-running) |
| `--limit N` | Max runs to show (default: 20) |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `-q, --quiet` | Print only run ids, one per line (or JSON array of ids with -o json) |
| `--by-pipeline` | Pivot into one row per pipeline with a status sparkline of the last N runs |
| `--sparkline N` | Sparkline length when --by-pipeline is set (default: 30) |
| `--style STYLE` | Sparkline glyph style: ascii\|block\|dot (default: ascii) |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Last 20 local runs
sparkwing runs list

# Failed runs in the past day
sparkwing runs list --status failed --since 24h

# Exclude success from the list
sparkwing runs list --status '!success' --since 24h

# Runs on main, excluding canary
sparkwing runs list --branch main --search '-canary'

# Runs that hit a specific failure
sparkwing runs list --error 'permission denied'

# Runs finished today
sparkwing runs list --finished-after today

# List prod runs
sparkwing runs list --profile prod --limit 50

# By-pipeline rollup with sparkline
sparkwing runs list --by-pipeline --since 7d

# By-pipeline JSON for an agent
sparkwing runs list --by-pipeline -o json --since 24h

# Pipe the most recent run id into another verb
sparkwing runs list --limit 1 -q | xargs -I{} sparkwing runs logs --run {}
```

## `sparkwing runs logs`

Print a run's logs

Without --profile, reads logs from the local run directory. Pass --profile
NAME to read from a remote controller's logs service (profile must
carry both controller + logs URLs). Line-selection filters
(--tail/--head/--lines/--grep) apply server-side in cluster mode so
the CLI never tails giant logs over the wire.

--since D drops nodes whose StartedAt is older than now-D; useful for
runs that have been retried several times where only the newest
attempt matters. Filtering is node-level (log lines aren't
timestamped on disk).

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `--node NODE_ID` | Limit output to one node id |
| `--tail N` | Print only the last N lines |
| `--head N` | Print only the first N lines |
| `--lines A:B` | 1-indexed inclusive line range |
| `--grep PATTERN` | Substring match (case-sensitive) |
| `--since DURATION` | Only include nodes that started within the last D (e.g. 5m, 1h) |
| `--tree` | Merge root + descendant runs into one stream (local only) |
| `-f, --follow` | Tail the log(s) until the run terminates |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name (omit for local-only reads) |

### Examples

```sh
# Read local logs
sparkwing runs logs --run run-20260422-142501-abcd

# Last 20 lines of a remote run
sparkwing runs logs --run run-... --profile prod --tail 20

# Only the most recent attempt's output
sparkwing runs logs --run run-... --profile prod --since 5m

# Search logs for an error substring
sparkwing runs logs --run run-... --grep 'permission denied'

# Merge a parent run with every descendant
sparkwing runs logs --run run-... --tree

# JSON stream for an agent
sparkwing runs logs --run run-... -o json

# Plain text with node/step prefix
sparkwing runs logs --run run-... -o plain

# Force the colored renderer when piping
sparkwing runs logs --run run-... -o pretty | less -R
```

## `sparkwing runs prune`

Delete finished runs older than a threshold, or by id

Prunes terminal runs (success / failed / cancelled) so the
controller's SQLite store doesn't grow unbounded. Supply either
--older-than DUR (batch by age) or one-or-more run ids via --run
(repeatable). Use --run - to read ids from stdin. The two modes
are mutually exclusive.

Use --dry-run first to confirm the victim list.

### Flags

| Flag | Description |
|---|---|
| `--older-than DURATION` | Prune runs older than this |
| `--run RUN_ID` | Run id to prune (repeatable; use --run - to read ids from stdin) |
| `--dry-run` | List matching runs without deleting |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Preview what a 7-day prune would delete
sparkwing runs prune --older-than 7d --dry-run --profile prod

# Delete a few specific runs
sparkwing runs prune --run run-A --run run-B --profile prod

# Prune ids from another query
sparkwing runs list --pipeline scratch -q | sparkwing runs prune --run - --profile prod
```

## `sparkwing runs receipt`

Emit a run's audit + cost receipt as JSON

Recomputes the per-run receipt from the run + nodes
rows on demand and prints it as JSON. The receipt bundles identity
hashes (pipeline_version_hash, inputs_hash, plan_hash, per-node
outputs_hash), per-step observability (durations, outcomes), and
runner-time × profile-rate compute cost.

Local mode reads from the SQLite store and uses the local profile's
cost_per_runner_hour for the cost calc. --profile NAME reads from the
remote controller's receipt endpoint; in that case the controller's
configured rate (not the local profile) supplies cost.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `-o, --output FORMAT` | Output format: json (default) |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Local receipt as JSON
sparkwing runs receipt --run run-...

# Prod receipt
sparkwing runs receipt --run run-... --profile prod
```

## `sparkwing runs retry`

Trigger fresh runs copying pipeline + args from old ones

Issues a new trigger per source run with the same pipeline, args,
branch, and SHA. Each new run is tagged with retry_of=<old-id>.

Pick a rerun scope explicitly:
  --failed   reuse cached/passed nodes from the source run;
             re-execute only the failed or unreached subset.
  --all      ignore prior outcomes and re-execute every node.

One of --failed or --all is required -- the silent default
caused operators to ship a partial rerun when they meant a full
one (and vice versa).

Pass --run once per source id (repeatable). Use --run - to read ids
from stdin, one per line. Failures on individual ids don't abort
the batch; the verb prints a per-id status line and exits non-zero
only when at least one id failed.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Source run id (repeatable; use --run - to read ids from stdin) |
| `--failed` | Rerun from failed: reuse passed nodes, re-execute only failed/unreached |
| `--all` | Rerun all: re-execute every node from scratch |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Rerun only the failed nodes
sparkwing runs retry --failed --run run-...

# Rerun every node from scratch
sparkwing runs retry --all --run run-...

# Rerun every recently failed run
sparkwing runs list --status failed --since 1h -q | sparkwing runs retry --failed --run - --profile prod
```

## `sparkwing runs stats`

Aggregate run counts, success %, avg + p95 duration

Per-pipeline aggregates across the last 500 root runs (or the --since window). In-flight runs count toward RUN (running) but do not contribute to timing percentiles.

--capacity switches to the measured capacity profiles admission learns from: each pipeline's p50/p99 duration, its CPU and memory distributions (p50/p95/peak across recent runs), its queue-wait p50/p99, sample count, and whether the admission charge comes from a pin, measurement, or the cold-start default. The resource percentiles show whether a pipeline is steady or spiky; admission always charges the peak, because under-reserving a spiky pipeline recreates the oversubscription admission exists to prevent. A pipeline whose pin has drifted from its measured peaks carries the exact fix. Capacity profiles are local-only.

--reset clears a pipeline's learned capacity profile so it re-learns from a cold start, the escape hatch for a poisoned measurement (one freak run that recorded an absurd peak). Name the pipeline with --pipeline NAME, or reset every pipeline with --all --yes. An explicit .Resources() pin is preserved: admission keeps charging the pin while the profile re-learns. The command prints how many rows were dropped, how many pinned rows were cleared, and how many samples were discarded.

### Flags

| Flag | Description |
|---|---|
| `--pipeline NAME` | Restrict to one pipeline (required with --reset unless --all) |
| `--since DURATION` | Only runs newer than this (e.g. 7d) |
| `--capacity` | Show measured capacity profiles instead of run aggregates |
| `--reset` | Delete a pipeline's learned capacity profile so it re-learns (keeps pins) |
| `--all` | With --reset, reset every pipeline's learned profile |
| `--yes` | Confirm --reset --all |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# 7-day local stats
sparkwing runs stats --since 7d

# Prod stats as JSON
sparkwing runs stats --profile prod -o json

# Measured capacity per pipeline
sparkwing runs stats --capacity

# Reset a poisoned profile
sparkwing runs stats --reset --pipeline build

# Reset every learned profile
sparkwing runs stats --reset --all --yes
```

## `sparkwing runs status`

Show one run's status (non-zero exit unless status=success)

Prints a summary of the run (pipeline, status, node states).
With --follow, polls until the run reaches a terminal status. Pass
--profile NAME to read from a remote controller.

Exit code contract: after rendering, 'jobs status' exits 0 only when
status == success. Any non-success terminal status (failed, cancelled)
exits 1; a run that is still running when the (non-follow) read
returns also exits 1. Pass --exit-zero to inspect a known-failed run
without the shell redline. For a blocking wait, use 'jobs wait'.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (e.g. run-20260422-142501-abcd) (required) |
| `-f, --follow` | Poll until the run reaches a terminal state |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--steps` | Render every step under every node (plain output). Failed / skipped / annotated nodes always include their steps; this flag forces success nodes too. |
| `--exit-zero` | Return exit code 0 even when the run failed/cancelled |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Check a local run once
sparkwing runs status --run run-20260422-142501-abcd

# Follow a running job to completion
sparkwing runs status --run run-... --follow

# Inspect a known-failed run without nonzero exit
sparkwing runs status --run run-... --exit-zero

# Expand every step on every node
sparkwing runs status --run run-... --steps

# Check a prod run
sparkwing runs status --run run-... --profile prod
```

## `sparkwing runs summary`

Aggregated work view: groups, work items, modifiers, annotations

Run-level rollup of every node in one render. Mirrors the
dashboard's Summary tab: run header + run-wide annotations +
node groups + work items (nodes and inner steps) + modifiers
in effect + any approval-gate state. Useful for the
"did this run actually do what I asked" agent question.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `-o, --output FORMAT` | Output format: pretty\|json (default: pretty on TTY, json when piped) |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Quick run rollup
sparkwing runs summary --run run-...

# JSON for an agent
sparkwing runs summary --run run-... -o json
```

## `sparkwing runs timeline`

ASCII waterfall of nodes (and optional steps) for a run

Renders one row per node, laid out along the run's wall-clock
span. With --steps each node also expands into its inner Work
steps. Useful for an agent reasoning about parallelism and the
critical path without correlating logs by hand. JSON output
emits start/end offsets in milliseconds per row.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier (required) |
| `--steps` | Include per-step rows under each node |
| `--width N` | Bar width in characters (default: 60) |
| `-o, --output FORMAT` | Output format: pretty\|json (default: pretty on TTY, json when piped) |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Default node waterfall
sparkwing runs timeline --run run-...

# Expand into per-step bars
sparkwing runs timeline --run run-... --steps

# JSON for an agent
sparkwing runs timeline --run run-... --steps -o json
```

## `sparkwing runs tree`

Show a run and every descendant run as an ASCII tree

Walks parent_run_id links so cross-pipeline spawns (RunAndAwait) show up under their originating run. Local mode reads from SQLite; --profile NAME reads from the profile's controller.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Root run identifier (required) |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name; omit for local-only |

### Examples

```sh
# Tree for a local run
sparkwing runs tree --run run-20260422-142501-abcd

# Tree for a prod run as JSON
sparkwing runs tree --run run-... --profile prod -o json
```

## `sparkwing runs triggers`

Fire, list, or inspect controller triggers

Triggers are the controller's queue of pending work. Every
pipeline run starts as a trigger (from a webhook, hook, 'sparkwing
run --profile', or 'triggers fire') that a worker atomically claims and
turns into a run.

'fire' posts a synthetic trigger -- the sparkwing equivalent of
'gh workflow run'. 'list' surfaces queued / in-flight / done
entries so operators can see what's stuck without diving into
controller logs. 'get' inspects one trigger by id.

Connection info comes from the selected profile (--profile NAME);
there are no --controller / --token flags on this command.

### Subcommands

- `list` -- List pending / claimed / done triggers
- `get` -- Inspect one trigger's full metadata by id

### Examples

```sh
# List pending triggers on prod
sparkwing runs triggers list --profile prod --status pending

# Inspect one trigger
sparkwing runs triggers get --id run-... --profile prod

# Fire a trigger (use pipeline run)
sparkwing pipeline run --pipeline deploy --profile prod
```

## `sparkwing runs triggers get`

Inspect one trigger's full metadata by id

Fetches GET /api/v1/triggers/{id} and prints the full row (pipeline, args, git, env, status, claim lease). Defaults to a compact multi-line rendering; -o json emits the raw response.

### Flags

| Flag | Description |
|---|---|
| `--id TRIGGER_ID` | Trigger / run identifier (same value 'fire' prints) (required) |
| `-o, --output FORMAT` | Output format: json emits the raw response |
| `--profile NAME` | Profile name (default: current default) (required) |

### Examples

```sh
# Inspect one trigger
sparkwing runs triggers get --id run-20260422-142501-abcd --profile prod

# Raw JSON for scripting
sparkwing runs triggers get --id run-... --profile prod -o json
```

## `sparkwing runs triggers list`

List pending / claimed / done triggers

Queries GET /api/v1/triggers on the selected profile's
controller. Empty filters return the most recent 20 entries
across all statuses.

Useful when the queue looks stuck ("why isn't my trigger being
claimed?"): --status pending shows unclaimed work, --status
claimed shows what a worker has in-flight. The repo filter
matches GITHUB_REPOSITORY on the trigger env so webhook-driven
entries narrow cleanly.

### Flags

| Flag | Description |
|---|---|
| `--status STATUS` | Filter by status: pending \| claimed \| done |
| `--pipeline NAME` | Filter by pipeline name |
| `--repo OWNER/NAME` | Match GITHUB_REPOSITORY on the trigger env |
| `--limit N` | Max triggers to show (default: 20) |
| `-q, --quiet` | Print only trigger ids, newline-separated |
| `-o, --output FORMAT` | Output format: json emits the raw triggers array |
| `--profile NAME` | Profile name (default: current default) (required) |

### Examples

```sh
# Recent triggers on prod
sparkwing runs triggers list --profile prod

# Just pending
sparkwing runs triggers list --profile prod --status pending

# Pipeline-specific, JSON
sparkwing runs triggers list --profile prod --pipeline build-test-deploy --limit 5 -o json
```

## `sparkwing runs wait`

Block until a run reaches a terminal status

Polls the run until its status is success / failed /
cancelled, then exits. Exit code contract:

  0   status == success
  1   status == failed or cancelled
  2   timed out before the run reached a terminal status
  3+  infrastructure error (controller unreachable, run not found, ...)

Pair with 'jobs find --wait' for the "push then find then wait" loop
described in the CLI wishlist.

### Flags

| Flag | Description |
|---|---|
| `--run RUN_ID` | Run identifier to wait on (required) |
| `--timeout DURATION` | Give up (exit 2) after this long (default: 10m) |
| `--poll DURATION` | Poll interval (default: 3s) |
| `-o, --output FORMAT` | Output format: pretty\|json\|plain |
| `--profile NAME` | Profile name (cluster mode). Omit to poll the local SQLite store. |

### Examples

```sh
# Wait for a local run
sparkwing runs wait --run run-20260422-142501-abcd

# Wait with a custom timeout
sparkwing runs wait --run run-... --timeout 30m --profile prod

# Tight polling on a fast run
sparkwing runs wait --run run-... --poll 500ms --profile prod
```

## `sparkwing secrets`

Manage secrets (local dotenv or controller-stored)

Without --profile, reads/writes the laptop dotenv at
~/.config/sparkwing/secrets.env (masked) or
~/.config/sparkwing/config.env (--plain). Used by jobs invoked
through 'sparkwing run <pipeline>' locally.

With --profile PROF, reads/writes the named profile's controller.
Used for prod / staging secrets that the cluster needs at run
time. Pipelines pull a secret by listing it in the
sparkwing.yaml 'secrets:' block. Raw values never transit the
CLI except via 'secrets get'.

### Subcommands

- `set` -- Store (or replace) a secret value
- `get` -- Print a secret's raw value to stdout
- `list` -- List secret names + metadata (never prints values)
- `delete` -- Remove a secret

## `sparkwing secrets delete`

Remove a secret

Deletes the secret row from the controller. Pipelines that reference the name will fail to resolve until the secret is re-added.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Secret name to remove (required) |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Delete a secret
sparkwing secrets delete --name API_TOKEN --profile prod
```

## `sparkwing secrets get`

Print a secret's raw value to stdout

Prints only the raw value (no trailing newline) so it can be
piped into another command. Use 'secrets list' for metadata.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Secret name (required) |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Fetch a secret
sparkwing secrets get --name API_TOKEN --profile prod
```

## `sparkwing secrets list`

List secret names + metadata

Prints a table of name, created_at, and the principal that last updated each secret. Raw values are never printed by this command.

### Flags

| Flag | Description |
|---|---|
| `--grep PATTERN` | Filter by name substring (case-sensitive) |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# List secrets on prod
sparkwing secrets list --profile prod

# Filter to API-related names
sparkwing secrets list --profile prod --grep API
```

## `sparkwing secrets set`

Store (or replace) a secret value

Uploads --value (or the contents of --file) to the controller
under --name. Replaces any existing secret with that name.
Prefer --file for long or multi-line values so the raw text
does not land in shell history.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Secret name (unique per controller) (required) |
| `--value VALUE` | Secret value (prefer --file for long values) |
| `--file PATH` | Read value from file (keeps value out of shell history) |
| `--plain` | Store as non-masked config (e.g. REGION, LOG_LEVEL) -- value will NOT be redacted in run logs. Default is masked. |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Set a masked secret (default)
sparkwing secrets set --name API_TOKEN --value abc123 --profile prod

# Set from a file
sparkwing secrets set --name TLS_CERT --file ./tls.crt --profile prod

# Set non-masked config
sparkwing secrets set --name REGION --value us-east-1 --plain --profile prod
```

## `sparkwing update`

Self-update the CLI binary

Downloads, checksum-verifies, and atomically installs the latest
(or a specific) sparkwing release from GitHub Releases.

By default the command fetches the latest version pointer, pulls
the matching tarball for the current OS/arch, verifies its SHA256
against the published SHA256SUMS, and replaces the running binary
via an atomic rename. macOS arm64 binaries are ad-hoc-codesigned
after installation to avoid SIGKILL on first run.

--check is the read-only probe: it reports the installed version
and the latest published release, exits 0 when already current,
and exits 1 when a newer release exists (useful for CI/notifications).

Downgrades are blocked by default. Pass --force to install an older
release (e.g. bisecting a regression).

For SDK (go.mod) bumps, use 'sparkwing version update --sdk'.

### Flags

| Flag | Description |
|---|---|
| `--check` | Report installed vs latest; exit 1 if a newer release exists (read-only) |
| `--force` | Allow downgrading to an older release |
| `--override-hold` | Cross an operator version hold |
| `--version TAG` | Target release tag (e.g. v0.17.0). Default: latest. |

### Examples

```sh
# Check for a newer release (read-only)
sparkwing update --check

# Update to latest
sparkwing update

# Pin to a specific release
sparkwing update --version v0.44.0

# Downgrade to an older release
sparkwing update --version v0.40.0 --force
```

## `sparkwing version`

Show + update versions (CLI, SDK, sparks)

Reports the installed CLI version + build provenance, the
latest published release on GitHub (with a short network
fetch -- bounded by ~3s, fail-soft when offline), and the
.sparkwing/go.mod SDK pin + any sparks-* libraries declared
alongside it.

Behind-by-version is computed via semver compare for both the
CLI itself and the SDK pin so an agent reading -o json can
trigger an upgrade without parsing prose.

--offline skips the network fetch entirely; -o json emits the
structured report; -o plain prints semver lines (CLI then
latest) for shell pipelines.

### Subcommands

- `update` -- Self-update CLI binary or bump SDK pin (requires --cli or --sdk)
- `hold` -- Show, set, or clear the operator ceiling on CLI upgrades

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `--offline` | Skip the network fetch for latest release |
| `--changelog` | Print the changelog for the installed release |

### Examples

```sh
# Human-readable card
sparkwing version

# Agent-readable record
sparkwing version -o json

# CLI semver only (scripts)
sparkwing version -o plain | head -n1

# Local-only (no network)
sparkwing version --offline

# Changelog for the installed release
sparkwing version --changelog

# Update the CLI binary
sparkwing version update --cli

# Bump the SDK pin in this project
sparkwing version update --sdk
```

## `sparkwing version hold`

Show, set, or clear the operator ceiling on CLI upgrades

A version hold is an operator-set ceiling that the tool enforces:
once set, 'sparkwing version update --cli' (and 'sparkwing update')
refuse to install anything beyond it, so an agent cannot perform a
major upgrade against operator instruction.

The ceiling shape controls its reach:

  vMAJOR.MINOR       caps a whole minor series -- every patch of that
                     minor is allowed, the next minor is refused
                     (e.g. v0.15 allows v0.15.9 but refuses v0.16.0).
  vMAJOR.MINOR.PATCH exact ceiling -- nothing above that patch installs.

With no flags, prints the current hold and where it is set. The hold
persists in the user config (XDG_CONFIG_HOME or ~/.config/sparkwing/
version-hold); the SPARKWING_VERSION_HOLD environment variable
overrides the file for a shell or a whole fleet. Releases beyond the
hold still show in 'sparkwing version' so the operator sees what is
being deferred.

### Flags

| Flag | Description |
|---|---|
| `--set VERSION` | Set the ceiling (e.g. v0.15 or v0.15.4) |
| `--clear` | Remove the hold so upgrades are unrestricted |

### Examples

```sh
# Show the current hold
sparkwing version hold

# Hold the minor series at v0.15
sparkwing version hold --set v0.15

# Pin an exact ceiling
sparkwing version hold --set v0.15.4

# Lift the hold
sparkwing version hold --clear
```

## `sparkwing version update`

Self-update the CLI binary (--cli) or bump this project's SDK pin (--sdk)

Two targets, one verb:

  --cli   Replace the running sparkwing binary with the target
          release. Resolves the version pointer from GitHub Releases,
          downloads + checksum-verifies the tarball, and atomically
          installs it. macOS arm64 binaries are ad-hoc-codesigned
          to avoid SIGKILL on first run.

  --sdk   Bump the SDK pin in this project's .sparkwing/go.mod via
          'go get github.com/sparkwing-dev/sparkwing@<version>',
          then 'go mod tidy'. Doesn't touch the running binary.

Exactly one of --cli or --sdk must be set; they conflict with
each other so a typo can't update the wrong half. --version
applies to whichever target is selected.

### Flags

| Flag | Description |
|---|---|
| `--cli` | Self-update the sparkwing CLI binary |
| `--sdk` | Bump the SDK pin in this project's .sparkwing/go.mod |
| `--version TAG` | Target release tag (e.g. v0.17.0). Omit for latest. |
| `--force` | Allow downgrading to an older release (--cli only) |
| `--override-hold` | Cross an operator version hold (--cli only) |

### Examples

```sh
# Update the CLI to latest
sparkwing version update --cli

# Pin the CLI to a specific release
sparkwing version update --cli --version v0.44.0

# Downgrade the CLI
sparkwing version update --cli --version v0.40.0 --force

# Bump the SDK in this project to latest
sparkwing version update --sdk

# Pin the SDK to a specific release
sparkwing version update --sdk --version v0.44.0
```

