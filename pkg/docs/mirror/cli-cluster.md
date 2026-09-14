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
- `credits` -- Inspect and top up the prepaid credit balance
- `limits` -- Read and set the compute guards
- `image` -- Rollout helpers for images referenced by a gitops repo
- `webhooks` -- Connect, inspect, and replay GitHub webhooks
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

## `sparkwing cluster credits`

Inspect and top up the prepaid credit balance

Cloud runner time is prepaid. One hundred credits is one dollar,
so a ten dollar top-up is a thousand credits. The balance is
grants minus charges: a claim reserves a minute of cloud runner
time before it is granted, heartbeats charge the seconds they
cover, and the finish refunds whatever of the reservation the
node did not use. Runners the operator did not mark metered are
never charged.

### Subcommands

- `show` -- Print the balance, the rate table, and the recent burn
- `grant` -- Add free or paid credits to the ledger, or reverse a paid grant
- `history` -- List grants and charges, newest first
- `settings` -- Read or set the credit rate table, the grace period, and the charge cap
- `allowance` -- Read or set how many retained bytes a team keeps

### Examples

```sh
# Read the balance and the burn
sparkwing cluster credits show --profile prod

# Load ten dollars
sparkwing cluster credits grant --kind paid --amount 1000 --reference pay_12345 --profile prod
```

## `sparkwing cluster credits allowance`

Read or set how many retained bytes a team keeps

Retained bytes are what a team still has stored after its
runs end, and the storage pass bills them at the storage rate.
The allowance is how many of them the team asked to keep: the
pass expires its oldest finished runs above the allowance before
it bills and never bills above it, so the allowance is both what
the team keeps and the most it pays for. An allowance of zero
keeps everything and caps nothing. This verb is the only writer;
rewriting a team's quota leaves the allowance alone.

Reading with no flag reports the calling token's own allowance
and what it currently retains. An admin token reads another
team by naming it. Setting one needs the admin scope and names
the team. The free allowance every team keeps unbilled and the
price of a gibibyte-day are controller-wide settings on
`sparkwing cluster credits settings`.

### Flags

| Flag | Description |
|---|---|
| `--principal NAME` | Team whose allowance to read or set; required to set one |
| `--gb N` | Gibibytes of retained storage to keep; 0 keeps everything |
| `--bytes N` | Bytes of retained storage to keep; 0 keeps everything |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Read what this token's team keeps
sparkwing cluster credits allowance --profile prod

# Keep fifty gibibytes for a team
sparkwing cluster credits allowance --principal acme --gb 50 --profile prod
```

## `sparkwing cluster credits grant`

Add free or paid credits to the ledger, or reverse a paid grant

Adds credits and records who added them, which kind they are, and
the payment they came from. One hundred credits is one dollar.
A grant that lifts the balance above zero lets metered runners
claim again and stops the cancellation of nodes running on an
empty balance. A reference is the payment id: granting it twice
returns the first grant rather than adding the credits again. A
reversal takes a refunded payment back out with a negative
amount, its own reference (the refund id) and --reverses naming
the paid grant's reference. Requires the admin scope.

### Flags

| Flag | Description |
|---|---|
| `--kind KIND` | Grant kind: free \| paid \| reversal (required) |
| `--amount N` | Credits to add, negative on a reversal; 100 credits is one dollar (required) |
| `--reference REF` | Payment id or operator note recorded with the grant; granting the same one twice returns the first grant |
| `--reverses REF` | Reference of the paid grant a reversal takes back |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Load ten dollars against a payment
sparkwing cluster credits grant --kind paid --amount 1000 --reference pay_12345 --profile prod

# Hand out trial credits
sparkwing cluster credits grant --kind free --amount 500 --profile prod

# Take a refunded payment back out
sparkwing cluster credits grant --kind reversal --amount -1000 --reference re_9 --reverses pay_12345 --profile prod
```

## `sparkwing cluster credits history`

List grants and charges, newest first

Lists every movement of the ledger newest first: grants with
their kind and reference, and the reservation a claim took, the
usage an interval billed, and the refund of a reservation a node
did not use, each with the run, node, token prefix and seconds
it covered, and the cpu class and rate it was billed at. Charges
render negative because they take credits
out and a refund renders positive. -o json emits one JSON record
per line.

### Flags

