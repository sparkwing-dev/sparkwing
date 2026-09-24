# Migrating to the next release

## Default and operator cache expires after 30 days

Before starting an upgraded controller with `--cache-blob-store`, back up or
export its `cache/` object-store prefix to a separate location. Include both
`cache/teams/default/` and the operator token's root objects under
`cache/{bins,cache,artifacts}/`. Verify the copy before starting the new
controller. A database backup alone cannot recover deleted object bytes.

The controller starts its storage pass at startup. Its first successful pass
deletes cache objects last written more than 30 days ago in every team
namespace and the operator token's root. Reads do not extend the age. It
also removes expired default-team direct-object rows, so copying S3 bytes
back alone does not restore their signed-download visibility. Keep the
object and database backups together if rollback or recovery is needed.
Controllers without `--cache-blob-store` do not run this object-store pass.

## Cloud operator commands leave the public CLI

Sparkwing Cloud operators switch credit, storage allowance, refund, freeze,
and token metering operations to the private `sparkwing-ops` CLI. The public
`sparkwing cluster credits` group and `sparkwing cluster tokens set-metered`
command are removed. Public `sparkwing cluster tokens create` no longer accepts
`--metered`; create or mark metered runner tokens with the private tool.

Self-hosted token and runner administration and object-store controls stay in
`sparkwing`.
The controller's HTTP routes are unchanged, and team owners still see their
own billing in the dashboard.

## Upgrading a controller from v0.60.0

v0.60.0 runs schema v47. This release migrates the database to v69 when the
controller first starts, and a v0.60.0 binary cannot open it afterwards, so
the backup is the only way back.

1. Read the sections below for anything your deployment configures.
2. Stop the controller and back up its database: the Postgres database, or
   the SQLite `state.db` under the controller's `SPARKWING_HOME`.
3. Upgrade the controller and the CLI, in either order. A released CLI still
   works against the new controller.
4. Start the controller with the same `SPARKWING_SECRETS_KEY` it ran with. Its
   first start migrates the schema and reseals stored secrets, and logs how
   many it resealed.
5. Verify: the startup line reads `runs-store schema 69`,
   `GET /api/v1/health` answers, and `sparkwing runs list` shows your history.

To roll back, stop the controller, restore the backup, and start v0.60.0.

## Metering needs a signed license

A controller without a signed `metering` or `multi-team` feature no longer
serves credit or team billing routes, accepts metered token changes, checks
balances at claim time, or writes credit charges. Its dashboard hides Billing,
and its storage-tier checks give every team unlimited room. Operator-set
storage quotas still apply.

An existing signed `multi-team` license includes metering without re-issuance.
For a deployment that used credits without a multi-team license, contact Korey
for a metering license and help running sparkwing-ops before upgrading. An
unlicensed deployment can upgrade without a data migration; stored credit
rows and token markers remain dormant.

## Dashboard session and CSRF cookies carry the `__Host-` prefix

On a dashboard that keeps `Secure` cookies, the session and CSRF cookies are
now named `__Host-sw_session` and `__Host-sw_csrf` rather than `sw_session` and
`sw_csrf`. A browser refuses a `__Host-` cookie that carries a `Domain`
attribute, which is the write a sibling host under the same registrable domain
would use to plant a session on a victim.

Every signed-in browser is signed out once on upgrade, because the old cookie
names are no longer read. Signing in again is the whole of the recovery.

