# Deployment modes

Sparkwing has two launch modes: **Local** and **Sparkwing Cloud**.
Self-hosted runners register to Cloud. Operating a shared controller is deferred
to a future licensed offering.

| Mode | Infrastructure you operate | Dashboard and history | Execution |
| --- | --- | --- | --- |
| Local | none | private to this OS user | this machine |
| Sparkwing Cloud | optional self-hosted runners | shared with your team | Cloud and registered runners |

## Local

`sparkwing run` starts the local controller on demand and hosts the run in the
CLI process. The controller owns SQLite, logs, secrets and local object storage
under `~/.sparkwing/`. Its private socket checks the connecting OS user. Local
mode needs no account, profile or network listener.

```bash
sparkwing run build
sparkwing dashboard start
sparkwing dashboard open
```

After the first successful build, exact-pinned pipelines can run offline. See
[Getting started](getting-started.md#offline-after-the-first-build).

## Sparkwing Cloud

Cloud owns shared history, the team dashboard, schedules, webhooks, credentials
and the queue. Clients and self-hosted runners connect over outbound HTTPS.
They hold no database or object-store credentials.

Save a member credential from stdin, then submit a pipeline:

```bash
sparkwing profiles add --controller https://api.sparkwing.example --profile work --token-stdin
sparkwing pipeline trigger build --profile work
```

`profiles add` checks public health and authenticates through
`/api/v1/auth/whoami`. Passing the probe grants only the credential's existing
authority. Read commands use the same explicit profile:

```bash
sparkwing runs list --profile work
```

Add `--working-tree` to submit an immutable snapshot of tracked and unignored
untracked files. The controller stores the bundle and seals its digest. Awarded
workers acquire that exact source through their claim authority.

A trigger, webhook run or schedule that selects no runner runs anywhere, on
Cloud and on self-hosted controllers alike: any eligible claimant can take its
plan step, generator evaluations and each job. The team's claim ranking orders
the claimants, best first: `here` (the personal runner on the machine whose
CLI started the run), `personal` (the requester's own machines),
`github-actions`, `self-hosted` (the team's shared runners and a local
controller's own queue) and `cloud` (the launcher; a self-hosted controller has
none and refuses `cloud` in a ranking). Each unit waits up to 5 seconds from readiness for the
best-ranked claimant that is online. The best-ranked claimant present takes it
at once. A claimant that asks while a better one is online is recorded. When
the window closes, the best-ranked recorded asker has 2 more seconds to come
back. After that, the first eligible asker wins. The launcher takes part only
while the team has credits or a saved card. A job that requires a `pool=`,
`name=` or `class=` term skips the ranking and goes to the runners it names.

Runs started by a push, pull request, release or schedule skip `here` and
`personal`. A personal machine takes them only after its owner requests it and
a team administrator approves. The owner requests with their own account
session, not a stored token:

```bash
sparkwing runners automation --profile work --runner laptop --request true --session-stdin
sparkwing runners automation --profile work --runner laptop --approve true
```

An approved machine ranks as `self-hosted` for automated runs and takes only
its owner's: schedules the owner armed, and App runs whose commit or pull
request author is the owner's linked GitHub account (the sender of the event
does not count). The owner's GitHub login must also be able to read the run's
repository and every extra repository. The controller checks that through the
GitHub App before each claim and caches the answer for 10 minutes. Revoking
the machine or running `--approve false` ends the approval. The console's
Machines page shows the same request and approval controls.

A team administrator reorders or drops tiers. A dropped tier takes no
unselected work, and `none` means first come:

```bash
sparkwing team settings set --profile work --claim-ranking cloud,self-hosted
```

App runs and App-armed schedules take their owner from the repository's App
configuration (`runner: anywhere | cloud | github-actions | pool=<name>`,
default `anywhere`). `--runner NAME` or `--runner label=LABEL` keeps the run on
matching self-hosted runners, as does a pool route rule, and `--runner here`
hosts it in the submitting CLI. Intake seals the choice in the run's request,
so retries keep it. A run whose work nobody has claimed after 30 seconds shows
a notice in `sparkwing runs status`. When only metered Cloud placement could
take that work and the team has no credits or payment method left, the notice
is `credit_hold` instead, naming the hold, and the launcher takes the work as
soon as credits or a card arrive. Work an unmetered runner of the team could
take keeps the `unclaimed_work` notice. When no eligible runner is online at
all, the notice is `no_eligible_runner` and names why: a tier the ranking drops
("Cloud is off in this team's runner ranking"), a pin ("this job targets
pool=gpu only"), or that no runner of the allowed tiers is online.

## Self-hosted runners

Enroll a machine into a Cloud team:

```bash
sparkwing runners add --profile work --runner builder
```

Personal capacity belongs to your own account and requires an independent
account session during enrollment. Team capacity uses a team-admin credential.
See [Self-hosted runners](self-hosting.md#add-workstation-capacity).

## Selecting a profile

A profile in `~/.config/sparkwing/config.yaml` selects a controller connection.
Pass `--profile NAME` on the command that should reach Cloud. `sparkwing run`
uses local state. Its `--sw-cache-from PROFILE` and `--publish PROFILE` options
opt into read-only remote cache access and publication separately. Pipeline
binaries stay in the machine's local cache. There is no separate cache service.

When isolating runtime state with `SPARKWING_HOME`, set `SPARKWING_CONFIG` to a
private config path too before saving a profile. See
[Storage backends](backends.md) and [Local execution](local-execution.md).

## Advanced shapes

### Hosted controller

The repository's controller and Helm chart are Cloud operator infrastructure.
The controller owns persistence and issues scoped object transfers through
`/data`. Runners never write directly to its database. The SDK pin selects the
verified matching engine, and controller APIs retain the engine's support window.

Cloud operators can create a team and its first administrator without OAuth:

```bash
sparkwing operator teams create --team support-test --display-name Support --profile deployment -o json
```

This requires deployment authority with `operator.tokens`, or the operator's
direct browser session and CSRF proof. The operation returns the first team
credential once. See [Cloud operations](auth.md#cloud-operations-and-test-environments)
and the [controller chart](../charts/sparkwing-full/README.md).
