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

  `--trigger-sources`, `--metrics-addr` and `--dependency-proxy` carry over,
  and `--token` becomes the file `agent-token` under `--credentials-dir`; `--image-pull-policy`, `--kubeconfig`, `--runner-controller-url`
  and `--runner-logs-url` gain the `--trigger-runner-` prefix, and
  `--artifact-store` becomes `--trigger-artifact-store`. Drop
  `--claim-nodes=false` to let the same pod also run nodes. The worker's
  `--log-store` has no runner equivalent: the runner streams logs to `--logs`.
  `--k8s-cpu-ceiling`, `--k8s-memory-ceiling` and `--k8s-job-deadline` become
  `--cpu-ceiling`, `--memory-ceiling` and `--deadline`.
- **Edge cases:** `sparkwing cluster worker`, the CLI's in-process claim loop
  for a profile, is unchanged.

## sparkwing-logs reads flags only

- **Before:** every `sparkwing-logs` flag took its default from an
  environment variable of the same meaning: `SPARKWING_CONTROLLER_URL`,
  `SPARKWING_REQUIRE_AUTH`, `SPARKWING_LOGS_ARCHIVE_STORE`,
  `SPARKWING_LOGS_ARCHIVE_IDLE`, the `SPARKWING_LOGS_*` limits, and
  `SPARKWING_LOGS_EGRESS_*`.
- **After:** the service reads none of them. A variable left in the pod's
  environment is ignored, so the setting it carried falls back to the flag's
  default. A malformed flag value stops the service at startup with an error
  naming the flag.
- **Operator steps:** the `sparkwing-runner-bundle` chart already passes flags
  and needs nothing. In a manifest of your own, move each variable to its flag:

  | Variable | Flag |
  |---|---|
  | `SPARKWING_CONTROLLER_URL` | `--controller` |
  | `SPARKWING_REQUIRE_AUTH` | `--require-auth` |
  | `SPARKWING_LOGS_ARCHIVE_STORE` | `--archive-store` |
  | `SPARKWING_LOGS_ARCHIVE_IDLE` | `--archive-idle` |
  | `SPARKWING_LOGS_MAX_NODE_BYTES` | `--max-node-bytes` |
  | `SPARKWING_LOGS_MAX_RUN_BYTES` | `--max-run-bytes` |
  | `SPARKWING_LOGS_MAX_INFLIGHT_BYTES` | `--max-inflight-bytes` |
  | `SPARKWING_LOGS_MIN_FREE_BYTES` | `--min-free-bytes` |
  | `SPARKWING_LOGS_RETENTION` | `--retention` |
  | `SPARKWING_LOGS_SWEEP_INTERVAL` | `--sweep-interval` |
  | `SPARKWING_LOGS_SEARCH_MAX_BYTES` | `--search-max-bytes` |
  | `SPARKWING_LOGS_SEARCH_TIMEOUT` | `--search-timeout` |
  | `SPARKWING_LOGS_MAX_LINE_BYTES` | `--max-line-bytes` |
  | `SPARKWING_LOGS_BINARY_RATIO` | `--binary-ratio` |
  | `SPARKWING_LOGS_MAX_STORE_BYTES` | `--max-store-bytes` |
  | `SPARKWING_LOGS_MAX_STORE_OBJECTS` | `--max-store-objects` |
  | `SPARKWING_LOGS_WARN_STORE_BYTES` | `--warn-store-bytes` |
  | `SPARKWING_LOGS_WARN_STORE_OBJECTS` | `--warn-store-objects` |
  | `SPARKWING_LOGS_STORE_RECONCILE` | `--store-reconcile` |
  | `SPARKWING_LOGS_EGRESS_DAILY_ALARM_BYTES` | `--egress-daily-alarm-bytes` |
  | `SPARKWING_LOGS_EGRESS_MAX_DOWNLOADS` | `--egress-max-downloads` |
  | `SPARKWING_LOGS_EGRESS_MAX_LOG_STREAMS` | `--egress-max-log-streams` |

- **Edge cases:** `SPARKWING_REQUIRE_AUTH` also accepted `yes` and `on`;
  `--require-auth` is a boolean flag, so pass it bare. A retention set through
  `SPARKWING_LOGS_RETENTION` used to count as named and kept an archived
  service off its 90-day default; only `--retention` counts now.
  `SPARKWING_S3_ENDPOINT` is still read, because it has no flag yet.

