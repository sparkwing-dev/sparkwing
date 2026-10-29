# Migrating to the next release

v0.68 rewrites the engine, the store and the trust model. The upgrade is
mostly mechanical, but it is not in place: v0.68 starts a new, empty store,
and the CLI, each repository's SDK pin, the controller and its runners move to
v0.68 together. Read [What changed and why](#what-changed-and-why), then the
section for each way you use Sparkwing. The
[reference](#reference-every-breaking-change) at the end lists every breaking
change one by one.

## What changed and why

- **The engine hosts every run.** `sparkwing run`, runners and Cloud Jobs start
  the pipeline binary as a [node protocol](../node-protocol.md) process and
  plan and run each job through it. The CLI runs each repository with the
  engine its SDK pin names, so a repository gets the v0.68 engine only once
  its pin moves to v0.68.
- **A new store.** The schema starts again at a new baseline and has no
  importer, so run history, secrets, tokens, teams and schedules do not carry
  over. Later releases migrate the store in place.
- **Every run carries a sealed identity.** Intake records a trust class
  (`verified-default`, `verified`, `actions`, `schedule` or `asserted`) with
  the repository, ref and commit Sparkwing can prove. Pipeline secrets, OIDC
  claims, cache scopes and reserved pools read only that record, because a
  value the run supplied about itself proves nothing. A pipeline secret set
  for one repository can no longer reach another repository's pipeline of the
  same name.
- **One way to do each thing.** Command families folded into one registry
  (`cluster`, `cloud`, `configure`, `crons`, `fleet` and `serve` are gone),
  and the logs service, the cache service, password users and the assisted
  fleet are removed with no shims. A removed config key fails with an error
  naming it; a removed command fails with a usage error, and
  [the command table](#removed-and-renamed-commands) maps it to its
  replacement.
- **Cloud is prepaid.** Usage draws on a prepaid balance, Stripe holds every
  card, and a Cloud job runs only on a fresh, isolated pod.

## Before you start

1. Let in-flight runs finish. The new store does not carry them over, so a
   run still queued or running at the upgrade is lost.
2. Export anything you need from the old store: secret values you cannot
   recreate, run history and logs. A v0.67 binary still reads the old store.
3. Upgrade the controller and every runner in one window. Runner credentials
   live in the store, so every runner enrolls again against the v0.68
   controller.

## Upgrading from v0.67: v0.68 starts a new store

v0.68 replaces the store. It cannot read a store written by v0.67 or earlier,
and there is no import tool. Nothing in the old store carries over: run
history, logs, caches, secrets and tokens, and on a server also teams, members,
invitations, credits and schedules.
The new store's schema baseline moves to version 25.

When v0.68 finds an old store it refuses to start, names the store, and leaves
it unchanged. It never deletes or rewrites it.

- **Local installs:** move the old store aside and run again. Sparkwing creates
  a new store on the next start.

  ```sh
  mv ~/.sparkwing/state.db ~/.sparkwing/state.db.v067
  ```

  Move `state.db-wal` and `state.db-shm` with it if they exist. With
  `SPARKWING_HOME` set, the store is `$SPARKWING_HOME/state.db`. Add secrets
  again with `sparkwing secrets`.
- **Servers:** back up the database, then point the controller at an empty
  database (PostgreSQL) or an empty `state.db` path (SQLite). Re-create teams,
  invite members, mint tokens, store secrets and register runners against the
  new controller. Keep the backup if you need the old history, and read it with
  a v0.67 binary.

Releases after v0.68.0 ship schema changes as forward migrations. See
[Schema compatibility](../backup-restore.md#schema-compatibility).

## Upgrade a local install

These steps cover a laptop or CI machine that runs pipelines with
`sparkwing run` and keeps its own store.

1. Install the v0.68 CLI with `sparkwing update`, or with the install script
   from [Getting started](../getting-started.md).
2. Move the old store aside, as
   [the store section](#upgrading-from-v067-v068-starts-a-new-store) shows.
   The next command creates a new one.
3. Clean `~/.config/sparkwing/config.yaml`. The file must be mode `0600`, and
   every unknown key fails with its file and line. Delete the `agent`, `logs`
   and `fleet` sections and the `allow_repos` and `dispatch_wait` keys. A
   profile now holds only `controller.url` and an optional `controller.token`,
   so delete every other profile key. See
   [Machine settings](../machine-config.md).
4. Recreate controller connections with
   `sparkwing profiles add --controller URL --profile NAME --token-stdin`.
   A command reaches a controller only when `--profile` names it.
5. Move every repository's SDK pin to v0.68 before running it, as
   [Upgrade pipeline code](#upgrade-pipeline-code) shows.
6. Reinstall git hooks with `sparkwing pipeline hooks install`, because hooks
   an older CLI wrote can pass flags v0.68 removed, such as `--sw-local-only`.
7. Set local secrets again with `sparkwing secrets set --secret NAME`.
8. Open the local dashboard with `sparkwing dashboard start`, then
   `sparkwing dashboard open`. The dashboard now needs the one-time link that
   `open` prints; it expires after 60 seconds.

`sparkwing run` always runs on this machine and refuses `--profile`. To run on
a controller, use `sparkwing pipeline trigger PIPELINE --profile NAME`.

## Upgrade a self-hosted controller

Running your own shared controller is outside the launch offering, which is
local use and Sparkwing Cloud, but the `sparkwing-full` and
`sparkwing-runner-bundle` charts still ship. v0.68 does not migrate a v0.67
database: the controller refuses it with
`... is a store from Sparkwing v0.67 or earlier`, and leaves it unchanged.

1. **Back up the database** and keep a v0.67 controller binary if you need
   to read the old history.
2. **Point the controller at an empty database.** On PostgreSQL, create a new
   database and change the `pg-url` credential. On SQLite, move `state.db` out
   of the data directory or mount a new volume.
3. **Declare the deployment.** Pass `--deployment=local` (or `team` with a
   multi-team license) and `--store=sqlite` or `--store=postgres`. A
   controller with a `pg-url` credential refuses to start without
   `--store=postgres`.
4. **Drop removed flags:** `--argon2-memory-budget-mb`, `--cache-url`,
   `--cache-pod-url`, `--free-team-slots`, `--logs-url`,
   `--logs-archive-store` and `--trigger-dedupe-window`. Each now fails as an
   unknown flag. `--require-auth`, which the chart always passes, refuses to
   start until a deployment administrator exists, so seed one with a
   `bootstrap-admin-token` credential: `swk_` followed by unpadded base64url
   of 32 random bytes.
5. **Clean the credentials directory.** Startup refuses any file the
   controller does not read, so delete `logs-delete-token` and any other file
   the controller does not list. See
   [controller credentials](../self-hosting.md#controller-credentials).
6. **Plan sign-in.** Password users are gone. On a multi-team license,
   configure Google or GitHub sign-in and list operators in
   `--operator-accounts`; otherwise the operator token is the dashboard's
   sign-in. See [Password users are removed](#password-users-are-removed).
7. **Rebuild the team.** Create teams with
   `sparkwing operator teams create --team SLUG --profile PROFILE`, invite
   members from the console, enroll runners with `sparkwing runners add`, and
   set secrets again with their repository (see
   [secrets](#behavior-you-will-notice)). The controller host no longer
   claims work, so register at least one runner.
8. **Upgrade every runner at the same time**, including `sparkwing-runner`
   services and GitHub Actions runners. Enroll a runner that runs in a
   Kubernetes pod with
   `sparkwing runners add --runner NAME --profile NAME --platform kubernetes`.

### Helm values

`sparkwing-full` moves from chart 0.2.0 to 0.3.2 and `sparkwing-runner-bundle`
from 0.1.10 to 0.1.17. Neither chart falls back to `appVersion` for images.
Through `sparkwing-full`, the runner-bundle keys below sit under
`sparkwing-runner-bundle.`.

| Chart | Change |
|---|---|
| both | Set `image.repository` and `image.tag` for every enabled controller, runner and launcher image. |
| `sparkwing-full` | Add `controller.deployment` (`local`, `team` or `cloud`) and `controller.store` (`sqlite` or `postgres`). Every PostgreSQL install sets `controller.store: postgres`, or rendering fails. |
| `sparkwing-full` | Delete `controller.requireAuth`, `controller.allowOpenBootstrap`, `controller.logs`, `controller.cache.url`, `controller.argon2MemoryBudgetMB` and `controller.triggerDedupeWindow`. Rendering fails while `requireAuth`, `allowOpenBootstrap` or `logs` is present, even with the old default. |
| `sparkwing-full` | `controller.storage.type: emptyDir` needs `controller.store: postgres` and `controller.dataStore.s3URL`; otherwise use `pvc`. |
| `sparkwing-full` | The chart now creates a small retained PVC for the credential epoch. |
| `sparkwing-runner-bundle` | Set `runner.repos`. An empty list admits no repository. |
| `sparkwing-runner-bundle` | Delete the `cache` and `logs` blocks, the top-level `networkPolicy` block, `rbac.create`, `serviceAccount.shareAcrossComponents`, `runner.triggerRunner`, `runner.jobCeiling`, `runner.jobDeadline`, `runner.teamNodes` and `runner.gitcacheUrl`. A `logs` key fails rendering; the others are ignored. |
| `sparkwing-runner-bundle` | Pools that hold reserved secrets use `launcher.enabled: true` with `launcher.tokenSecret` and digest-pinned job images. The cluster's CNI must enforce NetworkPolicy. |

After the upgrade, delete the old logs PVC and the cache deployment, as
[The logs service is removed](#the-logs-service-is-removed) shows.

## Upgrade pipeline code

Every repository's `.sparkwing` module moves to the v0.68 SDK, because the
CLI runs each repository with the engine its pin names. Go pipelines pin `github.com/sparkwing-dev/sparkwing` in `.sparkwing/go.mod`.
`sparkwing upgrade` changes a pin only after the new SDK compiles and plans
the pipeline, so move the pin by hand, fix the code, then check it:

```sh
go -C .sparkwing get github.com/sparkwing-dev/sparkwing@v0.68.0
go -C .sparkwing mod tidy
go -C .sparkwing build ./...
sparkwing pipeline plan --pipeline build
sparkwing run build
```

The entrypoint moves out of the removed `pkg/runner` package:

```go
// Before
import "github.com/sparkwing-dev/sparkwing/pkg/runner"

func main() { runner.Main() }

// After
import "github.com/sparkwing-dev/sparkwing/sparkwing"

func main() { sparkwing.Main() }
```

Renamed and removed Go SDK APIs, all in package `sparkwing` unless named:

| Before | After |
|---|---|
| `pkg/runner.Main()` | `sparkwing.Main()` |
| `JobNode.Memoize(fn)`, `Key(...)`, `NoCache`, `TTL(d)` | `JobNode.Memo(recipe)`, `MemoKey(...)`, `NoMemo`, `MemoTTL(d)` |
| An `inputs.Files(...)` result called as `(ctx)` | Pass the recipe to `Memo`, combining several with `inputs.Compose` |
| Outcome `Cached` | `Memoized` |
| `JobNode.CacheDir(...)`, `NpmCache()` | `JobNode.Cache(...)`, `Npm()` |
| `Outputs(globs...)` | `Artifacts(globs...)` |
| `NeedsOptional(...)` | `NeedsIfPresent(...)` |
| `JobApproval(plan, id, cfg)`, `ApprovalConfig.OnExpiry` | `plan.Approval(id, cfg)`, `ApprovalConfig.OnTimeout` |
| `JobFanOutDynamic`, `ExpandGenerator` | `source.Generate(fn)`, and `NeedsGenerated` on the jobs that wait for it |
| `Resources(Cores(n), MemoryGB(n))` | A runner label: `Requires("sparkwing-4cpu")` on Cloud |
| `Config(ctx, name)`, `MustConfig` | `Variable(ctx, name)`, `MustVariable` |
| `RetryBackoff(d)` | `Backoff(d)` |
| `RetryAuto`, `WhenRunner`, `LoggerFromContext`, `NodeFromContext`, `StepFromContext`, `CancelTimeout` | Removed. Log with `sparkwing.Info`, `Warn` and `Error` |
| `pkg/controller`, `pkg/store`, `pkg/storage`, `pkg/logs`, `sparkwing/planguard` | Removed. Call the HTTP API instead |

Memo keys move into the plan, where the executing host evaluates them:

```go
// Before
sparkwing.Job(plan, "build", &Build{}).
    Memoize(func(ctx context.Context) (sparkwing.CacheKey, error) {
        files, err := inputs.Files("go.sum")(ctx)
        return sparkwing.Key(files, "v1"), err
    })

// After
sparkwing.Job(plan, "build", &Build{}).
    Memo(inputs.Compose(inputs.Files("go.sum"), inputs.Const("v1")))
```

Runner size is a label, not a resource request:

```go
// Before
sparkwing.Job(plan, "test", &Test{}).Resources(sparkwing.Cores(4), sparkwing.MemoryGB(8))

// After
sparkwing.Job(plan, "test", &Test{}).Requires("sparkwing-4cpu")
```

A job that starts another pipeline declares it, or the controller answers
`403`:

```go
// After
sparkwing.Job(plan, "release", &Release{}).StartsChildren("deploy")
```

Other changes the compiler does not catch:

- **Plan is deterministic.** Every job process plans the run again, and a job
  whose plan entry differs from the accepted one fails with `plan_drift`. Read
  the clock, randomness and the environment in step bodies, not in `Plan`.
- **Run options are not environment variables.** `--sw-only`,
  `--sw-start-at` and the other run flags no longer reach pipeline code as
  `SPARKWING_*` variables. `SPARKWING_DRY_RUN` remains.
- **Declare every secret.** A job reads only names its `Secrets()` struct
  declares; any other name answers `403` with reason `secret_undeclared`.
- **The binary needs a host.** A pipeline binary starts only under a host:
  `sparkwing run`, a runner, Cloud, or `sparkwingtest` in tests. Run on its own,
  it exits with an error.
- **Concurrency groups.** Plan and job groups are separate namespaces, a plan
  group cannot use box scope, and an approval cannot declare a group.
- **Memo entries start empty.** Each memoized job runs once to fill the new
  cache.

The TypeScript SDK, `@sparkwing/sdk`, is first released with v0.68; v0.67
carried only an unpublished prototype. A pipeline written against the
prototype pins `@sparkwing/sdk` 0.68.0 in `.sparkwing/package.json` with its
lockfile, and calls `main()` with no arguments.

## Sparkwing Cloud customers

Sparkwing Cloud runs v0.68 on its side. On yours:

1. Upgrade the CLI and every repository's pin, as in
   [Upgrade a local install](#upgrade-a-local-install) and
   [Upgrade pipeline code](#upgrade-pipeline-code).
2. Mint a CLI token on the console's **Team > Machines** page and save it:
   `sparkwing profiles add --controller URL --profile cloud --token-stdin`.
3. Set each pipeline secret again with its repository:
   `sparkwing secrets set --secret DEPLOY_KEY --repo acme/web --pipeline deploy --profile cloud`.
4. Update cloud trust policies for the new OIDC subject, as
   [OIDC runner environment and triggers](#oidc-runner-environment-and-triggers)
   shows.
5. Replace CPU and memory requests with a size label such as
   `sparkwing-4cpu`. A job with no size label runs on `sparkwing-2cpu`.
6. Add funds from the console's billing page. Automatic refill starts off; turn
   it on with
   `sparkwing billing refill --profile cloud --enable --threshold 20 --amount 50`.
   The team needs a saved card first.

If your team, balance, GitHub App connection or schedules are not there after
the upgrade, write to support@sparkwing.dev.

## Removed and renamed commands

Each removed command now fails with a generic usage error, so use this table
to find its replacement. Flags left over from v0.67 fail as unknown flags.

| Before | After |
|---|---|
| `sparkwing cloud connect`, `configure profiles add` | `sparkwing profiles add --controller URL --profile NAME --token-stdin` |
| `sparkwing cloud status`, `configure profiles show` | `sparkwing profiles show --profile NAME` |
| `sparkwing cloud disconnect` | `sparkwing profiles remove --profile NAME` |
| `sparkwing configure init` | none; `sparkwing profiles add` writes the machine file |
| `sparkwing crons ...` | `sparkwing schedules ...`, with the same verbs plus `pause` and `resume` |
| `sparkwing serve start`, `serve stop`, `serve logs` | `sparkwing dashboard start`, `dashboard stop`, `dashboard logs` |
| `sparkwing cluster tokens ...` | `sparkwing operator tokens ...` for deployment credentials, `sparkwing tokens ...` for team credentials |
| `sparkwing cluster limits ...`, `cluster object-store ...` | `sparkwing operator limits ...`, `operator object-store ...` |
| `sparkwing cluster runners add`, `cluster agents enroll`, `fleet init` | `sparkwing runners add --runner NAME --profile NAME` |
| `sparkwing cluster runners remove` | `sparkwing runners remove --runner NAME --profile NAME` |
| `sparkwing cluster concurrency` | `sparkwing queue list --group GROUP` |
| `sparkwing cluster users ...` | none; people sign in with Google or GitHub |
| `sparkwing cluster triggers ...`, `cluster worker`, `runs consumer ...` | none; `sparkwing queue list` shows queued work and runners claim it |
| `sparkwing debug run` | `sparkwing run PIPELINE --sw-pause-before JOB` |
| `sparkwing debug release` | `sparkwing debug resume` |
| `sparkwing debug replay`, `debug rerun`, `runs bounce` | `sparkwing runs cancel`, then `sparkwing runs retry --run RUN --failed` |
| `sparkwing pipeline sparks warmup` | `sparkwing cache warm` |
| `sparkwing pipeline sparks resolve` | none |
| `sparkwing run PIPELINE --profile NAME` | `sparkwing pipeline trigger PIPELINE --profile NAME` |
| `sparkwing run PIPELINE --plan`, `--explain` | `sparkwing pipeline plan --pipeline PIPELINE` |
| `sparkwing run PIPELINE config` | `sparkwing pipeline describe --pipeline PIPELINE --secrets` |
| `--sw-no-cache` | `--sw-no-memo` |
| `--sw-allow` | `--sw-allow-risk` |
| `--sw-fleet`, `--sw-local-only`, `--sw-artifact-digest-backfill` | none |
| `sparkwing secrets set --name NAME --plain` | `sparkwing secrets set --secret NAME --variable` |
| `--name PIPELINE` on `pipeline` commands | `--pipeline PIPELINE` |
| `--node JOB` on `runs` commands | `--job JOB` |
| `--query` on `commands`, `docs` and `pipeline list`; `--grep` on `secrets list` | `--search` |
| `sparkwing runs retry --all` | `sparkwing runs retry --run RUN --all-jobs` |
| `sparkwing update --check` | `sparkwing update --dry-run` |

Pipeline arguments now follow `--`, as in `sparkwing run deploy -- --region us-east-1`.

## Behavior you will notice

- **Pipeline secrets belong to one repository.** `sparkwing secrets set`,
  `get` and `delete` take `--repo OWNER/NAME` with `--pipeline`. Only runs
  whose code Sparkwing fetched from that repository through the GitHub App,
  or whose repository a GitHub Actions job proved, read them. Local, uploaded
  and `--runner here` runs read only `--shared` secrets. See
  [Secrets and run identity](../secrets.md).
- **Trust classes decide what a run receives.** A pool that holds reserved
  secrets takes only verified default-branch work, through a launcher, by
  default
  (`sparkwing runners add --runner NAME --profile NAME --reserved-for POOL --trust verified-default --launcher`).
  Verified default-branch work never runs on a persistent Kubernetes runner.
- **OIDC subjects gain `runner_environment`.** It is `fresh` on launcher pods,
  Cloud pods and GitHub-hosted Actions runners and `persistent` elsewhere.
  GitHub Actions-bound runs mint `trigger:actions`. A deploy role should
  require `runner_environment:fresh`. See [OIDC tokens](../oidc.md#subject-format).
- **Runs without a runner selector run anywhere.** Claimants rank `here`,
  `personal`, `github-actions`, `self-hosted`, then `cloud`; change it with
  `sparkwing team settings set --profile NAME --claim-ranking cloud,self-hosted`.
  Personal machines take only their owner's manual runs and never receive team
  git credentials.
- **Cloud jobs run on fresh pods.** A warm Cloud node serves only jobs of the
  same run, and Docker on Cloud refuses `--privileged` and host namespaces.
  See [Docker on Cloud](../limits.md#docker-on-cloud).
- **Caches are scoped to who wrote them.** Manual runs by different members no
  longer share memo, dependency-cache or output entries. See
  [Who shares a cache](../caching.md#who-shares-a-cache).
- **Child runs are declared.** A job starts only the child pipelines its plan
  names. Children nest at most 10 levels, and a root run may start 50
  descendants by default
  (`sparkwing team settings set --profile NAME --max-children-per-root 200`).
- **Job outputs that contain a secret are withheld.** The job passes with an
  `output withheld: contains a secret` warning. Return the secret's name
  instead of its value.
- **GitHub deliveries queue.** Webhooks answer `202` and process later. A
  failed delivery never retries on its own; a team admin chooses Process again
  in the team's GitHub settings. Release subscriptions need `release_tags`,
  and `pull_request_closed` runs only merged pull requests.
- **Cloud billing is prepaid.** A job whose balance cannot pay its next
  60-second slice stops at that renewal. One payment is at most $1,000, and
  storage tiers replace free-tier slots. Stripe holds the card: the console's
  Manage card opens Stripe's portal. See [Cloud billing](../billing.md).
- **Tokens.** A team admin can revoke another member's account token or
  personal machine but no longer rotate it. Admins still rotate shared runners.
- **Local dashboard.** It answers only a session from the one-time link that
  `sparkwing dashboard open` prints.

## Get help

Run `sparkwing docs migrations --version v0.68.0` to read this guide from the
CLI. For anything this guide does not cover, write to support@sparkwing.dev
with the command you ran and its full error.

## Reference: every breaking change

Each section below was written when its change landed. Where one disagrees
with the guide above, the guide describes v0.68.0. Because v0.68 starts a new
store, a section that says existing tokens, secrets, runs or schedules keep
working does not apply to an upgrade from v0.67. Sections that name
`internal/` Go packages concern code inside this repository, which other
modules cannot import.

## OIDC runner environment and triggers

Every OIDC subject gains a `runner_environment` segment between `runner` and
`ref`, so a trust policy that names a whole subject no longer matches. Insert
`runner_environment:fresh` to accept only launcher pods, Sparkwing Cloud pods
and GitHub-hosted Actions runners, or `runner_environment:*` to keep accepting
every host:

```
team:acme:repository_id:123456789:pipeline:deploy:trigger:push:runner:runner:runner_environment:fresh:ref:refs/heads/main
```

Runs from a GitHub Actions binding now mint `trigger:actions`; replace
`trigger:manual` in policies written for them. A run whose code Sparkwing
fetched from a GitHub repository now carries that `repository_id` with
`trigger:manual`, plus the `ref` and `sha` it ran when they are the branch or
tag head Sparkwing resolved, so a policy that names the repository and accepts
`trigger:*` now also admits those runs, and one that also pins
`ref:refs/heads/main` admits a member's run of main. See
[OIDC tokens](../oidc.md#what-each-value-proves).

## An ephemeral controller home needs PostgreSQL and S3

The `sparkwing-full` chart refuses `controller.storage.type=emptyDir` unless
`controller.store=postgres` and `controller.dataStore.s3URL` are both set,
because an emptyDir `/data` goes with the pod. Without them SQLite's
`state.db`, or every blob, artifact and log whose record PostgreSQL keeps,
would be lost on pod replacement. `controller.bucketCeiling.store` does not
satisfy the check: it measures a bucket and stores nothing. A
`--deployment=cloud` controller likewise refuses to start without an `s3://`
`--cache-blob-store`. Set `controller.dataStore.s3URL`. A self-hosted install
may instead switch to `controller.storage.type=pvc`; Cloud still requires S3.

## Storage tiers replace free-tier slots

Sparkwing Cloud no longer hands out free-tier slots. `--free-team-slots`, the
`free_slots` table and the sign-up gate's `free_tier_closed` and
`free_tier_unreadable` waitlist reasons are removed; drop the flag from
controller arguments. Every team stores within its tier quota instead, and every
write that grows stored data (run events, blob and source uploads, log
segments) is checked against it, against holds and against an empty balance.
Team standing JSON drops `cache_share_bytes`, `log_share_bytes` and
`event_share_bytes`. `Tenant.AppendEventCharged` is removed; events append
through `Tenant.AppendEvent`.

## Operator refund route removed

`POST /api/v1/operator/teams/{team}/refunds` is removed. Refund in the Stripe
dashboard; the verified refund webhook reverses the credits once.

## Fleet-wide hourly run cap removed

The optional `max_global_runs_per_hour` compute guard, and the
`operator.max_global_runs_per_hour` setting that fed it, are removed. It was
off by default and counted every team's runs against one budget. Per-principal
hourly limits (`max_runs_per_hour`) and prepaid credits still bound intake.
`sparkwing operator limits set` refuses the old name; drop it from scripts and
configuration. A value already stored is ignored.

## Pipeline secrets belong to one repository

A pipeline secret now belongs to one GitHub repository and one pipeline, like a
GitHub repository secret. Before, a secret set with `--pipeline ci` reached every
repository's pipeline named `ci`, so anyone who could push to one repository
could read another repository's `ci` secrets. Pipeline secrets now reach only
runs whose code Sparkwing fetched from the repository through the team's GitHub
App (pushes, pull requests, schedule fires, run-now and dashboard or CLI runs of
a connected repository) or whose repository a GitHub Actions job proved.
Uploaded, working-tree, local and `--runner here` runs read only `--shared`
secrets.

- `sparkwing secrets set`, `get` and `delete` take `--repo OWNER/NAME` together
  with `--pipeline`; `--pipeline` alone is refused. `--repo` also takes a
  `gh:<id>` key from `sparkwing secrets list -o json`.
- `sparkwing secrets list` shows each pipeline row's repository and filters by
  `--repo` and `--pipeline`.
- `POST /api/v1/secrets` takes `repo` with `pipeline`, and
  `GET`/`DELETE /api/v1/secrets/{name}` take `repo` with `pipeline` in the query.
  Secret metadata carries `repo` (the repository's name when the row was
  written) and `repo_key` (its identity).
- A local or manual run that needs a secret reads a `--shared` secret or a
  [local secret](../machine-config.md#local-secrets).
- `--shared` secrets are unchanged and stay team-wide. Protect deploy secrets
  with reserved pools or `--only-on`.

v0.68 starts a new store, so set each pipeline secret again with its
repository:

```sh
sparkwing secrets set --secret DEPLOY_KEY --file key.pem --repo acme/web --pipeline deploy
```

## Secret missing sentinel

Replace `secretname.ErrMissing` with `sparkwing.ErrSecretMissing` when checking
secret lookup failures with `errors.Is`. The SDK keeps the same sentinel for
stored-value misses, separate from transport and authorization failures.

## GitHub deliveries enter a durable inbox

GitHub webhooks now answer 202 with `status: queued` after storing the signed
delivery. Processing results, including duplicate run details and temporary
admission refusals, are no longer returned to GitHub. Failed deliveries never retry automatically. Team admins can inspect them in
the team’s GitHub settings and choose Process again. Credit exhaustion creates
a run that did not start. Use Run now after funding the team. Clients
that post signed deliveries must treat 202 as acceptance for later processing.
Disconnecting an installation revokes work still preparing source for admission
or schedule publication. Those refused effects leave no receipt to replay.

The store records the inbox, targeted effect results, team queue attribution,
idle free-slot tracking, per-team descendant ceilings and indexed overflow
policy. Claim polls seek each overflow account and test
its budget ranges before selecting policy routes. Repeated capped policies add no per-policy, run or node scan, even when the
jobs have different preferences.

## Stripe holds card data

The controller no longer stores card details. `GET /api/v1/team/billing` returns
`card` as `{"added_at": <unix seconds>}`; `card.brand` and `card.last4` are
removed. Read the card in Stripe's customer portal, which
`POST /api/v1/team/billing/portal` opens for a team admin once the team has a
card (409 with reason `no_card` before then). The billing page shows Manage
card instead of the card's brand and last four digits.

Each team keeps only its Stripe customer ID and the time its last card setup
completed. The schema drops `teams.card_payment_method`, `card_fingerprint`,
`card_brand` and `card_last4`, `credit_checkouts.payment_method`,
`receipt_email` and `warning_id`, `payment_warnings.fingerprint`, the card
columns of `payment_setups`, and the `payment_cards` table. Verified payment events persisted
in `payment_events` no longer carry a payment method or card details.

The checkout service contract changes with this controller. `POST
/internal/card-setup` receives the team owner's account `email`, which only Stripe
keeps, for its receipts. `POST /internal/refill` and its
reconcile, confirm and cancel operations receive the Stripe `customer` without
`payment_method` or `receipt_email`; the service must charge the customer's
default payment method off-session. The new `POST /internal/portal` receives
`team`, `deployment` and `customer` and answers with the `url` of a Stripe
customer portal session. Deploy the matching sparkwing-platform release.

Cards are no longer pinned to the first team they fund, and an early fraud
warning no longer blocks its card on other teams. A warning holds only the team
whose payment it names, takes back unprotected credit and queues its refund as
before. Stripe Radar handles card fraud; stop abuse of one card across many
teams with a Radar rule or by hand. A refill refused by an early fraud warning
leaves the team's card in place, and the warning's hold stops further refills.
A failed refill waits for Add funds or a new card setup; changing the card in
Stripe's portal does not restart refills.

## Payment ledgers and operator grants

Start with `--payment-mode live` to switch a test store to live once, even after
test activity. First drain test awards, reservations and refills as described in
the [cutover runbook](../billing.md#test-to-live-cutover-runbook). The switch commits
before the controller serves requests and restarts storage accounting at the
cutover instant. Later live starts preserve the switch and accounting watermarks.
Live-to-test startup is refused. Test credits, standing, holds and refill windows cannot affect
live decisions. Test Stripe customers and refill opt-ins are cleared at
cutover; add a live card and opt in again. Test financial rows remain in the store.
Grants, charges and holds retain mode tags. Reversals require an existing grant in
the same team and mode. The schema includes these columns and the reversal
foreign key.
Financial records and payer associations survive team deletion as tombstones.
Deletion never waits for financial finality; late payments, refunds and fraud
warnings reconcile against retained records. Purge final-settles awards. Holds retain their drain
deadline independently of operational work.
Operator grants require an explicit idempotency key. Use `admin grant --reverses`
to correct a free grant, naming its full reference and using a fresh key.

## Runner sizes are labels

Replace CPU and memory requests in pipeline declarations with runner labels.
Cloud size labels are `sparkwing-2cpu`, `sparkwing-4cpu`, `sparkwing-8cpu`,
`sparkwing-16cpu`, `sparkwing-32cpu` and `sparkwing-64cpu`. Without a size label,
Cloud uses `sparkwing-2cpu`. Billing follows the selected size's credit class.
The `sparkwing-` prefix belongs to Cloud. A required Cloud size label selects a
fresh, isolated Cloud pod. Rename any self-hosted, personal or pool runner size
labels using that prefix. Configuration refuses them, and claims strip them.
Every selector term must match, and alternatives within a term still use OR.

Go `Resources` and TypeScript `resources` declarations are
removed. Update SDK dependencies and author code together. Remove CPU and memory
availability fields from claim requests. Runner capacity no longer participates
in claim matching.

Go and TypeScript SDKs pass runner label declarations to the host. The host
trims outer whitespace, removes empty labels and deduplicates in declaration
order for both languages. Alternatives within a term keep their OR semantics.
TypeScript declarations containing empty labels now reach host normalization
instead of throwing during plan construction.

Foreground local submissions reserve their initial trigger for the submitting
OS account. This replaces the standalone process gate and also protects runs
submitted to a controller that is already running. Detached work still queues.

All claimants now share eligibility and fair-share ordering. An eligible team's
last successful award determines its next turn, followed by job arrival and
identity. Queue preference scores are removed. Explicit preference holds and
placement ranking remain. Concurrent claims share queue arbitration before
billing. Registration and temporary occupancy do not decide whether a push
needs credits. A placement policy permitting unmetered execution can wait for
its runner. Cloud-only work still needs funds at intake. Private owner placement snapshots
use the same eligibility and funding checks as awards. Reading a snapshot
leaves readiness, reservations and fair-share accounting unchanged.
Requirements are indexed when work is accepted, so unrelated label backlogs do
not delay matching jobs.
Metered claim leases account for billable time that begins during issuance.

Delivery receipts now include each targeted
admission, publication, withdrawal and lifecycle result. Reprocessing preserves
committed effects. Installation withdrawals keep their original repository targets
and result, so processing an old delivery cannot withdraw later schedules or consent.
Historical deliveries without complete receipts cannot be
reconstructed safely. Reconcile their outcomes before replaying them after a
cutover. Failed deliveries still require an admin's Process again action.

## Declared restore and credential epochs

The external `credential-epoch` fences bearer tokens and dashboard sessions.
Normal restore preparation preserves credentials and API/local schedule consent.
It cancels restored unfinished scheduled runs and holds schedules until operator
acknowledgment, which discards catch-up through its timestamp. Use
`restore prepare --sign-out-everyone` for security incidents (a stolen database
or leaked keys) to rotate the epoch and invalidate all earlier credentials and
API/local schedule consent. Recover post-backup revocations and member changes
from the controller's audit log stream, kept outside the database.
The epoch directory must be
writable and persistent; `--credential-epoch-dir` selects it when credentials
are read-only. Helm uses a dedicated retained PVC independent of database storage.

Recovery is manual; follow the operator runbook kept outside this repository.

## Cloud billing is prepaid

Cloud usage now draws only on a prepaid balance; a team never owes for usage.
Collections, the collection ladder, declined-charge holds, pay-now pages, owner
budgets and the credit grace period are removed. Teams top up with Add funds or
opt in to automatic refill. See [Cloud billing](../billing.md).

No data migration is needed: the staging and production stores had no open
card charges, no negative balances and no saved cards at the cut. The store
has no `card_charges`, `card_attempts` or `team_spend_days`, and keeps the
refill settings on `teams` and the immutable
`credit_checkouts.liability_shift` payment snapshot. Saved cards, card pins, fraud warnings
and refunds carry over in schema terms. Automatic refill starts off for every
team. An early fraud warning holds the team and blocks its card. Only payments
whose verified 3-D Secure result was `authenticated` keep their remaining credit
frozen without a refund. Other results, including `attempt_acknowledged` and
missing protection, are refunded and their credit reversed. The controller
stores the authentication result and its protection decision at settlement.
`payment_refunds.kind` distinguishes refunds, lost disputes and warning
obligations in the same durable settlement path. Pending obligations are
consumed atomically when their delayed payment grants credit.
`payment_setups` retains verified setup outcomes that arrive before their
setup association; a refill payment names its refill in its metadata, so its
outcome settles on arrival. All events for one payment use
the same provider-mode and payment lock, and terminal refund outcomes never
regress to pending. Warning identities and refund queues also record provider
mode.

Clients reading these fields must stop:

- `GET /api/v1/team/billing` drops `card_billed`, `open_charge` and
  `limits.budget_cents`, `limits.rung_cents`, `limits.spent_30d_cents`,
  `limits.spent_today_cents`, `limits.headroom_cents` and `limits.binding`.
  `min_billable_seconds` is renamed `slice_seconds`. `standing.history` drops
  `successful_charges`. The response adds a `refill` object with the team's
  settings, its refill caps, which follow paid history only, the counts in
  the current windows and the `limit`, if any, that stops the next refill.
- `POST /api/v1/team/billing/pay` and `PUT /api/v1/team/billing/budget` are
  removed. `PUT /api/v1/team/billing/refill` takes `enabled`,
  `threshold_cents`, `amount_cents`, `week_limit_cents` and
  `month_limit_cents`; it answers 409 with reason `no_card` when turning refill
  on without a saved card, and 400 for an amount outside $10 to $1,000.
- Operator credit settings no longer accept or report `grace_seconds` or
  `max_charge_seconds`, and operator credit state no longer reports
  `exhausted_at`.

A Cloud job whose balance cannot pay its next 60-second slice now stops at that
lease renewal, with no grace period: the renewal cancels the metered job with a
reason naming the shortfall and its remedy. If the stopped unit owns the run,
the whole run stops. Otherwise its owner continues orchestrating independent
unmetered jobs. The job's lease ends at the earlier of
its funded horizon and a drain deadline at most one slice away. A held team's
running Cloud work stops the same way, with the hold named. Retained storage no
longer takes the balance below zero. Kubernetes is the backstop during a launcher
outage: a pod can run up to one extra slice plus its start delay beyond its
funded deadline. This overage is never charged.
The checkout service drops its pay-now page and charge route. One
`POST /internal/refill` call creates and confirms the refill's off-session
payment, using the amount and Stripe customer the controller fixed when the
refill opened, with the refill id as idempotency key and payment metadata;
deploy the matching sparkwing-platform release with this controller.

Go callers of `internal/store` lose `NoCreditsHold`, `EventKindCreditsExhausted`,
`PoolHeartbeatInterval` and `DispatchedHeartbeatInterval`. A waiting unit's
`CreditHold` now carries the refusal's own text.
`Store.RenewLauncherPreparation` returns `[]LauncherPreparation` containing
the fence and committed expiry. Launcher sync's `prepared` entries retain
the fence fields and add `expires_at`, which preparation keepers must honor.

## Store raw handle, test hooks and dead readers removed

`Store.DB` is removed; use `Store.Ping` for readiness. No caller can reach the
store's connection, so every write goes through a transaction owner. Test
fixtures open their own connection with `teststore.SQL`, `teststore.Exec` or
`teststore.SQLite`. `Operator.CreateTeam`, `Store.SetTestClock`,
`SetTestBusyTimeout`, `Tenant.TestOnlySeedGitHubInstallation`, `OpenReadOnly`,
`OpenReadOnlySnapshot` and `logseg.SubjectUploader` are removed. Fixtures use
`teststore.SeedTeam` and `teststore.SeedGitHubInstallation`, and pass
`teststore.WithClock` when they open a store. A concurrency invariant
violation now fails its transaction instead of committing with a log line.

`capability.Grants`, `Routes`, `BillingRules` and the other policy tables are
functions returning copies. `LookupRoute` returns a copy too, so reading a
policy cannot change it.

## Local dashboard sign-in link

The local dashboard's TCP listener now answers 401 to every API request without
an `Authorization: Bearer` session; no cookie is set or accepted. Run
`sparkwing dashboard open` after `sparkwing dashboard start` and visit the link
it prints. The link works once and expires after 60 seconds, and the session
lives in that browser tab's `sessionStorage`, so a separately opened tab needs a new
link. The session ends after 12 hours without a request, 24 hours after sign-in,
on Log out, or when the local controller restarts. Tools that call the local API
should use the private socket, which needs no session.

## Shared runners accept less from the run

A runner runs one job after another in shared host state, so three inputs a run
supplied no longer reach it unchecked.

- **Pool trust.** A pool with reserved secrets defaults to `verified-default`.
  It requires verified default-branch work and a launcher-only credential.
  Enroll with `sparkwing runners add --profile prod --runner deploy-1 --repos github.com/acme/* --reserved-for deploy --trust verified-default --launcher`.
  In the runner chart, use `launcher.enabled=true` and `launcher.tokenSecret`.
  Remove `runner.trustClass` and `runner.reservedPools`; the credential supplies
  both facts. Enabling the launcher replaces the persistent runner and cache
  PVC. A run-level pool pin now uses controller dispatch. A team admin can
  use `--trust=any` to admit other runs, but a pool holding reserved secrets
  still requires a launcher.
- **Submitted environment.** A run's `run.submit_env_allow` values reach a
  shared runner only under names the runner's operator also lists, in the
  runner's own machine file `run.submit_env_allow`, `--submit-env-allow`, or the
  chart's `runner.submitEnvAllow`. Cloud Jobs, here-hosted runs and the requester's own
  personal machines accept every
  submittable name. Go toolchain names (`GOFLAGS`, `GOPROXY`, `GOTOOLCHAIN`,
  and similar) and network base names (`PATH`, `HOME`, `HTTP_PROXY`, and
  similar) cannot be submitted at all: intake refuses a run that sends one, and
  the CLI no longer captures them. Set them in the runner's own environment.
- **Remote cache reads.** A local run's `cache_from` profile needs the editor
  role. A reader's remote cache credential is refused, because dependency-cache
  archives are not checked for secrets.

Remote builds on a host the run's owner does not own set `GOENV=off`, so a
`go env -w` file on that host no longer changes the build. Pipeline binaries
cached before this release compile once more, because each cached binary now
carries a digest that is checked before every use.

## Runs without a runner selector run anywhere

On every controller, Cloud and self-hosted, each trigger, App webhook run,
schedule and child run that names no `--runner`, label, `here` or routed pool
now runs anywhere. Intake picks dispatch itself and ignores a client-supplied
`dispatch`. Any eligible claimant can take its plan step, generator evaluations
and each job; a local controller's own queue counts as a self-hosted runner.
Before, CLI triggers and webhook runs waited for a self-hosted runner, and
self-hosted controllers hosted CLI triggers whole. The team's claim ranking
orders claimants: `here` (the personal runner on the machine whose CLI started
the run), `personal`, `github-actions`, `self-hosted`, then `cloud`. Each unit
gives the best-ranked online claimant 5 seconds, then the best-ranked claimant
that asked during that window 2 more seconds. A job that requires a `pool=`,
`name=` or `class=` term skips the ranking. The Cloud launcher takes part only
while the team has credits or a saved card. Runners now claim plan and
evaluation units as well as triggers and jobs. Change the order with
`sparkwing team settings set --profile work --claim-ranking cloud,self-hosted`;
`none` means first come.

Personal machines take only their owner's manual runs: App and schedule runs,
and their retries and children, never reach them. Clients can no longer submit
a run with the `schedule` origin. The trigger CLI proves its machine's runner
credential with a short-lived signature instead of sending it. OIDC tokens
minted on a personal machine carry `runner_kind=personal`.

App automation and subscriptions take `runner: anywhere | cloud | github-actions
| pool=<name>`, default `anywhere`. `cloud` now pins runs to the launcher. Set
`github-actions` on repositories whose App runs a GitHub Actions workflow should
own. `PUT /api/v1/team/settings` takes optional `members_can_add_machines` and
`claim_ranking` fields, and the plain output of `sparkwing team settings show`
adds the ranking. Unclaimed runs carry an `unclaimed_work` notice, Cloud work held by
the team's credits carries a `credit_hold` notice instead, work no eligible
runner can take carries a `no_eligible_runner` notice naming the cause, and
`sparkwing_claim_empty_total` counts claim polls that matched nothing.

## Team git credentials never reach personal machines

Shared runners and Cloud work now receive the team's stored git credentials and
GitHub App tokens without a per-machine opt-in. Personal machines never receive
either: the source-credential release answers 403 with reason
`personal_machine`, and they fetch with their owner's own git access.
`PUT /api/v1/tokens/{token}/git-credentials`, the Machines page toggle, the
`git_credentials` field on token metadata, `Tenant.SetSourceMachineConsent`,
the `machineOwner` binding rule and the `git_credential_machines` table are
removed with no replacement. Reason `owner_opt_in_required` is replaced by
`personal_machine`. Drop scripts that called the consent route.

## Only a token's owner rotates it

`POST /api/v1/tokens/{token}/rotate` answers 403 when a team admin rotates
another member's account token or personal machine, and the token keeps
working. The owner rotates their own; the admin can still revoke it. Team
admins still rotate shared runners.

## Job outputs holding a secret are withheld

A job output containing a registered secret value in any encoding is no longer
published. The job passes with a `warn` annotation, `output withheld: contains
a secret`, and downstream jobs see no output. Return a reference to the secret,
such as its name, instead of its value.

## Credential rotation results and deletion times

Before, `Tenant.RotateCredentials` returned visited secret/git counts and its
secret callback returned `(value, error)`. It now returns
`(store.CredentialRotation, error)`. Its secret callback returns
`(value, skip, error)`, with `skip=true` retaining an unreadable envelope.
Read rotated-secret and git counts from `Rotated` and `GitCredentials`, and
skipped name/pipeline identities from `Skipped`. These fields describe only
the committed attempt. Errors return an empty result. The HTTP response keeps
its `rotated`, `skipped` and `git_credentials` fields.

Team-deletion request, completion, recheck and lease times use Unix nanoseconds
throughout the final schema. Update direct SQL integrations that used seconds.

## Retired token verifier

Remove `--argon2-memory-budget-mb` from controller arguments and
`controller.argon2MemoryBudgetMB` from chart values. Capability and session
authentication use stored digests. The retired Argon2 API has no replacement.

Remove calls to `store.ValidateRunInvocation` and handling for
`store.ErrSecretInputHash`. Runs enter through sealed intake, which validates
secret argument classification. `InvocationSecretArgsKey` remains a reporting
field. `Operator.TriggerTeam` is removed with the retired trigger sweep.

## Environment and source selections

Requests with nonempty environments set `environment_source` to `captured` for
private local snapshots or `submitted` for encrypted controller transport. Child
intake inherits the parent's source and snapshot. Changing invocation origin
never changes where those values live. Retry retains the original selection.

Local `--sw-ref` selects the execution revision. `--sw-pipeline-ref` selects the
pipeline code revision. Both resolve before engine selection, inspection and
building. The sealed request retains the exact execution and code commits.
`source.working_tree` records local code from the current tree, while
`source.committed_checkout` distinguishes a requested execution commit from the
working tree even when that commit equals HEAD.

An alias such as `deploy-prod` with `entrypoint: Deploy` selects the SDK's `Deploy`
registration. Author code still sees `deploy-prod` in its run context. Scaffolds
register that configured entrypoint.


## Go SDK replacements

An applicable Go SDK `replace` directive runs with the installed CLI's engine.
Sparkwing warns that engine pinning is off for that run. The replacement remains
in `pin.replacement`; the accepted engine identity records the installed
executable's digest. Resuming that identity requires the same executable.
Ordinary release pins still use signed release verification and exact matching.

## Tenant event append and deployment compute controls

`Store.AppendEvent` is removed. Select a tenant and use `Tenant.AppendEvent`.
It allocates the sequence and event bytes with the event insert in one
transaction. Missing and foreign runs return `ErrNotFound`. Event timestamps
come from the store clock. Profile drift uses its selected tenant, and the
local session reaper records outcomes through `DefaultTeam`.

`ComputeLimits`, `SetComputeLimits` and `ComputeUsage` move from `Store` to
`Operator`. Select the deployment handle with `AsOperator()`. The unused
`ComputeAlarmState` and single-setting `SetComputeLimit` methods are removed.
Set one limit through the batch writer. Validation, zero-valued removal and
transaction rollback remain unchanged. HTTP mutations still require the
separate authority-bearing settings adapter.

`Operator.CountPendingNodes` is removed. Read the ready count from
`CountNodesByQueueState` with `QueueStateReady`. Both operational gauges use
that one snapshot.

## Account mutations and raw team-switch retirement

Account writes use `SelectAccountTeam`, `CreateAccountTeam`,
`AcceptAccountInvitation` and `UnlinkAccountIdentity` with the authenticated
`capability.Authorized` value. They recheck the current direct account session
and commit the account, membership and selected session together. The controller
uses these store transactions by default. Unlinking requires sign-in within ten
minutes and preserves the selected session. Account, membership and identity
timestamps use the final schema's nanosecond unit.

`Store.CreateTeam`, `Store.AcceptInvitation`, `Store.UnlinkIdentity`,
`Store.SetActiveTeam` and `Store.SwitchSessionTeam` are removed. Use the
authenticated account writers above. The store clock supplies mutation and
expiry times. The unused
`Store.AccountIdentity`, `GitHubRepo.CanonicalURL`, `MetricSample.OneShot`,
`IsBusyErr` and `Tenant.CacheExcludedCounts` helpers are removed too.

## Raw run writer and unused liveness gauge removed

`Tenant.CreateRun` is removed. Executable intake uses `AdmitRun` and its sealed
request transaction. Reporting fixtures use `teststore.SeedRun`, which writes the
current sealed schema and creates no owner unit, job or capability. Existing
controller and web reporting fixtures now use that helper.

`Operator.CountActiveRunners` is removed. Its lease-window gauge had no production
caller. Tenant runner inventory uses `ReadRunners` and committed observations.
The unused `runTeamSQL` default-team fallback, `CompileLogNode`,
`ErrPlanAccepted`, `Token.IsValid`, `Token.HasScope`, `TokenEnded`,
`ErrTokenRevoked` and `ErrTokenExpired` are removed too. Capability authentication
and rotation continue through C2 and the store's live authority transactions.
Profile folding recognizes `memoized` jobs and excludes their execution samples.

## Explicit tenant reporting readers

`Store.GetRun`, `GetNode`, `ListNodes`, `ListEventsAfter` and
`GetActiveDebugPause` are removed. Select the tenant with `ForTeam` and use its
reporting readers. Cross-team `ListRuns` and `GetLatestRun` move from `Store` to
`Operator`. The dashboard backend takes one tenant at construction and uses it
for run, job and event reads. It has no default-team fallback or separate run
scope.

`Tenant.GetSecretForPipeline` is removed. Secret releases use `ReadSecret`
with current authority, declared names, pipeline/shared precedence and pool
restrictions. Trusted exact-row inspection still uses `GetSecretRow`.

## Authorized GitHub App configuration and launcher reservations

Tenant's raw `UnbindGitHubAppInstallation`, `PutGitHubAppTrigger`,
`DeleteGitHubAppTrigger`, `PutGitHubAppAutomation`, `DeleteGitHubAppAutomation`,
`SetGitHubAppExtraRepos` and `SetGitHubAppExtraRepoIDs` are removed.
Use the six authority-bearing methods in `gitHubAppWriter`. Tenant supplies the
production adapter. Supply slugs and verified numeric IDs together when setting
extra repositories. Actors and timestamps come from the store transaction.

`Store.ReadLauncherAwards` now takes the observed reservation fences as its
third argument. Pass nil for a snapshot with no reservations. Unknown or foreign
bootstrap fences refuse the snapshot. The controller returns exact reservation
identities for cleanup after the original award ends, including after exchange.

## Authorized team mutations

`Tenant.Rename`, `RequestDeletion`, `CreateInvitation`, `DeleteInvitation`,
`SetTrustLocalBuilds` and `DeleteRun` are removed. Use `RenameAuthorizedTeam`,
`DeleteAuthorizedTeam`, `CreateAuthorizedInvitation`,
`DeleteAuthorizedInvitation`, `SetAuthorizedBuildTrust` and
`DeleteAuthorizedRun` with the selected tenant and current `Authorized` value.
The store rechecks live authority, ancestry and membership in the committing
transaction. Actors and timestamps come from that transaction.

Invitation creation requires a nonempty request key. The same actor, tenant and
key retain the first invitation and its expiry. A different email or role
conflicts. Withdrawal retains the original replay result and daily count.
Team deletion preserves owner, last-team, checkout and hold guards.
Run deletion requires terminal state and drained claims, removes operational rows and success pins,
and preserves financial records. A repeated store call checks current authority
before accepting an already-removed run.

## Owner completion and retired store helpers

`Tenant.FinishRun` and its unfenced terminal writer are removed. Use
`Tenant.FinishRunAndUnit` with the current owner authority. Terminal time comes
from the store clock. Exact replay preserves it. The first successful run
records its team business event in the same transaction as the verdict, owner
completion and pipeline pin. An event failure rolls the transaction back.

The unused `ConcurrencyHeartbeatInterval`, `ConcurrencyHeartbeatTimeout` and
both `DefaultConcurrencyHeartbeat*` constants are removed. The shared lease
keeper owns refresh cadence and attempt deadlines.

The unused `JoinProfileKey`, `SplitProfileKey`, `DisplayProfileKey`,
`RepoIdentityFromURL` and `RepoIdentityFromPath` helpers are removed. Profile
views display the stored pipeline key. They no longer decode historical keys.


## Tenant writes and typed claim fences

`Store.CreateRun`, `CreateTrigger`, `CreateTriggerWithRun` and `FinishRun` are
removed. Select the registered tenant with `ForTeam(ctx, team)` for tenant
operations. Executable lifecycle writes use `Tenant.AdmitRun`, `AcceptPlan`,
`TransitionNode` and `FinishRunAndUnit` with current authority.

`Store.SetNodeArtifactManifest` and `Tenant.SetNodeArtifactManifest` are
removed. Commit the authorized manifest record with the terminal
`TransitionNode` result through `NodeResult.ManifestRecord`. The separate
raw manifest-column write is removed.

`Store.StartNode` and `Tenant.StartNode` are removed. Start a claimed job with
`Tenant.TransitionNode`, `NodeRunning`, `CauseStart` and the source attempt.
The transaction checks the current holder and establishes the attempt ordinal.

`Tenant.CreateSourceTriggerWithRun` and the unused source-upload admission
branch are removed. Source acquisition uses the accepted request and source
contracts. `NodeClaimFence`, `TriggerClaimFence`, their context constructors,
readers and `WithoutClaimFences` are removed. The unused holder, membership,
reservation, generation, attempt and trigger-generation header constants are
removed too. Supply the typed actor and claim
fence on each lifecycle request. Tenant checks remain in metric and metadata
writes. Metric insertion uses `(team, run_id, node_id, ts)`, accepts identical
retries and refuses conflicting readings without changing the stored sample.

## Execution attempts and Cloud metering

`AcknowledgeNodeExecutionStart`, `FinishNodeExecutionAttempt`, `ExecutionStart`
and `ExecutionAttemptFinish` are removed. Use `Tenant.TransitionNode` with the
current award and source attempt ordinal. Tenant-scoped job reads include the
committed attempts. The standalone `ListNodeExecutionAttempts` reader is private.
The unused coordinator ID API and GitHub runner scope APIs are removed.
Capability push bindings and the shared matcher own repository restrictions.

Cloud awards require credits regardless of the launcher's root metering flag.
Customer runners keep their enrolled metering policy. Existing award facts stay
fixed. Verified non-checkout payments persist their receipt and provider
correlation in the final schema.

## Sole claim queue and console metering

`Store.ClaimNextReadyNode`, `ClaimSpecificTrigger`, `ClaimSpecificTriggerFor`,
`PrincipalHoldsTriggerClaim` and `Tenant.TriggerClaimant` are removed.
`Tenant.HeartbeatTrigger` and `FinishTrigger` are removed too. Use `ClaimUnit`
for named or queued jobs and owner units, then `RenewUnit`, `ReleaseUnit` and
`FinishRunAndUnit` with the current award. `WithClaimProfile` and the store's
`RepoFilter` alias are removed. Current enrollment owns repository authority.

The console's runner metering route now commits through
`Operator.SetRunnerMetered`. Its selector is the enrolled root's public
capability prefix. A current operator account session and CSRF proof are
required. Existing awards retain their original billing facts. Runner inventory
keeps rotated credentials until their grace deadline.

## Legacy claim authentication and writers

`Store.AuthorizeClaimToken`, `AuthorizeRunnerClaim`, `ReplayClaimResult`,
`ReportAttempt`, `EnqueueChildRun` and `IsChildRunOf` are removed with their
raw claim classes, result-commit types and attempt-report types. Use capability
authentication, `Tenant.TransitionNode` and `Tenant.AdmitRun`. Current
transitions own generation checks, result replay, retry ordinals, events and
settlement. The unused `CheckClaimSlot`, `CheckClaimKey`,
`PrincipalHoldsRunClaim`, `TriggerClaimFenceIsLive` and `NodeClaimFenceIsLive`
readers are removed too. Current binding and mutation transactions recheck
authority using the store clock.

## Named job claims and renewal

`Store.ClaimNamedNode` and `NamedClaimOptions` are removed. Use `ClaimUnit` with
a `UnitDemand.Target` selecting `capability.UnitNode`, the run and the job.
Supply the live claimant capability prefix and observed labels and capacity.
The store derives the holder and checks the job's readiness and sealed demand.
Run owners self-claim through `Tenant.ReconcileNode`.

`HeartbeatNodeClaim` and `HeartbeatNodeClaimWithCredits` are removed. Use
`Store.RenewUnit` with the awarded capability prefix and the current execution
observation. `LeaseBeat` carries cancellation and the store-selected deadline.

The old `POST /api/v1/runs/{id}/nodes/{nodeID}/claim` and node heartbeat routes
are removed. Use the engine client's named claim and claim renewal methods.

## Unified trigger queue

`Store.ClaimNextTrigger` and `Store.ClaimNextTriggerFor` are removed. Use
`Store.ClaimUnit` with `UnitDemand.Kinds` containing `capability.UnitTrigger`
and the live claimant's capability prefix. The store derives the tenant and
repository scope and returns the awarded owner fence and one-time bearer.
Use `RenewUnit` and `FinishRunAndUnit` for that owner.

The unused `Store.NodeExecutionAttemptBelongsToLiveClaim` reader is removed.
Authentication snapshots retain current attempt and log-tail authority.

## Explicit tenant readers and owner completion

`Store.FinishRunAtGeneration` and `Tenant.FinishRunAtGeneration` are removed.
Use `Tenant.FinishRunAndUnit` with the current owner authority and its complete
fence. A stale owner cannot finish a successor's run, even with the same verdict.
`FinishTriggerAtGeneration`, `ReleaseClaimAtGeneration` and
`TriggerClaimGeneration` are removed from both handles too. Release unstarted
work with `Store.ReleaseUnit`, using the awarded `UnitClaim` and its capability.
Read the generation from that award or the current owner snapshot. Completion
closes the owner queue unit while retaining the run's separate verdict.

These default-team wrappers on `Store` are removed. Select the registered
tenant with `ForTeam(ctx, team)` and call its existing method. Local consumers
select `DefaultTeam` and handle the selection error.

- `CountRuns`, `DeleteRun`, `ListNodeSteps` and `ListNodeMetrics`.
- `ListTriggers`, `GetTrigger`, `GetRunAncestorPipelines` and `ListPendingTriggersForParent`.
- `ListApprovalsForRun`, `ListPendingApprovals` and `ListDebugPauses`.
- `FinishNodeWithOutputRef`, `NodeSettlement`, `HeartbeatTrigger` and `FinishTrigger`.

The unused `SupportsRunIdentityFilters`, `SupportsRunCursor` and
`SupportsRunDisplayFilters` probes are removed. Shared clients use the current
controller API contract. The run-filter response header remains unchanged.

## Raw cancellation and unused readers removed

`Store.RequestCancel`, `Tenant.RequestCancel`, `Store.CancelPendingTrigger`
and `Tenant.CancelPendingTrigger` are removed. Cancellation uses the tenant's
`TransitionRun` with current authority and `RunCancel`. It records the first
intent without taking result rights from live holders. The elected unit reaper
finishes cancelled unclaimed owner units. Terminal run outcomes never change.
The separate controller-plan cancellation writer is also removed.

The unused `FilterRuns` in-memory run selector is removed. SQL listing and
counting keep their filters and cursors. The unused transaction helpers
`fenceSensitiveClaimTx` and `fenceSensitiveTriggerClaimTx` are removed.

## Legacy store issuance and reports

`Store.RotateToken` is removed. Use the tenant's `RotateCapability` with
current capability-management authority. `Tenant.MintGitHubRunnerCredential`
is removed. Verified Actions exchange uses `Store.ProvisionCapability` with
`ProvisionActions`.

`Tenant.ReleaseConcurrencySlot` and `ResolveCoalesceFollowers` are removed.
Use `ReleaseAndNotify` and consume its follower and promotion results from
that transaction. `ActiveConcurrencyHolder` is removed. `ConcurrencyHolder`
returns an unexpired holder. Check `Superseded` when active capacity is required.

The unused `Tenant.ListSpawnedChildrenByRun`, `Operator.UsageMetrics`,
`UsageMetrics`, `UsageWeek`, `RunsBands`, `FirstGreenStats` and `UsageWeekStart`
store APIs are removed. The usage-metrics HTTP route was removed separately.

Attempt history derives executor identity from claim ancestry. A supplied
executor kind or location cannot override the awarded identity. Local host
and cloud pod names remain display observations.

## Membership mutations use current authority

`Tenant.SetMemberRole` and `RemoveMember` are removed. Use
`Tenant.UpdateMembership(ctx, actor, MembershipUpdate{AccountID: id, Role: role})`.
Pass a nil role to remove the member. The actor carries persisted session or
capability authority. The transaction checks current account membership and
administration authority, preserves the last owner, withdraws invitations above
the new role, moves sessions on removal, and revokes the member's affected
credentials. Failed writes roll back every effect.

The default-team `Store.ListNodeMetricsPage`, `TriggerClaimant` and
`RunExceedsWallClock` readers are removed. Select a tenant with `ForTeam` and
call the existing tenant methods.

The unused `MaxCLITokensPerMember`, `ErrCLITokenLimit` and
`Tenant.GitHubAppSubscribedRepository` symbols are also removed. Source credential
release uses the authorized sealed repository through `ReleaseSourceCredential`.

## Team token store API removed

`Tenant.CreateMemberToken`, `CreateMachineToken`, `TeamTokens`, `TeamToken`
and `RevokeTeamToken` are removed, along with `TokenFamily`, its constants,
`TeamTokenFamily`, `Token.Family` and `MachinePrincipal`.

Use `Store.ProvisionCapability` with a current session authority for member
roots or a registered `ProvisionMachine` origin for machines. Use the tenant's
`ListCapabilities`, `ReadCapability`, `RotateCapability` and `RevokeAuthority`
methods for credential management. Enrollment derives identity and checks
current team administration in the committing transaction.

## Secret store calls select a tenant

The default-team secret methods on `Store` are removed. Select the registered
tenant with `store.ForTeam(ctx, team)` before calling `GetSecretRow`,
`GetSecretForPipeline` or `ListSecrets`. Local readers select `store.DefaultTeam`.
Writes use `WriteSecret` and `RemoveSecret` under current administration authority.

## Raw secret writers and launcher token issuance removed

`Tenant.CreateOrReplaceSecret` and `DeleteSecret` are removed. Use `WriteSecret`
and `RemoveSecret` with current tenant administration authority. The transaction
selects the actor and time. Existing secret count and size limits still apply.
`Tenant.GetSecret` and `GetSecretForRun` are removed too. Metadata readers use
`GetSecretRow(name, pipeline)`. Claim releases use `ReadSecret`, which checks the
bound run, declarations, shared fallback, pool restrictions and release audit.

`Store.MintClaimToken` and `MaxClaimTokenLifetime` are removed. Awards use
`Store.Issue` within their claim transaction. Launcher delivery uses the one-use
bootstrap exchange under the current award fence.
The unused `Operator.TeamRunIDs` report is removed too. It has no production
caller.

The unused `Store.NodeClaimFenceNodeForRun`, `NodeExecutionAttemptIsLive`,
`TriggerExecutionAttemptIsLive` and `TriggerExecutionAttemptBelongsToLiveClaim`
helpers are also removed. Execution authentication uses the capability snapshot
and tagged attempt identity.

## Claim lifecycle and credential mutation APIs

`Store.StartClaimExecution`, `Store.HeartbeatClaim`, `ClaimBeat` and the matching
legacy client helpers are removed. Renew a capability-bound unit with
`Store.RenewUnit`. Start a job through `Tenant.TransitionNode` using `CauseStart`
and its current source attempt ordinal. Owner compute is reported through unit
renewal. The old claim-token branches of the job heartbeat and start handlers
are removed.

`Tenant.PutGitCredential`, `ConfirmGitCredential` and `DeleteGitCredential` are
removed. Use `WriteGitCredential(ctx, authorized, row)`,
`ConfirmGitCredentialFor(ctx, authorized, host, fingerprint)` and
`RemoveGitCredential(ctx, authorized, host)`. These transactions recheck current
administration authority and use the store clock.

`Store.ResealSecrets` and `SecretResealCounts` are removed with the startup
plaintext scan. The pre-launch store has no automatic historical-secret
conversion. Missing-key
checks still inspect sealed samples, and keyless secret access remains refused.

## Source credentials use capability transactions

`Tenant.ReleaseGitCredential`, `GitCredentialReleases`,
`SetGitCredentialMachine` and `GitCredentialMachine` are removed with
`GitCredentialRelease`, `SourceSpend`, `MaxSourceMints` and
`ErrSourceCredentialSpent`.

Use `Tenant.ReleaseSourceCredential(ctx, authorized)` with the current
capability authority. It derives the sealed source and executor, checks live
ancestry, claim fences, SSH confirmation and the shared-runner or Cloud
executor, and commits the audit before returning a sealed credential. Read
committed audits with `SourceReleaseAudits`. These calls use the store clock. The unused `capabilities.source_mints`
column is removed from the final schema.

## Run completion and planning units

`Store.CreatePlanNode` is removed. `Tenant.AdmitRun` creates the operational
planning or trigger unit. Claim it with `Store.ClaimUnit` before submitting
`Tenant.AcceptPlan`. A planning unit is separate from execution jobs.

`FinishRunIfActive` and `FinishRunsIfActive` on Store and Tenant are removed.
Use `Tenant.FinishRunAndUnit` with the current owner fence. It commits the run
verdict, owner closure, event and completion intents together. An exact replay
retains its original finish time. A different report against a terminal run
is refused. Cancellation uses the authority-bearing cancellation transaction
and lets held work drain before completion.

## GitHub runner enrollment uses current administration authority

`Tenant.AddGitHubRunnerBinding` and `RemoveGitHubRunnerBinding` are removed.
Use `RegisterGitHubRunner(ctx, authorized, binding)` and
`RevokeGitHubRunner(ctx, authorized, repositoryID)`. Each write rechecks the
current team administrator in its transaction and uses the store clock.
Removing an enrollment ends its Actions roots and their descendants atomically.
An Actions root needs the current numeric repository and owner registration.

## Secret release uses current authority

`Tenant.ReleaseClaimSecret` and `ErrSecretUndeclared` are removed. Use
`Tenant.ReadSecret(ctx, authorized)` through the controller's `SecretReader`
interface. It checks the current claim and accepted declaration and commits
the release audit with selection. Undeclared reads return the canonical
`forbidden` error with reason `secret_undeclared`. The caller supplies no clock.
The default tenant also implements `CredentialWriter` for secret and
git-credential mutations. Verified local peer presentations still need the
transport's authority adapter.

## Queue reads use explicit store owners

`CountPendingNodes` and `CountNodesByQueueState` move from Store to Operator. Select it with `store.AsOperator()` for deployment
metrics. These reads continue to aggregate across teams.

`Store.CountPendingTriggers` is removed. Select the intended Tenant with
`store.ForTeam(ctx, team)` before reading its pending trigger count. Local
consumers use DefaultTeam; controller requests use the authenticated team.

## Generated dependencies use accepted expansions

`UpdateNodeDeps` on Store, Tenant and Client and
`POST /api/v1/runs/{id}/nodes/{nodeID}/deps` are removed.
Declare `needs_generated` in the accepted plan and submit generated jobs with
`Tenant.AcceptExpansion`. Read `RunSnapshot.Scheduling` to resolve group members
and decide when dependents can run. Accepted declarations stay unchanged.

## Data signing and authentication metrics

Embedded controllers use `blob.NewFilesystem` or `blob.NewS3` as their physical
backend and pass it through `WithDataController` or `WithDataObjects`. The duplicate
controller filesystem and S3 signer types and `WithSignedDownloads` are removed.
Call `controller.ParseCloudFrontDataConfig` to validate CloudFront signing settings,
then call the returned config's `Backend` method with an S3 backend to sign
public-ingress downloads through CloudFront. Direct requests retain regional S3
URLs. Uploads keep the backend's size and checksum constraints. Retained log
readers use that same backend. `WithS3Logs` and the raw S3 log object adapter are removed. Replace
`s3.NewArtifactStore` with `s3.NewMeasurementStore` for bucket measurements.
Measurement handles expose `Usage` only. Local dashboard logs require committed
tagged segments because run-directory log fallback is removed.

Replace the `user` and `runner` credential labels on
`sparkwing_requests_by_principal_total` with `account` and `machine`. The other
labels are `service`, `operator`, `local`, `anonymous` and `other`. Requests count
once authentication succeeds, including later authorization refusals. Remove
queries for `sparkwing_auth_token_cache_total`, because the token-table cache is
removed.

## Runner observation snapshots

The controller resolves a tenant-scoped `runnerInventoryReader` from its
request tenant handle. Runner lists and queue headroom use the same committed
snapshot. The old process-local presence, heartbeat and headroom registries and
holder-name parser are removed. A missing snapshot adapter returns `policy_missing`.
The `/api/v1/runners` response retains `agents`. Equal display names can appear in
separate rows because they do not merge credentials. Last-seen and headroom
observation times remain separate. Runner metrics read the same snapshots.

`runnerInventoryReader.ReadRunners` returns `capability.RunnerSnapshot`.
The snapshot, observation and headroom types live in `internal/capability`,
so the store adapter can implement the reader without a controller import.

`Tenant.ReadRunners` implements the default adapter. It reads live authority,
accepted polls and current award fences in one store snapshot. Claims and
renewals update liveness in their committing transactions. A renewal preserves
the last accepted capacity measurement. Revoked ancestry, expired leases and
replaced awards contribute no activity. A cancelled holder remains visible
while its live lease drains. Cloud and `here` identities come from their awards.
Maximum concurrency and queue depth need enrollment and observation inputs
before the inventory can report them.

## Reporting credential routes

Reporting reads use the shared capability policy and the authorized resource's team. This includes run lists, receipts, attempts, steps, events, approvals, debug pauses, pipeline lists and latest runs, trends, runner views, sizing and logs. Unbound machine roots and deployment credentials cannot substitute for team membership. Work and trigger claims receive only the read alternatives declared for their bindings.

Job logs use `/api/v1/runs/{run}/jobs/{job}/logs`, with `/stream` and `/completeness` beneath it. The old `/runs/{run}/logs/{job}` paths are removed. Use `/api/v1/runners` instead of `/api/v1/agents`, `/api/v1/approvals` instead of `/api/v1/approvals/pending`, and `/api/v1/sizing` or `/api/v1/sizing/explain` instead of `/api/v1/capacity/profiles`. Read debug pauses at `/runs/{run}/debug-pauses`. The `paused` alias is removed.

## Legacy execution credential routes

The `/api/v1/triggers` and `/api/v1/runs/{run}/nodes` route families are
removed, including heartbeat, execution-start, execution-finish, claim input,
attempt results, node logs and direct node output upload. Raw
`POST /api/v1/runs/{run}/events` is also removed. Events come from the
committing state operation.

Use capability-backed `POST /api/v1/runs` for intake, `/runs/{run}/plan`
for plan acceptance, `/runs/{run}/jobs/{job}/transition` for job changes,
and the run or job `heartbeat` route for renewal. Use `/api/v1/data` for
authorized output uploads and records. Retired claim tokens and execution
headers are no longer an execution credential. Rebuild runner and controller
consumers together. No alias accepts the previous request shapes.

Embedded controllers remove `Server.WithLocalExecution`. Local execution now
uses the same capability and self-claim rules. The elected local runtime obtains
verified peer identity through `Server.HTTP()`.

Absent paths return canonical `not_found` before credential lookup. A
removed method on a path that still has another method returns canonical
`method_not_allowed` with `Allow`. Neither refusal renews a browser session.
The unused `WriteUnsupportedRoute` helper and `UnsupportedRouteError` constant
are removed. Embedded callers use the shared handler and canonical error envelope.

## Browser identity linking

Link start and completion use the shared browser proof policy. Use the account's
direct provider session and the matching session-bound CSRF form. Account-token
sessions and Authorization headers cannot link identities. Failures return the
canonical JSON error envelope instead of a settings-page refusal redirect.
Controller construction binds the store operator as the identity-link writer
to recheck the session and commit one-use nonce consumption and identity
attachment.

Nonlocal dashboard pages always require a live session. `Dashboard.Local`
selects local pages. Elected local controllers obtain the API and dashboard
handlers from `Server.HTTP()` and supply them through `wingd.LocalHTTP`.

## CLI client retirement

The legacy `Client`, `New` and `NewWithToken` are removed. Construct the shared
clients with `EngineOptions`. Its `HTTP` field supports the private api.sock
transport, and `Bearer` supplies the credential without exposing it in a URL.

| Operation | Shared client |
| --- | --- |
| Intake, claim, renewal, plan acceptance, transitions, step reports and completion | `NewEngineClient` |
| Public run history, steps and receipts | `NewRunReader` |
| Persisted logs and lifecycle events | `NewLogReader` |
| Job location and active pause | `NewDebugReader` |
| Approvals and annotations | `NewRunReview` |
| Secret values, metadata, writes, deletion and rotation | `NewSecretManager` |
| Authorized object transfers and records | `NewDataClient` |
| Run deletion with an empty HTTP 204 acknowledgement | `NewRunPruner` |

Intake uses the sealed `CreateRunRequest`. Hosts accept plans and expansions
atomically and report fenced job transitions and typed step observations. The old
trigger requests, node setters, claim-token headers, direct output uploads,
concurrency-slot client and log-segment uploader are removed. There is no raw
execution reader or capability probe that falls back to those APIs. The shared
transport decodes the common error envelope and follows the route table's replay
policy. Controller redirects are refused.

`repos info` reads each pipeline's latest admitted run through the local
controller's public projection. It follows pagination and reports read failures.
A queued run has no start time until execution starts. Follow persisted logs with
the returned cursor until the page is complete.

The old orchestrator readers, SQLite/profile factories, `HostTriggerRun`,
`TriggerHost`, `RunNodeOnce`, `RunHosted`, `HostClaimedTrigger`,
`RunLocalTriggerConsumer` and `ServeConsumer` are removed. Their state, logging,
admission, submission-environment and child-dispatch backends and the old
`pkg/localws` server are also removed. Captured environments use the shared host
snapshot contract.

Local lifecycle uses `wingd.LocalRuntime`. Foreground construction receives the
parsed `--home` before election. Lifecycle, managed units and logs share that
home. The elected controller supplies admission, private execution transport,
storage and process services. Typed step reports commit under the observed
attempt before the controller acknowledges persistence.

## Run deletion acknowledgement

`RunPruner.DeleteRun(ctx, run)` returns only an error. An empty HTTP 204 response
acknowledges deletion and log cleanup. The former `RunDeletion` type is removed
because the controller does not return a deleted-row or replay result.
`runs prune -o json` emits `run_id`, optional `dry_run` and any structured `error`.
The former `result` field is removed. Plain output emits the acknowledged selector.

## Public retry history

`GET /api/v1/runs/{run}/attempts` returns a complete `RunPage` of shared public
`RunDetail` values. Consumers read pipeline and retry lineage from `inputs`.
The former raw reporting row and its `invocation` field are removed. History
checks the caller's binding for every entry and fails before writing a partial
response if an entry is outside that binding.

## Sealed step selections

`--sw-start-at`, `--sw-stop-at` and `--sw-only` enter the request seal as
`options.start_at`, `options.stop_at` and `options.only`. Each carries presence,
value and origin. Plan acceptance rejects a changed selection, and retry keeps
the original values. Hosts must read these fields from the seal. The native
host integration remains unfinished.

## Internal launcher arguments

Launcher invocations require `--docker-image` with a digest-pinned DinD image
beside the runner `--image`. Remove `--deadline`. The Job deadline derives from
the awarded execution inputs. Plan awards omit `--job` in `fetch-source` and
`run-node`; work and eval retain the job from their private award metadata.

## Bounded daemon events

`daemon events` shows the newest 50 matching retained records by default, in
chronological order. `--limit` must be positive; increase it to include older
records. Offset and cursor continuation are removed.

JSON emits event rows followed by a `kind: page` object whose `truncated` field
reports whether older matching records were omitted. Consumers must distinguish
that object from event rows. `--explain --run ID` returns the complete related
timeline without a limit or page metadata.

## Daemon lifecycle reports

Daemon status, stop and restart share the local controller lifecycle used by
dashboard and doctor. JSON uses `kind: service`, `service: controller`, `action`
and `state: running|stopped`. It also carries `version`, `pid` when present,
`stay`, `installed` and `activity`. Mutation reports include `restarted`,
`stopped` and the preceding `previous_version` when a controller was replaced
or stopped. Plain output is the state.

Consumers of `running`, `healthy`, `binary_version`, schema versions or socket
paths must switch to this projection. Use doctor for controller diagnostics.
No status command opens the runs database. These adapters need the native
local-services binding before they can inspect a running controller.

Restart leaves a stopped controller stopped. The same exact version stays
running unless `--force` is selected. Replacement preserves the home, dashboard
address and stay policy, waits for shutdown, and refuses an unready or
wrong-version replacement.

## Job failure reports

Replace `JobResult.error` with `failure: {job, stage, error}`. The nested error
uses the canonical code, message, hint and details shape. The job must match the
report's job selector. Stage must be present. Recovery reads the persisted
failure without inferring its stage from an outcome or reason.

## Reconciliation sources and sealed guards

Private reconciliation can identify a previously materialized result with
`source.result` containing `run_id`, `job_id` and `state_seq`. This is separate
from an execution attempt. The controller rechecks its persisted state and read
authority before reuse. Do not invent an attempt for a memoized result.

Private scheduling snapshots retain `guard_refusal` and `cancel_requested`.
Owners must preserve both when reconstructing the scheduling decision.
Cancellation retains precedence over the guard refusal.

## Spark library inflation

Use `sparkwing pipeline sparks inflate --library MODULE`. The module can be a
sparks-core module name or a full Go module path. `--module` is removed.
`pipeline sparks catalog` prints the selector for each available module.
Plain output is the destination directory. JSON retains module, version,
destination and replacement metadata.

## Machine connection profiles

Profiles contain only `controller.url` and an optional `controller.token`.
Remove profile `state`, `secrets`, `rerun_image`, cache and log settings, and
remove project-level profile definitions. Use `sparkwing profiles add`,
`show`, `list`, `set` and `remove` to manage machine connections.

Every profile command validates the whole machine file. An invalid setting
in another section blocks reads and writes without exposing credential values.
An explicitly empty `SPARKWING_CONFIG` fails. Unset it to use the default path.
No reader imports old per-file settings.

Adding and removing profiles preserves concurrent edits. If a connection
changes during credential revocation, removal fails and retains that changed
connection. Reload it before retrying. Before: `configure init`. Its permission-repair behavior has been removed.

## Runner enrollment files

Runner settings use the `runner:` section, `repos` and `credentials_dir`.
The reusable credential is stored in a private file inside that directory.
Remove inline runner tokens and the old `agent:` and `allow_repos` keys.
Re-enroll with `sparkwing runners add --runner NAME --profile PROFILE --force`.
Its JSON receipt contains credential metadata rather than the bearer.

Enrollment previously wrote `max_concurrent: 2` when the flag was absent.
It now leaves that preference unset, using the shared default of one.
Pass `--max-concurrent 2` to retain two concurrent jobs. Existing explicit
values remain unchanged.

If writing the configuration or installing its service fails, enrollment
revokes the new credential and removes the new credential files. A failed
replacement restores the previous runner section. Concurrent configuration
changes are preserved. A controller revocation failure prints the exact
manual revocation command and returns nonzero.

## Queue and concurrency views

Replace bare `sparkwing queue` listings with `sparkwing queue list`.
Before: `sparkwing cluster concurrency --namespace GROUP --profile P`. Use
`sparkwing queue list --group GROUP --profile P`. Bare `queue` shows help.
The client uses `/api/v1/queue` and `/api/v1/queue/groups/{group}`.
Connection failures return nonzero without an empty queue record.

`queue priority` changes local admission priority only; queue groups have no
priority route. See [Queue views](../diagnosing-admission.md#queue-views) for the
unfinished native controller integrations.

## Local cache warming

Before: `sparkwing pipeline sparks warmup` compiled a pipeline and uploaded its
binary to gitcache. Local execution could also download that binary.

Use `sparkwing cache warm` from the checkout to prepare the local cache.
Go pipelines build on this machine. TypeScript checkouts install dependencies.
Compiled pipeline binaries stay local. Remove remote binary-upload steps from
warming scripts. Explicit result publication and memo/artifact reads use their
separate run options.

JSON warming output is one preparation receipt. Plain output is the prepared
path. Scripts that consumed progress from stdout must read stderr instead.
See [Warmup](../sparks.md#warmup) for native process-policy integration status.

## Controller diagnostics

Before, `sparkwing doctor` opened the local store and repaired abandoned state.
It returned repair-oriented fields such as `orphaned_runs` and
`permission_repairs`.

Doctor now gets controller findings through the lifecycle interface. Scripts
consume `findings`, `controller_checked` and `controller_error`. A failed
inspection returns nonzero and can still include partial findings. Treat an
empty array as complete only when `controller_checked` is true.

Doctor no longer repairs private-home permissions or reaps processes from the
CLI. Remove automation that expects those side effects. See
[Doctor reports](../diagnosing-admission.md#doctor-reports) for the unfinished
native runtime and machine-reader integration.

## Retry requests and receipts

Use `runs retry --run RUN --failed` to reuse successful jobs or `--all-jobs`
to execute the complete sealed plan again. Repeated `--run`, comma lists and
`--run -` select multiple runs. The old `full=1` query and legacy retry client
method are removed. Each source run gets an independent idempotency key.

Local and remote retries use the controller API. JSON emits one safe receipt
or structured failure per source run. Plain emits new run IDs. The CLI no longer
opens local or standalone state files to retry runs. Native retry intake, reuse
and here handoff still require integration. An incomplete here handoff exits 3
and its JSON failure receipt identifies the created run without its credential.

## Plan inspection

Use `sparkwing pipeline plan --pipeline NAME -- <pipeline arguments>` to supply
required inputs. The YAML pipeline name selects the SDK registration and remains
in the run context. The YAML entrypoint identifies the Go type. Secret inputs reach planning but are
redacted in previews and structured diagnostics. The preview JSON uses `jobs`
in place of `nodes`.

## Memo copies use transactional blob records

`AttemptReport.OutputFrom`, its `output_from` payload and
`Store.FinishNodeCopyingOutput` are removed. Use `blob.RecordStore.CopyMemo`
for authorized output and manifest materialization. The host memo layer
already calls it. Requests carrying `output_from` now fail strict decoding.
No alias or conversion of stored output pointers is provided.

The unused `backend.StoreBackend.GetNodeOutput` and
`backend.ClientBackend.GetNodeOutput` methods are also removed.

## Filesystem object writes use signed transfers

`fs.ArtifactStore`, `fs.NewArtifactStore` and the sharded filesystem object
layout are removed. Use the shared signed-data backend for byte transfers and
the controller for log deletion. Use `fs.NewUsageReporter` for deployment usage
totals. It exposes no object reads or writes. No object conversion is provided.

Run deletion and team purge/recheck use the configured common backend to remove
logs across stored retention classes. They leave CAS objects to reachability
collection. Authenticated log and host bindings remain integration work.

## Filesystem transfers use the shared data backend

The standalone controller opens its filesystem data backend under its home
when no S3 bucket is configured. Signed transfers use
`/api/v1/data/uploads/{upload}`, `/api/v1/data/objects/{key...}` and
`/api/v1/log-segments/{key...}`. They use the same verified physical objects
as the record-backed maintenance pass.

Removed `Server.WithOutputSignKey`, the older `/api/v1/outputs` handlers,
filesystem output moves and the raw filesystem log adapter. No data conversion
or fallback is provided. The legacy S3 output/log adapters still require their
final authenticated cutover. Local-controller construction and authenticated
data/host bindings remain separate integration work.

## Log readers expose only reads

`storage.LogStore` provides `Read`, `ReadRun` and `Stream`. Its unused
`Append` and `DeleteRun` methods are removed. Controller readers use
`client.NewLogReader`; the old `client.NodeLogs` adapter is removed. Writers publish tagged segments with `logseg.Sink`.
Delete a run through the controller's run API to remove its logs.

```go
// Before
logs.Append(ctx, runID, jobID, data)
logs.DeleteRun(ctx, runID)

// After
sink.Append(data, "", 0, 0)
controllerClient.DeleteRun(ctx, runID)
```

`storage.SafeLogIDs`, `SafeRelPath` and `SafeArtifactKey` are removed with
unused raw storage surfaces. Use `blob.LogObjectKey` for tagged log slots and
`blob.RecordNamespace` for record namespaces. Those helpers validate their
structured inputs.

## Filesystem logs use tagged segments

`fs.LogStore` and `fs.NewLogStore` are removed. Log writers use
a tagged `logseg.Sink` bound by the host. Filesystem and S3
storage use the same immutable segment format.

`conformance.TestLogStore` is removed.
Tagged-log coverage lives in `internal/storage/logseg` and the storage acceptance
cells, including verified offset reads and physical retention.

Daemon handshake and status responses drop `artifact_store_error`, which
described a cache backend the daemon no longer opens. Remove that field from
status consumers. API binding faults still use `api_ready` and `api_error`.

## Log identities use tagged subjects

- **Before:** log manifests carried separate run, node, attempt and generation
  fields. `logseg.AttemptPrefix` built an attempt-only key.
- **After:** manifests require `subject`, which identifies a job attempt or a
  trigger, plan or eval unit. Job keys include `/jobs/`. Owner keys include
  `/units/`. Readers reject a manifest whose subject differs from its key.
- **Upgrade steps:** pass `Options.Subject` when creating a log sink and use
  `blob.LogObjectKey` for signed slots and `blob.LogSubjectPrefix` for prefixes.
  The former `logseg` key helpers are removed. Update readers and writers together. Existing
  untagged manifests require a fresh run. There is no old-format reader.

## Direct source fetches own their Git objects

- **Before:** direct fetches reused a bare mirror per remote and checked out
  linked worktrees. Runner cleanup swept idle mirrors.
- **After:** each checkout fetches from upstream and owns its Git objects.
  Fetches need upstream access even for a commit an earlier run fetched.
  Failed fetches remove their checkout. Existing destinations remain untouched.
- **Upgrade steps:** stop older runners and their active runs before removing
  unused `source-direct` directories under the Sparkwing home. The new runner
  leaves these directories untouched.

## Local binary caches use one layout

- **Before:** cache status counted binaries in older directories, and pruning
  quarantined those entries before deleting them after a grace period.
- **After:** status and pruning use `cache/pipelines/v1/entries` in the Sparkwing
  home. Older directories are left untouched. The CLI's JSON cache report drops
  `legacy_entries` and `legacy_bytes`.
- **Upgrade steps:** update report consumers to use `entries` and `total_bytes`.
  Pipelines without a current cache entry compile on their next run. Stop older
  Sparkwing processes before removing their unused cache directories.

## Node state writes use transitions

`SetNodeStatus` and `RevokeNodeReady` on Store, Tenant and Client are removed.
The per-node `status` and `revoke-ready` HTTP endpoints are removed with them.
Use `Tenant.TransitionNode` for lifecycle changes. Queue withdrawal supplies a
`RevokeNode` command with the node reference and current run owner's authority.
Only ready, unclaimed jobs can be withdrawn. A competing claim or stale owner
returns an error without changing state or events. An identical committed
transition replays.

Debug pauses write `paused` to activity detail and clear it on resume.
They preserve the job's lifecycle status, start timestamp and claim.

## Owner leases replace run heartbeats

`TouchRunHeartbeat` and `TouchNodeHeartbeat` on Store, Tenant and Client, plus
`Run.LastHeartbeatAt`, are removed. Hosts use `Store.RenewUnit` or
`Tenant.RenewOwner` with their current owner authority. Owner renewal also
renews self-claimed jobs. A remotely renewed job
cannot extend the owner's lease.

Doctor no longer finalizes runs from heartbeat age or reports `orphaned_runs`.
The controller's elected `ReapUnits` pass recovers expired owners under the
store clock. Started local orphans fail in place. Unstarted owners requeue.
The legacy run-heartbeat and node-touch HTTP handlers are removed. The old
Kubernetes dispatcher no longer sends advisory heartbeat writes for its pods.
The pod's own claim renewal supplies its liveness timestamp.

## Plan acceptance uses tenant seals

`Store.AcceptPlan`, its legacy plan validator and `Client.SubmitPlan` are removed.
Use `Tenant.AcceptPlan` with `PlanAcceptance`, including the admitted request,
checkout declarations, resolved settings and the v2 `jobs` document. The store
reruns the shared validator and settings resolver before committing both the
second seal and jobs. Identical submissions replay. Changed sealed input returns
`spec_drift`. The controller's legacy claim-token plan-acceptance branch and the
old launched planner are removed.

Missing required secrets return canonical `plan_invalid`. The detailed refusal
is saved once for run status, after the failed acceptance rolls back, under the
current owner fence and store clock. No seal or job is committed by that refusal.

## Unit claims reserve compute credit

Metered `Store.ClaimUnit` awards commit a refundable compute reservation with
the claim. The award retains its CPU class, rate and payer after settings change
or bootstrap exchange. Unfunded queued work leaves funded matches eligible.
The shared billing calculator is required for renewal and final settlement.

## Offer-round fallback is removed

The warm dispatcher, its factory and the `finalize-ready` endpoint are removed.
`Store.FinalizeClaimRound`, `ClaimRoundResult`, `WithQueueRunners` and
`Client.FinalizeNodeReady` are removed with them. Use `Store.ClaimUnit` for queued
or named claims and `Tenant.ReconcileNode` for owner execution decisions.
Eligible work can be claimed immediately. Soft preferences rank eligible work.
A mismatch leaves the ready row and its queue position unchanged.

## Concurrency maintenance requires the operator fence

`Store.MaintainConcurrency` and the `store.Maintenance` registry are removed.
Acquire `controller.reaper` through `Operator.AcquireSingletonLease` and pass that
fence to `Operator.MaintainConcurrency(ctx, lease)`. The pass removes dead
reservations as complete group sets. Hosts retry `MayStart` for queued work, so
only the durable admission owner can grant a complete set. An error returns no
committed counts. The old concurrency-cache sweep and count APIs are removed.
Memo-record retention uses `Operator.MaintainRetention` under its maintenance
lease. `Store.PurgeDeadLocalConcurrency` and `Store.CountDeadLocalConcurrency`
are removed. The elected pass cleans terminal capacity in every scope. Doctor's
`dead_concurrency_holders` and `dead_concurrency_waiters` fields are removed with
its independent repair path.

## OAuth browser callbacks

OAuth start and callback use the shared route policy and browser-flow verifier.
Callback requires one flow cookie, one matching state and one code. A declined
callback carries one error and no code. Remove duplicate query selectors.
Refused proofs preserve the flow cookie. HEAD and unsupported methods return
the common `405 method_not_allowed` error.

Refusals return the common error envelope instead of a sign-in HTML page.
An invalid or replayed code returns `401 unauthenticated`. An unverified email
returns `403 forbidden`. Provider and session backend failures return
`503 unavailable`. The existing login budgets retain `429` and `Retry-After`.
Successful navigation and same-origin return paths retain their browser pages.

## Capability inventory and rotation

Use `GET /api/v1/tokens` instead of `GET /api/v1/team/tokens`. The collection
returns `{tokens:[...]}`. Read `subject.kind` and `subject.id` instead of
`family`, `kind`, `name` or `principal`, and `grants` instead of `scopes`.
`issued_at`, `expires_at`, `last_used_at` and `revoked_at` use RFC3339 timestamps.
Members see owned credentials. Team admins can see the team's credentials.
Deployment service/operator metadata uses `/api/v1/operator/tokens`.

Rotation uses `POST` on the selected token resource's `/rotate` suffix.
Send `grace_secs` and optional absolute `expires_at`, replacing `ttl_secs`.
Explicit expiry must pass the original parent's ceilings or current root
origin policy. Grace defaults to 24
hours, accepts zero for immediate replacement and has the existing seven-day
ceiling. Read the one-time bearer from `capability`, its metadata from `new`,
and the old credential's deadline from the RFC3339 `old_revoked_at`.
`old_prefix` replaces `old_replaced_by`. Never automatically retry rotation.

Embedded controllers provide `WithCapabilityManager`. Its reads and rotation
must recheck authority inside their store transaction. The retired token-table
APIs cannot supply this contract. Missing adapters return `policy_missing`.

## Session lifecycle

Session renewal and logout use the shared policy router with session proof.
Present one session cookie or one `Authorization: Session <id>` header.
Bearer credentials cannot substitute for a session. Renewal refuses HEAD and
ambiguous presentations and retains current membership and parent grant ceilings.

`POST /api/v1/auth/logout` takes no body. Remove `session_id` and supply the
session-bound CSRF header and cookie plus a matching Origin or Referer. Logout
ends only the verified session and clears its browser cookies. Both endpoints
return the common error envelope and no-store. Storage and key failures return
`unavailable`. Configure the bound secrets cipher before serving sessions.

Ordinary policy routes also accept the Session header and enforce the configured
absolute lifetime without renewing. Browser logout posts a form to `/logout`
with the session-bound `csrf_token`. It uses the shared proof boundary, returns
canonical failures and redirects to `/login` only after successful deletion.

## Browser capability requests

Shared policy routes authenticate browser sessions through their committed
hashed identity. Writes use the controller's session-bound CSRF verifier.
Configure the bound secrets cipher before serving sessions. The same cipher
protects the persisted session signing key. Forged, cross-session and
foreign-origin proofs return `forbidden`. Key access failures return
`unavailable`.

## Secret-by-name reads

GET `/api/v1/secrets/{name}` uses the shared capability policy. Member or
verified local authority with `secrets.read` or `team.admin` reads unmasked
config in its own team. Masked values require a live `secrets.release` claim
whose sealed run declares the name and permits its pipeline/shared scope and
reserved pool. Machine roots and deployment-admin tokens gain no read access.

Embedded controllers supply `WithSecretReader` with the authority-bearing tenant
transaction. Missing adapters return `policy_missing`. Bound encryption is
required, errors use the shared API envelope, and all responses use no-store.
Duplicate selectors and malformed queries fail after authentication.

`Server.ResealStoredSecrets` and `LegacyCipher` are removed with the startup
migration. Row-secret helpers require BoundCipher and never fall back to
unbound Seal/Open methods. Only team-bound envelopes can be opened for stored
rows. Explicit key rotation remains available for bound envelopes.

## Credential maintenance

Secret-key rotation requires live member or verified local `team.admin`
authority and selects only the authenticated team. Browser sessions require
CSRF. Rotation takes no body and returns the shared API error envelope.
Missing bound encryption returns `unavailable`. Plaintext and unbound rows
stay unreadable. Rewrite them through the authenticated credential API.
An unreadable git credential or any sealing failure rolls back secret and
git changes together. Unreadable secrets remain unchanged in `skipped`.

Source-release audit lists use the same team policy. Replace `token_prefix`
with `capability_prefix` in their responses. Each row also carries
`repository`, `job_id` and `generation`. Times remain Unix seconds.

The default store supplies `Tenant.RotateCredentials` and
`Tenant.SourceReleaseAudits`. Embedded controllers may replace them through
`WithCredentialRotator` and `WithSourceAuditReader`. Missing replacements return
`policy_missing`. `Store.RotateSecretValues` and `Store.RotateGitCredentialSecrets`
are removed. Select the authenticated tenant and pass its verified authority to
the combined rotation transaction.

## Credential mutations

Secret create/delete and git-credential create/confirm/delete require live
member or verified local authority with `team.admin`. Session mutations require
CSRF. The authenticated team selects the rows, and errors use the shared API
envelope. A deployment-admin bearer gains no team credential authority.

Embedded controllers supply `WithCredentialWriter` with tenant transactions
that recheck authority before commit. The default refuses writes with
`policy_missing` until that adapter is installed. Existing unfenced Tenant
writers cannot stand in for it. Secret names must pass the shared grammar on
replacement too. Omit an SSH port to use 22. An explicit zero is invalid.

## Credential inventories

Secret and git-credential lists use the shared capability policy. Use a live
member or verified local credential with `runs.read`. Secret inventory also
accepts `team.admin`. The authenticated team selects the rows. Machine roots,
claims and bootstraps cannot list them. Masked secret values and stored git
credentials remain private. An unreadable public config value fails the whole
secret list instead of disappearing from a successful response.

Embedded controllers may supply `WithCredentialInventoryReader` using Tenant's
existing read methods. Its reader must belong to the authenticated team.

## Source credentials

`POST /api/v1/runs/{run}/source-credential` takes no body and requires a live
`source.read` capability bound to the run. The response carries `kind`, `host`,
`secret`, optional `username` and optional `known_hosts`. Checkout identity and
policy come from the sealed execution input. The store commits authorization
and its audit before the controller decrypts the credential.

Embedded controllers may supply `WithSourceController` with the declared
transaction and bound-cipher interfaces. `WithSecretsCipher` also configures
C1's plan-secret opener. Missing keys refuse decryption and plan validation.

## Work claims

Use `POST /api/v1/runners/claim` with the shared claim request instead of the
separate trigger and job queue endpoints. Every requested kind needs its own
grant. The response contains a private `claim`, or an empty object when no work
matches. Enrollment supplies the runner identity, repository scope and pools.
The body supplies labels, current availability and the runner version.

`POST /api/v1/runs/{run}/claim` acquires the named trigger, including a reserved
here run. It requires the single `trigger` kind. `POST /api/v1/launcher/claim`
requires launcher authority and accepts plan, eval and job kinds. Configure the
engine controller's verified runner policy and the store's enrollment resolver
before accepting claims. Existing holders may renew while software policy
blocks new awards.

Send a pod bootstrap to `POST /api/v1/claims/exchange` with an empty JSON object.
It yields the original award's full in-memory capability once and consumes the
bootstrap. Renew through `POST /api/v1/claims/{claim}/heartbeat`, where `claim`
is the award's unit ID. Renewal cannot start a job without its start transition.

Claim, exchange and reconciliation responses carry bearer material once. Do not
retry a lost response automatically. The unused award recovers through its lease.
Launcher sync carries each immutable award fence as `{kind,id,holder,generation}`,
the Kubernetes Job as `{namespace,name,uid}`, and `node_uid`. It returns awards
with empty capabilities and full Job references to remove. Name-only cleanup
is removed. Embedded controllers must
supply `EngineController.Launcher`. Missing snapshot wiring returns
`policy_missing` and exposes no award.

The standalone artifact-manifest write is removed. Publish manifests through
record and result commits. Legacy remote state clients cannot publish a manifest
separately. Local state writers retain their existing direct persistence while
the host moves to result transactions.

## Data writes and records

Upload and commit use live controller capabilities through the shared policy
router. Replace `{kind,key,size,sha256,run_id}` with `{digest,size}` on
`POST /api/v1/data/upload`. PUT the bytes with the returned signed headers,
then commit with `{upload_id}`. Commit returns `{digest,created_at}` with
HTTP 200. Cache grants and the pretrigger user-token upload path are removed.

Create references with `POST /api/v1/data/records` and look them up with
`GET /api/v1/data/records`. The controller derives the team, repository, write
ref and uploader. Record responses omit private authority and receipt facts.
See [Direct data uploads](../data-uploads.md) for the request fields.

Embedded controllers must configure `WithDataController`. Upload discovery
uses those adapters. The older direct S3 output/log configuration does not
advertise authenticated data writes.

## Data reads

`POST /api/v1/data/download` accepts `{"digest":"<sha256>"}` through the shared
capability policy. Replace cache grants and `{kind,key}` requests with a live
data capability and an authorized digest. Responses include signed `headers`
alongside `url`, `sha256`, `size` and `expires`. Send those headers to storage.

`POST /api/v1/data/has-many` returns only readable live digests from its
`digests` array. Missing and unreadable objects are indistinguishable. This
query does not consume the team's download allowance. Both routes return
the common error envelope.

## Run detail

`GET /api/v1/runs/{run}` returns `RunDetail` with display-safe `inputs`,
`jobs`, `attempts`, `logs`, `engine` and `notices`. Replace readers of raw
run fields and the former `{run, nodes}` wrapper with this shared shape.
Fetch approval, step and child details through their own resources.

`include=secret_values` is removed. Secret argument values stay redacted for
members, claims and local peers. Missing or invalid classification hides every
value, including on finished runs. Execution inputs travel in the private
claim wire. Members require `runs.read`; bound claims require `source.read`
or `trigger.host` for their run. Bare machine roots cannot use this read.

Embedded controllers must configure the shared authorization adapters and
`EngineController.ReadRunStatus` when overriding the default
`Tenant.ReadRunView` adapter. The reader supplies one committed snapshot,
complete attempt history and validated log subjects for the selected team and
run. Missing wiring refuses the request with `policy_missing`.

## Queue reads

Read `/api/v1/queue` instead of `/api/v1/queue/state`. The CLI's remote queue
reader and the dashboard use the new path. The old path is removed. The
response retains the concurrency groups, holders, waiters and runner headroom
for the authorized team.

A member or verified local peer needs `runs.read`. Machine roots and work
claims cannot read the team queue. Configure the controller's shared verifier
and binding adapters before serving the route. Errors use the common envelope.
Embedded controllers may supply the existing tenant read contract through
`WithQueueReader`. Its default uses the server's store.

## Credit ledger reads

`GET /api/v1/credits` and `GET /api/v1/credits/history` use the shared
capability verifier. A member or verified local peer needs `runs.read` for
its active team. Configure the controller's shared authentication and binding
adapters before serving these routes. Machine roots and claims cannot read
the team ledger, even when they carry `runs.read`.

The paths and successful response fields stay the same. Errors use the
canonical envelope. Missing or dead credentials return `unauthenticated`.
Live credentials without member authority return `forbidden`. Authorized
reads return `not_found` when credit billing is disabled. History accepts
one nonnegative `limit`, at most 1000 rows of each kind. Omission or zero
uses the store's existing default.

## Job status

Read `/api/v1/runs/{run}/jobs` and `/api/v1/runs/{run}/jobs/{job}` instead
of the corresponding `/nodes` paths. The collection returns a JSON array of
safe job status objects. The detail returns one such object. Both use `id`
for the job identifier and omit raw declarations, environment, holder and
capability fields. Private execution inputs use the awarded execution wire.
The existing Go client's status readers use these paths and the canonical
error envelope. Missing jobs return the typed `not_found` API error.

Members need `runs.read`. The live run owner may read the collection and
details with `trigger.host`. A job's live `data.read` authority reaches its
own detail only. Controllers need the shared authentication/binding adapters
and C1's tenant snapshot/node reads. No old path or credential fallback remains.

## Pipeline sizing

Replace `/api/v1/pipelines/{name}/profile` with
`/api/v1/pipelines/{name}/sizing` for the read and its `pin`, `observations`,
`contention` and `waits` operations. Replace the `node` query with `job`.
The old paths are removed. The Go controller client uses the new paths.

Reads require member `runs.read` or an executable claim with `source.read`
and authority for the sealed repository and pipeline. A bare runner or
Actions credential no longer reads arbitrary sizing records. Bootstrap
credentials cannot read sizing. Writes require `node.report` on the bound
pipeline. Admin grants cannot bypass the claim check.

Controllers must configure the shared authentication and binding adapters.
Sizing writes also require an authority-bearing transaction adapter through
`WithSizingStore`. An absent adapter returns `policy_missing`. Implementations
must recheck revocation, membership and the claim fence under the write lock,
and avoid duplicating a measurement on an exact retry.

## Credential discovery

`GET /api/v1/auth/whoami`, `GET /api/v1/services` and
`GET /api/v1/data/capabilities` require live capability authority, a session or
a verified local peer. Service and data discovery also require an active team.
Configure the controller's shared authentication reader and transport adapters
before serving these routes. An incomplete policy returns `policy_missing`.

`whoami` returns the committed subject ID in `principal`, its subject kind in
`kind`, and effective grants in `scopes`. The kind is `account`, `machine`,
`service`, `operator`, `local` or `anonymous`. `token_prefix` is present only
for a capability. The response omits `role` and session identifiers.
Callers should handle the common `unauthenticated`, `forbidden` and `not_found`
error envelope. Bootstrap and eval credentials cannot discover services.

The OIDC discovery document and public keys stay public. Their disabled-issuer
response uses the same error envelope. All five discovery routes accept only
`GET` and return `method_not_allowed` with `Allow: GET` for other methods.

## Job reporting

Annotations, summaries and step start, finish and skip requests use
`/api/v1/runs/{run}/jobs/{job}`. Replace `/nodes/{nodeID}` in those seven
reporting paths with `/jobs/{job}`. The Go controller client uses the new
paths. The old paths are removed.

Reports require an awarded job or trigger capability with `node.report`, or
the run's local publication authority. Caller-supplied holder and generation
headers grant no authority. Step finish accepts `passed`, `failed` and
`cancelled`, plus an optional inner error with a code and masked message.
Annotations and summaries accept at most 128 KiB of masked text. Their job
and step routes accept a stable run ID or its number within the authenticated
team. Selecting a number preserves the awarded job and holder fences.

`RunReview.AppendAnnotation` returns only an error after the controller's empty
HTTP 204 acknowledgement. It has no `Applied` result. The JSON output of
`runs annotations add` contains `run_id`, `job_id` and optional `step_id`.
The former `applied` field is removed because annotation writes do not return
transition or replay facts.

## Unit recovery requires the elected reaper lease

`Store.ReapExpiredNodeClaims`, `Maintenance.RecoverExpiredNodeClaims`,
`Maintenance.ReapExpiredTriggers` and `AgentLossRecovery` are removed. Acquire
`controller.reaper` through `Operator.AcquireSingletonLease`, then pass that fence
and a batch limit to `Store.ReapUnits`. Results identify the team, run and unit.
Lease loss requeues unfinished remote work within the same run. Started local
owners fail in place. Unstarted owners consume no execution attempt or disruption.
`Store.RequeueUnstartedClaim` and `Tenant.RequeueUnstartedClaim` are removed.
Expired owner awards use the same leased pass. An unstarted owner requeues
without a disruption, and a replacement award advances its generation.
The former holder cannot renew, finish or accept a plan for the replacement.

## Build trust uses tenant handles

`Store.TrustLocalBuilds` and `Store.SetTrustLocalBuilds` are removed. Obtain a
tenant with `Store.ForTeam`, then call `Tenant.TrustLocalBuilds` or
`Tenant.SetTrustLocalBuilds` without a team argument. Local stores use
`store.DefaultTeam`. The default is false. The HTTP build-trust routes and
their membership requirements keep their existing behavior.

## Financial totals use persisted awards

`Store.CreditLedgerTotals` subtracts refundable reservations using the immutable
billing awards and intervals. Reading a report leaves billing checkpoints and
intervals unchanged. Unstarted metered awards contribute no settled seconds.
Open compute intervals stop at the persisted charged checkpoint in reports.
Storage charges affect balances but contribute no compute seconds.

The unused `Store.RunFinished` method is removed. Leased retention and tagged
log-stream maintenance own pruning.

## Grant requests use tenant handles

`Store.RecordCreditGrant` is removed. Obtain a tenant with `Store.ForTeam`,
then call `Tenant.RecordCreditGrant`. Local stores use `store.DefaultTeam`.

## Billing reads use tenant handles

`Store.CreditBalanceMicro`, `Store.CreditState` and `Store.ListCreditGrants` are
removed. Obtain a tenant with `Store.ForTeam`, then call the matching Tenant method.
Local stores use the handle for `store.DefaultTeam`.

## Rate-table presence uses the settings snapshot

`Store.CreditRateTableSet`, `Store.CreditRateMicroPerSecond` and
`Store.CreditGraceSeconds` are removed. Read `CreditSettings.RateTableSet` and
`CreditSettings.RateMicroPerSecond` from the same settings snapshot; the grace
setting is gone (see [Cloud billing is prepaid](#cloud-billing-is-prepaid)).

`Store.SetCreditRateTable` is removed. Write the table through
`SetCreditSettings` with `CreditSettingsUpdate.RateTable`.

## Abandoned logs use tagged streams

`Store.AbandonedNodeLogs` and the controller's old periodic node-log sweep are
removed. `Operator.SealAbandonedLogStreams` closes existing execution streams as
incomplete under the maintenance lease. Job attempts keep their 60-second final
tail grace. Trigger, plan and eval streams use their own persisted unit fences.
Sealing preserves committed segments and job outcomes. Published logs still
require the publisher's final commit because they have no execution lease.

## Source bundles use record reachability

`Store.ClaimExpiredSourceBundles`, `Store.DeleteSourceBundleRows`,
`Store.PruneExpiredUploads`, `store.DirectCacheMaxAge` and
`controller.CacheObjectMaxAge` are removed. Cache listings no longer expire bytes
by object age. Keep source bytes through blob records. Run
`Operator.MaintainRetention` under the maintenance lease to expire records and
release upload-ticket quota holds. Use `Operator.MarkBlobTombstones` and
`Operator.CollectBlobTombstones` with the verified object backend for physical
collection after the 24-hour grace.

## Node logs reach the controller as segments

- **Before:** a Kubernetes Job, warm runner, self-hosted agent, GitHub
  Actions runner, launcher Job or the trigger runner's in-process nodes
  appended node logs to the logs service at their logs URL, as did a laptop
  run against a remote profile, and without one
  wrote them to a disk nobody could read and that went away with the pod.
  `sparkwing runs logs --profile` and `runs logs --grep --profile` read the
  logs service the profile or the controller named.
- **After:** against a controller that keeps segments, every one of those
  runners uploads each node's log as gzip segments, each through a URL the
  controller signs for its exact size and Content-MD5, plus a sealing
  manifest it posts to the controller, into the controller's cache bucket
  (`--cache-blob-store`) or, without one, its output directory, whether or
  not it has a logs URL. The dashboard and the CLI read every node through
  the controller, which serves segments first and the logs service for a
  node that wrote none, so logs the service already holds stay readable.
  Deleting a run or a team deletes its segments.
- **Operator steps:** keep the logs service running so its existing logs
  stay readable. On an S3 cache bucket, grant the controller
  `s3:GetLifecycleConfiguration` and add enabled lifecycle rules expiring
  `<cache prefix>/logs/r90/` after at least 90 days and `<cache prefix>/logs/r30/`
  after at least 30, plus `<cache prefix>/logs/r<days>/` after at least `<days>`
  for each team `--log-retention team=days` names. The controller deletes log
  objects at the class age itself, so set each rule longer as a backstop; [self-hosting.md](../self-hosting.md#node-log-retention)
  has the command. Until a class has its rule, the controller issues no
  upload URLs into it and those nodes' runners keep writing to their logs
  URL, or their own disk without one. A controller that keeps segments on
  its own disk needs no rule and keeps them until their run is deleted.
  On a multi-team controller segment bytes count against the team's log
  storage.
- **Logs the service already holds:** nothing converts them; they become
  unreadable when you retire the service.
- **Mixed versions:** upgrade the controller first. A runner that predates
  segments keeps writing to its logs URL, which the controller still reads
  through. A runner of this release against an older controller finds no
  segments advertised and writes to its logs URL, or locally without one,
  as before. Rolling the runners back returns their new logs to the logs
  service.

## The logs service is removed

This release ships no `sparkwing-logs`. Its steps replace the ones in
[Node logs reach the controller as segments](#node-logs-reach-the-controller-as-segments)
that keep the logs service running.

- **Before:** `sparkwing-logs` held node logs that a runner without
  segments, or a node from before segments, wrote; the controller read
  through it and announced it as `logs` in `GET /api/v1/services`. The
  runner-bundle chart deployed it, and runners, agents and profiles named
  its URL.
- **After:** the controller is the only place node logs go. It keeps them
  as segments in its cache bucket (`--cache-blob-store`) or, without one,
  its output directory; a controller with neither keeps no node logs, so a
  runner keeps the log on its own disk. Pipeline binaries built against any SDK
  keep logging unchanged: the runner serves their appends on loopback.
- **Before you upgrade:** nothing converts the service's logs; delete its
  volume once you no longer need the files.
- **Helm:** delete `sparkwing-runner-bundle.logs` (or `logs` for a
  standalone runner bundle) and `controller.logs` from your values; a
  values file that still sets them fails to render. The upgrade removes the
  logs Deployment, Service and ServiceAccount; the logs PVC is kept by its
  resource policy, so delete it once the export is done:

  ```sh
  kubectl -n <namespace> delete pvc <release>-sparkwing-runner-bundle-logs
  ```

- **Controller:** drop `--logs-url` and `--logs-archive-store`, and delete
  the `logs-delete-token` file from `--credentials-dir` (or the
  `controller.credentialsSecret` key). Nothing reads it. Leased maintenance
  bills committed blob and tagged-log inventory. Deleting a team deletes its
  segments.
- **Runners and agents:** drop `--logs` from `sparkwing-runner runner` and
  `launch`, `--trigger-runner-logs-url` from `runner`, and the `logs:` key
  from an agent's `config.yaml`, which now fails to load with
  `field logs not found`. `sparkwing runners add --runner NAME --profile PROFILE` has no `--logs`.
  Drop any `SPARKWING_LOGS_EGRESS_*` variables; nothing reads them.
- **Profiles:** the `logs` and `cache` surfaces are gone; remove them.
- **Credentials and API:** the `logs.write` and `logs.delete` scopes are
  gone, and minting a token with either is refused; runner tokens carry
  `nodes.claim`, `triggers.claim`, `runs.state` and `secrets.read`.
  Existing tokens that list them keep working with the rest of their
  scopes. `GET /api/v1/runs/{id}/log-access`,
  `POST /api/v1/runs/{id}/nodes/{nodeID}/claim/validate`, the `logs` field
  of `GET /api/v1/services` and signed `kind=log` downloads are removed;
  read logs through `GET /api/v1/runs/{id}/logs/{node}`. The storage
  counter routes answer only the cache's operator token.
- **Laptop:** `sparkwing dashboard start` uses the local controller and its
  shared log segments. `sparkwing runs prune` removes completed history.
- **Go packages:** `pkg/logs` and `internal/storage/sparkwinglogs` are removed.
  Read tagged log pages through `client.NewLogReader` from
  `internal/controller/client`. Each page reports whether the run and its logs
  are complete.
- **Mixed versions:** upgrade the runners and the controller together. A
  runner from before segments has nowhere to send its logs once the
  service is gone, and keeps them on its own disk.

## Operator powers need the operator console

- **Before:** an admin token reached a team's billing standing and the
  sign-up waitlist through `GET`/`POST /api/v1/teams/{team}/trust`,
  `GET /api/v1/signups/waitlist`, `POST /api/v1/signups/waitlist/approve`,
  `free` and `reversal` grants on `POST /api/v1/credits/grants`, holds
  naming a team and every release on `POST /api/v1/credits/freezes`, and
  refunds on `POST /api/v1/credits/reversals`. It also passed every other
  `credits.grant` route.
- **After:** those powers answer only a listed operator's own signed-in
  console session (`--operator-accounts`). The four admin routes above are
  removed. Paid checkout events use the signed `/webhooks/stripe` callback.
  `POST /api/v1/credits/grants` is removed.
  The card, refund, warning, dispute, unit and payment-lookup service routes
  under `/api/v1/credits/` are also removed. No service token can submit
  financial outcomes. Card and dispute provider-event integration remains
  incomplete. The console's trust action changes standing. Its unfreeze releases a
  payment dispute's hold named by `hold`. The billing-model section below
  describes the current response fields. The new
  `sparkwing-controller admin freeze|unfreeze` holds or releases a team
  from inside the controller pod when console sign-in is down.
- **Migrate:** list the operators' account ids in `--operator-accounts`
  (Helm: `controller.operatorAccounts`) and have each sign in with Google
  or GitHub before the upgrade; an operator token's session does not reach
  the console. Move each call:

  | Before (admin token) | After |
  |---|---|
  | `GET /api/v1/teams/{team}/trust` | `GET /api/v1/operator/teams/{team}`, field `billing` |
  | `POST /api/v1/teams/{team}/trust` | `POST /api/v1/operator/teams/{team}/trust`, same body |
  | `GET /api/v1/signups/waitlist` | `GET /api/v1/operator/waitlist`, newest first, with `offset` |
  | `POST /api/v1/signups/waitlist/approve` | `POST /api/v1/operator/waitlist/approve`, same body |
  | `free` grant on `POST /api/v1/credits/grants` | `POST /api/v1/operator/teams/{team}/grants` with `amount_cents`, `reason` and `key` |
  | `reversal` grant, or a refund on `POST /api/v1/credits/reversals` | refund in the Stripe dashboard; callback settlement remains incomplete |
  | hold naming `team` on `POST /api/v1/credits/freezes` | `POST /api/v1/operator/teams/{team}/freeze` |
  | `release` on `POST /api/v1/credits/freezes` | `POST /api/v1/operator/teams/{team}/unfreeze`, with `hold` for one dispute's hold |

  A script that used the admin token for any row above now gets `404` or
  `403`.
- **The checkout service's token:** `credits-token` is removed from controller
  credentials. Delete its configuration after switching checkout settlement
  to the signed provider callback. The controller no longer seeds a service
  token on startup.
- **Runner permissions:** GitHub Actions enrollment moves from
  `/api/v1/team/github-runners` to `/api/v1/runners/github`. Removal selects
  the positive numeric repository ID in `/api/v1/runners/github/{repository}`.
  Both mutations require live team administration and session-bound CSRF
  when using a session. The dashboard uses these paths.
- **Runner metering:** `/api/v1/tokens/{prefix}/metered` is removed. Use the
  console-only `/api/v1/operator/runners/{runner}/metered` with an explicit
  boolean `metered`. The runner selector is the enrolled root's public
  capability prefix. The response contains `runner` and `metered`.
- **Go:** select the registered team with `Store.ForTeam`. Use
  `Tenant.ReleaseCreditFreezes` to release a named hold. A release requires the
  actor, reason and one hold ID. An empty ID is refused. Provider disputes and
  refunds use verified payment-event settlement. Raw callback APIs and
  `Store.PaidGrantTeam` are removed.
  A caller of the removed `Store.DisputeTeam` reads the team from its hold
  or from `Tenant.CreditFreeze`.

## Password users are removed

- **Before:** the controller kept dashboard users with argon2id passwords.
  `POST /api/v1/auth/login` and the `/login` form exchanged a username and
  password for a session, `GET`, `POST` and `DELETE /api/v1/users` and
  `sparkwing cluster users add|list|delete` managed the accounts, and an open
  single-team controller offered a "Create first admin" form at
  `/login/bootstrap`.
- **After:** dashboard sign-in is Google or GitHub, where a multi-team license
  and an OAuth client offer them, or an operator token: a user token holding
  `admin`, such as the bootstrap admin token, pasted into the `/login` form.
  The routes, the form, the CLI commands and the client's `CreateUser`,
  `ListUsers` and `DeleteUser` are gone; each route answers `404`, or `401` to
  a request with no credential. The new store has no users table or
  password sessions, so each person signs in again. A token session
  follows its token: revoking or expiring the token ends it.

Before upgrading:

1. Choose how each person signs in. On a controller with a multi-team license,
   configure Google or GitHub sign-in (`--google-client-id`,
   `--github-client-id`, their secrets and `--oauth-redirect-uris`, as
   [auth.md](../auth.md#google-and-github-sign-in) lists) and invite each
   former password user to their team. Without a license the controller offers
   no identity provider, so the operator token is the dashboard's only sign-in.
2. Make sure the operator holds an admin token. A controller provisioned with
   `--bootstrap-admin-token-file` (chart: `controller.bootstrapAdminToken.name`)
   already has one. Otherwise provision the deployment administrator through
   the controller's private bootstrap credential before upgrading.

3. Before: `sparkwing cluster users`. Remove that command from scripts. A script that used
   `POST /api/v1/auth/login` for a session calls the API with a bearer token
   instead.

**Edge cases:**

- A password user with a narrower scope set has no direct replacement: an
  operator token carries `admin`. Give that person an account on a licensed
  controller with the matching role, or a bearer token of the narrower scopes
  for the CLI.
- An open controller (empty tokens table) serves the dashboard without
  sign-in, as before, and its `/login` redirects to the page asked for.
- Rollback: an older controller or CLI cannot read the new store. Roll back by
  starting the older binary against the old store you set aside.

## Team CLI and runner tokens become team tokens

Scripts that mint team CLI or runner tokens through the API move to one route.
Tokens already minted keep working.

- **Before:** a team member minted a personal CLI token with
  `POST /api/v1/team/cli-tokens` and an editor minted a runner token with
  `POST /api/v1/team/runner-tokens`. Each kind had its own `GET` list and
  `DELETE /{prefix}` revoke, and an owner released git credentials to a
  machine with `PUT /api/v1/team/runner-tokens/{prefix}/git-credentials`.
- **After:** those routes are gone with no alias.
  `POST /api/v1/team/tokens` mints a team token (`swt_`) whose `scopes` come
  from exactly one family. Member scopes (`runs.read`, `logs.read`,
  `triggers.read`, and for an editor or owner `runs.write`, `runs.control`,
  `approvals.write`) make a CLI token: any member mints one, with no `name` or
  `repos`, and it lasts 90 days. Machine scopes (`nodes.claim`,
  `triggers.claim` and `runs.state`, including `nodes.claim` or
  `triggers.claim`) make a runner token: an editor mints one
  with `name` and `repos`, and it lasts 90 days past its last use. A request
  that mixes families, names an unknown scope or sends none answers `400`.
  `GET /api/v1/team/tokens`, `DELETE /api/v1/team/tokens/{prefix}` and
  `PUT /api/v1/team/tokens/{prefix}/git-credentials` replace the list, revoke
  and git-credential routes for both kinds. An agent name is unique across live
  runner and machine tokens.

To upgrade:

1. Leave existing tokens alone. A `swu_` CLI token or `swr_` runner token
   minted from team settings keeps authenticating as before until it expires
   or is revoked, and `GET /api/v1/team/tokens` lists it with the new ones.
2. Point scripts at the new route and name the scopes. The route still needs
   a signed-in session, as the old ones did. A runner token becomes this body:

   ```json
   {"scopes": ["nodes.claim", "triggers.claim", "runs.state"],
    "name": "build-box", "repos": ["github.com/acme/*"]}
   ```

   A CLI token sends the scopes of the caller's role, for an editor
   `["runs.read", "logs.read", "triggers.read", "runs.write", "runs.control", "approvals.write"]`,
   and no `name` or `repos`.
3. Move list, revoke and git-credential calls to `/api/v1/team/tokens` and
   `/api/v1/team/tokens/{prefix}`. Read each row's `family` to tell a member
   token from a machine token.

**Edge cases:**

- Minting a machine token whose `name` a live runner or machine token already
  holds answers `409`, as it did for runner tokens. Revoke the old token
  first, or pick another name.
- The 10 live member tokens per member and 100 live machine tokens per team
  count the old CLI and runner tokens too.
- Before: `sparkwing cluster tokens`. Operator credentials now use
  `sparkwing operator tokens list --profile PROFILE`; the unauthenticated token
  minting route is removed. The dashboard's **Team > Machines** page uses
  shared machine-registration routes.

## Assisted offers, enrollment and the foreground fleet are removed

The assisted-offer claim protocol had no shipped helper client, so nothing
but tests reached it. Machines that helped a run go back to being ordinary
runners, which claim nodes the way warm trigger runners always have.

- **Before:** an operator enrolled an executor with
  `sparkwing cluster agents enroll` (`PUT /api/v1/agents/{name}`), binding a
  runner token to a name, priority, budget and placement. An enrolled
  executor kept itself live with `POST /api/v1/agents/{name}/heartbeat`,
  previewed work with `POST /api/v1/nodes/claim/prepare` and offered for a
  node by sending `executor_name` and a reservation to
  `POST /api/v1/nodes/claim`; its token could no longer make an ordinary
  claim. `sparkwing fleet init` wrote a `fleet` section into `config.yaml`,
  and `sparkwing run PIPELINE --sw-fleet` ran a foreground coordinator that
  offered nodes to the helpers that section named.
- **After:** those routes answer `404`, and `POST /api/v1/nodes/claim`
  refuses the offer fields as unknown. Before: `sparkwing fleet`.
  Before: `sparkwing cluster agents enroll`. These commands and `--sw-fleet`
  are removed. Runners claim through `POST /api/v1/runners/claim`.
  `sparkwing runners list --profile PROFILE` shows the runners that claimed in the last hour with the labels, slot limit
  and headroom they advertised; `active_slots`, `base_priority`,
  `priority_ceiling`, `budget` and the `gateway` type are gone. The store
  has no executor or offer tables, and an older controller refuses it.

To upgrade:

1. Delete the `fleet:` section from `config.yaml`. Until it is gone every
   command that reads the file stops with
   `unknown section "fleet"`.
2. For each machine that was an enrolled helper, run it as an ordinary
   runner: `sparkwing runners add --profile PROFILE --runner MACHINE`
   on it mints its runner token and starts the service, or deploy the runner
   bundle in Kubernetes. Give it the capabilities its enrollment listed with
   `--labels`, and its `max_concurrent` with `--max-concurrent`. Revoke the
   token the enrollment named once nothing uses it.
3. Run pipelines without `--sw-fleet` or `--sw-allow-secret-file`; the
   latter only fed the fleet's source snapshot, and
   `sparkwing pipeline trigger PIPELINE --profile PROFILE --working-tree --allow-secret-file PATH` keeps it. A foreground `sparkwing run` executes
   on the machine you run it on; to spread a run across machines, trigger it
   on a controller with runners attached.
4. Code embedding `internal/controller/client` drops `PrepareExecutorClaim`,
   `OfferExecutorClaim` and `HeartbeatExecutor`, and calls `ClaimNode` where
   it called `ClaimNodeAs`. Code embedding `internal/store` renames
   `Tenant.ListLegacyAgentClaims` to `ListAgentClaims`, `LegacyAgentClaim` to
   `AgentClaim`, and drops the executor, offer and scheduling-summary methods.
   Claim-round finalization is removed entirely.

**Edge cases:**

- The enrolled executors' priorities, budgets and trusted locations have no
  runner equivalent. A runner advertises its own labels and capacity, and
  the controller places work by those.
- Runs record no `executor_offer_received`, `executor_offer_expired`,
  `executor_offer_declined`, `executor_offer_awarded` or `executor_selected`
  events from now on; runs already stored keep theirs.

## Runner claims answer with claim tokens

A runner's credential stays in the runner. The process that runs a node's
code, and every Kubernetes Job a runner creates, authenticates with the
node's claim token instead.

- **Before:** a self-hosted runner, a Kubernetes or warm trigger runner and
  `sparkwing cluster worker` handed their bearer to the code they ran. A
  Kubernetes or warm runner passed it to the `handle-trigger` child, and the
  Kubernetes node runner wrote it into every Job's `SPARKWING_AGENT_TOKEN` as
  a literal value, readable by anyone who can `get` Jobs in the namespace.
- **After:** the node claim routes (`POST /api/v1/nodes/claim`,
  `POST /api/v1/runs/{id}/nodes/{nodeID}/claim`) and the trigger claim routes
  (`POST /api/v1/triggers/claim`, `POST /api/v1/triggers/{id}/claim`) add a
  `claim_token` field. A node's claim token acts as the runner that made the
  claim, confined to the claim's run and to writes on its own node, and stops
  when the claim ends. Beyond its run it reaches only what `Ref.Get`,
  `TryGet` and `RunAndAwait` need in its own team: a pipeline's latest run,
  run outputs, starting and polling its own node's child runs, and the
  spawned-child lookup. A trigger's claim token reads the run's declared
  secrets. A Job's `SPARKWING_AGENT_TOKEN` holds its node's claim token, and
  every trigger child reaches the controller through a loopback broker that
  admits only its run's routes. The store keeps them in the
  `trigger_claim_tokens` table.

To upgrade:

1. Upgrade the controller and every runner together: the runner bundle, the
   `sparkwing-runner runner` service, GitHub Actions runners and
   self-hosted runners registered to Cloud. A runner from this release gets no claim token
   from an older controller, and its nodes fail with `401`.
2. Leave runner tokens as they are. They keep claiming, heartbeating and
   making the runner's own calls; only the work they claim moves to claim
   tokens.

**Edge cases:**

- A runner older than this release keeps working against the upgraded
  controller, ignoring the `claim_token` field and running nodes on its
  bearer.
- A node's claim token answers `403 claim_mismatch` for a route outside its
  run and those cross-pipeline calls, or a write to another node, and
  `403 claim_ended` once its claim has ended. A pipeline that reached
  another run through its runner's bearer from inside a node, beyond those
  calls, no longer can.

## Node secret reads need the claim token

A node reads its run's secrets only through its claim, so every read is one
the run's plan declares and every release is recorded.

- **Before:** `GET /api/v1/secrets/{name}?run=<id>` answered any bearer
  holding `secrets.read` and a live claim on the run with any secret of the
  run's pipeline, with no declaration check and no event. A runner token,
  and scripts given one, read secrets this way.
- **After:** that bearer gets `403 claim_required`. A claim token reads a
  name its run's plan declares and leaves a `secret_released` event; an
  undeclared name answers `403 secret_undeclared`. The operator (`admin`)
  and a team's owner (`team.admin`) still read a row by name; a masked row
  still answers a team owner `403 write_only`. `secrets.read` is no longer a
  machine team token scope: `POST /api/v1/team/tokens` refuses it with
  `400`, and `sparkwing runners add --runner NAME --profile PROFILE`, the dashboard and the GitHub
  Actions exchange stop granting it. Existing tokens that carry it keep
  authenticating; the scope releases nothing.

To upgrade:

1. Upgrade every runner together with the controller (see
   [Runner claims answer with claim tokens](#runner-claims-answer-with-claim-tokens)).
   A runner older than this release reads no secret from the upgraded
   controller, so its nodes fail at their first secret.
2. Declare every secret a pipeline reads in its `Secrets()` struct. A node
   that calls `sparkwing.Secret(ctx, name)` for a name the struct does not
   declare now gets `secret_undeclared` on a runner, as it already did on a
   controller-dispatched run.
3. Move scripts that read secrets with a runner or machine token to an
   operator credential (managed with `sparkwing operator tokens create --principal NAME --scope SCOPE --profile PROFILE`) or a team owner's
   credential.
4. Drop `secrets.read` from scripts that mint machine tokens through
   `POST /api/v1/team/tokens`, and from code that calls
   `store.Store.ClaimedRunsFor`, which is removed; `ClaimedRunFor` stays.

**Edge cases:**

- A local run whose profile reads secrets from a controller does so with
  the profile's token, which reads by name only when it is an operator or
  team-admin credential, as before.
- A node that runs inside its trigger's process, under the in-process node
  runner, reads through the trigger's claim token; its release event names
  the run and no node.

## The cache's static operator token is removed

Remove `SPARKWING_CACHE_TOKEN`, the `cache-token` credential and
`cache.tokenSecret` from older installations. The separate cache service,
its grant-key replacement and the controller's cache proxy are now removed.
Do not provision a replacement cache token or grant key. Remove the cache
deployment and use the controller's authenticated data routes for byte storage.

## Cache-service accounting is removed

The cache service and its accounting client have been deleted. Remove the
cache deployment and its `controller-token` credential. The controller no longer
registers the cache's storage-reserve, commit, release, download-charge or
egress-total routes. The `storage.count` service scope has been removed.
Storage and download accounting stays in the controller's database.

## The source-token route is removed

- **Before:** `POST /api/v1/runs/{id}/source-token` minted a read-only GitHub
  App token for a runner holding a claim on the run, and
  `GET /api/v1/capabilities` advertised it as `github_app.source_tokens`. A
  runner asked it when the git-credential route answered a plain 404, as a
  controller older than v0.66 does.
- **After:** the route answers 404 and the capabilities answer has no
  `source_tokens` field. A runner asks only the controller's source credential
  route and reports an error when the controller lacks it. A runner or
  controller older than v0.66 fetches no source from a controller of this
  release, and a runner of this release fetches no source from a controller
  older than v0.66.

To upgrade:

1. Upgrade every runner and the controller to this release together. The
   release ships after 2026-10-15, two weeks after v0.66, so a fleet that
   kept current already runs v0.66 or later.
2. Drop `github_app.source_tokens` from code that reads
   `GET /api/v1/capabilities`; `github_app` still names the App's `slug`.

## Source credentials go to claim tokens only

- **Before:** a runner without the git cache asked
  `POST /api/v1/runs/{id}/git-credential` with its own token while it held a
  claim on the run. The controller minted a GitHub App token for whatever
  github.com repository the run named, if an installation of the team covered
  it, kept it for reuse, and otherwise released the team's stored git
  credential for the host. The answer carried `repository` and
  `extra_repositories`.
- **After:** `POST /api/v1/runs/{id}/source-credential` is the only route,
  and it answers only a claim token of the run: a node claim's or trigger
  claim's, which every runner claim returns, or a Cloud Job's. A bearer gets
  `403 claim_token_required` and `git-credential` answers 404. The App token
  reads the repository the App's webhook recorded on the run's trigger, or a
  repository the run names by hand only when one of the team's App trigger
  subscriptions names it. The team's stored host credential is unchanged in
  who receives it. A node's claim gets at most three credentials and none
  once its attempt starts; every App token issued appears in
  `GET /api/v1/team/git-credentials/releases`. The answer names the run's
  repository and the owner's extra repositories in `repositories`, the run's
  first.

To upgrade:

1. Upgrade every runner together with the controller. A runner older than
   this release asks the removed route and fails to fetch source straight
   from the controller; a runner using the operator's git cache is
   unaffected.
2. For a repository your team starts runs of by hand (`sparkwing run` against
   the controller, the API or the dashboard) rather than through the GitHub
   App, subscribe it under **Team > GitHub**, or store a git credential for
   github.com under **Team > Git credentials**. Until then those runs fail at
   the fetch with `no_source_credential` and a message naming the
   subscription. A public repository needs the same, since a runner presents
   no credential of its own.
3. If an owner listed extra repositories before their GitHub IDs were
   recorded, save the list again; such a list now answers 409 rather than
   minting by name.
4. Direct source-release consumers use the capability transactions described
   in [Source credentials use capability transactions](#source-credentials-use-capability-transactions).


## S3 and Postgres state profiles are removed

`sparkwing run` executes on this machine and records to this machine's
store. It no longer takes `--profile`, and a profile's `state:` is
`sqlite` or `controller`. Shared run history lives on a controller:
`sparkwing pipeline trigger <pipeline> --profile P` runs a pipeline there.
This ends the temporary exception
[v0.67.0](v0.67.0.md#sparkwing-run-takes---profile-only-for-s3-or-postgres-state)
kept for S3 and Postgres state profiles.

| Before | After |
|---|---|
| `sparkwing run build --profile shared` with an S3 or Postgres `state:` | `sparkwing pipeline trigger build --profile prod` to run on a controller, or `sparkwing run build` to run here |
| `state: {type: s3, ...}` or `state: {type: postgres, ...}` in a machine profile (`~/.config/sparkwing/config.yaml`) | Delete the `state:` key, or the profile, or replace it with a controller profile (`sparkwing profiles add --controller URL`). The config fails to load with `state.type: s3 is removed` (or `postgres`) and a pointer to this section. |
| The same in a project profile (`.sparkwing/sparkwing.yaml`) | A project profile declares all four surfaces, so deleting `state:` alone fails with `state surface is required`. Remove the profile, or set `state: {type: sqlite}` for this machine or `state: {type: controller}` for the profile's controller. Both errors name these choices. |
| `url_source: env:VAR` on any surface | Delete it; it named the Postgres state DSN. The config fails to load with `field url_source not found`; a Postgres `state:` that carries it fails on that key before its type is checked. |
| `mirror_local:` on a profile | Delete it. The config fails to load with `field mirror_local not found`. |
| `cache: {binaries: {...}}` | Delete the `binaries:` block. The config fails to load with `field binaries not found`. Runs fetch prebuilt binaries from the git cache `SPARKWING_GITCACHE_URL` names. |
| `sparkwing run X --sw-artifact-digest-backfill` | Delete the flag; it applied only to binaries fetched from a profile's cache. `run` refuses it as an unknown runner flag. |
| `sparkwing serve start --profile P [--no-local-store]` | `sparkwing dashboard start` for this machine's runs, or the controller's dashboard for shared ones. |
| `guards: {require: [profile:name=prod]}` | `profile:local`, `profile:controller` or an `arg:` token. A run never has a named profile, so the token never matched; a config naming it now fails to load. |
| `sparkwing runs list --profile shared` against an S3 or Postgres state profile | Nothing reads that history any more. The bucket or database is yours to keep or delete. |

- **Before:** `sparkwing run deploy --profile prod`.
- **Refusal:** that invocation fails before it
  compiles anything: `run: --profile is removed; sparkwing run executes on
  this machine against its own store, so run on the profile's controller
  with sparkwing pipeline trigger deploy --profile prod`. `--profile`
  before the verb fails the same way.
- **Hooks:** a hook installed before v0.67.0 with `--profile 'P'` on its
  `sparkwing run` lines fails until you reinstall it with `sparkwing
  pipeline hooks install`.
- **Mixed versions:** a pipeline binary built against this SDK refuses a
  run that an older CLI started with `--profile`, naming `sparkwing
  pipeline trigger`, rather than recording it locally. This CLI still
  drops an inherited `SPARKWING_PROFILE` and `SPARKWING_SECRETS_PROFILE`,
  so a pipeline binary from an older SDK cannot be pointed at a remote
  store either.
- **Controllers:** a controller on PostgreSQL is unchanged; only a laptop
  or CI run opening a shared database or bucket itself is removed. The
  `install/terraform/mode3-postgres` module now documents the controller's
  database alone.
- **Output:** a run no longer writes `runs/<id>/state.ndjson` into this
  machine's cache directory. `run_start` reports no `backends` or
  `profile` block.
  `sparkwing profiles show --profile NAME -o json` drops `mirror_local`. The
  controller's health no longer reports `object_store.stalled`.
- **Go packages:** `internal/storage/s3state` is removed. `internal/controller`
  drops `Loopback`, `LoopbackState`, `LoopbackConcurrency` and
  `NewLoopback`; a local run's node processes reach the full controller.
  `pkg/backends` drops `TypePostgres`, `Spec.Binaries` and
  `Surfaces.BinaryCache` and `Spec.URLSource` and adds
  `MigrationGuideURL`. It also drops `Spec`, `Spec.ResolvedToken` and the
  `Type*` constants; `MigrationGuideURL` is all that remains. `stdoutlogs.CheckSpec` drops its `urlSource`
  parameter.
  `storeurl.OpenStateStoreFromSpec` is removed.
  `pkg/localws` drops `Options.NoLocalStore`. `pkg/pipelines` drops
  `KindProfileName` and `GuardContext.ProfileName`.

## Pipelines run on the engine host

The engine now hosts the run. `sparkwing run` and a launcher Job start the
pipeline binary as `<binary> --sw-node-protocol`, plan in one process,
and run each node in a process of its own, as the
[node protocol](../node-protocol.md) describes. A `.sparkwing` module built
against an older SDK does not answer that protocol, so it no longer runs.
Nothing converts an old pin.

- **Steps:** move the pin to this release's SDK with
  `sparkwing repos update --in-place`, or `go get` the SDK version in
  `.sparkwing/go.mod`, then run the pipeline once. A module that replaces
  the SDK with a local checkout needs that checkout at this release.
- **Refusal:** an old binary that does not answer describe fails before the
  run starts. Rebuild `.sparkwing` against a matching SDK. A binary that speaks
  another protocol version names both versions and its SDK.
- **Plan code must be deterministic.** Every node process plans the run
  again, and a node whose plan entry differs from the one the run accepted
  fails with `plan_drift`. Read time, randomness and the environment in
  step bodies, not in `Plan`.
- **Run options are not environment variables.** `--sw-start-at`,
  `--sw-stop-at`, `--sw-only`, `--sw-no-cache` and the other run flags no
  longer reach pipeline code as `SPARKWING_*` variables. A dry run still
  sets `SPARKWING_DRY_RUN`. A node sees every other `SPARKWING_*` variable
  you set; the engine strips its own credentials and plumbing, such as
  `SPARKWING_SECRETS_KEY`, `SPARKWING_AGENT_TOKEN` and
  `SPARKWING_NODE_TOKEN`.
- **Limits:** a step's output is at most 16 MiB as JSON, a cleanup runs
  for at most 2 minutes, the SDK retries a `429` from a node route for at
  most 2 minutes, and `after_run` after a timeout runs for at most 2
  minutes. See [node protocol limits](../node-protocol.md#limits).
- **Inspection:** `sparkwing pipeline plan` evaluates no `skip_if`; a
  step with one reports `may_skip` (`user_skipif`) instead of
  `would_skip`, and plan no longer reports lint warnings or
  `--sw-dry-run` decisions. An unknown pipeline flag
  fails as invalid arguments instead of being dropped.
- **Plan document:** dependency caches are listed under `caches` instead
  of `dir_caches`, including in `sparkwing pipeline plan --pipeline PIPELINE -o json`.

## The pipeline binary answers only the node protocol

The compiled pipeline binary no longer runs, plans or inspects a pipeline by
itself; it answers `--sw-node-protocol` and exits 2 on anything else. A
script that called the binary directly moves to the CLI:

| Before | After |
|---|---|
| `<binary> <pipeline> [flags]` | `sparkwing run <pipeline> [flags]` |
| `<binary> <pipeline> --plan`, `sparkwing run <pipeline> --plan` | `sparkwing pipeline plan --pipeline <pipeline>` |
| `<binary> <pipeline> --explain`, `sparkwing run <pipeline> --explain` | `sparkwing pipeline plan --pipeline <pipeline>` |
| `<binary> <pipeline> config`, `sparkwing run <pipeline> config` | `sparkwing pipeline describe --pipeline <pipeline> --secrets` |
| `<binary> ops queue`, `ops doctor`, `ops version` | `sparkwing queue`, `sparkwing doctor`, `sparkwing version` |
| `<binary> ops stats`, `ops stats-reset` | none |
| `<binary> replay-node`, `sparkwing debug replay` | none; a hosted run records no dispatch snapshot |
| `<binary> handle-trigger`, `run-node`, `sparkwing handle-trigger` | none; the CLI and `sparkwing-runner` host runs |

`sparkwing pipeline describe --pipeline ci --secrets -o json` prints `{"pipeline": ..., "secrets":
[{"name": ..., "required": true}]}` (or `"optional": true`), read from
describe, instead of the per-field Go names and resolution notes the binary
printed.

## Cache grants are removed

The `swcg` protocol, `cache-grant-key` controller credential and
`POST /api/v1/runs/{id}/cache-grant` route are gone. Data upload, commit,
download and capability requests now use live controller capabilities.
Replace grant authentication and `{kind,key}` downloads with the requests in
[Data storage](../data-uploads.md) and [Data downloads](../api.md#data-downloads).

Byte-storage consumers use the `blob.Store`, `DataSigner` and `RecordStore`
contracts. Controller embedders configure their authenticated adapters with
`WithDataController`. Services advertise data transfers only when those adapters
are configured. There is no compatibility endpoint or cache-service fallback.
Existing output and job-log handlers keep their separate transfer setup until
their own cutover.

## Raw artifact plug-ins are removed

`storage.ArtifactStore`, `storage.ErrListNotSupported` and
`conformance.TestArtifactStore` are removed. The unused raw
`s3.ArtifactStore.Delete` method is also removed. Use the common signed-data
backend and authorized records. A digest or a raw object existence check does
not establish read access or an upload receipt.

Deployment usage reporting remains separate through `storage.UsageReporter`.
Its filesystem constructor is `fs.NewUsageReporter`.

## Storage uses record-backed maintenance

`Store.PrunableNodeLogs` and `Store.DeleteRunNodeLogs` are removed. Run and team
deletion use the controller APIs and the shared backend. Retention uses the
store's leased maintenance pass.

`Server.WithDataObjects` also installs the backend's verified manifest reader
for result attachment. Configure it before serving or sharing the store.

Remove calls to `WithStoragePass` and `RunStoragePass`, references to
`StoragePassEvery`, and readers of the `storage_pass` health field. No forwarding
API remains. Controller embedders supply the physical backend with
`WithDataObjects`. Leased retained-data maintenance marks unreachable blobs,
collects eligible tombstones and expires filesystem logs. Bucket listings no
longer replace tenant usage counts. Log lifetime follows retention classes.

## Storage standing uses retained inventory

Use `Store.ForTeam(ctx, team)` followed by `Tenant.StorageStanding(ctx)` for
the team's storage report. The snapshot returns retained event, blob and log
bytes plus live blob-upload reservation bytes. Historical usage counters remain
financial records and do not determine retained storage. Deleting a run releases
its retained event bytes without a repair call.

`Store.StorageStandingFor`, `Store.TeamStorage`, `TeamStorageUsage` and
`Store.ReconcileFreeEventBytes` are removed. The pre-launch schema also removes
the duplicate `free_slots.event_bytes` column.

`Store.ReleaseExpiredStorage` is removed. Final upload reservations expire
through quota admission and leased blob maintenance. An expired upload ticket
cannot commit, including a zero-byte ticket.

## Credit persistence uses explicit handles

Deployment billing settings now use `store.AsOperator()`. This includes
`CreditSettings`, `SetCreditSettings`,
`CreditRateTable` and `WarmCPUClassCores`. Their raw Store methods are removed.
The settings, validation and update rules are unchanged.

`Store.GrantCredits`, `Store.ListCreditCharges` and `Store.AppendEventOnce` are
removed. Select the intended team with `ForTeam` and call its Tenant method.
Local callers that mean the default team must select `store.DefaultTeam`.

## Console authorization

Account deletion, team deletion, GitHub App installation unbinding, orphan
reconciliation and credit-settings writes move under `/api/v1/operator`.
The former deployment-admin paths are removed. These actions require an
operator-console session and CSRF proof. Bearer tokens cannot perform them.
Console errors use the shared `error` object with `code`, `message`, and optional
`details`. Account-deletion conflicts place affected teams in `error.details.teams`.

Controller embedders must configure `Server.WithAuthorization` with the shared
verifier, binding checker, verified transport identity and session-bound CSRF
adapter before constructing the handler. Missing dependencies refuse requests.
The operator account list alone does not configure API authentication.

## Deployment authorization

Use `/api/v1/operator/limits` and `/api/v1/operator/signups` with a deployment
credential carrying `operator.limits`. Object-store administration uses
`/api/v1/operator/object-store` and its `/breaker`, `/reset-breaker`, `/settings`,
`/quotas/{team}` and `/quotas/{team}/allowance` routes, with
`operator.object_store`. An operator-console session may use both groups;
its mutations require CSRF proof. Quota selectors name the team.

`GET /api/v1/storage` returns team data. Deployment totals are on
`GET /api/v1/operator/object-store`. The former compute-limits, signups,
object-store and storage-settings paths are removed, along with
`GET /api/v1/admin/usage-metrics` and the repo-dispatch mutation route.

## Credential revocation

Use `DELETE /api/v1/tokens/{token}` for a team capability. The old
`DELETE /api/v1/team/tokens/{prefix}` path is removed. Deployment token
revocation moves to `DELETE /api/v1/operator/tokens/{token}` and requires
`operator.tokens` or an operator console session. The Go admin client's
`RevokeToken` uses the deployment path.

Member updates use `/api/v1/team/members/{account}`. PATCH accepts a JSON object
with `role` set to `reader`, `editor` or `owner`. DELETE removes the membership.
Browser mutations require CSRF proof. Last-owner refusals use the shared
`conflict` error with HTTP 409. Dead credentials use `unauthenticated` with
HTTP 401. The old token-state response fields and one-hour retry hint do not
apply to these routes.

### Member credential issuance

Replace member requests to `/api/v1/team/tokens` and the former global token
mint with `POST /api/v1/tokens`. Send `grants` and optional RFC3339
`expires_at`. Read `capability`, `prefix`, `team`, `grants` and optional
`expires_at`. The request no longer accepts principal, kind, scopes, TTL or
metering. A session provisions its own member root. A member bearer narrows
its authority through delegation. Save the returned capability privately and
do not automatically retry issuance.

## Root provisioning

Machine enrollment uses `POST /api/v1/runners` with `name`, `repos`, optional
`reserved_pools`, optional narrow `grants` and optional absolute `expires_at`.
It requires `team.admin`. `POST /api/v1/team/tokens` is removed. Deployment
service issuance uses `POST /api/v1/operator/tokens` with `name`, operational
`grants` and optional `expires_at`, under `operator.tokens` or console authority.
Both return `capability`, `prefix`, `team`, `grants` and optional RFC3339 expiry.
They return the bearer once, use no-store and require session CSRF for cookie
authentication. Do not automatically replay issuance.

Go callers use `client.NewRunnerTokenManager(...).EnrollRunner` for enrollment
and `client.NewTokenManager(..., true).CreateToken` with kind `service` for
deployment issuance. The legacy `Client.EnrollRunner` and `Client.CreateServiceToken`
methods are removed. `sparkwing runners add --runner build --reserved-for POOL` forwards the pool proposal
to the team-admin enrollment route. Repeated and comma-separated values are
preserved. Enrollment sends one request and never replays a lost bearer response.
The legacy `Client.CreateToken`, `ListTokens`, `LookupToken`, `RevokeToken`,
`RotateToken` and `ListSecrets` methods are removed. Use `NewTokenManager` and
`NewSecretManager` for typed management operations. The legacy enqueue API is removed. Submit a sealed `CreateRunRequest` through
`Controller.CreateRun`; the request retains its captured environment digest and
resolved intake options. Controller integrations retain the
existing `WithCredentialIssuer` and the store's origin-admission transaction.
Machine and service enrollment use persisted root registrations by default.
The store checks current authority, canonical registration selectors, repository
patterns, pools, active names and the existing machine limit before commit.
An active name returns `conflict`. The runner limit returns `quota_exceeded`.
Bootstrap and Actions provisioning use the store's native origin checks.
Console-origin persistence remains a separate integration.

## Launcher claims use the shared unit queue

Replace `Store.ClaimLaunch` with `Store.ClaimUnit`. A named plan selects
`UnitRef{Kind: capability.UnitPlan, RunID: runID}` without a job ID. A named
job selects `capability.UnitNode` and its job ID. Queue requests supply the
requested kinds. A deployment service capability carrying `claims.launch`
provides the launcher identity. Labels and platform remain observations.
Claim requests no longer carry CPU, memory or free-capacity fields.

`Client.ClaimLaunch`, `LaunchClaim`, `LaunchClaimRequest`, `MinLaunchLifetime`
and `LaunchDeadlineSlack` are removed. HTTP consumers call `Claim` on the controller returned by
`client.NewEngineClient` with `EngineOptions.Launcher`. Awards carry the persisted expiry and bootstrap
presentation. Exchange the bootstrap before using its execution authority.
The shared queue passes over unaffordable work without an in-process cursor.

`Tenant.SetRepoDispatch` is removed. Dispatch belongs to the admitted request.
The unused `Store.SeedToken`, `ErrSeededTokenRevoked`,
`Store.CheckClaimSensitive`, `Store.PrincipalHoldsProfileClaim`,
`Tenant.SpendSourceCredential`, `Tenant.SourceClaimant` and
`Tenant.GitCredentialMachines` APIs are removed too. Use the capability issuer,
current binding checks and `Tenant.ReleaseSourceCredential` for their retained
responsibilities.

## Manual retries use sealed intake

Use `Tenant.AdmitRun` with `Request.Lineage.RetryOf` and current retry authority.
The transaction retains the original sealed request and records the retrier in
lineage. The raw `Store.CreateRetryWithRun`, `Tenant.SetRetriedAs` and
`InheritSecretArgs` APIs are removed. Hosts use the retained source and secret
classification from the seal.

`Tenant.ReadNode`, `ReadSnapshot` and reconciliation expose `NodeRow.OutputRecord`
and `NodeRow.ManifestRecord` from the committed terminal result. Use these
selectors for materialized jobs, which may have no execution attempt. Authorize
record reads through the data contract before reading bytes.

Console-origin persistence must be provided before enabling console issuance.

## Account discovery authorization

The three account discovery reads under `/api/v1/me` require a live account
session and use the common error object. They no longer enter the legacy bearer
or renewing session middleware. GET requests do not switch the session's team.
Use the active-team action when changing membership. Cookie and Session-header
presentations use the same account policy.

Credit-settings reads use the member `runs.read` policy. Configure shared
route authorization before constructing an embedded controller's handler.

## Account deletion is console-only

`DELETE /api/v1/me` is removed with its `confirm_email` request and recent-sign-in
check. Use `DELETE /api/v1/operator/accounts/{account}` from an operator-console
session with CSRF proof. A member session or deployment bearer cannot delete an
account. The existing last-owner and financial refusals still apply.

## Account mutations use session policy

Active-team selection, team creation, invitation acceptance and identity
unlinking use the shared account-session policy. Cookie and Session-header
mutations require session-bound CSRF and same-origin proof. These calls no
longer renew the session. Bearer and local-peer authority cannot use them.
Errors use the common object, and closed invitations return `conflict`.

Embedded controllers supply `WithAccountWriter` with authority-bearing account
transactions. Each operation rechecks the selected session at commit. Team
changes commit account and session selection together. Unlinking retains the
recent-sign-in and last-method checks. Missing transactions return
`policy_missing`; raw account-store methods are not fallback adapters.

## OIDC claim authorization

OIDC minting uses `POST /api/v1/runs/{run}/oidc-token` with a live run-bound
`oidc.mint` capability. Passing the runner’s machine root or an account token
no longer works. Hosts use the awarded job capability. The audience request
and signed-token response retain their shapes. Errors use the common envelope.
Embedded controllers supply `WithOIDCReader` for authority-rechecked sealed
identity and an effective ancestry expiry. The legacy claimed-run lookup and
dispatched-node refusal are removed.

## Concurrency admission and queue views

Plan and job concurrency groups now have separate namespaces. A group named
`deploys` on the plan and on a job names two independent budgets. Definitions
need to agree only within their level. Persisted group keys now include `plan:`
or `job:`.

Box scope is available only on job groups. Pipeline acceptance refuses a
box-scoped plan group with an error naming the group and pointing at job-level
box scope. A run can span machines, so move that declaration from
`plan.Concurrency(group)` to the jobs that need a per-machine budget. Use global
or run scope for a whole-run gate.

If a run loses its plan reservation after releasing work, it waits to reacquire
the group. Released jobs finish, and no further work releases until capacity
returns. Reacquisition queues without a timeout, including when the original
policy was skip, fail or cancel-others. Queue views show
`waiting to reacquire <group>`.

Group waiter rows and responses drop the retired coalesce cache hash and leader
fields. Memo keys are no longer recognized as concurrency groups.

Read a group through `GET /api/v1/queue/groups/{group}`. Group waiter positions
are one-based.

The single-group `acquire`, `heartbeat`, `release`, `cancel-waiter`, `holder`,
`resolve` and `force-release` endpoints are removed, including their older
`/api/v1/concurrency/{key}` spellings. Execution hosts admit capacity through the
shared admission owner under their unit's award. The store reads accepted groups,
policies and lease bounds. Callers cannot choose them.

## Typed step reports

Send `StepReportRequest` to the job's `steps/start`, `steps/finish` or
`steps/skip` route. Include `step_id`, `state` and the positive `source_attempt`
captured when execution began. State `running` selects start, `skipped` selects
skip, and `passed`, `failed` or `cancelled` selects finish. The old step-finish
`status` field and structured `error` object are removed. `error` is masked
text. Optional `started_at` and `finished_at` preserve nanoseconds.

A successful write returns HTTP 200 with `{"recorded":true}`. Wait for that
acknowledgement before emitting the step diagnostic. A source ordinal that no
longer matches returns `lock_held`. Do not read the latest attempt to replace a
captured ordinal when retrying the report.


## Verified billing events and deployment bootstrap

Automated billing callbacks use `POST /webhooks/stripe`. The signature verifier
accepts checkout outcomes, `setup_intent.succeeded`, payment-intent success and
failure, individual refund creation/update/failure, early fraud warning
creation/update, and dispute creation/closure. It passes provider identities,
minor-unit amounts and statuses to settlement. Provider metadata never selects
a team, credit grant or card attempt. A paid charge's 3-D Secure result is read
from Stripe with the configured key and checked against the signed object; card
details are never kept. Test and live use the same
path with different keys.

The store must connect non-checkout outcomes to its committed billing records.
That settlement integration is still incomplete. The removed service routes
have no forwarding aliases. Keep provider retries enabled until settlement can
commit their outcomes.

Replace `swu_` bootstrap credentials with a `swk_` controller capability
presentation. `EnsureBootstrapAdminToken` takes a context, `BootstrapAdminIssuer`
and raw presentation. It forwards a
trusted `ProvisionSpec.Seed` to its once-only transaction. It no longer calls
`CreateTokenIfNoneExist` or accepts a caller clock. `Store.EnsureBootstrapCapability`
commits the seed through `Store.Issue` and closes the persisted deployment gate.
A live repeated seed is a no-op. A different seed or an ended seed is refused,
including after restart or credential retention. `Store.HasDeploymentAdministrator`
checks complete current ancestry for startup readiness. Bootstrap authority
is operational only. Sign in with an account capability or OAuth for dashboard
access.


`EnableAuthFromStore` always requires credentials. It no longer disables
authentication when the legacy token table is empty or unreadable. The
single-team anonymous bootstrap mode is removed. Local access uses the
controller's verified peer path. `--require-auth` retains a startup-readiness
check for a live deployment administrator capability. The store must implement
`HasDeploymentAdministrator(context.Context) (bool, error)` for that check.
A missing reader refuses startup when the flag is set. It never falls back to
`ListTokens`. Health reports authentication enabled before bootstrap completes.


## Schedule and review credential routes

Move `/api/v1/crons` and its descendants to `/api/v1/schedules`. Replace
`POST /crons/{id}/disarm` with `DELETE /schedules/{schedule}`. Repository
replacement and deletion remain `PUT` and `DELETE` beneath `/schedules/repos`.
The retired routes have no aliases. Use member `runs.read` for schedule reads
and `runs.control` for mutations. Deployment admin tokens and unbound runner
roots do not substitute for member authority.

Send one `Idempotency-Key` header when running a schedule, resolving an approval
or creating a debug pause. Schedule run-now returns its `replayed` result.
Approval resolution accepts only `approved` or `denied`, with optional `comment`;
the server derives the approver. Its response is an `approval` envelope with
`replayed`. Approval objects use `job_id`. Replace debug-pause `node_id` with
`job_id` and omit lifecycle/release fields. The raw approval-request route is
removed; execution owns gate creation.

Controller routes use the resolved tenant for the private `scheduleWriter` and
`runControlWriter` ports. Legacy schedule, approval and debug-pause writes are
not substitutes. The default refuses a missing adapter. Team invitation,
trust, GitHub App, billing and storage reads now require the shared capability
policy; API metrics requires `operator.limits` or console authority.

## Remaining team credential routes

Team rename/deletion, invitation writes, build trust, run deletion, GitHub App
configuration and billing writes use the shared capability policy. The legacy
token-table verifier, positive and negative caches, here/Actions route fences,
legacy tenant-route table and browser API middleware are removed. The Go
`Authenticator`, `NewAuthenticator`, `WithAuthenticator`, `EnableAuthFromStore`,
`AuthEnabled` and controller `ScopesForRole` APIs are removed. Constructing a
controller installs its policy boundary. Use `WithAuthorization` for an
embedded authority reader and binding checker. Use `store.ScopesForRole` for
role projection.

Embedded transaction adapters use `WithTeamWriter`, `WithGitHubAppWriter` and
`WithBillingWriter`. Missing adapters return `policy_missing`. A write must
recheck current authority and membership inside its committing transaction.
No former raw write is a fallback.

Invitation creation, billing checkout and card setup require an
`Idempotency-Key` header. Their JSON failures use the canonical error envelope.
Billing refusal fields live under `error.details`, with their business code in
`reason`. A completed billing-page replay returns the saved URL. The checkout
service must preserve its `key` across retries. Card setup also receives the
committed `setup_id`.

The account dashboard no longer calls the removed self-service account-deletion
route. Account deletion belongs to the deployment operator console.

## Browser connection and console transactions

The GitHub App browser flow uses the shared policy router and current account
session authority. Embedded controllers supply `WithGitHubConnectWriter` for
linked GitHub identity reads, one-use flow consumption and installation binding.
The old browser Principal lookup and standalone form middleware are removed.

Console writes require `WithConsoleWriter`. Raw credit, billing, hold, deletion,
waitlist, settings and installation methods are no longer HTTP fallbacks.
Missing transaction adapters return `policy_missing`. Waitlist approval requires
an `Idempotency-Key` header. Keep the same key when retrying an approval request.
Console clients read failures from `error.message` in the canonical envelope.

The unused token-table metering and HTTP billing-finalization helpers are removed.
Claim billing and settlement belong to the store's immutable award lifecycle.


## Final credential helpers and reporting writes

The controller `Principal`, `PrincipalFromContext`, `HasScope` and `Scope*`
exports are removed. Embedded handlers receive `capability.Authorized` from the
shared policy router. Team selection comes from that value. An absent credential
no longer selects the default team through a legacy helper.

Replace `/api/v1/runs/{id}/nodes/{nodeID}/metrics` and `/usage` with
`/api/v1/runs/{run}/jobs/{job}/metrics` and `/usage`. The Go client's metric and
usage methods use the final paths. Embedded controllers supply `WithMetricWriter`
for committing writes. A missing adapter returns `policy_missing`.

Deployment settings writes require `WithOperatorSettingsWriter`. Its transactions
recheck deployment authority, and a signup request commits its mode and limits
in one transaction. Raw settings methods are no longer HTTP write fallbacks.

Runner budgets use the verified team and holder after shared policy authorization.
Empty claims spend the polling budget. Awarded work does not. Empty polls are not
audited. Job path identifiers appear as `run_id` and `job_id` in audit records.
The unused live-log HTTP handlers and their `LiveLogRead` type are removed.
Dashboard log reads retain the shared policy routes and committed segment reader.
That reader joins its authorized segments to the in-memory ring.

Egress concurrency refusals use `error.code=rate_limited`, with
`egress_download_limit` or `egress_stream_limit` in `error.details.reason`.
The exported `EgressDownloadLimitCode` and `EgressStreamLimitCode` constants are
removed. Read the canonical error object instead of a top-level code.

## Store credential readers and local-first claims

Embedded callers replace `Store.LookupToken` and `LookupTokenByPrefix` with
`ReadAuthentication` or `LookupCapability`, passing the complete digest.
`ListTokens` and `RevokeToken` are removed. Use the tenant or operator capability
inventory and authority-bearing mutation methods. Authentication reads preserve
the credential's expiry.

`CreateHereTriggerWithRun`, `HereRunner` and its principal-prefix helpers are
removed. `AdmitRun` and `Issue` own here credentials. The initial acquisition
window remains `limits.ClaimWindow`. `ClaimedRunFor` and the unused raw claim
origin and agent-claim readers are removed. Secret reads use `ReadSecret`.

`RecordRunnerPoll(ctx, UnitDemand)` replaces `RecordAgentLabels`. The controller
stores observed capacity and labels with store time. Configured local-first
preferences, hold and liveness now apply to shared named and queued job claims.
Only a recent eligible runner with live authority and enough advertised capacity
can hold work. No new flag, deadline or customer limit is added.
## Egress accounting uses tenant and replica counters

`Store.RecordEgressUsage`, `Store.ListEgressUsage` and `Store.PruneEgressUsage`
are removed. Write additive bytes with `Tenant.AddUsage` using `egress_bytes`,
a UTC day and the controller's trusted replica ID. Read day or month totals with
`Tenant.EgressUsage`. Deployment reports use `Operator.EgressUsageAcrossTeams`.
Pruning uses `Operator.PruneEgressUsage` with the maintenance lease.

The controller records delivered successful-response bytes at request completion,
including partial transfers and reads served without an in-memory meter. Logs
consume egress while remaining exempt from download caps. Alarm reload uses team
totals. A failed counter write emits an error. A controller crash before request
completion can lose that request's egress sample. Thirteen months of reports stay
available. There is no principal-based persistence or periodic flush.

## Event charges use tenant handles

`Store.AppendEventCharged` is removed. Obtain a tenant with `Store.ForTeam`,
then call `Tenant.AppendEvent(ctx, runID, nodeID, kind, payload)`. The caller
no longer selects a charged principal. The tenant supplies the team and the
store owns time.

Event writes, retained run counters, shared storage quotas and replica usage
commit together. Pending uploads and events share monthly and run byte capacity.
Events consume no object capacity. Usage is recorded without a configured quota.
The existing event limits and free-tier policy keep their meaning. The HTTP
request and response shapes stay the same.

## Download charges use tenant handles and replica identity

`Store.ChargeDownload` is removed. Obtain a tenant with `Store.ForTeam`, then
call `Tenant.ChargeDownload`. `DownloadCharge.ReplicaID` identifies the trusted
controller replica. The request no longer accepts `Team` or `Now`. The store
selects the current UTC day under its shared quota lock.

Admission and replica usage commit together. Free and funded caps keep their
existing behavior, including finishing the download that crosses the cap and
recording already delivered bytes. Logs stay exempt. Controller integrations
use their existing lease-holder identity and require no new configuration.

## Paid credit requires verified checkout settlement

`POST /api/v1/credits/grants` is removed. `Tenant.GrantCredits` and
`Tenant.RecordCreditGrant` refuse `paid` grants.
Free operator grants keep their console path. `Tenant.RecordCreditGrant` accepts
only free grants. Payment reversals require a verified provider event or the
authorized console refund transaction. `ValidCreditGrantKind` and
`ErrReversalExceedsPayment` are removed with the raw reversal path.

`Tenant.AppendEventOnce` and the unused `Operator.DropCardAttempt` are removed.
State events commit with their transition. External event ingestion uses
`Tenant.AppendEvent`.

Queued refund callbacks use verified provider settlement, which selects the
persisted payment and exact refund object. `Operator.MarkCardRefundMade` applies
any matching outcome recorded before the provider response was bound. Callback
callers no longer supply refund-attempt keys.

Checkout integrations must supply C2's verified provider event to
`Store.SettlePayment`. The committed checkout supplies the team and credit
amount. The event must match its session, payment, amount, currency and test/live
mode. An actionable fraud warning holds the team and blocks the card. A
protected payment grants frozen credit without a refund; an unprotected payment
is refused and refunded. Replayed events grant once. The controller verifies
and settles signed provider events.

## Storage counters use verified upload tickets

The `/internal/storage/reserve`, `/internal/storage/commit`,
`/internal/storage/release` and `/internal/downloads/charge` endpoints are removed.
Remove cache `controller-token` credentials and service-counter integrations.
`Store.RenewStorage` and its rolling block receipts are removed.

The final storage adapter reserves quota with `Tenant.ReserveBlobUpload`, verifies
physical bytes, then commits with `Tenant.CommitBlobUpload`. Signing or transfer
failure uses `Tenant.AbortBlobUpload`. The ticket binds the exact uploader and
retains immutable replay state. The signed-data controller adapter remains an
integration dependency. There is no service-token fallback.

`Operator.ConfigureDeployment` writes the final settings table and refuses sealed values.
Its slot count and signup behavior keep their existing meaning.

## Storage maintenance uses committed inventory

Remove `Server.WithStoragePass` and `Server.RunStoragePass` calls. The controller's
existing maintenance timer runs leased retention, tagged-log sealing and storage
billing. An embedding that serves `Handler` directly calls `Server.MaintainStorage`.
`StoragePassEvery` is removed. `StorageMaintenanceInterval` names the timer period.

`Store.StorageMarks`, `ReconcileStorage`, `TeamsOverFreeLogShare`,
`PrunableNodeLogs`, `PruneStorageCommitReceipts` and `PruneDownloadDays` are removed.
Use `Tenant.StorageRetainedBytes` for retained inventory and `Tenant.StorageUsageFor`
for summed replica usage. `Operator.ChargeRetainedStorage` and `MaintainRetention`
require the current maintenance lease. Bucket listings no longer replace usage
counters. Log retention follows the run's persisted class. Allowance-driven
physical eviction still requires the final storage adapter.


## Fenced store metadata and approval transitions

`StateStoreOld` is removed. Hosts use the tenant-scoped ports in `internal/nodehost`,
implemented by `*store.Tenant`.
`Store.UpdatePlanSnapshot` and `Tenant.UpdatePlanSnapshot` are removed. Submit
plans through `AcceptPlan` and generated declarations through `AcceptExpansion`.
Neither accepted plan can be replaced through a raw snapshot writer.

`StartNodeStep`, `FinishNodeStep`, `SkipNodeStep`, `AppendNodeAnnotation`,
`AppendStepAnnotation`, `SetNodeSummary`, `SetStepSummary` and
`UpdateNodeActivity` are removed from Store and Tenant. Use `RecordStep`,
`WriteAnnotation` and `WriteSummary` with the current holder and attempt.
Step metadata requires an existing step. It does not create placeholder rows.
`ValidateStepTerminalStatus`, the old annotation bound constants and their
sentinel errors are removed. Shared limits and canonical API errors apply.
Closing an execution attempt closes its running steps in the same transaction.
A retry keeps the earlier attempt's steps separate.

`CreateApproval` and the raw store `ResolveApproval` are removed. Owners open
approval gates through `TransitionNode`. Authorized decisions and timeouts use
that transition too. A resolved gate cannot be reopened or overwritten.
Approval reads require an explicit tenant.

`ResolveClaimInput`, `ClaimInputRequest`, `ClaimInput`, `ClaimInputKind`,
`PlanPipelineRef`, `ClaimMemoKey`, `ClaimRepoSlug` and `ErrInputUndeclared` are
removed with their unused legacy decoder and memo-key builder. Current data
reads use the authorized record and read-set interfaces. `Tenant.OwnsRun`,
`ListPendingTriggersForParent` and `RunExceedsWallClock` are removed. Current
snapshots, child invocation relations and sealed limits supply those facts.


## Ordered placement and unused store APIs

`ClaimPlacement.Preferences` replaces `DefaultPrefers`, `Hold` and `Live`.
Supply an ordered slice of `PlacementPreference{Prefers, Hold}` values. An empty
`Prefers` step admits everyone. Each hold adds to the prior steps' holds from
the job's ready time. A later runner waits only while a live, eligible earlier
runner has an unexpired hold. The existing local-first controller flags still
configure two steps. An explicit job preference uses the first configured hold.
Runner liveness uses the accepted poll timestamp rather than other credential
activity. `RunnerPresence` is removed.

`Store` and `Tenant` lose `CreateDebugPause`, `ReleaseDebugPause` and
`FindSpawnedChildTriggerID`. `Tenant.GetRunAncestorPipelines`, `NodeSettlement`
and `NodeMetering` are removed too. Pause resume uses the authorized tenant
transaction. Child admission and lineage use sealed intake and the run snapshot.
Current awards retain immutable metering and settlement facts.

## Reporting transactions and schedule lease

The raw `Store` and `Tenant` methods `AddNodeMetricSample` and `AddNodeUsage`
are removed. Use the tenant's `WriteMetric` and `WriteUsage` with the current
`capability.Authorized` from the reporting route. The store rechecks ancestry,
tenant and reporting fences inside the write transaction. Use a current run-host
award to record process accounting after the job has finished.

`RunCronTickLeased` moves from `Store` to `Operator`. It uses `schedules.tick`
and the store clock. `Operator.ReleaseSingletonLease` releases an exact lease
fence. No legacy metadata table is needed for cron ticks.


## Sealed reporting and job completion

`Store.FinishNode`, `Store.FinishNodeWithReason` and
`Tenant.FinishNodeWithOutputRef` are removed. Submit a `NodeTransition` with the
current holder, generation and source attempt through `Tenant.TransitionNode`.
Exact reports replay. Changed terminal metadata is refused.

`GetRun`, `ListRuns`, `GetTrigger` and `ListTriggers` project their arguments
through `runspec.DisplayRequest` or `DisplaySpec`. Before describe supplies a
valid classification, every argument value is hidden. Execution and retry
read the private snapshot APIs. `Run.Number` exposes the allocated team number.
Repository filters read `repo_display`; text search also reads `repo_key`.
The trigger repo filter runs in SQL without a scan horizon. Unstarted runs
retain a zero `StartedAt` and participate in cursor paging.

`GetNode` and `ListNodes` read the current generation and node-wide attempt
ordinal, with attempt history from `node_attempts`. Output and manifest
selectors remain on the typed `ReadNode` and `ReadSnapshot` records.
Unused offer-priority fields, raw claim-token fields and `Node.ArtifactManifest`
are removed. The unused legacy claim-token type and kind constants are removed.

## Job creation, queue publication and sealed display

`Store.CreateNode`, `Store.MarkNodeReady` and `Tenant.MarkNodeReady` are removed.
Create jobs through `Tenant.AcceptPlan` or `Tenant.AcceptExpansion`. Publish
ready work with the current owner's `Tenant.TransitionNode`, `NodeReady` and
`CauseRelease`. Exact replay preserves the original queue timestamp.

Acceptance enforces the configured job-count ceiling in the same transaction
as the seal or expansion. Refusal leaves no partial jobs or accepted expansion.
Metered per-run ceilings use persisted award billing facts.

`Run.RedactedForDisplay`, `Run.SecretArgNames`, `RedactedRun`, `RedactedRuns`, `RedactInvocation`,
`RedactedArgValue` and `IncludeSecretValues` are removed from `internal/store`.
`Run` is a redacted reporting view. Display projection belongs to
`runspec.DisplayRequest` and `DisplaySpec`. Receipt builders consume that view
and omit input hashes when classification is unknown or a secret is supplied.

GitHub commit reports select the tenant's canonical numeric repository key
and exact SHA. Repository renames preserve the reports. A different repository
with the old name inherits none. The unused child-invocation table is removed
because sealed run intake already owns child lineage and request replay.


## Sealed intake rate guards and run control

`Tenant.AdmitRun` applies the configured hourly run guards before creating a run
and its owner. `max_runs_per_hour` is the single per-principal guard and includes
unmetered member submissions within their tenant. Repeated sealed intake requests
return their first admission without spending another slot. The unused
`Tenant.OldestWaitingReadyNodeForPrincipal` lookup is removed.

The canonical debug-pause and approval routes now use Tenant's run-control
transactions. Both require an Idempotency-Key and current member authority.
Reusing a key with a changed body returns conflict. Public text masks classified
arguments and masked team secrets before state, events and the private replay
receipt commit together. Approval history records the authenticated account
subject. Pauses and decisions cannot be created for terminal or cancelling work.

## Shared lease fences and sealed credential writes

Ordinary claim, renewal, release, reaper, secret, source and metric transactions
share the controller-owner fence with PostgreSQL `FOR KEY SHARE`. Acquiring,
renewing and releasing singleton leases take `FOR UPDATE`. Lease checks read
database time after lock waits. A database clock read failure refuses commit.
Reaping commits each target independently, continues after a target fails, and
returns committed recoveries alongside the joined errors. Run admission refuses
commit unless exactly one tenant run-number counter advances.

`Tenant.WriteSecret` takes `SealedSecret`. `Tenant.WriteGitCredential` takes
`SealedGitCredential`. Construct them with `store.SealSecret` or
`store.SealGitCredential`, the bound cipher, tenant and row. Their row accessors
return copies. Zero values and values sealed for another tenant are refused.
The credential writer still rechecks current administration inside its transaction.

The raw Store and Tenant `CreateToken` and `CreateTokenWith` methods are removed,
as are `FinalizeNodeCredits`, `TokenMetered`, their mutable node/trigger credit
columns, and `storage_reservations` and `storage_commit_receipts`. Capability
provisioning and immutable billing awards own issuance, charging and refunds.
Compute usage counts live metered node awards. Unused raw token identity, scope
and lease-limit declarations are removed. Controller fixtures issue current
capabilities, and the browser fixture uses the bootstrap-capability gate.

## Cloud billing uses standing

Standing replaces the team and shared-card dollar caps in the
[Cloud billing model](../billing.md). That page contains the standing and
launch-signup policy table.

`GET /api/v1/team/billing` reports `standing`, `cloud_vcpu` and
`max_job_seconds` inside `limits`. New accounts receive no automatic grant.

The billing trust action accepts `trust` and `reason`. Granting standing selects
High. Revocation selects Held. Automatic standing uses payment history and
prepaid balance. Active payment holds take precedence. Removed fields are
`limit_cents`, `daily_cap_cents`, `rung_cents` and `runner_cap` on that action.
The corresponding override fields and purchase-window totals are removed from
operator and team billing responses. Team billing also removes
`balance_cap_micro`, `purchase_max_cents`, `purchase_limit_cents` and
`purchased_30d_cents`. Its limits remove `ceiling_cents`, `daily_cap_cents` and
`credit_limit_cents`. The dashboard consumes standing.

The final rewrite schema removes dollar-cap columns and shared-card spend buckets. Existing
awards retain immutable pricing and their captured deadline. New Cloud awards
serialize capacity admission with reservation and issuance. The signup gate serializes launch
admission and leaves operator approval available beyond the launch cap.

The operator signup status includes `account_limit` and `counts.admitted` and
reports `launch_capacity` while automatic launch admission is full. Existing
accounts still sign in. Explicit operator approval remains available.

Launcher queue selection passes over teams whose standing capacity is full and
jobs larger than that standing permits. Named oversized claims still refuse
with the standing named. A lost payment dispute retains the team's hold until
an operator releases it. Winning a dispute releases that dispute's hold.

Billing page routes now use tenant transactions through the production controller.
Checkout and card setup preserve the caller's idempotency key across
provider failures. The checkout service returns its hosted-session `id` and
`url`, plus `customer` and `setup_intent` for card setup. These facts bind
verified callbacks to the reserved payer.

The raw tenant methods `OpenCreditCheckout`, `AttachCreditCheckout`,
`DropCreditCheckout`, `SetSpendBudget` and `StartRecoveryAttempt` are removed.
Embedded callers use `PrepareAuthorizedBillingPage`,
`CompleteAuthorizedBillingPage` and `RecordAuthorizedBillingPageFailure` with
current team authority. An interrupted provider
request retains its reservation because delivery may have succeeded.
Controller startup persists `--payment-mode` through the operator billing owner,
so the verifier and new reservations use the same mode without a separate
settings-row seed.

The legacy claim-input operation is removed with its retired input-resolution
API. Executors use committed result records through `/data`, and local node
credential checks use the own-job data-authority route. Job and child output
reads return verified JSON values through the shared capability policy.

## Explicit Cloud overflow

`sparkwing run` defaults to local execution. User `run.routing`, repository
`defaults.run` and pipeline `run` settings can select Cloud ownership with ranked
claimants and hold times. Cloud routing hosts the run in the invoking CLI and
requires an attached session. Unreachable controllers fall back before intake.

The intake wire adds `request.burst`, `request.local_secret_names` and a private
`local_secrets` map outside the seal. Here claims add observed OS, architecture,
capacity and availability. Renewal may refresh here availability. Personal
runner enrollment adds `personal:true`. `GET` and `PUT /api/v1/overflow` manage
the authenticated account's monthly allowance. The store gains account
subject-kind provenance and account overflow controls.

Personal enrollment uses `sparkwing runners add --runner NAME --personal
--session-stdin --profile PROFILE`. Supply an existing independent account
session on stdin. A member token cannot create independent machine authority.
The session and CSRF proof stay in memory. Failed installation revokes the new
runner credential before returning an error.

## Cloud Helm images and launcher

Helm no longer falls back to `appVersion` for empty image tags. Set repository
and tag for every enabled controller, runner or launcher image. The Cloud Job
runner and DinD images also require `sha256:` digests.

Use [the Cloud values example](../../charts/sparkwing-full/examples/cloud.yaml)
with `sparkwing-full`. It enables the launcher and disables the pool runner.
Provide its separate `launcher.tokenSecret`, RDS `databaseSecret`, bootstrap,
encryption and file-named credentials Secrets before startup. See the
[chart README](../../charts/sparkwing-full/README.md) for SSM mappings and the
team-node, admission API and network prerequisites. Existing pool deployments
keep `runner.enabled=true` and explicitly pin their runner image.


## Sealed controller schedule intake

`Tenant.CreateTrigger`, `CreateTriggerWithRun` and `FindTriggerByIdempotencyKey`
are removed. Controller schedules use `Tenant.AdmitScheduledRun`, which checks
the persisted schedule and current consent inside the shared intake transaction.
The duplicate-trigger sentinel errors, `controller.CronIdempotencyKey` and
`store.WithCreatingPrincipal` are removed. Ordinary run intake derives its
principal from live authority. The sealed request's authority ID identifies a
scheduled run's cadence without the legacy trigger environment column.

## Maximum card payment

Add funds payments and automatic refills now have a $1,000 maximum at every
standing tier, including High. The earlier $5,000 automatic refill bound is
removed. Reduce any larger saved refill amount before it can run again.
Use the billing page contact request, or `sparkwing billing request-tier --profile cloud --tier high --message <text>`, to ask about larger payments or higher limits. Requests
from High-standing teams also reach the operator queue. A standing grant does
not raise the maximum per payment. `GET /api/v1/team/billing` reports
`purchase_max_cents` alongside the purchase minimum. An older saved refill above
the maximum stops with `refill.limit=payment_size`.

## Sealed run provenance

Intake seals every run with the identity it speaks for: a trust class
(`verified-default`, `verified`, `actions`, `schedule` or `asserted`), the ref,
commit, event and pipeline, and GitHub's repository ID for every class except
`asserted`. OIDC claims, data and cache scopes, pool access rules and
pipeline-scoped secret release read only that value. Check each of these before
upgrading:

- **Trust policies that accept `trigger:cron`.** A schedule mints
  `trigger:cron`, with `repository_id`, `refs/heads/<default>` and the branch
  head, only when a team admin armed it (App automation consent, or a member
  holding `team.admin` when it fires), it follows the default branch GitHub
  reports through the App at fire time, it has no locked commit and no override
  of arguments, cadence, timezone, overlap or catch-up, and the App resolves its
  commit at fire time. Schedules an editor armed, locked, overridden or
  non-default-branch schedules and "run now" mint `trigger:manual` with no
  `repository_id`, `ref` or `sha`. Name the repository in `cron` policies, and
  have an admin re-arm any deployment schedule an editor armed.
- **Trust policies that read a manual run's `repository_id`, `ref`, `sha` or
  `repository`.** CLI, API and dashboard runs, the schedules above, children not
  at their parent's exact revision and runs demoted by a `source:` pin omit all
  four, so their subject reads `repository_id::` and ends in `ref:`.
- **Trust policies for GitHub Actions-bound runs.** These keep
  `trigger:manual` with the push's ref and sha, and now carry its
  `repository_id` and `repository`. Policies matching `repository_id::` for them
  must name the repository ID.
- **Child runs.** A job may start only the child pipelines its plan declares,
  with Go `JobNode.StartsChildren("deploy", ...)` or TypeScript
  `job.startsChildren("deploy")`. For another repository, use Go
  `StartsChildrenIn("gh:123", "deploy")` or TypeScript
  `startsChildrenIn("gh:123", "deploy")`. Both methods append declarations.
  The repository argument is its canonical key, such as `gh:123` or
  `git:gitlab.com/acme/repo`. An undeclared child answers `403`. A child at
  its parent's exact revision inherits the parent's identity, including its
  `pipeline` claim and pipeline-scoped secrets. Any other child is asserted
  under its own pipeline name and reads only team-wide shared secrets, like a
  GitHub reusable workflow called without `secrets: inherit`. Pairing another
  ref or code commit with an explicit commit in the inherited repository
  answers `403`. Award issuance seals declared pipeline/repository pairs the
  team is authorized to run, separately from the claim's own source and data
  scope. Team repository registration or live App installation coverage
  authorizes an explicit repository. A child in another repository always
  runs as asserted. Children nest at most 10 levels deep counting the root and one
  root uses the team-set child ceiling (1 to 1000, default 50, retries excluded). Team admins
  change it with `team settings set --profile work --max-children-per-root 200`
  or in Team settings; changes are audited. Past either bound intake
  answers `409` with `limit` `max_child_depth` or `max_children_per_root`. A
  child spends the admission budget recorded on its parent. App and schedule
  roots use the team bucket for flood and hourly limits. A retry charges the retrier and its
  descendants inherit that charge, while its source provenance stays unchanged.
- **Release subscriptions.** `release_published` and `release_prereleased`
  require the new `release_tags` patterns on
  `PUT /api/v1/team/github-app/triggers`; a release subscription without them
  answers `400`. `tags` now selects tag pushes only. Add `release_tags` such as
  `["v*"]` and protect those tags in GitHub.
- **Closed pull request subscriptions.** `pull_request_closed` runs only a
  merged pull request, at its merge commit, as `refs/heads/<base>` with
  `trigger:pull_request`. A pull request closed without merging starts nothing.
- **`source:` branch or tag pins.** A pin that is not a full commit ID, unless
  it resolves to the execution commit, demotes App, schedule and Actions runs
  to asserted: no OIDC `repository_id`, `ref` or `sha`, no branch-scoped pool
  and a cache scope of their own. The run still receives its pipeline's scoped
  secrets. Pin a full commit ID, or protect the pinned branch. See
  [secrets](../secrets.md).
- **Shared manual caches.** Every run writes only its own scope: records an
  unverified run writes land in `manual:<writer>:<ref>`, keyed by the submitting
  principal, the member who armed a schedule, an admin-armed schedule, the parent
  or demoted run, or `actions`. Manual runs by different members no longer share
  memo, dependency-cache or output entries. Every run reads verified records of
  the default branch, and a verified pull request also its base. Selectors that
  named `manual:<ref>` must name the writer. See
  [caching](../caching.md#who-shares-a-cache).
- **Pool rules.** Reserved pools are granted only to verified App runs, with
  the children and retries that inherit them, and to fires of admin-armed
  default-branch schedules. A typed repository grants nothing, a rule with
  source `github` requires a run a signed App delivery started, and a
  `--branch` rule, normalized and matched as a glob, matches only a verified
  App run on that branch or an admin-armed schedule fire when it names the
  default branch. Branch rules accept only the `*` and `?` wildcards. Rules are
  read at each use, so removing one stops placement, reserved secrets and OIDC
  for runs already admitted, retries included.
- **Extra repositories.** Owner-approved extra repositories join the source
  token only for a verified push to the default branch. Other runs fetch only
  their own repository.

A run admitted before the upgrade has no sealed provenance, so its OIDC mints,
data grants, children and pipeline-scoped secret reads are refused. A retry
copies that missing identity, so let in-flight runs finish before upgrading and
start any that fail this way as new runs.

## The controller declares its deployment

`sparkwing-controller` no longer works out what kind of deployment it is from
which credential files exist. Declare it:

- `--deployment=local|team|cloud` (default `local`). A controller with a
  multi-team license must pass `--deployment=team`; the hosted service passes
  `cloud`. `team` and `cloud` refuse to start without a license that verifies
  and grants multi-team. A `cloud` or `cloud-free` limits profile needs
  `--deployment=cloud`, which also needs `--external-url`.
- `--store=sqlite|postgres` (default `sqlite`). A controller with a `pg-url`
  credential must pass `--store=postgres`; `cloud` requires it.

A license that expires while the controller runs no longer turns metering, the
free tier and sign-in off. Health reports `degraded` with a `license: expired`
problem, and the next start refuses until the license is renewed.

The credentials directory now refuses any file whose name the controller does
not read, so a Secret key such as `pg_url` stops startup instead of leaving the
controller on an empty SQLite store. Move unrelated keys out of the Secret named
by `controller.credentialsSecret`. A `.previous` or `.published` key without its
current key, including `github-app-webhook-secret.previous` on its own, stops
startup.

Startup binds every listener before it opens the store, and writes the
bootstrap seed and `--payment-mode` only after every other check passes.
Controllers sharing a store must declare the same payment mode. `--external-url` follows the runners' rule:
`https`, or `http` only on loopback or a Kubernetes Service. Without
`--external-url`, the GitHub runner binding routes answer
`capability_unsupported` rather than a workflow built from the request's host.

`sparkwing-controller admin` drops `--credentials-dir`. It opens the store the
serving controller recorded in `$SPARKWING_HOME/deployment.json`, checks that
it is the same store, and refuses otherwise, instead of creating a SQLite store.

Runners and the launcher refuse a plain `http://` controller URL unless its host
is loopback or a Kubernetes Service (`*.svc`, `*.svc.cluster.local`); use
`https://`.

The `sparkwing-full` chart (0.3.0) adds `controller.deployment` and
`controller.store`, validates mode values against `values.schema.json`, and
always passes `--require-auth`. `controller.requireAuth` and
`controller.allowOpenBootstrap` are removed: unset them. A bootstrap seed left
mounted after its handoff now only logs a warning, so you can remove it from
the Secret whenever convenient. Charts that set
`controller.databaseSecret.name` must also set `controller.store=postgres`.

### Single active controller

PostgreSQL controllers take a dedicated session advisory lock before migrations
or startup writes. A second controller waits as a standby. Loss of the lock or
its connection causes an immediate non-zero exit without draining. Promotion
bumps a `controller_epoch` row in the lock transaction. Every write transaction
locks and checks that epoch, so promotion waits for in-flight writes and the
former controller's later writes abort and fail-stop. SQLite controllers and
wingd share the home's `wingd/d.lock` OS file lock. A second owner fails fast,
while admin remains available. Webhook ordering and bucket write guards use the active
process's state, so cross-controller ordering and storage-state propagation do
not apply. `--payment-mode` remains a matching shared setting.

Break-glass admin checks the actual table schema and transport endpoint,
including Unix sockets, and keeps verification and writes on that connection.
Schema-validation and connection-configuration errors omit credentials. An empty `--addr` refuses startup before opening the store.

## Controller and store packages are internal

The controller, its client and cipher tests, the store and its test helpers,
and storage backends are implementation packages under `internal/`.
Their former public import paths are removed without aliases.

Before, external Go code could import controller or store implementation
packages under `pkg/`. After, external callers use the documented HTTP API,
the `sparkwing` author SDK, or public wire contracts such as `pkg/wingwire`.
Go prevents external imports of the internal implementation packages.

Within Sparkwing, `NodeRef` and the credential DTOs `CreateTokenRequest`,
`CreateServiceTokenRequest`, `EnrollRunnerRequest`, `CreatedToken` and
`RotatedToken` live in `internal/controllerwire`.
`RunSnapshot` remains a private host read model in `internal/store`.
Routes, JSON field names and runtime behavior are unchanged.

## Run deletion and audit retention

Editors may delete only runs they submitted. Owners may delete any run in their
team. Deletion retains a tombstone and the run's secret and source credential
release history in the append-only audit log. Team and account deletion also
retain audit facts. Every deletion and its authorization retain separate facts
even when a later run reuses the deleted run's ID. SQLite audit tables exclude
the implicit rowid so replacement cannot bypass their guarded primary key.

The store uses SQLite STRICT tables and stronger relational and row
constraints on both dialects.

Audit entries retain account IDs and invitation IDs rather than personal details.
Each audit kind accepts a closed payload of identifiers, enums, digests and
generated receipt IDs. Caller keys and names use digests when retained. Free-form
reasons and messages are excluded. The database stamps each row's recording time
for retention. Caller occurrence times remain separate and cannot shorten it.
Deleted accounts render as "deleted account". Operator storage settings expose
`audit_retention_days` (default 400 days, zero restores the default). The controller
prunes older audit rows on its existing maintenance loop. The guards enforce
append-only against application SQL. Database credentials can disable them.
The team's first successful run remains a durable fact after its audit row
expires, so retention cannot label a later success as the first.

Operator refund replay receipts persist with the financial state independently
of audit retention. Replaying a key returns the original result, including its
balances and `created` value. Changed requests under that key still conflict
after audit expiry. Refund audits retain identifiers, digests and financial
facts, with no free-form reason.

Step lifecycle timestamps now use the store clock. Runner start and finish
timestamps remain separate observations with nanosecond precision. Clock skew
cannot prevent attempt recovery from closing running steps.

## Bounded cancellation and derived completion

Cancel requests give active work at most five minutes to drain. A holder that
keeps heartbeating cannot extend that deadline. Cloud funding refusal still has
its shorter deadline of at most one prepaid slice.

`POST /api/v1/runs/{run}/finish` drops `outcome`. Send only `reason` and an optional
masked `error`. The controller derives the verdict from accepted plan and job
results. A run without an accepted plan finishes failed, or cancelled when
cancellation was requested.

The internal node API replaces `NodeTransition` with named commands. Store
mutation methods no longer accept caller wall-clock arguments. The store captures
database time for transaction decisions. The store keeps stored,
generated cancellation deadlines.

## Durable start admission

The store keeps durable admission records. Owners propose releases. Hosts use the private `may-start` operation
under the actual job award, including jobs without concurrency groups. The private
`capacity/acquire` operation is removed. A wait grants no partial group set.
Released jobs continue when the plan waits to reacquire capacity. Burst returns
its provisional placement and job reservations when compute is unavailable.

Approval predicates use fenced evaluation permission without job groups or compute.
A true predicate records skip. Memo lookup preserves authority refusals, and only
committed producer success makes a memo reusable. `cancel_others` replaces logical
capacity immediately and requests cooperative cancellation. Cleanup or the
five-minute drain deadline bounds disconnected holders.

## Host-provided masking

Run pipeline code through a Sparkwing host to get masked logs and diagnostics.
The SDKs no longer mask standalone execution. Continue calling `Mask` before
logging author-generated credentials.

Short secrets now remain secrets. See [secret values](../secrets.md#values-that-can-be-secrets)
for the masking length rule and save-time warning. Secret writes return
HTTP 200 with metadata and an optional `warning` field.
Host argument registration
now includes raw values, defaults,
effective values and every normalized list item. Planning and generator output
stays buffered until declarations are known. Failed discovery withholds it.
The describe cache is rebuilt with the safe presentation format.

The store keeps the private registration projection with each accepted
expansion and binds it to the expansion digest. Successful expansion submissions must explicitly
supply version 1 registration, even when empty. An accepted expansion without
its registration is refused rather than resumed with incomplete masking. Runs sealed without version 1
host masking registrations cannot resume. Start a new run. This is a pre-launch
format cut with no migration. Previously stored short secrets remain usable.

## Ranking uses available claimants

For a ready job with no named runner, the controller holds placement for the
best-ranked tier with a live claimant on the same team that matches the job's
labels and has a free slot in its latest poll. Busy or offline better tiers
cause no wait. The hold ends after 5 seconds, when any eligible claimant may
claim the job. The former recorded-ask winner and extra 2-second grace period
are removed. Explicit `Prefers` holds remain; preference scores are removed.
Runner claim clients can send `free_slots`; omission offers one slot, while
an explicit zero reports that the claimant is busy. Each award consumes one
advertised slot until the next poll.

## Memo inputs run on the host

Existing memo entries are invalidated. Jobs execute once to populate the new
namespace. No cache migration is needed.

Go `inputs.Files`, `RepoFiles`, `Tree`, `Env`, `Const`, `Parts` and `Compose`, and
TypeScript `files`, `repoFiles`, `tree`, `env`, `parts`, `constant` and `compose`, return
input recipes. Pass them directly to `Memo` / `memo` instead of invoking them
as callbacks. Recipes travel in the plan and run on the executing host after
admission and SkipIf, before the job body.

`MemoKey` / `memoKey` collect ordered JSON values. Author callbacks can still
resolve references or other inputs and return those values for host hashing.
`NoMemo` / `noMemo` still bypass reuse. Callback errors and empty strings fail.
`Compose` / `compose` combine recipes. Combine author values inside a callback
with `MemoKey` / `memoKey`.

Tree roots must be relative paths inside the checkout. Absolute and escaping
roots are refused. Symlink directory components and replacements cannot redirect
fingerprinting outside that scope. Empty `Const` / `constant` values fail host
validation. TypeScript recipe literals must be JSON values. Nonfinite numbers and
`undefined` are refused, including nested values.

Environment recipes read the effective job environment, including body and
startup declarations. Names compare case-insensitively on Windows. Unset remains
distinct from empty. Filesystem errors are host errors. Go and TypeScript now
use the same path rule: basenames match anywhere, trailing slashes match a
literal prefix from the checkout root, and `**` spans path segments. TypeScript
`ignore("cache/")` therefore keeps `nested/cache/data`. See [Caching](../caching.md).
