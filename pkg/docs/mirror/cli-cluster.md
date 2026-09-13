<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing cluster

Every `sparkwing cluster` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing cluster`

Operate and inspect the sparkwing cluster

Inspect controller health, executors, admission, users, tokens, images,
and webhooks. Select the controller with --profile NAME.
Configure profiles with 'sparkwing configure profiles'.

'worker' executes queued triggers on this machine. 'gc' removes stale
warm-runner storage. Manage secrets with 'sparkwing secrets' and the
local dashboard with 'sparkwing serve'.

### Subcommands

- `status` -- Connectivity + fleet + queue health check against a remote cluster
- `agents` -- Inspect the controller's fleet view
- `runners` -- Enroll and retire this machine as a runner
- `worker` -- Claim triggers from a profile's controller and run them in-process
- `gc` -- Sweep stale warm-PVC state
- `users` -- Manage dashboard login users
- `tokens` -- Manage controller API tokens
- `image` -- Rollout helpers for images referenced by a gitops repo
- `webhooks` -- Inspect and replay GitHub webhooks
- `concurrency` -- Inspect a single concurrency namespace: holders + queue
- `object-store` -- Operate the controller's object-store request budget

### Examples

```sh
# Cluster health summary
sparkwing cluster status --profile prod

# List fleet agents
sparkwing cluster agents list --profile prod
```

## `sparkwing cluster agents`

Inspect the controller's fleet view

Hits GET /api/v1/agents on the selected profile's controller.
Prints persisted executor registrations, including idle and
offline agents and gateways, plus recent legacy claim-only runners.

### Subcommands

- `list` -- Print the controller's known agents
- `enroll` -- Enroll or update a trusted executor

### Examples

```sh
# List prod agents
sparkwing cluster agents list --profile prod
```

## `sparkwing cluster agents enroll`

Enroll or update a trusted executor

Binds one exact runner or service token prefix to an
operator-owned executor envelope. The token must be live and carry
nodes.claim; its stored principal becomes audit metadata. Re-enrollment
with the same credential updates trusted scheduling fields without changing
live headroom. Changing the prefix requires a new heartbeat.

Use a distinct revocable token for every coordinator membership. The
prefix is accepted as input but is never returned by the agents API. A
controller accepts at most 256 enrolled executors. Adding another returns `executor enrollment limit reached: maximum 256 per controller`.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Executor name (required) |
| `--token-prefix PREFIX` | Exact runner or service token prefix (required) |
| `--kind KIND` | Executor kind (agent\|gateway) (default: agent) |
| `--location WHERE` | Trusted placement location (local\|cloud\|unknown) (default: unknown) |
| `--capability LABEL` | Trusted capability (repeatable) |
| `--base-priority N` | Base scheduling priority (0-100) (default: 0) |
| `--priority-ceiling N` | Highest effective priority (0-100) (default: 100) |
| `--max-concurrent N` | Trusted concurrent slot ceiling (default: 1) |
| `--budget-cores N` | CPU contribution ceiling (0 = uncapped) (default: 0) |
| `--budget-memory-bytes N` | Memory contribution ceiling in bytes (0 = uncapped) (default: 0) |
| `--profile NAME` | Admin controller profile (required) |

### Examples

```sh
# Enroll a workstation agent
sparkwing cluster agents enroll --profile prod --name desk --token-prefix swr_01234567 --kind agent --location local --capability linux --max-concurrent 2 --budget-cores 4 --budget-memory-bytes 8589934592

# Enroll a capacity gateway
sparkwing cluster agents enroll --profile prod --name build-gateway --token-prefix sws_01234567 --kind gateway --location cloud --capability linux-amd64 --max-concurrent 8
```

## `sparkwing cluster agents list`

Print the controller's known agents

Fetches /api/v1/agents and renders a table of fleet members.
Registered executors report their operator-assigned identity,
kind, trusted placement location, capabilities, concurrency limit, and
measured resource headroom. A stale registration remains visible
as offline; recent legacy claim-only runners remain visible too.

Use -q to print names, one per line, for shell piping
(xargs and similar commands).

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile name (required) |
| `-o, --output FMT` | Output format (json\|table) |
| `-q, --quiet` | Print agent names, one per line |

### Examples

```sh
# List agents on prod
sparkwing cluster agents list --profile prod

