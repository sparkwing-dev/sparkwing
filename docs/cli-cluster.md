<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing cluster

Every `sparkwing cluster` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

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

- `status` -- Connectivity + fleet + queue health check against a remote cluster
- `agents` -- Inspect the controller's fleet view
- `worker` -- Claim triggers from a profile's controller and run them in-process
- `gc` -- Sweep stale warm-PVC state
- `users` -- Manage dashboard login users
- `tokens` -- Manage controller API tokens
- `image` -- Rollout helpers for images referenced by a gitops repo
- `webhooks` -- Inspect and replay GitHub webhooks
- `concurrency` -- Inspect a single concurrency namespace: holders + queue

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

- `list` -- Print the controller's known agents

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

Composite verbs that operate on the images: block of a
kustomization.yaml plus the downstream ArgoCD / kubectl dance.
Building and pushing images stays with the consumer pipeline --
this subcommand only owns the "bump tag, commit, push, sync,
wait for rollout" path.

### Subcommands

- `rollout` -- Bump a kustomization image tag, commit+push, sync ArgoCD, optionally wait

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
  2. SPARKWING_GITOPS_REPO explicit environment configuration

If neither is set, rollout exits before reading or changing a repository.
Sparkwing never guesses a path from the user's home-directory layout.

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
| `--gitops-repo PATH` | Gitops repo path (or SPARKWING_GITOPS_REPO) |
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
Token creation prints the raw value to stdout exactly ONCE --
stash it immediately.

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
| `--principal NAME` | Free-form label identifying the token holder (required) |
| `--scope CSV` | Comma-separated scopes (e.g. runs.read,runs.write); auth.md lists the full set |
| `--ttl DURATION` | Token lifetime (e.g. 30d, 720h). 0 = never expires |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Mint a service token with write scopes
sparkwing cluster tokens create --type service --principal deploy-bot --scope runs.read,runs.write --profile prod

# Mint a user token that expires in 30 days
sparkwing cluster tokens create --type user --principal alice --scope admin --ttl 720h --profile prod
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
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# List all active tokens
sparkwing cluster tokens list --profile prod

# Audit every revoked service token
sparkwing cluster tokens list --type service --include-revoked --profile prod

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
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Inspect a token before revoking
sparkwing cluster tokens lookup --prefix a1b2c3d4 --profile prod
```

## `sparkwing cluster tokens revoke`

Mark a token revoked

Subsequent requests using the token receive HTTP 401. Revocation is immediate and irreversible.

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
lets callers cycle credentials without downtime.

### Flags

| Flag | Description |
|---|---|
| `--prefix PREFIX` | Non-secret prefix of the token to rotate (required) |
| `--grace DURATION` | Window during which the old token still authenticates (default: 24h) |
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
interactively.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Dashboard username (required) |
| `--password PASSWORD` | Password (omit to prompt interactively) |
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Interactive add
sparkwing cluster users add --name alice --profile prod

# Non-interactive add for CI
sparkwing cluster users add --name ci-bot --password "$CI_BOT_PW" --profile prod
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
| `--profile NAME` | Profile name (required) |

### Examples

```sh
# Delete a user
sparkwing cluster users delete --name alice --profile prod
```

## `sparkwing cluster users list`

Print every user

Prints name, created_at, and last_login_at for every user in
the controller's users table.

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

Sparkwing-aware wrapper over the GitHub hooks API. Shells out
to 'gh api' (inherits your gh auth); install gh from
https://cli.github.com if it isn't on PATH.

Value-add over 'gh api' alone: the deliveries view joins
GitHub's delivery log with sparkwing's trigger/run rows so
each delivery shows the run id it produced and the run's
terminal status -- without two separate lookups.

### Subcommands

- `list` -- List GitHub hooks configured on a repo
- `deliveries` -- List recent deliveries for a hook, joined with trigger state
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
or against a local 'sparkwing dashboard start' via --profile local.

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