A custom browser client that reads the CSRF token out of `document.cookie` to
fill `X-CSRF-Token` reads `__Host-sw_csrf` first and falls back to `sw_csrf`,
because a deployment running `SPARKWING_WEB_INSECURE_COOKIES=1` drops the
prefix along with `Secure`. See [auth.md](../auth.md#dashboard-authorization) for
the cookie contract.

## Runners carry a cache grant instead of the cache token

A runner no longer reads `SPARKWING_CACHE_TOKEN`. After each claim it asks the
controller's `POST /api/v1/runs/<run>/cache-grant` for a grant naming the
run's team, and the run's cache traffic carries that grant. The token let any
team's pipeline read and replace every other team's cached binaries, which the
other team's launcher then executed.

- Delete `cache_token` from `agent.yaml`; the agent refuses to load a file
  that still carries it. The installer no longer writes it.
- The runner-bundle chart no longer sets `SPARKWING_CACHE_TOKEN` on the runner.
  A runner deployed by hand should drop it too, because the pipeline binary
  can read its launcher's environment.
- The operator's own runners (a runner token in the `default` team) keep
  building private repositories the cache clones over SSH: their runs' grants
  read and register any mirror. Point their `--gitcache` (or
  `SPARKWING_GITCACHE_URL`) at the cache itself. The controller's
  `/api/v1/gitcache` proxy serves an operator runner only its claimed run's own
  source or a repository connected to that run's pipeline, and serves no other
  team.
- The controller must hold the cache's token (`SPARKWING_CACHE_TOKEN` on the
  controller) to mint grants. Until it does, or on a controller that predates
  the route, runs go without the binary cache and compile instead.
- The pipeline binary a trigger runs no longer inherits the launcher's whole
  environment. It gets the Go toolchain, proxy, locale and Kubernetes service
  variables, `AWS_REGION`, `SPARKWING_*` and `OTEL_*` settings that are not
  credentials, and the run's own `SPARKWING_AGENT_TOKEN` and
  `SPARKWING_CACHE_GRANT`. A pipeline that relied on another launcher variable
  should receive it as a secret instead.

## The cache's token and grant key are Secrets of their own

The runner-bundle chart used `controller.tokenSecret`, the runner's own
token, as the cache's operator token and, through it, as the key cache grants
were signed with. Pipeline code can read the runner's token, so any team's
pipeline could mint a grant for another team and replace the binaries that
team's launcher runs. The cache now reads its operator token from
`cache.tokenSecret` and its grant key from `cache.grantKeySecret`, and the
full chart hands the controller the same two as `SPARKWING_CACHE_TOKEN` and
`SPARKWING_CACHE_GRANT_KEY`.

1. Create two random Secrets, neither of them a runner token:

   ```bash
   kubectl -n sparkwing create secret generic sparkwing-cache-token \
       --from-literal=token="$(openssl rand -hex 32)"
   kubectl -n sparkwing create secret generic sparkwing-cache-grant-key \
       --from-literal=key="$(openssl rand -hex 32)"
   ```

2. Set `cache.tokenSecret.name` and `cache.grantKeySecret.name` (under
   `sparkwing-runner-bundle.` in the full chart). The chart refuses to render
   when any two of `controller.tokenSecret`, `cache.tokenSecret` and
   `cache.grantKeySecret` name the same Secret key.
3. A controller outside the chart needs the new cache token as
   `SPARKWING_CACHE_TOKEN` and the grant key as `SPARKWING_CACHE_GRANT_KEY`.
   Anything else that called the cache with the old shared token, such as an
   operator's shell, switches to the new cache token.

Grants minted before the upgrade stop verifying, and runs mint fresh ones on
their next claim.

## A metered pool runs trigger nodes through node claims

Credits are charged on node claims, and a trigger holder that runs nodes in
its own process never makes one. A metered token's trigger claim now names its
node runner, and the controller refuses `inprocess` with `403`
`metered_inprocess_nodes`.

- Set `runner.triggerRunner.kind` to `k8s` or `warm` on a runner-bundle
  install whose token is metered, or pass `--trigger-runner=k8s` (or `warm`)
  to `sparkwing-runner runner`. A pool left on `inprocess` stops its trigger
  loop with that reason and keeps claiming nodes.
- A client that claims triggers directly sends `"node_runner": "k8s"` or
  `"warm"` in the claim body when its token is metered.
- A metered `k8s` or `warm` claim answers `402` while the team's balance cannot
  cover the cheapest class's first minute, and the trigger stays pending.

## BoundCipher takes the owning team

