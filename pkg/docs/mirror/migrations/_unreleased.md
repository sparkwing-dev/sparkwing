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

## Node bounce requests move to their run's team

- **Before:** a bounce request was recorded without its team, so every row
  carried the default team whatever team its run belonged to.
- **After:** schema 93 moves each existing request into the team of the run it
  names, and new requests record their run's team. A team's open requests stay
  visible to its runners, and its next request continues the node's sequence.
- **Upgrade:** nothing to do; the controller applies schema 93 on start. An
  older binary keeps reading the upgraded database.

## Store methods removed or moved to Tenant

- **Before:** `pkg/store` exported `Store.ActiveExecutorActivity`,
  `Store.PrincipalHoldsPipelineClaim` and `Store.FailNodeForUnpricedClass`.
- **After:** the first two are gone, and the third is a method of `Tenant`.
  Many other `Store` methods now act on the default team only; their `Tenant`
  methods of the same name serve every team.
- **Upgrade:** code embedding `pkg/store` replaces
  `st.ActiveExecutorActivity(ctx, now)` with
  `team.ActiveExecutorActivity(ctx, now)` on the handle from
  `st.ForTeam(ctx, slug)`, replaces `st.PrincipalHoldsPipelineClaim` with
  `st.PrincipalHoldsProfileClaim` or `st.PrincipalHoldsRunClaim`, and calls
  `FailNodeForUnpricedClass` on the run's team handle. Code that reads or
  writes runs, nodes or triggers of a team other than the default goes
  through that team's handle.