# Just agent names for piping
sparkwing cluster agents list --profile prod -q
```

## `sparkwing cluster concurrency`

Inspect a single concurrency namespace: holders + queue

Shows who holds a concurrency namespace's slots
and the queue of waiters behind it, each with its admission-rank
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

Update an image tag in a GitOps repository, commit and push the change,
sync ArgoCD, and wait for rollout. Publish the image before using these
commands.

### Subcommands

- `rollout` -- Bump a kustomization image tag, commit+push, sync ArgoCD, optionally wait

### Examples

```sh
# Update the example runner image
sparkwing cluster image rollout --image fictional-runner --tag commit-abc123 --wait
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
  2. SPARKWING_GITOPS_REPO explicit environment configuration

If neither is set, rollout exits before reading or changing a repository.
Sparkwing never guesses a path from the user's home-directory layout.

The command is idempotent: if the newTag already matches --tag
there is nothing to commit, and the pipeline continues to sync
+ wait without error. Use --dry-run to preview the plan without
writing, committing, pushing, syncing, or waiting.

Tool requirements:
  - argocd missing  -> sync is skipped with a one-line notice
  - kubectl missing -> --wait / --tail-logs error before side effects

This verb does not build or push the image itself. The consumer
pipeline that produced --tag is responsible for publishing the
image to the registry before calling rollout.

### Flags

| Flag | Description |
|---|---|
| `--image NAME` | Short image name (matches the suffix of the ECR URL) (required) |
| `--tag TAG` | New tag to write in kustomization.yaml (required) |
| `--gitops-repo PATH` | Gitops repo path (or SPARKWING_GITOPS_REPO) |
| `--namespace NS` | Kubernetes namespace for rollout status + logs (default: sparkwing) |
| `--argocd-app NAME` | ArgoCD app name (default: derived from --image) |
| `--message MSG` | Commit message (default: 'chore: bump <image> to <tag>') |
| `--wait` | Block until 'kubectl rollout status deployment/<image>' returns |
| `--tail-logs` | After rollout, 'kubectl logs -f -l app=<image>' until ctrl-c |
| `--dry-run` | Print what would happen without writing, committing, pushing, or syncing |

### Examples

```sh
# Preview the example runner image update
sparkwing cluster image rollout --image fictional-runner --tag commit-abc123 --dry-run

# Bump and wait for the rollout
sparkwing cluster image rollout --image fictional-runner --tag commit-abc123 --wait

# Bump, sync, wait, then tail pod logs
sparkwing cluster image rollout --image fictional-service --tag commit-abc123 --wait --tail-logs
```

## `sparkwing cluster object-store`

Operate the controller's object-store request budget

The controller counts every object-store request it makes, by class
(put, get, list, delete), against a per-minute rate and a per-day
budget. A class that spends either budget trips: writes of that class
fail closed and reads keep serving until their own budget trips. The
state appears on 'sparkwing cluster status' and on the controller's
Prometheus metrics as sparkwing_object_store_requests_total,
sparkwing_object_store_trips_total, and sparkwing_object_store_tripped.

Budgets come from SPARKWING_OBJECT_STORE_<CLASS>_PER_MINUTE and
SPARKWING_OBJECT_STORE_<CLASS>_PER_DAY on the controller process.
SPARKWING_OBJECT_STORE_TRIP_RESET chooses whether a tripped class
clears when its day window rolls (day, the default) or waits for an
operator (manual). A local process that must finish past a tripped
budget sets SPARKWING_OBJECT_STORE_BREAKER=off.

### Subcommands

- `status` -- Show the controller's object-store request budget
- `reset-breaker` -- Clear a tripped object-store request budget

### Examples