| Flag | Description |
|---|---|
| `--limit N` | Maximum rows of each kind, up to 1000 (0 = the controller's default) |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Read the ledger
sparkwing cluster credits history --profile prod

# Sum today's charges
sparkwing cluster credits history --profile prod -o json | jq 'select(.type=="charge") | .amount_micro'
```

## `sparkwing cluster credits settings`

Read or set the credit rate table, the grace period, and the charge cap

Prints the runtime settings the ledger prices work with, and
sets the ones named by a flag. The rate table prices one cloud
runner second at every cpu class, and a node is billed at the
smallest class covering the cpu and memory it pinned; a request
above the largest class fails the node. The rate is what a
four-core second costs, which is the four-core entry of the
table under another name, so a body may name one or the other,
never both. The warm cpu class is the largest class the warm
runner pool serves: a node above it starts a Kubernetes node
sized to its class instead, and zero starts a node of its own
for every class. The grace period
is how long a node keeps running after it has consumed the
reservation its claim paid for with the balance at zero: a node
inside that reservation is never cancelled, because the ledger
already took payment for it. The charge cap is the most seconds
any one charge may bill, which forgives a controller outage or a
stalled heartbeat loop rather than billing the gap. A flag left
off leaves that setting alone, and a refused value moves
nothing. Grace zero cancels a metered node at the first
heartbeat past its reservation, which bounds the unpaid overrun
to one heartbeat interval per node. An installation that never
set a table bills the default ladder. Reading needs the
runs.read scope and setting needs admin.

### Flags

| Flag | Description |
|---|---|
| `--rate-table PAIRS` | Price every cpu class, as CORES=MICRO pairs: 2=10000,4=20000,8=36667 |
| `--warm-cpu-class-cores N` | Largest cpu class the warm runner pool serves; a larger class starts a node of its own |
| `--rate-micro N` | Micro-credits one four-core cloud runner second costs, 1 to 1000000000000; refused once a rate table exists |
| `--grace-seconds N` | Seconds a node runs past its reservation on an empty balance; 0 cancels at the next heartbeat |
| `--max-charge-seconds N` | The most seconds any one charge may bill, 6 to 86400 |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Read the settings
sparkwing cluster credits settings --profile prod

# Cut a node off at the first heartbeat past its reservation
sparkwing cluster credits settings --grace-seconds 0 --profile prod

# Reprice a cloud runner second at 0.03 credits
sparkwing cluster credits settings --rate-micro 30000 --profile prod

# Price the six sizes at the GitHub Actions rates
sparkwing cluster credits settings --rate-table 2=10000,4=20000,8=36667,16=70000,32=136667,64=270000 --profile prod
```

## `sparkwing cluster credits show`

Print the balance, the rate table, and the recent burn

Prints the balance in credits, what was granted and charged, the
price of a cloud runner second at every cpu class, the credits
burned over the last day, the grace period a node gets past the
reservation its claim paid for, and the cap on what any one
charge may bill. A
controller that was never granted anything reads a zero balance
and charges nothing, because nothing is metered until an
operator marks a token.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Read the balance
sparkwing cluster credits show --profile prod

# Read the balance as JSON
sparkwing cluster credits show --profile prod -o json
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

- `show` -- Print every compute guard and the cloud runners in use
- `set` -- Set one compute guard

### Examples

```sh
# Read the guards and what they measure
sparkwing cluster limits show --profile prod

# Cap the cloud runners one principal holds
sparkwing cluster limits set --name max_concurrent_runners --value 20 --profile prod
```

## `sparkwing cluster limits set`

Set one compute guard

Sets one guard to a ceiling, or to zero to remove it. The guards are
max_concurrent_runners, max_global_runners, runner_alarm, max_run_seconds,
max_nodes_per_run, max_runs_per_hour, max_global_nodes_per_run,
max_global_runs_per_hour, min_cron_interval_seconds, runner_scale_base,
runner_scale_step_credits and runner_scale_ceiling. The per-principal
guards bind a principal holding a metered token; the two max_global settings bind
every run. The three runner_scale settings raise max_concurrent_runners by one
runner_scale_base for every runner_scale_step_credits of paid credit granted in
the last 30 days, held under runner_scale_ceiling. Work past a guard answers
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

# Add a hundred runners per 5000 credits loaded
sparkwing cluster limits set --name runner_scale_step_credits --value 5000 --profile prod
```

## `sparkwing cluster limits show`

Print every compute guard and the cloud runners in use

Prints each guard with its ceiling, or "unlimited" when nothing set
one, then the cloud runners claimed now in total and per principal. A
cloud runner is a claim a metered token holds, so a controller that
marks no token metered reads zero.

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
state appears on 'sparkwing cluster status' and on the controller's
Prometheus metrics as sparkwing_object_store_requests_total,
sparkwing_object_store_trips_total, and sparkwing_object_store_tripped.

Budgets come from SPARKWING_OBJECT_STORE_<CLASS>_PER_MINUTE and
SPARKWING_OBJECT_STORE_<CLASS>_PER_DAY on the controller process.
SPARKWING_OBJECT_STORE_TRIP_RESET chooses whether a tripped class
clears when its day window rolls (day, the default) or waits for an
operator (manual). A local process that must finish past a tripped
budget sets SPARKWING_OBJECT_STORE_BREAKER=off.

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
- `set-metered` -- Mark an existing token as one credits pay for

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
| `--metered` | Mark the token as one whose node claims cost credits |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Mint a service token with write scopes
sparkwing cluster tokens create --type service --principal deploy-bot --scope runs.read,runs.write --profile prod

# Mint a user token that expires in 30 days
sparkwing cluster tokens create --type user --principal fictional-user --scope admin --ttl 720h --profile prod

# Mint a metered cloud runner token
sparkwing cluster tokens create --type runner --principal agent:cloud-pool --scope nodes.claim --metered --profile prod
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

## `sparkwing cluster tokens set-metered`

Mark an existing token as one credits pay for

Sets or clears the metering marker on a token that is already
minted, which is how a warm pool already running starts costing
credits without a new credential. Metering is an operator
decision: a runner's own labels never make its work billable.
A claim by a metered token reserves a minute of cloud runner
time and is refused when the balance cannot cover it.

### Flags

| Flag | Description |
|---|---|
| `--prefix PREFIX` | Non-secret token prefix (from 'tokens list') (required) |
| `--metered BOOL` | true to charge this token's claims, false to stop (required) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Start charging the warm pool
sparkwing cluster tokens set-metered --prefix swr_a1b2c3d4 --metered true --profile prod

# Stop charging a token
sparkwing cluster tokens set-metered --prefix swr_a1b2c3d4 --metered false --profile prod
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

Connect, inspect, and replay GitHub webhooks

Manage GitHub webhooks through the installed 'gh' command and its
credentials. 'connect' registers a repository against a pipeline on both
sides and 'disconnect' removes it; the deliveries view joins delivery
records with Sparkwing triggers and run outcomes.

### Subcommands

- `connect` -- Connect a GitHub repository to a pipeline
- `disconnect` -- Remove a repository's webhook and its controller binding
- `list` -- List GitHub hooks configured on a repo
- `deliveries` -- List recent deliveries for a hook, joined with trigger state
- `replay` -- Queue a redelivery of a specific delivery UUID

### Examples

```sh
# Connect a repository to a pipeline
sparkwing cluster webhooks connect --profile prod --repo your-org/my-app --pipeline build

# List hooks on a repo
sparkwing cluster webhooks list --repo your-org/my-app

# Recent deliveries for a hook
sparkwing cluster webhooks deliveries --repo your-org/my-app --hook 123456789 --since 1h --profile prod
```

## `sparkwing cluster webhooks connect`

Connect a GitHub repository to a pipeline

Registers both sides of a webhook in one command. It generates a
signing secret, stores the binding on the controller, creates or
updates the repository's webhook through 'gh' so it posts to the
controller's delivery URL for this pipeline, and asks GitHub for a
ping so the answer the controller gave is part of the output.

The secret is never printed and never passed in a command line; the
controller stores it and verifies every delivery's HMAC against it.
Re-running the command rotates the secret on both sides.

The delivery URL comes from the controller: its --external-url when
it announces one, and otherwise the URL this command reached it at.

### Flags

| Flag | Description |
|---|---|
| `--repo OWNER/NAME` | GitHub repo (owner can be omitted if gh has a default) (required) |
| `--pipeline NAME` | Pipeline the deliveries fire (required) |
| `--events LIST` | Comma-separated GitHub events (default: push,pull_request) |
| `--profile NAME` | Profile name (the controller that stores the binding) (required) |

### Examples

```sh
# Connect push and pull-request triggers
sparkwing cluster webhooks connect --profile prod --repo your-org/my-app --pipeline build

# Connect pushes only
sparkwing cluster webhooks connect --profile prod --repo your-org/my-app --pipeline build --events push
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

## `sparkwing cluster webhooks disconnect`

Remove a repository's webhook and its controller binding

Removes the binding the controller verifies deliveries against, then
deletes the webhook on GitHub through 'gh'. The controller answers with
the webhook it was bound to, so a repository connected to two
controllers under the same pipeline name loses only this one. A webhook
written by hand is matched by its pipeline path instead, and every
deleted hook is printed with its URL.

Either side already being absent is reported rather than failing, so a
half-finished connect is cleaned up by running this once.

### Flags

| Flag | Description |
|---|---|
| `--repo OWNER/NAME` | GitHub repo (required) |
| `--pipeline NAME` | Pipeline the webhook fires (required) |
| `--profile NAME` | Profile name (the controller holding the binding) (required) |

### Examples

```sh
# Disconnect a repository
sparkwing cluster webhooks disconnect --profile prod --repo your-org/my-app --pipeline build
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
