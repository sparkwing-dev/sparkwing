# Migrating to the next release

The `sparkwing-full` chart now refuses to render a controller that would start
unauthenticated. Installs that already name a bootstrap admin token Secret need
nothing; every other install sets one value on upgrade.

## sparkwing-full requires a bootstrap admin token

- **Before:** `controller.requireAuth` defaulted to `false` and
  `controller.bootstrapAdminToken.name` to empty. A fresh install served every
  controller route, including `POST /api/v1/tokens` and first-admin creation,
  unauthenticated to anything that reached the Service until a token was minted
  and the controller restarted.
- **After:** `controller.requireAuth` defaults to `true`, and the render fails
  with an error naming both options while `controller.bootstrapAdminToken.name`
  is empty and `controller.allowOpenBootstrap` is not `true`.
- **Upgrade, recommended:** create a Secret holding a token-shaped value and
  name it. The controller ignores it once a live token exists, and
  `--require-auth` keeps refusing to start if the tokens table is ever emptied:

  ```bash
  # The umask makes the file 0600: it holds an admin bearer.
  (umask 077 && printf 'swu_%s' "$(openssl rand -hex 24)" > "$HOME/sparkwing-bootstrap-admin")
  kubectl -n sparkwing create secret generic sparkwing-bootstrap-admin \
      --from-file=token="$HOME/sparkwing-bootstrap-admin"
  helm upgrade sparkwing ./charts/sparkwing-full --namespace sparkwing -f my-values.yaml \
      --set controller.bootstrapAdminToken.name=sparkwing-bootstrap-admin
  ```

- **Upgrade, keeping the previous shape:** set
  `controller.allowOpenBootstrap=true`. The chart then omits `--require-auth`.
  A controller that already holds a live token stays authenticated; one whose
  tokens table is empty serves every route unauthenticated until a token is
  minted and it restarts, as before. The value must be a YAML bool; a quoted
  string fails the render.
- **Edge cases:** `controller.requireAuth=false` also renders without a
  bootstrap token and keeps the old behavior. Values files that set
  `requireAuth: true` without a bootstrap token, which crash-looped a fresh
  install, now fail at render time instead.

The OIDC subject gains a repository segment, so every cloud trust policy that matches `sub` must be rewritten before the controller is upgraded, or the roles it guards refuse Sparkwing tokens.

## OIDC subject names the repository

The `sub` of a controller-issued ID token now carries GitHub's numeric repository id between the team and the pipeline. Before, two repositories of one team that subscribed the same pipeline name minted the same subject, so a push to `main` of either repository satisfied a trust policy meant for one of them.

- **Before:** `team:acme:pipeline:deploy:trigger:push:runner:runner:ref:refs/heads/main`
- **After:** `team:acme:repository_id:123456789:pipeline:deploy:trigger:push:runner:runner:ref:refs/heads/main`

Every trust policy, federated credential or role that matches `sub` must be updated:

1. Find the repository id: `gh api repos/<owner>/<name> --jq .id`.
2. Insert `repository_id:<id>:` after `team:<team>:` in every AWS `StringEquals` or `StringLike` condition on `<issuer>:sub`, every Entra federated credential `subject`, and every Vault `bound_subject` or glob on `sub`.
3. A token minted before the upgrade carries the old subject and lives at most one token lifetime (10 minutes by default), so to avoid a gap, admit both forms during the rollout and drop the old one afterwards. In AWS that is a list of both values under `StringLike`; in Entra, a second federated credential.

AWS trust policy, before:

```json
"StringLike": {"api.sparkwing.dev:sub": "team:acme:pipeline:deploy:trigger:push:runner:runner:ref:refs/heads/main"}
```

After:

```json
"StringLike": {"api.sparkwing.dev:sub": "team:acme:repository_id:123456789:pipeline:deploy:trigger:push:runner:runner:ref:refs/heads/main"}
```

**Edge cases:**

- The segment is set only when a GitHub App delivery started the run, or when a retry or child run inherits that repository. Runs from the CLI, the API or a schedule carry an empty segment, `team:acme:repository_id::pipeline:...`. A policy for such runs inserts `repository_id::`.
- A policy that ends in `*` after an earlier segment, such as `team:acme:*`, keeps matching, and now also matches every repository. Narrow it to `team:acme:repository_id:<id>:*` when the role belongs to one repository.
- Google Cloud and Vault examples that condition on the individual claims keep working, but they should add a condition on `repository`, because a pipeline name is not unique across a team's repositories. See [OIDC tokens for cloud roles](../oidc.md).

Go callers of `pkg/store` update one call site; nothing else changes on a
running install.

## SettleCardPayment returns the matched warning

`Store.SettleCardPayment` gains a middle return, a `*store.WarnedCardPayment`
that is nil unless an actionable early fraud warning matched the payment or
the card that paid. It names the warning, whether the payment settled the
open charge as a repayment (`Repays`), the debt it repaid and the rest the
ledger does not hold. A caller alerts on it, because a warned payment
either repays a pay-now debt with the team held or grants nothing, and any
unapplied rest is the operator's to return.