```sh
# Clear a tripped budget
sparkwing cluster object-store reset-breaker --profile prod
```

## `sparkwing cluster object-store reset-breaker`

Clear a tripped object-store request budget

Clears every tripped request class on the selected controller and
resets its per-minute and per-day window counters, then prints the
budget as it stands. Lifetime request and trip totals survive, so the
metrics keep their history.

Reach for this after fixing what caused the trip. A budget that keeps
tripping wants a larger limit or a caller that stops retrying, not a
repeated reset.

Hits POST /api/v1/object-store/reset-breaker on the selected
profile's controller, which needs an admin-scoped token.

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile selecting the controller (required) |
| `-o, --output FORMAT` | Output format (json\|table) |

### Examples

```sh
# Clear a tripped budget
sparkwing cluster object-store reset-breaker --profile prod
```

## `sparkwing cluster object-store status`

Show the controller's object-store request budget

Prints each request class with its per-minute rate, its per-day budget,
how much of each window the controller has spent, how many times the
class has tripped, and whether it is refusing requests now. Changes
nothing.

Hits GET /api/v1/object-store/breaker on the selected profile's
controller, which needs an admin-scoped token.

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile selecting the controller (required) |
| `-o, --output FORMAT` | Output format (json\|table) |

### Examples

```sh
# Read the budget
sparkwing cluster object-store status --profile prod
```

## `sparkwing cluster runners`

Enroll and retire this machine as a runner

Turns one machine into a runner for the selected profile's
controller in a single command. 'add' mints a scoped runner token, writes the
owner-only agent config, and installs the user service. 'remove' stops that
service and revokes the token.

Use 'sparkwing cluster agents list' to see the runners a controller knows
about.

### Subcommands

- `add` -- Mint a runner token, write the config, start the service
- `remove` -- Stop the runner service and revoke its token

### Examples

```sh
# Enroll this machine
sparkwing cluster runners add --profile prod --name dev-laptop

# Retire this machine
sparkwing cluster runners remove --profile prod
```

## `sparkwing cluster runners add`

Mint a runner token, write the config, start the service

Mints a runner token carrying nodes.claim, triggers.claim,
runs.state, secrets.read and logs.write against the profile's controller,
writes ~/.config/sparkwing/agent.yaml at mode 0600, then installs and starts
the user service: a systemd user unit on Linux, a LaunchAgent on macOS. On
Windows it prints the manual supervision steps instead.

The config is written in claim mode, which is the mode that executes work.
An existing config is never replaced without --force, because the token it
holds stays live until it is revoked.

Nothing is minted until the config validates and the machine answers: a
missing sparkwing-runner, an unreachable service manager, or an unusable
setting fails first. If a step after the mint fails, the output names the live
token and the command that revokes it.

