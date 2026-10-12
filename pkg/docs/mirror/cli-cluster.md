<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing cluster

Every `sparkwing cluster` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing cluster`

Operate and inspect the sparkwing cluster

Inspect and operate a controller's executors, triggers, admission, users and
tokens. Select the controller with --profile NAME; connect one with
'sparkwing cloud connect' and check its health with 'sparkwing cloud status
--cluster'.

'worker' executes queued triggers on this machine. Manage secrets with
'sparkwing secrets' and the local dashboard with 'sparkwing serve'.

### Subcommands

- `agents` -- Inspect the controller's fleet view
- `runners` -- Enroll and retire this machine as a runner
- `worker` -- Claim triggers from a profile's controller and run them in-process
- `triggers` -- List or inspect controller triggers
- `users` -- Manage dashboard login users
- `tokens` -- Manage controller API tokens
- `limits` -- Read and set the compute guards
- `concurrency` -- Inspect a single concurrency namespace: holders + queue
- `object-store` -- Operate the controller's object-store request budget

### Examples

```sh
# Cluster health summary
sparkwing cloud status --profile prod --cluster

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

## `sparkwing cluster limits`

Read and set the compute guards

Compute guards bound what the controller starts before the credit
ledger bills it: the cloud runners one principal holds at once, the cloud
runners the whole controller holds, the wall-clock seconds a run may hold them
for, the nodes one run may carry, the runs created per hour, and the shortest
interval a cloud schedule may declare. Every guard is zero by default, which is
unlimited, so a controller that sets none behaves as it did before the guards
existed.

### Subcommands

- `show` -- Print every compute guard and visible runner usage
- `set` -- Set one compute guard

### Examples

```sh
# Read the guards and what they measure
sparkwing cluster limits show --profile prod

# Cap the cloud runners one team holds
sparkwing cluster limits set --name max_concurrent_runners --value 20 --profile prod
```

## `sparkwing cluster limits set`

Set one compute guard

Sets one guard to a ceiling, or to zero to remove it. The guards are
max_concurrent_runners, max_global_runners, runner_alarm, max_run_seconds,
max_nodes_per_run, max_runs_per_hour, max_global_nodes_per_run,
max_global_runs_per_hour and min_cron_interval_seconds.
max_concurrent_runners counts a team's cloud runners across all its tokens;
the other per-principal guards bind a principal holding a metered token in its
team; the two max_global settings bind every run. Work past a guard answers
429 with a Retry-After and the run records a compute_limit_blocked event.
Requires the admin scope.

### Flags

| Flag | Description |
|---|---|
| `--name GUARD` | Guard to set (required) |
| `--value N` | Ceiling; 0 removes it (required) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Hold the fleet under fifty cloud runners
sparkwing cluster limits set --name max_global_runners --value 50 --profile prod

# Warn at forty
sparkwing cluster limits set --name runner_alarm --value 40 --profile prod

# Remove the per-run node cap
sparkwing cluster limits set --name max_nodes_per_run --value 0 --profile prod
```

## `sparkwing cluster limits show`

Print every compute guard and visible runner usage

Prints each guard with its ceiling, or "unlimited" when nothing set
one. An operator also sees the cloud runners claimed now in total and per
principal, and whether runner_alarm has been reached. Team readers see their
own paid runner cap without another team's fleet activity.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Read the guards
sparkwing cluster limits show --profile prod

# Read the guards as JSON
sparkwing cluster limits show --profile prod -o json
```

## `sparkwing cluster object-store`

Operate the controller's object-store request budget

The controller counts every object-store request it makes, by class
(put, get, list, delete), against a per-minute rate and a per-day
budget. A class that spends either budget trips: writes of that class
fail closed and reads keep serving until their own budget trips. The
state appears on 'sparkwing cloud status' and on the controller's
Prometheus metrics as sparkwing_object_store_requests_total,
sparkwing_object_store_trips_total, and sparkwing_object_store_tripped.

