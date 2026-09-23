# Migrating to the next release

## Schema 54 refuses an older binary

Schema 54 adds the GitHub App tables and marks pull requests from forks as
untrusted runs, and it records the `untrusted-runs` requirement. A binary that
predates it cannot read the mark, so it refuses to open a migrated store rather
than hand an untrusted run secrets or a metered runner. Roll every controller
and every local CLI sharing the store forward together. See
[GitHub App](../github-app.md).

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

## A runner without the git cache names the repositories it may build

`sparkwing-runner runner` started without `--gitcache` refuses to start until it
has at least one `--allow-repo`. Such a runner fetches, compiles and runs each
run's pipeline code as the user who started it, and any team member who can
trigger a run chose the repository, so the machine's owner now names what it
may build.

- **Before:** `sparkwing-runner runner --controller https://c --also-claim-triggers ...`
- **After:** `sparkwing-runner runner --controller https://c --allow-repo 'github.com/acme/*' --also-claim-triggers ...`

A pattern is a host and path with no scheme; `*` matches within one path
segment, and matching ignores case. Quote a pattern that holds `*`. The runner
sends the list as `allow_repos` with every trigger and node claim to a
controller whose `GET /api/v1/capabilities` advertises `claims.allow_repos`,
and that controller hands it only runs from those repositories. The runner and
controller upgrade in either order: against an older controller the runner
claims without the field, may claim a run outside its list, and fails that run
before fetching anything, with a reason naming the repository and the list. A
runner that sends no list claims as before. A `--github-actions` runner
given no list builds only its own repository, and a runner with `--gitcache` is
unaffected unless given a list.

`POST /api/v1/team/runner-tokens` now requires `repos`, a list of the same
patterns, and the `command` it returns carries one `--allow-repo` per pattern.
A client that sends only `name` gets 400. See
[local-execution.md](../local-execution.md#what-a-laptop-runner-trusts).

## A trigger names one repository

`POST /api/v1/triggers` answers 400 when `git.repo_url`,
`trigger.env.GITHUB_REPOSITORY` and `git.github_owner`/`git.github_repo` name
different repositories. The run page read the GitHub name while a
direct-source runner fetched `git.repo_url`, so such a trigger showed one
repository and ran another. Send only the fields that name the repository you
mean, or make them agree; `https://github.com/acme/app.git`,
`git@github.com:acme/app.git` and `acme/app` agree.
