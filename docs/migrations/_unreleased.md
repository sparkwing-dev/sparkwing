# Migrating to the next release

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

## Rate-table presence uses the settings snapshot

`Store.CreditRateTableSet` is removed. Read `CreditSettings.RateTableSet` so the
presence flag and prices come from the same settings snapshot.

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
collection after the 24-hour grace. Rebuild pre-launch stores from the final
schema. There is no old-row migration or compatibility adapter.

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
  `<cache prefix>/logs/r90/` after 90 days and `<cache prefix>/logs/r30/`
  after 30, plus `<cache prefix>/logs/r<days>/` after `<days>` for each
  team `--log-retention team=days` names; [self-hosting.md](../self-hosting.md#node-log-retention)
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
  `controller.credentialsSecret` key); nothing reads it. The storage pass
  counts and prunes logs from the segments, and deleting a team deletes its
  segments.
- **Runners and agents:** drop `--logs` from `sparkwing-runner runner` and
  `launch`, `--trigger-runner-logs-url` from `runner`, and the `logs:` key
  from an agent's `config.yaml`, which now fails to load with
  `field logs not found`. `sparkwing cluster runners add` has no `--logs`.
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
- **Laptop:** `sparkwing serve` runs no logs service and writes only
  `SPARKWING_CONTROLLER_URL` to `dev.env`; the dashboard reads node logs
  from the run directories, and `sparkwing runs prune` removes them through
  the dashboard.
- **Go packages:** `pkg/logs` and `pkg/storage/sparkwinglogs` are removed.
  Read node logs with `client.NodeLogs` from
  `pkg/controller/client`, and their completeness with `pkg/storage/logseg`.
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
  removed. `POST /api/v1/credits/grants` records `paid` grants only and
  `POST /api/v1/credits/freezes` holds by `payment_id` and `dispute_id`
  only; the `reverses`, `team` and `release` fields are gone and are
  refused as unknown. Every `credits.grant` route answers `403` to an
  admin token. The console's trust action sets the daily cap, card rung and
  Cloud node cap alongside the purchase limit, and its unfreeze releases a
  payment dispute's hold named by `hold`. The new
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
  | `reversal` grant, or a refund on `POST /api/v1/credits/reversals` | refund in the Stripe dashboard; the checkout service reverses it |
  | hold naming `team` on `POST /api/v1/credits/freezes` | `POST /api/v1/operator/teams/{team}/freeze` |
  | `release` on `POST /api/v1/credits/freezes` | `POST /api/v1/operator/teams/{team}/unfreeze`, with `hold` for one dispute's hold |

  A script that used the admin token for any row above now gets `404` or
  `403`.
- **The checkout service's token:** `POST /api/v1/tokens` and
  `POST /api/v1/tokens/{prefix}/rotate` refuse `credits.grant` with `403`,
  and its routes answer only a service token, never an anonymous caller.
  `POST /api/v1/tokens` trims each scope and refuses any that is not
  exactly a known scope, so a padded spelling is checked as the plain one.
  The token the checkout service already holds keeps working. Before the
  next rotation, put its value in the controller's `credits-token`
  credential so the controller adopts it; to rotate, write a new
  `sws_` value there and in the checkout service, restart, and revoke the
  old prefix. A value whose token was revoked is never seeded again; the
  controller logs an error at start until the file holds a new value.
- **Go:** pass the actor, the reason and the one hold's id to
  `Store.ReleaseCreditFreezes`, which refuses an empty id; a
  caller of the removed `Store.DisputeTeam` reads the team from the hold it
  holds, or from `Tenant.CreditFreeze`.

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
  a request with no credential. Schema v96 drops the users table and ends
  every password session, so each person signs in again. A token session
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
   already has one. Otherwise mint one while a password admin can still act:

   ```bash
   sparkwing cluster tokens create --type user --principal operator \
       --scope admin --profile prod
   ```

3. Remove `sparkwing cluster users` from scripts. A script that used
   `POST /api/v1/auth/login` for a session calls the API with a bearer token
   instead.

**Edge cases:**

- A password user with a narrower scope set has no direct replacement: an
  operator token carries `admin`. Give that person an account on a licensed
  controller with the matching role, or a bearer token of the narrower scopes
  for the CLI.
- An open controller (empty tokens table) serves the dashboard without
  sign-in, as before, and its `/login` redirects to the page asked for.