`controller.BoundCipher` binds an envelope to the team that owns its row.
`SealBound(name, scope, shared, masked, plain)` is now
`SealBound(team, name, scope, shared, masked, plain)`, and `OpenBound` gains the
same leading `team`. A custom cipher passed to `WithSecretsCipher` adds the
parameter and includes it in its additional authenticated data;
`ciphertest.TestBoundCipher` now fails an implementation whose envelope opens
under another team.

A custom cipher that also implements `controller.LegacyCipher`
(`OpenLegacy(name, scope, shared, masked, envelope)`) lets the controller
reseal the envelopes it wrote before this release. Without it those rows are
logged and left as they are at startup, and each read of one answers `500`
until the secret is set again.

A self-hosted controller using the built-in cipher needs no change. Its first
start reseals every row in place and logs how many it resealed; keep the same
`SPARKWING_SECRETS_KEY` across the upgrade.

## A runner without the git cache fetches with the credential the controller releases

`sparkwing-runner runner` started without `--gitcache` fetches each run's
source straight from its host, with the credential the controller releases for
the run on `POST /api/v1/runs/{id}/git-credential`: the team's GitHub App
token when an installation the team holds covers the repository, else the git
credential the team stored for the host. A run with neither fails before
anything is fetched, with a message naming both remedies. Such a runner never
falls back to the machine's own git credentials.

A runner its owner fences with `--allow-repo` is the exception: it uses the
controller's credential when one is released and otherwise the machine's own,
which is how a laptop runner builds what its owner can read.

- **Before:** `sparkwing-runner runner --controller https://c --allow-repo 'github.com/acme/*' --github-app-source --also-claim-triggers ...`
- **After, a cloud runner:** `sparkwing-runner runner --controller https://c --also-claim-triggers ...`
- **After, a laptop runner:** `sparkwing-runner runner --controller https://c --allow-repo 'github.com/acme/*' --also-claim-triggers ...`

`--github-app-source` and `SPARKWING_GITHUB_APP_SOURCE` are removed; the App
token is the first choice for every direct runner. A runner that relied on the
machine's own credentials without `--allow-repo` must either gain the list or
have the team connect the GitHub App or store a git credential for the host.

A pattern is a host and path with no scheme; `*` matches within one path
segment, and matching ignores case. Quote a pattern that holds `*`. The runner
sends the list as `allow_repos` with every trigger and node claim to a
controller whose `GET /api/v1/capabilities` advertises `claims.allow_repos`,
and that controller hands it only runs from those repositories. The runner and
controller upgrade in either order: against an older controller the runner
claims without the field, may claim a run outside its list, and fails that run
before fetching anything, with a reason naming the repository and the list.
Against a controller without the git-credential route, a runner asks for the
run's App source token instead. A `--github-actions` runner given no list
builds only its own repository, and a runner with `--gitcache` is unaffected
unless given a list.

`POST /api/v1/team/runner-tokens` now requires `repos`, a list of the same
patterns, and the `command` it returns carries one `--allow-repo` per pattern.
A client that sends only `name` gets 400. See
[local-execution.md](../local-execution.md#what-a-laptop-runner-trusts).

The sparkwing-runner-bundle chart no longer refuses
`runner.alsoClaimTriggers=true` without a gitcache.

## A trigger names one repository

`POST /api/v1/triggers` answers 400 when `git.repo_url`,
`trigger.env.GITHUB_REPOSITORY` and `git.github_owner`/`git.github_repo` name
different repositories. The run page read the GitHub name while a
direct-source runner fetched `git.repo_url`, so such a trigger showed one
repository and ran another. Send only the fields that name the repository you
mean, or make them agree; `https://github.com/acme/app.git`,
`git@github.com:acme/app.git` and `acme/app` agree.

## Execution attribution

Schema 68 adds a defaulted `run_id` column to `github_runner_credentials`.
The store migrates on open. Existing credentials retain an empty run ID, so
their attempts can show the repository but cannot recover the workflow run.
New GitHub Actions credential exchanges record the verified OIDC run ID.

Upgrade every process sharing a runs store before relying on the new execution
history fields. Older binaries do not record the new runner identity, and a
mixed deployment can still produce attempts with incomplete attribution.