## sparkwing-cache reads flags and a credentials directory

- **Before:** `sparkwing-cache` took each flag's default from an environment
  variable, including eleven without the `SPARKWING_` prefix, and its two
  secrets from `SPARKWING_API_TOKEN` and `SPARKWING_CACHE_GRANT_KEY` or the
  `--api-token` and `--grant-key` flags. A value it could not parse was
  ignored: a malformed duration or boolean kept the default without a word, and
  a malformed byte count printed a warning and stayed unlimited.
- **After:** the cache reads settings from flags alone and its secrets from
  `--credentials-dir`, a directory holding one file per secret: `cache-token`
  (the operator token) and `cache-grant-key`. An absent file turns that
  feature off, as an unset variable did. `--api-token` and `--grant-key` are
  gone, because a flag value shows in `/proc/<pid>/cmdline`. A malformed flag
  value now stops the cache at startup with an error naming the flag; so does
  a `--credentials-dir` that names no directory.
- **Operator steps:** the `sparkwing-runner-bundle` chart projects
  `cache.tokenSecret` and `cache.grantKeySecret` into
  `/etc/sparkwing/credentials` and passes the flags; upgrading the chart needs
  no value changes. In a manifest of your own, mount the two Secret keys as
  files and move each variable to its flag:

  ```yaml
  args:
    - --credentials-dir
    - /etc/sparkwing/credentials
  volumeMounts:
    - name: credentials
      mountPath: /etc/sparkwing/credentials
      readOnly: true
  volumes:
    - name: credentials
      projected:
        defaultMode: 0400
        sources:
          - secret:
              name: sparkwing-cache-token
              items: [{key: token, path: cache-token}]
          - secret:
              name: sparkwing-cache-grant-key
              items: [{key: key, path: cache-grant-key}]
  ```

  | Variable or flag | Use instead |
  |---|---|
  | `SPARKWING_API_TOKEN`, `--api-token` | the file `cache-token` under `--credentials-dir` |
  | `SPARKWING_CACHE_GRANT_KEY`, `--grant-key` | the file `cache-grant-key` under `--credentials-dir` |
  | `PORT`, `PORT_ADDR` | `--addr` |
  | `DATA_DIR` | `--data-dir` |
  | `PROXY_CACHE_DIR` | `--proxy-cache-dir` |
  | `PROXY_CACHE_TTL` | `--proxy-cache-ttl` |
  | `PROXY_MAX_AGE` | `--proxy-max-age` |
  | `FETCH_INTERVAL` | `--fetch-interval` |
  | `FETCH_FRESH_WINDOW` | `--fetch-fresh-window` |
  | `RECLONE_COOLDOWN` | `--reclone-cooldown` |
  | `GITCACHE_REPOS` | `--auto-register-repos` |
  | `SSH_KEY_DIR` | `--ssh-key-dir` |
  | `SPARKWING_CONTROLLER_URL` | `--controller` |
  | `SPARKWING_METRICS_ADDR` | `--metrics-addr` |
  | `SPARKWING_CACHE_PUBLIC_URL` | `--public-url` |
  | `SPARKWING_CACHE_TRUST_FORWARDED_HOST` | `--trust-forwarded-host` |
  | `SPARKWING_CACHE_ALLOW_UNAUTHENTICATED` | `--allow-unauthenticated` |
  | `SPARKWING_CACHE_BLOB_STORE` | `--blob-store` |
  | `SPARKWING_CACHE_MAX_ARCHIVE_BYTES` | `--max-cache-archive-bytes` |
  | `SPARKWING_CACHE_MAX_STORE_BYTES` | `--max-store-bytes` |
  | `SPARKWING_CACHE_MAX_STORE_OBJECTS` | `--max-store-objects` |
  | `SPARKWING_CACHE_WARN_STORE_BYTES` | `--warn-store-bytes` |
  | `SPARKWING_CACHE_WARN_STORE_OBJECTS` | `--warn-store-objects` |
  | `SPARKWING_CACHE_STORE_RECONCILE` | `--store-reconcile` |
  | `SPARKWING_CACHE_PROXY_MAX_BYTES` | `--proxy-max-bytes` |
  | `SPARKWING_CACHE_EGRESS_DAILY_ALARM_BYTES` | `--egress-daily-alarm-bytes` |