- **Before:** `created, err := st.SettleCardPayment(ctx, payment, now)`
- **After:** `created, warned, err := st.SettleCardPayment(ctx, payment, now)`

A redelivery of a settled payment returns nil, so an alert keyed on it fires
once per payment.

The cache service keeps a run's cache entries apart by repository and git ref.
Nothing needs configuring; expect more cache misses on new branches at first.

## Cache entries follow the repository and ref

**Before:** every run of a team read and wrote one shared set of `/cache`,
`/bin` and `/artifacts` entries in the cache service, so a pull request's run
could replace the dependency archive or binary a later `main` run restored.

**After:** the cache grant a run receives carries its repository and refs, read
by the controller from the run's trigger. The run writes only under its own
ref and reads, in order, its own ref, its pull request's base branch, the
repository's default branch, and finally the entries written before this
release.

A run whose ref and commit its submitter chose writes under a ref of its own,
so it reads what its branch's pushes wrote but never replaces it. That covers
runs started from the CLI, the API and the dashboard, and retries or child
runs that descend from one of them. Runs whose ref the server holds write
under the real ref and warm the caches pushes read: runs a signed GitHub
webhook started, scheduled runs that follow a branch tip, and retries or child
runs of either that keep its ref and commit and upload no source. A schedule
pinned to a commit counts as submitter-chosen, because any editor can pin one
to a commit that is not on the branch.