Budgets come from the controller's --object-store-budget, as
class:window=count entries such as put:minute=600. A class tripped by its
day budget clears when the day window rolls.

The same breaker carries the bucket ceiling. A controller started with
--max-bucket-bytes or --max-bucket-objects measures the bucket on an
interval, freezes object writes once it holds more than the ceiling,
and reports the freeze on health and as
sparkwing_object_store_bucket_ceiling_frozen. Buckets are unlimited by
default.

### Subcommands

- `status` -- Show the controller's object-store request budget
- `reset-breaker` -- Clear a tripped object-store budget or ceiling freeze

### Examples

```sh
# Clear a tripped budget
sparkwing cluster object-store reset-breaker --profile prod
```

## `sparkwing cluster object-store reset-breaker`

Clear a tripped object-store budget or ceiling freeze

Clears every tripped request class on the selected controller, resets
its per-minute and per-day window counters, and thaws a frozen bucket
ceiling, then prints the budget as it stands. Lifetime request and trip
totals survive, so the metrics keep their history. A thawed bucket that
is still over its ceiling freezes again at the next measurement, so a
thaw buys the window to delete objects or raise the ceiling.

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
class has tripped, and whether it is refusing requests now, followed by
the bucket ceiling: what the bucket holds, the ceilings it is held to,
and whether object writes are frozen. Changes nothing.

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
writes the agent section of ~/.config/sparkwing/config.yaml at mode 0600, then installs and starts
the user service: a systemd user unit on Linux, a LaunchAgent on macOS. On
Windows it prints the manual supervision steps instead.

The section is written in claim mode, which is the mode that executes work.
An existing agent section is never replaced without --force, because the token
it holds stays live until it is revoked. Every other section of the file is
kept.

Nothing is minted until the config validates and the machine answers: a
missing sparkwing-runner, an unreachable service manager, or an unusable
setting fails first. If a step after the mint fails, the output names the live
token and the command that revokes it.

With --allow-repo the agent fetches each run's source itself, from the
repositories the list names, with the credential the controller releases or
else this machine's own git credentials. Without it the agent fetches through
the controller's gitcache proxy.

The command prints the token prefix and the revoke command. The raw token
reaches only config.yaml.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Runner name, shown in the dashboard (required) |
| `--allow-repo PATTERN` | Repository this machine may build and fetch directly, as host/path with '*' within one segment (repeatable) |
| `--labels CSV` | Comma-separated self-asserted placement labels |
| `--max-concurrent N` | Concurrent jobs this machine accepts (default: 2) |
| `--contribution SPEC` | CPU and memory this machine contributes (4,8gb or 50%,50%) (default: 50%,50%) |
| `--logs URL` | Logs service URL (default: the profile's logs surface, then the controller's announcement) |
| `--config PATH` | config.yaml whose agent section to write (default: ~/.config/sparkwing/config.yaml) |
| `--force` | Replace an existing agent section |
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

# Fetch source directly for the team's repositories
sparkwing cluster runners add --profile prod --name dev-laptop --allow-repo 'github.com/acme/*'
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
| `--config PATH` | config.yaml whose agent section holds the token (default: ~/.config/sparkwing/config.yaml) |
| `--no-service` | Revoke the token without touching the service |
| `--profile NAME` | Profile naming the controller that issued the token (required) |

### Examples

```sh
# Retire this machine
sparkwing cluster runners remove --profile prod
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

--prefix PREFIX prints the full record of one token as indented JSON.

### Flags

| Flag | Description |
|---|---|
| `--prefix PREFIX` | Print the full record of the token with this non-secret prefix |
| `--type KIND` | Filter by token type |
| `--include-revoked` | Include revoked tokens in the output |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# List all active tokens
sparkwing cluster tokens list --profile prod

# One token's full record
sparkwing cluster tokens list --prefix swu_abc123 --profile prod

# Audit every revoked service token
sparkwing cluster tokens list --type service --include-revoked --profile prod

# Inspect the warm-runner pool token's scopes as JSON
sparkwing cluster tokens list --profile prod -o json | jq 'select(.principal=="agent:fictional-runner") | .scopes'
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

## `sparkwing cluster triggers`

List or inspect controller triggers

Inspect the controller's queue of pipeline triggers. 'list' shows pending,
claimed, and completed entries. 'get' reads one trigger by identifier.
Select the controller with --profile NAME.

Submit work with 'sparkwing pipeline trigger <pipeline> --profile NAME'.

### Subcommands

- `list` -- List pending / claimed / done / failed triggers
- `get` -- Inspect one trigger's full metadata by id

### Examples

```sh
# List pending triggers on prod
sparkwing cluster triggers list --profile prod --status pending