- **Edge cases:** a cache whose environment carried a malformed value used to
  start on the default; with the value moved to its flag it refuses to start,
  so check the value before the upgrade. `--allow-unauthenticated` is a boolean
  flag: pass it bare, because `--allow-unauthenticated=yes` is refused.
  The controller still reads the same token as `SPARKWING_CACHE_TOKEN` and the
  grant key as `SPARKWING_CACHE_GRANT_KEY`; when it moves to a credentials
  directory it reads the same `cache-token` and `cache-grant-key` file names,
  so one projected Secret volume will serve both. `SPARKWING_S3_ENDPOINT`,
  `SPARKWING_LOG_FORMAT` and `SPARKWING_LOG_LEVEL` are still read.

## sparkwing-runner reads flags and a credentials directory

- **Before:** `sparkwing-runner runner` and `sparkwing-runner launch` took
  their bearer from `SPARKWING_AGENT_TOKEN` or `--token`, and `runner` seeded
  most flags from `SPARKWING_*` variables. The Kubernetes Job ceilings,
  deadline and team-node switch existed only as `SPARKWING_K8S_CPU_CEILING`,
  `SPARKWING_K8S_MEMORY_CEILING`, `SPARKWING_K8S_JOB_DEADLINE` and
  `SPARKWING_RUNNER_TEAM_NODES` on the runner pod, which each trigger's
  `handle-trigger` child inherited and read.
- **After:** both commands read their bearer from the file `agent-token` under
  `--credentials-dir`; `--token` is gone, because a flag value shows in
  `/proc/<pid>/cmdline`. The runner takes `--cpu-ceiling`, `--memory-ceiling`,
  `--deadline` and `--team-nodes`, the launcher's names, validates them at
  startup, and hands them to each trigger as `handle-trigger` flags.
  `handle-trigger` no longer reads those four variables, nor
  `SPARKWING_RUNNER_SA`, `SPARKWING_IMAGE_PULL_POLICY` and
  `SPARKWING_DEPENDENCY_PROXY_URL`, which the runner already passed as flags.
  `POD_NAME`, `POD_NAMESPACE`, `KUBECONFIG` and the GitHub Actions variables
  are still read, because other tools define them. The token a Job or a
  trigger child receives from Sparkwing still crosses in
  `SPARKWING_AGENT_TOKEN`; that is Sparkwing's own protocol, not a setting.
- **Operator steps:** the `sparkwing-runner-bundle` chart projects
  `controller.tokenSecret` as `agent-token` and passes
  `runner.jobCeiling` as flags; upgrading needs no value changes. A Helm
  user who set `SPARKWING_K8S_JOB_DEADLINE` or `SPARKWING_RUNNER_TEAM_NODES`
  through `runner.extraEnv` moves them to the new values `runner.jobDeadline`
  and `runner.teamNodes`. An external
  gitcache moves from a `SPARKWING_GITCACHE_URL` entry in `runner.extraEnv` to
  `runner.gitcacheUrl`. In a manifest of your own, mount the token Secret as a
  file and move each variable to its flag:

  | Variable or flag | Use instead |
  |---|---|
  | `SPARKWING_AGENT_TOKEN`, `--token` | the file `agent-token` under `--credentials-dir` |
  | `SPARKWING_K8S_CPU_CEILING` | `sparkwing-runner runner --cpu-ceiling` |
  | `SPARKWING_K8S_MEMORY_CEILING` | `--memory-ceiling` |
  | `SPARKWING_K8S_JOB_DEADLINE` | `--deadline` |
  | `SPARKWING_RUNNER_TEAM_NODES` | `--team-nodes` |
  | `SPARKWING_CONTROLLER_URL` | `--controller` |
  | `SPARKWING_LOGS_URL` | `--logs` |
  | `SPARKWING_GITCACHE_URL` | `--gitcache` |
  | `SPARKWING_RUNNER_SA` | `--trigger-runner-sa` |
  | `SPARKWING_CACHE_URL` | `--trigger-artifact-store` |
  | `SPARKWING_DEPENDENCY_PROXY_URL` | `--dependency-proxy` |
  | `SPARKWING_IMAGE_PULL_POLICY` | `--trigger-runner-image-pull-policy` |
  | `SPARKWING_WARM_MODULES` | `--warm-modules` |
  | `SPARKWING_LOCAL_RESERVE` | `--local-reserve` |
  | `SPARKWING_TEAM` | `--team` |

  A machine connected from the dashboard runs the new command the machines
  page prints, which writes the token to
  `$HOME/.config/sparkwing/runner-credentials/agent-token` with mode `0600`
  before it starts the runner.