- Rollback: an older controller or CLI refuses the new store. Roll back by
  restoring a backup taken before the upgrade.

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
  and git-credential routes for both kinds. Schema v97 makes an agent name
  unique across live runner and machine tokens; it needs no operator action.

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
- Operator tokens (`POST /api/v1/tokens`, `sparkwing cluster tokens`), the
  bootstrap token and GitHub Actions exchange credentials are unchanged. The dashboard's **Team > Machines** page uses the new routes
  and works as before.

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
  refuses the offer fields as unknown. `sparkwing fleet`, `--sw-fleet` and
  `sparkwing cluster agents enroll` are gone. Any runner or machine token
  with `nodes.claim` claims through `POST /api/v1/nodes/claim`, including one
  that was enrolled. `GET /api/v1/agents` and `sparkwing cluster agents list`
  show the runners that claimed in the last hour with the labels, slot limit
  and headroom they advertised; `active_slots`, `base_priority`,
  `priority_ceiling`, `budget` and the `gateway` type are gone. The store
  has no executor or offer tables, and an older controller refuses it.

To upgrade:

1. Delete the `fleet:` section from `config.yaml`. Until it is gone every
   command that reads the file stops with
   `unknown section "fleet"`.
2. For each machine that was an enrolled helper, run it as an ordinary
   runner: `sparkwing cluster runners add --profile PROFILE --name MACHINE`
   on it mints its runner token and starts the service, or deploy the runner
   bundle in Kubernetes. Give it the capabilities its enrollment listed with
   `--labels`, and its `max_concurrent` with `--max-concurrent`. Revoke the
   token the enrollment named once nothing uses it.
3. Run pipelines without `--sw-fleet` or `--sw-allow-secret-file`; the
   latter only fed the fleet's source snapshot, and
   `sparkwing pipeline trigger --working-tree --allow-secret-file` keeps it. A foreground `sparkwing run` executes
   on the machine you run it on; to spread a run across machines, trigger it
   on a controller with runners attached.
4. Code embedding `pkg/controller/client` drops `PrepareExecutorClaim`,
   `OfferExecutorClaim` and `HeartbeatExecutor`, and calls `ClaimNode` where
   it called `ClaimNodeAs`. Code embedding `pkg/store` renames
   `Tenant.ListLegacyAgentClaims` to `ListAgentClaims`, `LegacyAgentClaim` to
   `AgentClaim`, `FinalizeExecutorClaimRound` to `FinalizeClaimRound` and
   `ExecutorClaimRoundResult` to `ClaimRoundResult`, and drops the executor,
   offer and scheduling-summary methods.

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
  admits only its run's routes. Schema v99 adds the `trigger_claim_tokens`
  table; it needs no operator action.

To upgrade:

1. Upgrade the controller and every runner together: the runner bundle, the
   `sparkwing-runner runner` service, GitHub Actions runners and
   `sparkwing cluster worker`. A runner from this release gets no claim token
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
  `400`, and `sparkwing cluster runners add`, the dashboard and the GitHub
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
   operator token (`sparkwing cluster tokens create`) or a team owner's
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
4. Code that calls `store.Tenant.SpendSourceCredential` or
   `ReleaseGitCredential` passes the claim token and a
   `store.GitCredentialRelease`; code that decoded
   `controller.GitCredentialResponse` or `controller.SourceTokenResponse`
   decodes `store.SourceCredential`.

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
| `state: {type: s3, ...}` or `state: {type: postgres, ...}` in a machine profile (`~/.config/sparkwing/config.yaml`) | Delete the `state:` key, or the profile, or replace it with a controller profile (`sparkwing cloud connect`). The config fails to load with `state.type: s3 is removed` (or `postgres`) and a pointer to this section. |
| The same in a project profile (`.sparkwing/sparkwing.yaml`) | A project profile declares all four surfaces, so deleting `state:` alone fails with `state surface is required`. Remove the profile, or set `state: {type: sqlite}` for this machine or `state: {type: controller}` for the profile's controller. Both errors name these choices. |
| `url_source: env:VAR` on any surface | Delete it; it named the Postgres state DSN. The config fails to load with `field url_source not found`; a Postgres `state:` that carries it fails on that key before its type is checked. |
| `mirror_local:` on a profile | Delete it. The config fails to load with `field mirror_local not found`. |
| `cache: {binaries: {...}}` | Delete the `binaries:` block. The config fails to load with `field binaries not found`. Runs fetch prebuilt binaries from the git cache `SPARKWING_GITCACHE_URL` names. |
| `sparkwing run X --sw-artifact-digest-backfill` | Delete the flag; it applied only to binaries fetched from a profile's cache. `run` refuses it as an unknown runner flag. |
| `sparkwing serve start --profile P [--no-local-store]` | `sparkwing serve start` for this machine's runs, or the controller's dashboard for shared ones. |
| `guards: {require: [profile:name=prod]}` | `profile:local`, `profile:controller` or an `arg:` token. A run never has a named profile, so the token never matched; a config naming it now fails to load. |
| `sparkwing runs list --profile shared` against an S3 or Postgres state profile | Nothing reads that history any more. The bucket or database is yours to keep or delete. |