The command prints the token prefix and the revoke command. The raw token
reaches only the config file.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Runner name, shown in the dashboard (required) |
| `--labels CSV` | Comma-separated self-asserted placement labels |
| `--max-concurrent N` | Concurrent jobs this machine accepts (default: 2) |
| `--contribution SPEC` | CPU and memory this machine contributes (4,8gb or 50%,50%) (default: 50%,50%) |
| `--logs URL` | Logs service URL (default: the profile's logs surface) |
| `--config PATH` | Agent config to write (default: ~/.config/sparkwing/agent.yaml) |
| `--force` | Replace an existing agent config |
| `--no-service` | Write the config without installing or starting the service |
| `--profile NAME` | Profile naming the controller to enroll against (required) |

### Examples

```sh
# Enroll this machine
sparkwing cluster runners add --profile prod --name dev-laptop

# Enroll with a capacity ceiling and labels
sparkwing cluster runners add --profile prod --name build-box --max-concurrent 4 --contribution 4,8gb --labels linux,arch=amd64

# Write the config and supervise the agent yourself
sparkwing cluster runners add --profile prod --name dev-laptop --no-service
```

## `sparkwing cluster runners remove`

Stop the runner service and revoke its token

Reads the token out of the agent config, stops and removes the
user service, then revokes that token on the profile's controller. The service
stops first, so a claim in flight finishes against a credential that still
authenticates. A prefix the controller reports as anything but a runner token
is refused, naming what it found.

A service file that runs a different agent config is left alone.

The config file stays on disk holding the revoked token; 'runners add --force'
replaces it.

### Flags

| Flag | Description |
|---|---|
| `--config PATH` | Agent config to read the token from (default: ~/.config/sparkwing/agent.yaml) |
| `--no-service` | Revoke the token without touching the service |
| `--profile NAME` | Profile naming the controller that issued the token (required) |

### Examples

```sh
# Retire this machine
sparkwing cluster runners remove --profile prod
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
| `--profile NAME` | Profile name (required) |
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
profile named by --profile.
Token creation prints the raw value to stdout once --
save it before leaving this command.

### Subcommands

- `create` -- Mint a new API token
- `list` -- List token prefixes + metadata
- `revoke` -- Mark a token revoked
- `lookup` -- Print metadata for a single token
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
| `--principal NAME` | Name identifying the token holder (required) |
| `--scope CSV` | Comma-separated scopes; use sparkwing docs read --topic auth for the supported set |
| `--ttl DURATION` | Token lifetime (30d, 720h, and similar durations). 0 = never expires |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Mint a service token with write scopes
sparkwing cluster tokens create --type service --principal deploy-bot --scope runs.read,runs.write --profile prod

# Mint a user token that expires in 30 days
sparkwing cluster tokens create --type user --principal fictional-user --scope admin --ttl 720h --profile prod
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
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# List all active tokens
sparkwing cluster tokens list --profile prod

# Audit every revoked service token
sparkwing cluster tokens list --type service --include-revoked --profile prod

# Inspect the warm-runner pool token's scopes as JSON
sparkwing cluster tokens list --profile prod -o json | jq 'select(.principal=="agent:fictional-runner") | .scopes'
```

## `sparkwing cluster tokens lookup`

Print metadata for a single token

Prints the JSON metadata for a token given its non-secret prefix. Useful for
confirming principal + scopes before revoking or rotating.

### Flags

| Flag | Description |
|---|---|
| `--prefix PREFIX` | Non-secret token prefix (required) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Inspect a token before revoking
sparkwing cluster tokens lookup --prefix a1b2c3d4 --profile prod
```

## `sparkwing cluster tokens revoke`

Mark a token revoked

Subsequent requests using the token receive HTTP 401. Revocation is immediate
and irreversible.

### Flags

| Flag | Description |
|---|---|
| `--prefix PREFIX` | Non-secret token prefix (from 'tokens list') (required) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Revoke a leaked token
sparkwing cluster tokens revoke --prefix a1b2c3d4 --profile prod
```

## `sparkwing cluster tokens rotate`

Mint a replacement token with a grace window

Creates a new token and schedules the old token for revocation
after --grace. During the grace window, both tokens work, which
lets callers cycle credentials without downtime. The controller
caps --grace at 7 days, and revoking the old prefix cuts a grace
window short.

### Flags

| Flag | Description |
|---|---|
| `--prefix PREFIX` | Non-secret prefix of the token to rotate (required) |
| `--grace DURATION` | Window during which the old token still authenticates (maximum 168h) (default: 24h) |
| `--ttl DURATION` | TTL of the new token (0 = preserve the old token's remaining TTL) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Rotate a token with a 48h grace window
sparkwing cluster tokens rotate --prefix a1b2c3d4 --grace 48h --profile prod
```

## `sparkwing cluster users`

Manage dashboard login users

Seeds admin credentials in the controller's users table, used
by the web pod's login flow. Connection info comes from the
profile named by --profile.

### Subcommands

- `add` -- Create a dashboard user
- `list` -- Print every user
- `delete` -- Remove a dashboard user

## `sparkwing cluster users add`

Create a dashboard user

Prompts for a password on stdin with echo disabled when stdin
is a TTY (the password is not shown on-screen or recorded in
shell history). Passing --password skips the prompt -- useful
for CI seed flows but leaks via shell history if used
interactively. --scope sets what the account's dashboard
sessions may reach; omitting it grants admin. The first account
on a controller must be an admin, so a --scope list that omits
admin is refused until one exists.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Dashboard username (required) |
| `--password PASSWORD` | Password (omit to prompt interactively) |
| `--scope LIST` | Comma-separated scopes (omit to grant admin; the first account must include admin) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Interactive add of the first admin
sparkwing cluster users add --name fictional-user --profile prod

# Read-only account, once an admin exists
sparkwing cluster users add --name viewer --scope runs.read,logs.read --profile prod

# Non-interactive add for CI
sparkwing cluster users add --name ci-bot --password "$CI_BOT_PW" --profile prod
```

## `sparkwing cluster users delete`

Remove a dashboard user

Deletes the user row, every session that user holds, and
revokes every token minted under that principal name except the token
this request authenticates with, in one transaction. The sessions and
tokens are revoked and the auth cache on the serving replica is
cleared; auth.md describes the windows that remain elsewhere.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Dashboard username to remove (required) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Delete a user
sparkwing cluster users delete --name fictional-user --profile prod
```

## `sparkwing cluster users list`

Print every user

Prints name, scopes, created_at, and last_login_at for every
user in the controller's users table.

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# List users
sparkwing cluster users list --profile prod
```

## `sparkwing cluster webhooks`

Inspect and replay GitHub webhooks

Inspect GitHub webhooks through the installed 'gh' command and its
credentials. The deliveries view joins delivery records with Sparkwing
triggers and run outcomes.

### Subcommands

- `list` -- List GitHub hooks configured on a repo
- `deliveries` -- List recent deliveries for a hook, joined with trigger state
- `replay` -- Queue a redelivery of a specific delivery UUID

### Examples

```sh
# List hooks on a repo
sparkwing cluster webhooks list --repo your-org/my-app

# Recent deliveries for a hook
sparkwing cluster webhooks deliveries --repo your-org/my-app --hook 123456789 --since 1h --profile prod
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
| `-o, --output FMT` | Output format (json\|table) |
| `--profile NAME` | Profile name (used for trigger/run lookups) (required) |

### Examples

```sh
# Recent deliveries for a hook
sparkwing cluster webhooks deliveries --repo your-org/my-app --hook 123456789 --since 1h --profile prod
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
| `-o, --output FMT` | Output format (json\|table) |

### Examples

```sh
# List hooks on a repo
sparkwing cluster webhooks list --repo your-org/my-app
```

## `sparkwing cluster webhooks replay`

Queue a redelivery of a specific delivery UUID

Requests another attempt for the selected GitHub webhook delivery.
Read the hook's deliveries to inspect the resulting attempt.

### Flags

| Flag | Description |
|---|---|
| `--repo OWNER/NAME` | GitHub repo (required) |
| `--hook N` | GitHub hook id (required) |
| `--delivery UUID` | Delivery GUID to redeliver (required) |

### Examples

```sh
# Redeliver a webhook attempt
sparkwing cluster webhooks replay --repo your-org/my-app --hook 123456789 --delivery 00000000-0000-4000-8000-000000000001
```

## `sparkwing cluster worker`

Claim triggers from a profile's controller and run them in-process

Polls the trigger queue at the selected profile's
controller and executes each claimed trigger in-process on this host.
Use sparkwing-runner for --runner k8s|warm and image or service-account flags.

Run against a remote controller via --profile prod (or whichever profile),
or against a local 'sparkwing serve start' via --profile local.

### Flags

| Flag | Description |
|---|---|
| `--profile PROFILE` | Profile name from profiles.yaml (required) |
| `--poll DUR` | Claim poll interval when the queue is empty (default: 1s) |
| `--heartbeat DUR` | Claim-lease heartbeat cadence (default: 5s) |

### Examples

```sh
# Run against a named profile
sparkwing cluster worker --profile local

# Faster polling for tight dev loops
sparkwing cluster worker --profile local --poll 250ms
```
