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
under its own ref. A grant minted by an older controller keeps the old
team-wide behavior until it expires, so upgrade the controller and the cache
together.

**Why:** a deploy role that trusts `ref:refs/heads/main` relies on branch
protection deciding what runs on `main`; a shared cache let a branch's run put
code into a later `main` run.

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
   them, so leaving them changes nothing but hides what is configured.

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