# Inspect one trigger
sparkwing cluster triggers get --id run-fictional --profile prod

# Submit a trigger
sparkwing pipeline trigger fictional-deploy --profile prod
```

## `sparkwing cluster triggers get`

Inspect one trigger's full metadata by id

Fetches GET /api/v1/triggers/{id} and prints the full row (pipeline, args,
git, env, status, claim lease). Defaults to a compact multi-line rendering; -o
json emits the raw response.

### Flags

| Flag | Description |
|---|---|
| `--id TRIGGER_ID` | Trigger / run identifier (the value 'pipeline trigger' prints) (required) |
| `-o, --output FORMAT` | Output format: json emits the raw response |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Inspect one trigger
sparkwing cluster triggers get --id run-fictional --profile prod

# Raw JSON for scripting
sparkwing cluster triggers get --id run-fictional --profile prod -o json
```

## `sparkwing cluster triggers list`

List pending / claimed / done / failed triggers

Queries GET /api/v1/triggers on the selected profile's
controller. Empty filters return the most recent 20 entries
across all statuses.

Useful when the queue looks stuck ("why isn't my trigger being
claimed?"): --status pending shows unclaimed work, --status
claimed shows what a worker has in-flight. The repo filter
matches GITHUB_REPOSITORY on the trigger env so webhook-driven
entries match the selected repository; that value is not indexed, so the
search covers the newest 5,000 triggers matching the other filters and
an older entry is not reported.

### Flags

| Flag | Description |
|---|---|
| `--status STATUS` | Filter by status: pending \| claimed \| done \| failed |
| `--pipeline NAME` | Filter by pipeline name |
| `--repo OWNER/NAME` | Match GITHUB_REPOSITORY on the trigger env, over the newest 5,000 triggers |
| `--limit N` | Maximum triggers to show (default: 20) |
| `-q, --quiet` | Print only trigger ids, newline-separated |
| `-o, --output FORMAT` | Output format: json emits the raw triggers array |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Recent triggers on prod
sparkwing cluster triggers list --profile prod

# Just pending
sparkwing cluster triggers list --profile prod --status pending

# Pipeline-specific, JSON
sparkwing cluster triggers list --profile prod --pipeline fictional-build --limit 5 -o json
```

## `sparkwing cluster users`

Manage dashboard login users

Seeds admin credentials in the controller's users table, used
by the dashboard's password sign-in. Connection info comes from the
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

## `sparkwing cluster worker`

Claim triggers from a profile's controller and run them in-process

Polls the trigger queue at the selected profile's
controller and executes each claimed trigger in-process on this host.
For k8s or warm execution, run sparkwing-runner runner --also-claim-triggers
--trigger-runner k8s|warm, which carries the image and service-account flags.

Run against a remote controller via --profile prod (or whichever profile),
or against a local 'sparkwing serve start' via --profile local.

### Flags

| Flag | Description |
|---|---|
| `--profile PROFILE` | Profile name from config.yaml (required) |
| `--poll DUR` | Claim poll interval when the queue is empty (default: 1s) |
| `--heartbeat DUR` | Claim-lease heartbeat cadence (default: 5s) |

### Examples

```sh
# Run against a named profile
sparkwing cluster worker --profile local

# Faster polling for tight dev loops
sparkwing cluster worker --profile local --poll 250ms
```