- **Edge cases:** `--team-nodes` reaches a trigger as
  `handle-trigger --runner-team-nodes`, which a pipeline built from an SDK
  older than v0.66.0 rejects; the runner passes it only when set. A pipeline
  built from an SDK older than this release still reads the four Job
  variables if they remain in the runner pod's environment, so delete them
  rather than leaving them beside the flags. `sparkwing-runner agent`, which
  reads `config.yaml`, is unchanged.

## Leftover variable names are removed

- **Before:** `SPARKWING_GITCACHE` named a gitcache for the SDK's clone helper
  ahead of `SPARKWING_GITCACHE_URL`.
- **After:** the clone helper reads `SPARKWING_GITCACHE_URL` alone.
- **Author or operator steps:** rename `SPARKWING_GITCACHE` to
  `SPARKWING_GITCACHE_URL`.
- **Edge cases:** `SPARKWING_TOKEN`, `SPARKWING_TRIGGER_CLAIM_GENERATION`,
  `SPARKWING_TRIGGER_GENERATION` and `SPARKWING_ATTEMPT_ORDINAL` were stripped
  from child environments although nothing set or read them; they no longer
  appear in the code.

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
| `sparkwing configure profiles rm NAME`, `... delete NAME` | `sparkwing cloud disconnect --name NAME --keep-token` |
| `sparkwing configure profiles dup ...` | none; see [Connection verbs fold into cloud](#connection-verbs-fold-into-cloud) |
| `sparkwing secrets rm ...`, `sparkwing secrets remove ...` | `sparkwing secrets delete ...` |
| `sparkwing pipeline sparks ls`, `... rm ...` | `sparkwing pipeline sparks list`, `... remove ...` |
| `sparkwing configure xrepo ls`, `... rm ...` | `sparkwing repos list --checkouts`, `sparkwing repos remove ...` |
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

## Connection verbs fold into cloud

| Before | After |
|---|---|
| `sparkwing configure profiles add --name N --controller URL --token-stdin` | `sparkwing cloud connect --name N --controller URL --token-stdin` |
| `sparkwing configure profiles add --name N --controller URL` (controller not up, or unauthenticated) | `sparkwing cloud connect --name N --controller URL --no-probe` |
| `sparkwing configure profiles add ... --token T` | `printf %s "$T" \| sparkwing cloud connect ... --token-stdin` |
| `sparkwing configure profiles remove --name N` | `sparkwing cloud disconnect --name N --keep-token` |
| `sparkwing configure profiles test --profile P [-o json]` | `sparkwing cloud status --profile P [-o json]` |
| `sparkwing cluster status --profile P [-o json]` | `sparkwing cloud status --profile P --cluster [-o json]` |
| `sparkwing profile [--profile P] [-o json]` | `sparkwing configure profiles show [--profile P] [-o json]` |
| `sparkwing cluster tokens lookup --prefix X --profile P` | `sparkwing cluster tokens list --prefix X --profile P` |
| `sparkwing configure profiles duplicate --src A --dst B` | copy the `A:` entry under `profiles:` in config.yaml to `B:` |

- **`cloud connect` probes by default:** it checks the controller answers
  unless `--no-probe` is given, and refuses an existing profile name unless
  `--force` is given, as `configure profiles add` refused one.
  `--token` on the command line is gone; pipe the token to `--token-stdin`.
- **`cloud status` output:** a superset of `configure profiles test`: the same
  probe table and exit code, plus the principal, scopes and dashboard when the
  profile names a controller. JSON adds `controller`, `principal`, `scopes`,
  `token_prefix` and `dashboard` beside `profile`, `probes` and `ok`.
- **`cloud status --cluster`** prints the old `cluster status` report in the
  same shape, with the same exit codes.
- **`configure profiles show`** keeps `--name NAME [--show-token]` for one
  config.yaml entry; without `--name` it prints the resolution report
  `sparkwing profile` printed.

## Repository verbs gather under repos

| Before | After |
|---|---|
| `sparkwing configure xrepo add [path]` | `sparkwing repos add [path]` |
| `sparkwing configure xrepo remove <path-or-basename>` | `sparkwing repos remove <path-or-basename>` |
| `sparkwing configure xrepo prune` | `sparkwing repos prune` |
| `sparkwing configure xrepo list [--pipelines=false] [-o json]` | `sparkwing repos list --checkouts [--pipelines=false] [-o json]` |
| `sparkwing repos [-o json]` | `sparkwing repos list [-o json]` |
| `sparkwing update --sdk [--version V]` | `sparkwing repos update --in-place [--version V]` |
| `sparkwing update --sdk --check` | `sparkwing repos update --in-place --check` |

- `repos update --in-place` runs the old `update --sdk` path unchanged: native
  `go get` for the resolved release, then `go mod tidy`, in the checkout you
  stand in or the one `sparkwing -C DIR` names. It neither compares plans nor
  commits, and it refuses `--apply`, `--verify` and `--repo`, which act on the
  tracked fleet.
- `--in-place --check` keeps the exit codes: 0 current or ahead, 1 an update
  is available, 2 unknown, diverged or a failed check.
- `sparkwing update` updates the CLI only. `--cli` still names that target;
  `--force` and `--override-hold` are unchanged.

## daemon explain moves to daemon events --explain

| Before | After |
|---|---|
| `sparkwing daemon explain --run ID [-o json]` | `sparkwing daemon events --run ID --explain [-o json]` |

The output and exit codes are unchanged.

## cluster image rollout is removed

`sparkwing cluster image rollout --image NAME --tag TAG [--wait] [--tail-logs]`
edited `images[].newTag` in a gitops checkout's kustomization.yaml, committed
and pushed, ran `argocd app sync`, and waited with `kubectl rollout status`.
Run those steps from a pipeline job instead:

```bash
cd "$GITOPS_REPO" && kustomize edit set image "NAME=REGISTRY/NAME:TAG"
git commit -am "rollout NAME TAG" && git push
argocd app sync APP && kubectl rollout status deploy/NAME -n NAMESPACE
```

`SPARKWING_GITOPS_REPO` is no longer read.

## Pipeline steps no longer see SPARKWING_AGENT_TOKEN

- **Before:** the pipeline binary kept the runner's `SPARKWING_AGENT_TOKEN` in
  its environment, so every command a step started inherited it, and a step
  that printed it showed the raw value in the run's logs. `.CacheDir` used it
  as the cache bearer when no `SPARKWING_CACHE_GRANT` or
  `SPARKWING_CACHE_TOKEN` was set.
- **After:** the pipeline binary removes the token from its environment when
  it starts and masks it in node output. Commands a step starts do not see it,
  and `.CacheDir` sends only the cache grant or cache token.
- **Author steps:** a step that called the controller with
  `$SPARKWING_AGENT_TOKEN` uses the SDK instead (`sparkwing.Secret`,
  `sparkwing.RunAndAwait`), which reaches the controller through the node's own
  connection.
- **Operator steps:** a cache that accepted the agent token because its API
  token was set to the same value now needs the controller to mint cache grants
  (`SPARKWING_CACHE_GRANT_KEY` on the controller and the cache), or the dependency cache runs without the service.

## cmd/sign-manifest is removed

- **Before:** `go run ./cmd/sign-manifest -genkey` printed an Ed25519 keypair,
  and the same helper could sign and verify a manifest.
- **After:** the helper is gone. The release workflow signs and verifies
  with `cmd/verify-release`.
- **Operator steps:** generate a seed with `openssl rand -base64 32`, store it
  as `SPARKWING_UPDATE_SIGNING_KEY`, and print its public key with
  `SPARKWING_RELEASE_SIGNING_KEY=<seed> go run ./cmd/verify-release --public-key`.

## Storage and store names that moved

- **Before:** `storeurl.OpenS3`, `storeurl.SDKMaxAttempts` and
  `storeurl.SDKMaxBackoff` lived in `pkg/storage/storeurl`.
- **After:** they are `Open`, `SDKMaxAttempts` and `SDKMaxBackoff` in
  `pkg/storage/s3`, beside `ParseURL` and `NewClient`. Behaviour is unchanged.
- **Author steps:** replace `storeurl.OpenS3(ctx, raw)` with `s3.Open(ctx, raw)`
  from `github.com/sparkwing-dev/sparkwing/pkg/storage/s3`.

## The controller's dispatcher hook is removed

- **Before:** `controller.Server.WithDispatcher` took a `controller.Dispatcher`
  that was called with a `controller.RunRequest` after each trigger was
  recorded, defaulting to `controller.NoopDispatcher`.
- **After:** the types and the option are gone. A trigger is recorded and a
  runner claims it by polling, as with the default before.
- **Author steps:** remove the `WithDispatcher` call. Start work for a recorded
  trigger by claiming it (`POST /api/v1/triggers/claim`, or
  `client.ClaimTriggerFor`).

## Removed store and release helpers

- **Before:** `store.Store.ChargeNodeCredits` billed a node's elapsed seconds
  directly, and `go run ./cmd/sign-manifest -genkey` printed a signing key.
- **After:** both are gone; node renewal, finish and launch settlement bill
  inside their own transactions.
- **Author steps:** settle a node with `FinalizeNodeCredits`; generate a release
  seed as described above under `cmd/sign-manifest is removed`.

## The job-args schema builder is removed

**Before:** a job embedding `sparkwing.WithArgs[T]` turned every exported
field of `T` into a kebab-cased flag, and declared constraints in Go
through a `Schema()` method built with `sparkwing.NewSchema[T]()`.
`sparkwing.Arg[T]` and `ArgOrDefault` read any job's arg by flag name.

```go
type DeployArgs struct {
    Replicas int
    Image    string
    Strategy string
}

func (DeployJob) Schema() (*sparkwing.Schema, error) {
    s := sparkwing.NewSchema[DeployArgs]()
    s.Field("Replicas").Default(3)
    s.Field("Image").Required()
    s.Field("Strategy").Default("rolling").OneOf("rolling", "recreate")
    return s.Build()
}
```

**After:** `T` uses the pipeline Inputs tags, and only `flag:`-tagged
fields are flags. Required, default and enum checks run before any step,
as they do for Inputs.

```go
type DeployArgs struct {
    Replicas int    `flag:"replicas" default:"3"`
    Image    string `flag:"image" required:"true"`
    Strategy string `flag:"strategy" default:"rolling" enum:"rolling,recreate"`
}
```

**Author steps:**

1. Add a `flag:"name"` tag to every `T` field that should stay a flag,
   using the kebab-cased name the field had (`MainSourceRoot` was
   `--main-source-root`). An untagged field now fails as an unknown flag
   when passed.
2. Move constraints into tags and delete the `Schema()` method:

| Removed | Use instead |
|---|---|
| `.Required()` | `required:"true"` |
| `.Default(v)` | `default:"v"` |
| `.OneOf(a, b)` | `enum:"a,b"` (string fields; needs `default` or `required`) |
| `.Min`, `.Max`, `.Range`, `.Positive`, `.Custom` | check the value at the top of the step and return an error |
| `.Computed(fn)`, `.DependsOn` | compute the value inside the step from `j.Args(ctx)` |
| `.RequiredWhen(p)`, `Group(...)`, predicates | validate the combination inside the step and return an error |
| `.Bind("target")` | a `flag:"target"` tag; `Bind` only checked its argument |
| `sparkwing.Arg[T](ctx, flag)`, `ArgOrDefault` | `j.Args(ctx)` on the job that declares the flag, or a pipeline Inputs field read in `Plan` |

Move a `secret:"true"` arg to the pipeline's Inputs: the run masks only
pipeline secrets, so a job secret panics at registration.

Tools that read `Plan.TransitiveArgsSurface` or `JobArgSchemas` use
`Plan.JobArgs`, which returns `DescribeArg` records stamped with the
owning job id.

## Six unused SDK names are removed

**Before:** the `sparkwing` package exported `Cache`, `Logs` and `State` (type
aliases for the `pkg/storage` interfaces), `TypeName`, `FailureFromContext` and
`(*SpawnSpec).ResolvedID`.

**After:** none of them exist. No pipeline, sparks library or guide used them.

**Author steps:**

| Removed | Use instead |
|---|---|
| `sparkwing.Cache`, `sparkwing.Logs`, `sparkwing.State` | `storage.ArtifactStore`, `storage.LogStore`, `storage.StateStore` from `github.com/sparkwing-dev/sparkwing/pkg/storage` |
| `sparkwing.TypeName(p)` | `reflect.TypeOf(p).Elem().Name()` for a pointer, or `reflect.TypeOf(p).Name()` |
| `sparkwing.FailureFromContext(ctx)` | the `sparkwing.Failure` argument an `OnFailure` handler already receives |
| `(*SpawnSpec).ResolvedID()` | nothing; it always returned `""` |

The `sparkwing-web` service is gone: the controller serves the dashboard,
sign-in, and the browser flows on its own listener. Operators repoint the
console host at the controller and delete the web Deployment; `sparkwing
serve` users change nothing.

## The controller serves the dashboard

**Before:** `sparkwing-web` hosted the dashboard bundle, the login page, OAuth
and GitHub App browser flows, and a proxy that forwarded an allowlist of
`/api/v1/*` routes to the controller and log reads to the logs service. The
`sparkwing-full` chart deployed it as `<release>-web`, and its Ingress routed
there.

**After:** `sparkwing-controller` serves the dashboard pages, `/login`,
password and first-admin sign-in, logout, `/auth/{provider}/start` and
`/callback`, identity linking at `/auth/{provider}/link`, the GitHub App pages
under `/github/app/`, and the dashboard's log, grep, event-stream and capacity
reads, on the same port as its API. Session cookies are `__Host-sw_session` and
`__Host-sw_csrf`. Log reads go to the logs service named by the controller's
`--logs-url`, carrying the caller's own credential. Pages require sign-in
whenever the controller enforces token auth. A controller built without
`bin/build-web.sh` still starts, and its pages answer 503 naming that step.
The chart deploys no web Deployment or Service, and its Ingress routes every
host to the controller Service.

**Upgrade:**

1. Point the console host's Ingress or load balancer at the controller
   Service. A console host and an API host can both point at it; a reverse
   proxy in front can still split them.
2. Delete the `sparkwing-web` Deployment and Service. A `sparkwing-full`
   upgrade removes them and names the Ingress after the controller. A
   values file that still sets `web` fails to render: move `web.logs.url` to
   `controller.logs.url` and drop the rest.
3. Move `sparkwing-web` flags to the controller:

   | `sparkwing-web` | Controller |
   |---|---|
   | `--require-login` | Follows the controller's own auth: pages require sign-in whenever it enforces tokens |
   | `--hsts` | `--hsts` |
   | `--trusted-proxy-addr` | `--trusted-proxy-addr`, which also reads `X-Forwarded-Proto` as TLS evidence |
   | `SPARKWING_WEB_INSECURE_COOKIES` with `--allow-insecure-cookies-remote` | `--insecure-cookies` |
   | `--controller`, `--logs`, `--token`, `--profile`, `--state-spec`, `--logs-spec`, `--artifacts-spec` | None; the controller reads its own state and uses `--logs-url` |
   | `--allow-unauthenticated-remote`, `--allow-origin` | None; the controller refuses cross-site browser writes to `/api/` |

   Chart users: `ingress.tls` passes `--hsts`, and `ingress.allowInsecure=true`
   without TLS passes `--insecure-cookies`.
4. A dashboard published over plain HTTP needs `--insecure-cookies` (chart:
   `ingress.allowInsecure=true`), or browsers drop the `Secure` session cookies.
5. Register the OAuth and GitHub App callback URLs on the host that now
   reaches the controller, and list the sign-in callbacks in
   `--oauth-redirect-uris`. The paths are unchanged.

`sparkwing serve` runs the controller in local mode with the dashboard
attached and needs nothing.

**Removed routes:** `POST /api/v1/auth/oauth/{google,github}/{start,exchange}`,
`POST /api/v1/me/identities/{provider}/link` and `/link/complete`,
`POST /api/v1/team/github-app/connect`, `/connect/available`,
`/connect/select` and `/connect/complete`, `GET /api/v1/operator/session` and
`GET /api/v1/auth/bootstrap-needed` answer 404. No CLI command called them; a
client of your own drives the browser pages that replace them.
`/api/v1/auth/login`, `/api/v1/auth/session` and `/api/v1/auth/logout` remain.

**Why:** two processes split one browser surface, and the proxy's route
allowlist and service bearer were a second authorization layer to keep in
step with the controller's own.

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