- **Refusal:** `sparkwing run deploy --profile prod` fails before it
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
  `sparkwing configure profiles show -o json` drops `mirror_local`. The
  controller's health no longer reports `object_store.stalled`.
- **Go packages:** `pkg/storage/s3state` is removed. `pkg/controller`
  drops `Loopback`, `LoopbackState`, `LoopbackConcurrency` and
  `NewLoopback`; a local run's node processes reach the full controller.
  `pkg/backends` drops `TypePostgres`, `Spec.Binaries` and
  `Surfaces.BinaryCache` and `Spec.URLSource` and adds
  `MigrationGuideURL`. `stdoutlogs.CheckSpec` drops its `urlSource`
  parameter.
  `storeurl.OpenStateStoreFromSpec` opens `sqlite` and `controller` only.
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
- **Refusal:** an old binary fails before the run starts with `pipeline
  entry exited without answering describe; rebuild .sparkwing against a
  matching SDK`. A binary that speaks another protocol version names both
  versions and its SDK.
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
  of `dir_caches`, including in `sparkwing pipeline plan --static -o json`.

## The pipeline binary answers only the node protocol

The compiled pipeline binary no longer runs, plans or inspects a pipeline by
itself; it answers `--sw-node-protocol` and exits 2 on anything else. A
script that called the binary directly moves to the CLI:

| Before | After |
|---|---|
| `<binary> <pipeline> [flags]` | `sparkwing run <pipeline> [flags]` |
| `<binary> <pipeline> --plan`, `sparkwing run <pipeline> --plan` | `sparkwing pipeline plan --name <pipeline>` |
| `<binary> <pipeline> --explain`, `sparkwing run <pipeline> --explain` | `sparkwing pipeline plan --static --name <pipeline>` |
| `<binary> <pipeline> config`, `sparkwing run <pipeline> config` | `sparkwing pipeline describe --name <pipeline> --secrets` |
| `<binary> ops queue`, `ops doctor`, `ops version` | `sparkwing queue`, `sparkwing doctor`, `sparkwing version` |
| `<binary> ops stats`, `ops stats-reset` | none |
| `<binary> replay-node`, `sparkwing debug replay` | none; a hosted run records no dispatch snapshot |
| `<binary> handle-trigger`, `run-node`, `sparkwing handle-trigger` | none; the CLI and `sparkwing-runner` host runs |

`pipeline describe --secrets -o json` prints `{"pipeline": ..., "secrets":
[{"name": ..., "required": true}]}` (or `"optional": true`), read from
describe, instead of the per-field Go names and resolution notes the binary
printed.

## Cache grants are removed

The `swcg` protocol, `cache-grant-key` controller credential and
`POST /api/v1/runs/{id}/cache-grant` route are gone. The grant-authenticated
`/api/v1/data/upload`, `/commit`, `/download` and `/capabilities` endpoints
are removed with them. Services no longer announce `data_download_url` or
`direct_data`.

Byte-storage consumers use the `blob.Store`, `DataSigner` and `RecordStore`
contracts. Their authenticated controller and remote-client adapters still need
integration. There is no compatibility endpoint or cache-service fallback.
Existing output and job-log handlers keep their separate transfer setup until
their own cutover.

## Raw storage writes are create-only

`pkg/storage.ArtifactStore` exposes `PutIfAbsent` for immutable raw objects.
It returns `ErrPreconditionFailed` when the key already exists. Plug-in
implementations must provide that method and remove `Put` and `Has`. The shared
conformance suite checks that a second write cannot replace the first payload.

Use an authorized blob presence query for distributed data. A digest or a raw
object existence check does not establish read access or an upload receipt.