**What you see:** the first run on a new branch restores its base branch's
cache as before. A branch's writes stay on that branch, so a sibling branch or
another repository of the team no longer finds them and rebuilds. Artifacts a
node hands to later nodes are content-addressed and stay shared by the team,
so a memoized or retried node still stages them on any ref. Entries from
before the upgrade stay readable by every run until a run writes the same key
under its own ref. The cache refuses a grant minted by an older controller,
so upgrade the controller before or with the cache; see
[Cache grants need a claim and a scope](#cache-grants-need-a-claim-and-a-scope).

**Why:** a deploy role that trusts `ref:refs/heads/main` relies on branch
protection deciding what runs on `main`; a shared cache let a branch's run put
code into a later `main` run.

A deployment that still starts `sparkwing-runner worker` switches to the
combined runner before upgrading; the chart and the documented manifests never
ran it.

## sparkwing-runner worker is removed

- **Before:** `sparkwing-runner worker --controller <url> [--runner inprocess|k8s|warm]`
  polled the trigger queue and ran each claimed trigger, either in-process or by
  sending its nodes to Kubernetes Jobs or the warm pool.
- **After:** the `worker` subcommand is unknown and exits 2. The combined
  runner is the only trigger-claiming loop in `sparkwing-runner`.
- **Upgrade:** replace the container args:

  ```text
  sparkwing-runner worker --controller=URL --logs=URL --runner=k8s \
      --namespace=NS --image=IMAGE --runner-sa=SA --image-pull-secret=SECRET
  ```

  becomes

  ```text
  sparkwing-runner runner --controller=URL --logs=URL --also-claim-triggers --claim-nodes=false \
      --trigger-runner=k8s --trigger-runner-namespace=NS --trigger-runner-image=IMAGE \
      --trigger-runner-sa=SA --trigger-runner-image-pull-secret=SECRET
  ```

  `--trigger-sources`, `--token`, `--metrics-addr` and `--dependency-proxy`
  carry over; `--image-pull-policy`, `--kubeconfig`, `--runner-controller-url`
  and `--runner-logs-url` gain the `--trigger-runner-` prefix, and
  `--artifact-store` becomes `--trigger-artifact-store`. Drop
  `--claim-nodes=false` to let the same pod also run nodes. The worker's
  `--log-store` has no runner equivalent: the runner streams logs to `--logs`.
  `--k8s-cpu-ceiling`, `--k8s-memory-ceiling` and `--k8s-job-deadline` become
  `SPARKWING_K8S_CPU_CEILING`, `SPARKWING_K8S_MEMORY_CEILING` and
  `SPARKWING_K8S_JOB_DEADLINE` in the runner pod's environment, which each
  trigger's child inherits.
- **Edge cases:** `sparkwing cluster worker`, the CLI's in-process claim loop
  for a profile, is unchanged.

## Flag-mirror environment variables are no longer read

Each of these variables only supplied a flag's default. Neither chart sets
them; a custom manifest that does passes the flag instead.

| Variable | Use instead |
|---|---|
| `SPARKWING_TRIGGER_RUNNER` | `sparkwing-runner runner --trigger-runner` |
| `SPARKWING_RUNNER_IMAGE` | `--trigger-runner-image` |
| `SPARKWING_IMAGE_PULL_SECRET` | `--trigger-runner-image-pull-secret` |
| `SPARKWING_RUNNER_CONTROLLER_URL` | `--trigger-runner-controller-url` |
| `SPARKWING_RUNNER_LOGS_URL` | `--trigger-runner-logs-url` |
| `SPARKWING_RUNNER_NODE_SELECTOR` | `--trigger-runner-node-selector`, once per entry |
| `SPARKWING_RUNNER_TOLERATION` | `--trigger-runner-toleration`, once per entry |
| `SPARKWING_GITCACHE_CONCURRENCY` | `sparkwing-cache --git-fork-limit` |
| `SPARKWING_WINGD_VERSION` | `sparkwing wingd run --version` |
| `SPARKWING_FORCE_COLOR=1` | `CLICOLOR_FORCE=1` |

`SPARKWING_BAKED_BINARY` named a pipeline binary inside the runner image for
triggers without a repository. The runner image ships no such binary, so the
fallback only failed later with `no such file`; such a trigger now fails at
once saying it has no repository. Delete the variable from any runner
manifest. `SPARKWING_CHAOS_KEEP` kept chaos-test homes and has no replacement;
a failed chaos test still keeps its home.

**Edge case:** a variable set on the runner pod also reached each trigger's
`handle-trigger` child, which read the same names. Both reads are gone, so
the flag on `sparkwing-runner runner` is the only way to set these values.

Per-pipeline GitHub webhooks are gone, so every repository that posted to
`/webhooks/github/<pipeline>` must move to the GitHub App before the
controller is upgraded, or its pushes and pull requests stop starting runs.

## Per-pipeline GitHub webhooks are removed

- **Before:** a repository webhook posted to `POST /webhooks/github/{pipeline}`,
  signed with a secret from `sparkwing cluster webhooks connect`, the
  `GITHUB_WEBHOOK_BINDINGS` document or `GITHUB_WEBHOOK_SECRET`, and the
  controller's `GITHUB_TOKEN` posted commit statuses for its pull request runs.
- **After:** the [GitHub App](../github-app.md) is the only signed GitHub
  trigger. The old route, the bindings API and table, the `sparkwing cluster
  webhooks` commands, the three environment settings and the chart values
  `controller.githubWebhookSecret` and `controller.githubStatusToken` are gone.
  A delivery to the old URL answers `404`. Schema v92 drops the stored bindings
  and their secrets; nothing reads them back.

To move a repository:

1. Configure the App on the controller, once per deployment, as
   [Operator configuration](../github-app.md#operator-configuration) lists:
   `SPARKWING_GITHUB_APP_ID`, `SPARKWING_GITHUB_APP_SLUG`,
   `SPARKWING_GITHUB_APP_PRIVATE_KEY_FILE` (or `_PRIVATE_KEY`) and
   `SPARKWING_GITHUB_APP_WEBHOOK_SECRET`, with the App's webhook pointed at
   `https://<controller>/webhooks/github-app`. The `sparkwing-full` chart takes
   these through `controller.extraEnv`, and its ingress does not route
   `/webhooks/github-app`, so add a rule for it as you did for the old path.
   The hosted service already runs the App.
2. A team owner opens **Team -> GitHub**, chooses **Connect GitHub**, and
   installs the App on the account that owns the repository
   ([Connecting a team](../github-app.md#connecting-a-team)).
3. Subscribe each pipeline the old webhook URL named, in the same tab or with
   the API. The old webhook ran `push` and `pull_request` (`opened`,
   `synchronize`, `reopened`) and ignored tag pushes; this subscription keeps
   that, and `branches` narrows it:

   ```bash
   curl -sS -X PUT https://<controller>/api/v1/team/github-app/triggers \
     -H "Authorization: Bearer $SPARKWING_TOKEN" \
     -H 'Content-Type: application/json' \
     -d '{"repository":"your-org/my-app","pipeline":"build-deploy","push":true,"pull_request":true}'
   ```

4. Delete the old webhook on GitHub (repository **Settings -> Webhooks**), or
   leave it to fail with `404`.
5. Remove `GITHUB_WEBHOOK_SECRET`, `GITHUB_WEBHOOK_BINDINGS` and `GITHUB_TOKEN`
   from the controller's environment, and `controller.githubWebhookSecret` and
   `controller.githubStatusToken` from Helm values. The controller ignores
   the environment variables, and the `sparkwing-full` chart refuses to
   render while either value still names a Secret.

**Edge cases:**

- Runs the App starts report a check run named `sparkwing/<pipeline>`
  instead of a commit status under the same name. A branch protection rule
  that requires `sparkwing/<pipeline>` and pins it to a source expects the
  old token's statuses; set the App as that check's source. An installation
  whose owner has not accepted the Checks permission gets commit statuses
  until it does.
- App runs carry the repository id in the OIDC subject, so a trust policy
  written for webhook runs with `repository_id::` must name the id
  ([OIDC subject names the repository](#oidc-subject-names-the-repository)).
- A run already queued from the old webhook still runs. If it mints an OIDC
  token after the upgrade, its trigger is `trigger:manual`, because it carries
  no repository id.
- A subscription runs only for repositories in the team's installation, and a
  pull request from a fork starts nothing, as before.

## Legacy cache routes are removed

**Before:** `sparkwing-cache` served source archives and single files
(`/archive`, `/file`, `/tree-hash`, `/branch-contains`), tarball uploads
(`/upload`, `/uploads/<id>`), ancestor negotiation and bundle seeding
(`/sync/negotiate`, `/sync/seed`), an eager mirror fetch (`/git/refresh`) and
job-ID artifacts (`/artifacts/<job>`). The controller proxied seeding and
refresh as `POST /api/v1/gitcache/seed` and `POST /api/v1/gitcache/refresh`.
All of them took the cache's operator token or `admin`, except
`/artifacts/<job>`, which also took a run's cache grant.

**After:** those routes answer 404. The cache keeps `/git/register`,
`/git/<name>/...`, `/bin/...`, `/cache/...`, `/admin/teams/{team}`,
`/admin/store-ceiling/...`, `/health`, `/metrics`, `/stats` and `/proxy/...`.
The controller keeps `POST /api/v1/gitcache/git/register` and the clone proxies.

**What replaces each:**

| Removed | Use instead |
|---|---|
| `/archive`, `/file`, `/tree-hash`, `/branch-contains` | Clone `/git/<name>` with `SPARKWING_CACHE_TOKEN` and read the checkout. |
| `/git/refresh`, `POST /api/v1/gitcache/refresh` | Nothing: a clone refreshes a mirror older than `FETCH_FRESH_WINDOW` before it answers. |
| `/sync/seed`, `POST /api/v1/gitcache/seed`, `/sync/negotiate`, `/upload`, `/uploads/<id>` | Give the cache a credential for origin and register the repository, or run a runner without `--gitcache` so it fetches from origin itself. `--working-tree` already uploads to the direct data store. |
| `/artifacts/<job>` | Declare `Outputs` and `Consumes`; the SDK stores artifacts under content-addressed `/bin/artifacts/...` keys or the direct data store. |

**Operator steps:**

- Remove `--max-artifact-bytes` and `--workspace-seed-max-age` from the cache's
  arguments before upgrading; a cache started with either exits.
  `SPARKWING_CACHE_MAX_ARTIFACT_BYTES` and `WORKSPACE_SEED_MAX_AGE` are ignored.
  With the runner-bundle chart, drop `cache.limits.maxArtifactBytes`.
- Drop dashboards and alerts on `sparkwing.gitcache.archives_served`,
  `sparkwing.gitcache.files_served` and `sparkwing.gitcache.recovery_reclones`.
- Optionally reclaim space: `<data-dir>/archives/`, `<data-dir>/uploads/`,
  `<data-dir>/artifacts/` and each `<data-dir>/teams/<team>/artifacts/` (or
  `teams/<team>/artifacts/` and `teams/<team>/scopes/*/artifacts/` under
  `--blob-store`) hold nothing the cache reads any more, and the store ceiling
  no longer counts the first three. Mirrors may still carry
  `refs/sparkwing-workspace/*` and `refs/sparkwing-workspace-archive/*` refs
  from working-tree seeds; nothing prunes them now, and deleting them with
  `git update-ref -d` inside the mirror is safe.

**Why:** these routes were a second way into the shared mirrors and the
volume, kept for flows the CLI and runners no longer use, and each needed its
own review.

## Cache grants need a claim and a scope

**Before:** the cache honored any grant its key signed. A grant with no claim
and no repository scope, which controllers minted before grants carried a
scope, read and wrote the team's whole cache tree until it expired.

**After:** the cache and the controller's data routes refuse, with 401, a
grant that names no claim or no repository scope. Every grant a current
controller mints names both.

**Upgrade:** upgrade the controller before the cache, or both together. A run
holding a grant from an older controller loses the cache, and a runner that
asks for a fresh grant from an upgraded controller gets one; nothing else
needs changing.

**Why:** the grant is the cache's only team boundary, and a grant that named
neither the claim nor the repository it was minted for outlived both.

## doctor no longer reports box-slot locks

- **Before:** `sparkwing doctor` read `$SPARKWING_HOME/box-slots`, reported a
  live holder as a pipeline pinned before v0.16, and purged idle lock files.
  `sparkwing queue` printed the same warning.
- **After:** neither command reads the directory. `doctor -o json` no longer
  carries `legacy_box_slot_files_removed` or `live_legacy_holders`, and
  `-o plain` no longer prints those two rows.
- **Upgrade:** scripts that read either field drop it. A leftover
  `$SPARKWING_HOME/box-slots` directory holds only lock files and can be
  deleted with `rm -r "${SPARKWING_HOME:-$HOME/.sparkwing}/box-slots"`.

## Unused pkg functions are removed

Go programs that import these `pkg/` functions call the replacement instead:

| Removed | Use instead |
|---|---|
| `controller.Serve(ctx, st, addr, logger)` | `controller.ServeWith(ctx, controller.New(st, logger), addr)` |
| `logs.Serve`, `logs.ServeWithTokens`, `logs.ServePrivateWithTokens` | `logs.ServeWith(ctx, logs.ServeOptions{Root, Addr, ControllerURL, Logger, Private})` |
| `sparkwinglogs.FromClient(c)` | `sparkwinglogs.New` with the client's URL and token |
| `store.DetectDialect(dsn)` | `store.Open` for a SQLite path, `store.OpenPostgres` for a `postgres://` DSN |
| `controller.AuditFields`, `store.SetArgon2AcquireTimeout`, `backends.LayerSurfaces` | nothing; no caller used them |

`backends.LayerSurfaces` layered a pipeline target's backend over the
profile's, a block `sparkwing.yaml` no longer accepts.

## Unimplemented backend types are removed

Code that imports `TypeGCS`, `TypeAzureBlob` or `TypeMySQL` from
`github.com/sparkwing-dev/sparkwing/pkg/backends` no longer compiles; remove
the reference, since no build ever opened a backend of those types. Nothing
changes at run time: a profile naming `gcs`, `azure-blob` or `mysql` failed at
run start before and still does, now with an error naming the types its
surface accepts.

## The warm-PVC pool is removed

**Before:** `sparkwing-controller --pool` kept a set of PVCs labeled
`sparkwing.dev/pool=cache` warm with privileged `docker:27-dind` pods and
served `GET /api/v1/pool` and `POST /api/v1/pool/{checkout,return,heartbeat}`.
`sparkwing-full` turned it on by default (`controller.pool.enabled: true`) and
gave the controller a Role over PVCs, pods, configmaps, events and namespaces,
a ClusterRole over StorageClasses, and a warmer ServiceAccount.

**After:** the controller has no pool and makes no Kubernetes API calls. The
four routes answer 404. Nothing Sparkwing ships ever checked a PVC out, so no
build loses a cache it was using.

**Operator steps:**

- Remove `--pool`, `--pool-namespace`, `--warmer-service-account` and
  `--kubeconfig` from the controller's arguments before upgrading; a
  controller started with any of them exits. `SPARKWING_WARMER_SA` and
  `POD_NAMESPACE` are no longer read.
- With `sparkwing-full`, drop `controller.pool.*` and `rbac.*` from your
  values. The upgrade deletes the controller's Role, RoleBinding, ClusterRole,
  ClusterRoleBinding and `<release>-sparkwing-full-cache-warmer`
  ServiceAccount, and the controller pod no longer mounts an API token.
  Annotations on `serviceAccount` for IRSA or Workload Identity keep working.
- Outside the chart, delete the Role and ClusterRole you granted the
  controller for the pool, and any `sparkwing-cache-warmer` ServiceAccount.
- Reclaim the pool's storage once the new controller runs:
  `kubectl delete pod,pvc -n <pool-namespace> -l sparkwing.dev/managed=pool-manager`.
  The `sparkwing-cache-config` ConfigMap, if you created one, is no longer read.
- Drop dashboards and alerts on `sparkwing.pool.reconcile_duration`,
  `sparkwing.pool.warm_duration`, `sparkwing.pool.checkouts` and
  `sparkwing.pool.returns`.

**Why:** the pool held the controller's broadest Kubernetes rights and ran
the only privileged pods Sparkwing created, and no runner, launcher or chart
template ever mounted one of its PVCs.

## The concurrency notify stream is removed

**Before:** `GET /api/v1/concurrency/{key}/notify?run_id=...&node_id=...`
held a server-sent event stream open for up to 30 minutes and sent one
`ready`, `superseded` or `stream_end` event when the waiter resolved.

**After:** the route answers 404. Nothing Sparkwing ships opened it.

**Upgrade:** a client of your own that waited on the stream polls
`GET /api/v1/concurrency/{key}/resolve?run_id=...&node_id=...` instead. It
takes the node's claim token or `runs.state` rather than `runs.read`, and its
`status` field carries the same outcomes.

**Why:** an unused stream that a claim token could hold open for half an hour
was attack surface with no caller.

## The controller artifact route is removed

**Before:** a controller or local daemon given an artifact store served
`GET /api/v1/artifacts/{key}` to a `runs.read` caller, run keys to the run's
team and content-addressed keys to the operator.

**After:** the route answers 404. Nothing Sparkwing ships called it.

**Upgrade:** a script that read artifacts this way reads the artifact store
its profile names directly, the local directory or the bucket. On a cluster
with direct data storage, `POST /api/v1/data/download` returns a signed URL
for an `artifacts/...` key.

**Why:** a second read path into the shared artifact store needed its own
team check and its own egress accounting, and served no caller.

## Four operator routes are removed

**Before:** an `admin` token could read the controller's egress meter with its
top consumers (`GET /api/v1/egress`), read a named team's credit balance
(`GET /api/v1/credits/teams/{team}`), and give a team a free-tier slot past
`--free-team-slots` (`PUT /api/v1/storage/teams/{team}/free-slot`). The cache's
operator token could list its mirror files with `GET /repos`.

**After:** all four answer 404.

**What replaces each:**

| Removed | Use instead |
|---|---|
| `GET /api/v1/egress` | `sparkwing_egress_day_bytes` and `sparkwing_egress_daily_alarm` on `/metrics`; `/api/v1/health` reports the alarm. Per-principal totals are no longer served. |
| `GET /api/v1/credits/teams/{team}` | The operator console's `GET /api/v1/operator/teams/{team}`, or `GET /api/v1/credits` with the team's own credential. |
| `PUT /api/v1/storage/teams/{team}/free-slot` | Raise `--free-team-slots` so the team takes a slot with its first byte. |
| cache `GET /repos` | List `<data-dir>/repos/` on the cache volume. |

**Why:** each was a privileged route with no caller, and each needed its own
review as the route table moves to one declared list.

## CLI aliases and internal verbs

Every verb the CLI dispatches is now a registered command, so `--help`,
`sparkwing commands` and completion see the whole surface.

| Before | After |
|---|---|
| `sparkwing configure profiles ls` | `sparkwing configure profiles list` |
| `sparkwing configure profiles rm NAME`, `... delete NAME` | `sparkwing configure profiles remove NAME` |
| `sparkwing configure profiles dup ...` | `sparkwing configure profiles duplicate ...` |
| `sparkwing secrets rm ...`, `sparkwing secrets remove ...` | `sparkwing secrets delete ...` |
| `sparkwing pipeline sparks ls`, `... rm ...` | `sparkwing pipeline sparks list`, `... remove ...` |
| `sparkwing configure xrepo ls`, `... rm ...` | `sparkwing configure xrepo list`, `... remove ...` |
| `sparkwing runs consumer kill` | `sparkwing runs consumer stop` |
| `sparkwing run <pipeline> config [-o json]` | `sparkwing pipeline describe --name <pipeline> --secrets [-o json]` |
| `sparkwing pipeline publish` | none; a runner compiles the pipeline on first use and shares the binary through the cache |
| `sparkwing run-node` | none; the pipeline binary and `sparkwing-runner run-node` keep the node protocol |

- **Shell completion:** the nine `_complete-*` helpers are one hidden
  `sparkwing __complete KIND`. A completion script installed by an earlier
  release calls the old names and completes nothing; regenerate it:

  ```bash
  sparkwing completion --shell zsh > "${fpath[1]}/_sparkwing"
  ```

- **`crons tick`:** hidden from `--help` and `sparkwing commands`, and still
  invoked by the same name. OS timers installed by `crons install` need
  nothing. A host that runs the tick from its own scheduler keeps calling
  `sparkwing crons tick`.

## cluster gc, examples scaffold and retired-flag pointers are removed

| Before | After |
|---|---|
| `sparkwing cluster gc [--root DIR] [--profile P]` | none; `sparkwing-runner runner` sweeps its warm root when it starts |
| `sparkwing examples scaffold --name EXAMPLE` | `sparkwing examples --name EXAMPLE --body`, then save the body under `.sparkwing/jobs/` |
| `--on P`, `--sw-on P` | `--profile P` |
| `sparkwing run X --sw-profile P` | `sparkwing pipeline trigger X --profile P` |
| `--sw-target T` | `--target T` |
| `sparkwing run X --sw-isolated-home DIR` | `SPARKWING_HOME=DIR sparkwing run X` |

The retired spellings still fail; the error now says the flag is unknown
instead of naming its replacement.

## Root flags replace per-verb directory and home flags

`-C DIR` goes before the verb and applies to every verb.

| Before | After |
|---|---|
| `sparkwing run X --sw-cd DIR`, `sparkwing run X -C DIR` | `sparkwing -C DIR run X` |
| `sparkwing pipeline list --sw-cd DIR` (and every other verb that took `--sw-cd`) | `sparkwing -C DIR pipeline list` |
| `sparkwing pipeline sparks list --sparkwing-dir DIR/.sparkwing` | `sparkwing -C DIR pipeline sparks list` |
| `sparkwing cache explain --dir DIR/.sparkwing` | `sparkwing -C DIR cache explain` |
| `sparkwing pipeline hooks install --repo DIR` (also `uninstall`, `status`, `fire`) | `sparkwing -C DIR pipeline hooks install` |
| `sparkwing crons install --repo DIR` (also `uninstall`) | `sparkwing -C DIR crons install` |
| `sparkwing daemon status --home DIR` (also `queue`, `doctor`, `serve *`, `runs cancel/retry/bounce`, `runs consumer *`) | `SPARKWING_HOME=DIR sparkwing daemon status` |

- `-C` walks up from DIR to the nearest `.sparkwing/`, so it accepts any
  directory inside the repository. `--sparkwing-dir` and `cache explain --dir`
  named the `.sparkwing/` directory itself; pass its parent to `-C`.
- `pipeline lint --dir` still names the pipeline source to scan. `--repo`
  still names a registered repository on `repos`, and an `OWNER/NAME`
  filter on `runs` and `cluster` verbs.
- `--profile NAME` may also go before the verb, for verbs that accept it.
  Without `--profile`, verbs that read runs (`runs list/status/logs/stats`,
  `run`, `pipeline hooks`, `serve`, `pipeline trigger`) use
  `SPARKWING_PROFILE`, then the project's `defaults.profile`. `runs stats`
  read the local store unless `--profile` was given; it now follows that
  chain, so a repository with `defaults.profile` reads its controller. Verbs
  that change state elsewhere (`secrets`, `crons`, `runs cancel/retry/bounce/prune`,
  `cluster`) still act locally unless `--profile` is given.
- `SPARKWING_PROFILE` is set for a pipeline's own process by
  `sparkwing run --profile P`, so a `sparkwing runs ...` call inside a step
  now reads P's store.

## Runs verbs fold into status, list and logs

| Before | After |
|---|---|
| `sparkwing runs get --run ID` | `sparkwing runs status ID -o json --exit-zero` |
| `sparkwing runs wait --run ID [--timeout 10m] [--poll 3s]` | `sparkwing runs status ID --follow --timeout 10m [--poll 3s]` |
| `sparkwing runs summary --run ID` | `sparkwing runs status ID --view summary --exit-zero` |
| `sparkwing runs timeline --run ID [--steps] [--width N]` | `sparkwing runs status ID --view timeline [--steps] [--width N] --exit-zero` |
| `sparkwing runs receipt --run ID` | `sparkwing runs status ID --view receipt --exit-zero` |
| `sparkwing runs errors ID` | `sparkwing runs status ID --view errors --exit-zero` |
| `sparkwing runs tree --run ID` | `sparkwing runs status ID --view tree --exit-zero` |
| `sparkwing runs last [--pipeline P] [--watch]` | `sparkwing runs list --limit 1 [--pipeline P] [--watch]` |
| `sparkwing runs find --git-sha S --repo R --root-only --wait --find-timeout D` | `sparkwing runs list --sha S --repo R --root-only --wait --wait-timeout D` |
| `sparkwing runs failures [--group-by step\|node]` | `sparkwing runs list --status failed --group-by run\|step\|node` |
| `sparkwing runs grep --pattern P [filters]` | `sparkwing runs logs --grep P [filters]` |
| `sparkwing runs triggers list\|get` | `sparkwing cluster triggers list\|get` |

- **Exit codes:** `runs status` exits 1 for a run that did not succeed, and
  every view inherits that; add `--exit-zero` where a script read a view of a
  failed run. `--follow --timeout` keeps `runs wait`'s codes: 0 succeeded, 1
  failed or cancelled, 2 timed out, 3 the run could not be read.
- **Output shape:** `runs wait -o json` printed the run record; `runs status -o json`
  prints `{"run": ..., "nodes": ...}`, so read `.run.status` instead of
  `.status`. `runs get -o json` output is a subset of `runs status -o json`.
- **Defaults:** `runs find` looked back one hour by default; `runs list` has no
  default lookback, so pass `--since 1h` to keep that window. `runs failures`
  printed one row per failed run; that is `--group-by run`.
- **Profiles:** `runs get`, `wait`, `last`, `find`, `failures`, `grep` and the
  views read the local store unless `--profile` was given. Their replacements
  follow `runs status`: `--profile`, then `SPARKWING_PROFILE`, then the
  project's `defaults.profile`.

## Pipeline verbs fold into list, plan and hooks status

| Before | After |
|---|---|
| `sparkwing pipeline run X [flags]` | `sparkwing run X [flags]` |
| `sparkwing pipeline discover --query Q [-o json]` | `sparkwing pipeline list --query Q [-o json]` |
| `sparkwing pipeline explain --name X [-- pipeline-flags]` | `sparkwing pipeline plan --static --name X [-- pipeline-flags]` |
| `sparkwing pipeline explain --all` | `sparkwing pipeline plan --static --all` |
| `sparkwing pipeline hooks fire [--fleet]` | `sparkwing pipeline hooks status --prove [--fleet]` |
| `sparkwing pipeline hooks survey [--ungated]` | `sparkwing pipeline hooks status --all [--ungated]` |

- Output, exit codes and JSON shapes are unchanged; only the invocation moves.
- A CI step that gates on `pipeline explain --all` changes its command line
  and nothing else.
- Pipelines scaffolded by an earlier `pipeline new` list
  `sparkwing pipeline explain --name X` among their examples; edit the
  example to `sparkwing pipeline plan --static --name X`.

## Crons verbs fold into uninstall, list, show and set

| Before | After |
|---|---|
| `sparkwing crons disarm NAME` | `sparkwing crons uninstall --name NAME` |
| `sparkwing crons status` | `sparkwing crons list --timer` |
| `sparkwing crons next [--count N]` | `sparkwing crons list --next N` |
| `sparkwing crons next NAME [--count N]` | `sparkwing crons show NAME --next N` |
| `sparkwing crons lock NAME` | `sparkwing crons set NAME --pin` |
| `sparkwing crons unlock NAME` | `sparkwing crons set NAME --unpin` |
| `sparkwing crons pause NAME` | `sparkwing crons set NAME --pause` |
| `sparkwing crons resume NAME` | `sparkwing crons set NAME --resume` |
| `sparkwing crons reset NAME` | `sparkwing crons set NAME --reset` |

- **Exit codes and output:** each replacement prints what the old verb printed,
  in every `-o` format, and exits the same way. `crons list --timer` still exits
  1 when schedules are armed and the timer is not evaluating them, so a check
  script only changes its command line. `-o json` keeps the old shapes: the
  health record for `--timer`, one NDJSON instant per line for `--next`, the
  schedule row for `--pin`, `--unpin`, `--pause` and `--resume`, and
  `{"schedule": ..., "name": ...}` for `uninstall --name`. Error messages
  name the new spelling, such as `crons set --pause:`.
- **Defaults:** `crons next` showed 5 instants unless `--count` said otherwise;
  `--next` takes the count as its value, so write `--next 5` for the old
  default.
- **Profiles:** `--profile NAME` works on each replacement as it did on the old
  verb. `--pin` and `--unpin` refuse it, as `lock` and `unlock` did.
- **One action per call:** `--pin`, `--unpin`, `--pause`, `--resume` and
  `--reset` each stand alone on `crons set`, and none mixes with `--cron`,
  `--tz`, `--overlap`, `--catch-up` or `--arg`. `crons list` takes one of
  `--all`, `--timer` and `--next`. `uninstall --name` refuses `--fleet`, and
  leaves the OS timer in place as `disarm` did.
- **Positionals:** `crons list` and `crons uninstall` now refuse a stray
  positional. `crons uninstall NAME` used to ignore the name and disarm the
  whole repository.
- **Timer units:** the systemd and launchd units run `sparkwing crons tick`,
  which keeps its name, so an installed timer needs no change.

## Docs verbs fold into list, read and migrations

| Before | After |
|---|---|
| `sparkwing docs guides` | `sparkwing docs list --guides` |
| `sparkwing docs versions [--web] [--no-cache]` | `sparkwing docs list --versions [--web] [--no-cache]` |
| `sparkwing docs all` | `sparkwing docs read --all` |
| `sparkwing docs migrations list [--web]` | `sparkwing docs migrations [--web]` |
| `sparkwing docs migrations read --version V` | `sparkwing docs migrations --version V` |
| `sparkwing docs migrations read V` | `sparkwing docs migrations V` |
| `sparkwing docs migrations between --from A --to B` | `sparkwing docs migrations --from A --to B` |
| `sparkwing docs migrations between` | `sparkwing docs migrations --from v0.0.0` |
| `sparkwing docs cache info` | `sparkwing cache info --docs` |
| `sparkwing docs cache clear` | `sparkwing cache prune --docs` |

- **Agents:** a prompt, skill or script that runs
  `docs migrations read --version V` or `docs migrations between --from A --to B`
  drops the `read` or `between` word and keeps its flags. A bare
  `docs migrations` now lists the guides; the every-guide blob that bare
  `between` printed needs `--from v0.0.0`. The old words fail loudly:
  `docs migrations read --version V` is refused as an unexpected positional,
  and a bare `docs migrations read` as an invalid version.
- **Output and exit codes:** each replacement prints what the old verb printed
  in every `-o` format and exits the same way, with two exceptions.
  `cache info --docs` heads its pretty report `DOCS WEB CACHE` instead of
  `CACHE`, and with `-o plain` prints the cache directory where
  `docs cache info` refused plain. `cache prune --docs` honours `-o`:
  `-o json` prints `{"dir": ..., "removed": N}` and `-o plain` the removed
  count, where `docs cache clear` printed its sentence for every format.
- **Flag combinations:** `--guides` takes only `--output`; `--versions` takes
  `--web` and `--no-cache`; `read --all` takes only `--output`. `--version`
  and `--from`/`--to` on `docs migrations` cannot be combined. `--docs` on
  `cache info` and `cache prune` refuses `--all`, `--max-bytes` and
  `--max-entries`. `docs list` now refuses a stray positional.
