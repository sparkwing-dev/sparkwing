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

- The segment is set only when a GitHub App delivery started the run, or when a retry or child run inherits that repository. Runs from a legacy per-pipeline webhook, the CLI, the API or a schedule carry an empty segment, `team:acme:repository_id::pipeline:...`. A policy for such runs inserts `repository_id::`.
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
`/git/<name>/...`, `/bin/...`, `/cache/...`, `/repos`, `/admin/teams/{team}`,
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

## Unimplemented backend types are removed

Code that imports `TypeGCS`, `TypeAzureBlob` or `TypeMySQL` from
`github.com/sparkwing-dev/sparkwing/pkg/backends` no longer compiles; remove
the reference, since no build ever opened a backend of those types. Nothing
changes at run time: a profile naming `gcs`, `azure-blob` or `mysql` failed at
run start before and still does, now with an error naming the types its
surface accepts.
