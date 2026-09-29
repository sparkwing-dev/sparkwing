# Migrating to the next release

## One config.yaml for machine settings

Sparkwing reads every machine setting from one file,
`~/.config/sparkwing/config.yaml` (or `$XDG_CONFIG_HOME/sparkwing/config.yaml`),
instead of one file per setting. Each old file becomes a section:

| Old file | Section of config.yaml |
|---|---|
| `admission.yaml` | `admission` (same keys) |
| `budget` | `admission.budget` (the file's one setting line) |
| `agent.yaml` | `agent` (same keys) |
| `fleet.yaml` | `fleet` (same keys) |
| `profiles.yaml` | `profiles` (the file's `profiles:` map) |
| `repos.yaml` | `repos` (same keys, `repos` and `fallback_paths`) |

`version-hold` stays where it is. `secrets.env` and `config.env` are replaced
by the local secret store in `state.db`; see
[Local secrets in state.db](#local-secrets-in-statedb).

**Automatic copy.** The first command that reads or writes settings copies
each old file it finds into its section of `config.yaml`, written owner-only,
and prints one line for it. The old file stays where it is, untouched, so an
older sparkwing binary on the same machine (an installed release beside a new
build, a runner service started with `--config .../agent.yaml`, a running
daemon) keeps reading it, and rolling back loses nothing. Once `config.yaml`
has a section, the matching old file is ignored whether or not the two agree;
edit `config.yaml` from then on, and delete each old file once no older
binary needs it. A file that fails its section's own validation is not
copied, and only commands that read that section fail, naming the file and
the key to remove. A `profiles.yaml` key other than `profiles:`, such as the
`default:` older releases wrote, had no effect and is left out. The copy runs
only into the machine's own `config.yaml`, never into a file
`SPARKWING_CONFIG` names, and is skipped with a warning under a sandboxed
`SPARKWING_HOME`. `sparkwing doctor` copies too, and lists old files already
copied (safe to delete) apart from any it could not copy. The automatic copy
will be removed in a later release; after that, an old file is ignored.

Before:

```text
~/.config/sparkwing/profiles.yaml
  profiles:
    prod:
      controller: { url: https://api.sparkwing.example, token: swu_... }
~/.config/sparkwing/budget
  # leave room for the desktop
  50%,8gb
~/.config/sparkwing/admission.yaml
  mode: auto
~/.config/sparkwing/repos.yaml
  repos:
    - path: /srv/code/app
```

After:

```yaml
# ~/.config/sparkwing/config.yaml
profiles:
  prod:
    controller: { url: https://api.sparkwing.example, token: swu_... }
admission:
  mode: auto
  # leave room for the desktop
  budget: 50%,8gb
repos:
  repos:
    - path: /srv/code/app
```

**Path overrides.** `SPARKWING_CONFIG` names the file. `SPARKWING_PROFILES`,
`SPARKWING_REPOS` and `SPARKWING_FLEET_CONFIG` are gone; a command refuses to
start while one is set, so unset it and move that file's contents into the
matching section of the file `SPARKWING_CONFIG` names. `SPARKWING_BUDGET`
still overrides `admission.budget`.

**Stricter reads.** `config.yaml` must be an owner-only regular file, and an
unknown section or key fails the read with the file and line named. The old
`profiles.yaml`, `repos.yaml` and `admission.yaml` loaders ignored unknown
keys; fix any the move reports.

**Runners.** `sparkwing-runner agent --config PATH` and
`sparkwing cluster runners add|remove --config PATH` now name a `config.yaml`
and use its `agent` section. A service unit written by an older
`runners add` still passes `--config .../agent.yaml`; until the automatic copy
is removed, sparkwing reads `config.yaml` in its place and says so, and a
`--config` naming an agent file anywhere else loads that file whole as the
`agent` section. Edit the unit's `--config` to a `config.yaml` path before then. `install/service-install.sh`
adds the `agent` section to `config.yaml`, keeping the file's other sections,
and refuses when an `agent` section already exists.

**Daemon flag.** The internal `sparkwing wingd run --admission-config` flag is
gone; the daemon reads the `admission` section.

## Local secrets in state.db

`sparkwing secrets` without `--profile`, local runs and the dashboard (`sparkwing serve`) keep
this machine's secrets in the `secrets` table of `state.db` in
`SPARKWING_HOME`, instead of `~/.config/sparkwing/secrets.env` (masked) and
`config.env` (`--plain`). The sparkwing daemon serves them on its API socket
and starts when a command needs it. Every value is sealed with the
controller's cipher.

**The key.** The daemon creates `~/.config/sparkwing/secrets.key`
(or `$XDG_CONFIG_HOME/sparkwing/secrets.key`), 32 random bytes, owner-only, on
the first stored secret; the dashboard (`sparkwing serve`) and runs never create it, so store
the first secret with `sparkwing secrets set`. A key file that others can read
or that is a symlink is refused.
`SPARKWING_SECRETS_KEY` (base64 of 32 bytes, as for `sparkwing-controller`)
overrides the file, and `SPARKWING_SECRETS_KEY_FILE` moves it. Set them where
the daemon starts, then run `sparkwing daemon restart`. Back up `secrets.key`
with `state.db`; losing it loses every local secret. Sparkwing refuses to
create a new key while `state.db` holds values sealed under another one.

**Before upgrading.** Stop the daemon with `sparkwing daemon stop`, then back
up `state.db` in `SPARKWING_HOME` with its write-ahead log files as
[Back up](../backup-restore.md#sqlite) describes. That backup is the way back.

**Automatic import.** The daemon imports both dotenv files once, the first
time it opens `state.db`, which `sparkwing secrets` creates when the files
exist and `state.db` does not. Each name becomes an unscoped secret shared with
every pipeline; a name in both files imports once, masked. A name the store
already holds keeps the store's value. The import is all or nothing and is
recorded in `state.db`, so it never runs again: a secret deleted afterwards
stays deleted, and later edits to the files are ignored. The daemon log names
what it imported. Check the result with `sparkwing secrets list`. The files
are not changed, so an older sparkwing on the machine keeps reading them;
delete them once no older sparkwing needs them. The automatic import will be
removed in a later release; after that, the files are ignored.

**A failed import.** A malformed or unreadable file imports nothing, and until
the import succeeds `sparkwing secrets` and every secret a local run reads
fail with the import error, naming the file and line. Fix the file, then run
`sparkwing daemon restart`.

**Path overrides.** The import reads the files `SPARKWING_SECRETS` and
`SPARKWING_CONFIG_ENV` name when they are set in the daemon's environment;
after the import neither variable does anything. If the daemon starts without
them, add those values by hand:

```bash
sparkwing secrets set --name API_TOKEN --file ./token
sparkwing secrets set --name REGION --value us-east-1 --plain
```

A command running under a `SPARKWING_HOME` of its own keeps its secrets in
that home's `state.db`, does not import the machine's files, and refuses to
create the machine's key; point `SPARKWING_SECRETS_KEY_FILE` inside the home.

**`sparkwing serve --allow-remote`.** The dashboard now manages local secrets
without an account, so a remote bind lets every host that reaches it list,
overwrite and delete them; the server warns at startup. It never serves a
masked value. A browser origin on
another loopback port is refused unless it is the dev server's port 3100.

**What the key protects.** It keeps a copied or backed-up `state.db` sealed.
Code running as your account, pipeline steps included, can read the key file
and ask the daemon for any secret, as it could read the dotenv files before.

**An older daemon.** A daemon from an earlier release stores values
unencrypted, so `sparkwing secrets set` refuses to write through it and asks
for `sparkwing daemon restart`. A run no daemon hosts reads `state.db`
directly with the same key, and fails a secret read while the dotenv files
wait to be imported.

**Rolling back.** Stop the daemon, restore that backup as
[Restore](../backup-restore.md#restore) describes, and reinstall the older
release. It reads the old settings files
and dotenv files, which the upgrade left as they were, so any setting or
secret changed after the upgrade, which lives only in `config.yaml` and
`state.db`, must be redone by hand.

## Schema 73: trigger credit cursor

Stop metered trigger claims and wait for active claims to finish or expire,
then back up the controller database before upgrading. The migration refuses
to start while any trigger has an open credit reservation, regardless of its
status. Review any remaining completed or pending row against the ledger and
the backup before correcting it under the older build. Do not call
`FinishTrigger` blindly on a completed row: with no lease end, it can bill
through the time of that call. The migration records each new claim's
paid seconds, paid amount, and exact reservation ID on the trigger. A new
index speeds each team's balance read. The `trigger-credit-cursor-v1`
requirement makes older controllers refuse the upgraded database.

To roll back, stop the upgraded controller, restore the backup, and start the
older build. Do not delete the requirement from a live database.

## Schema 75: a credit is $0.001

A credit is $0.001 and keeps that value. `micro_per_credit` reads 100,000 and
`credits_per_dollar` 1,000; `micro_per_cent` stays 1,000,000, so stored
balances, grants and charges keep their dollar value and the checkout service
needs no change to grant. Schema 75 divides a stored
`runner_scale_step_credits` by 20, rounding to the nearest credit and never
below one, so the step keeps its dollar value. The `credit-value-v1`
requirement makes a controller older than schema 75 refuse the upgraded
database rather than read the restated step in its own credit and scale
runner caps 20 times sooner, so upgrade every controller together. To roll
back, stop the upgraded controller, restore a backup taken before the
upgrade, and start the older build.

The default rate ladder moves to 2-core 9,000, 4-core 18,000 and 8-core 33,000
micro-credits a second. An installation that stored
`credit_rate_micro_per_second` or `rate_table` keeps its stored prices. To bill
the new ladder, write it after the upgrade:

```
PUT /api/v1/credits/settings
{"rate_table": {"2": 9000, "4": 18000, "8": 33000}}
```

Writing the table also sets `credit_rate_micro_per_second` to the four-core
price. The minimum purchase rises from $5 to $10, and
`purchase_min_cents` reads 1,000.

## Sign-in no longer joins accounts by email

A user who signs in with Google and later with GitHub, or the reverse, under
the same verified address no longer lands in one account. The second
provider's first sign-in answers `409`, and the dashboard shows "An account
with this email already exists. Sign in the way you did before, then link this
provider from account settings." The user signs in with the first provider and
links the second from **Account -> Linked sign-ins**. Sign-ins attached before
the upgrade keep working. Code that read `store.SignInResult.Linked` drops it;
`errors.Is` on `store.ErrAccountExists` detects the refusal. No database
migration is required.

## Node metric reads are paged

`GET /api/v1/runs/{id}/nodes/{nodeID}/metrics` answers at most 1,000 samples
unless the request asks for up to `limit=10000`, and sets `next_cursor` while
more follow. A client that read the whole list in one request passes
`limit=10000`, which covers every sample a node can hold, or passes each
`next_cursor` back as `cursor` until the answer carries none. An older
`sparkwing` binary reading a node with more than 1,000 samples sees only the
first page. No database migration is required.

## 60-second minimum billable duration

Drain metered node and trigger claims before deploying the controller that
raises the minimum to 60 seconds. A claim reserved at 20 seconds under the
older build is covered for 60 once its pod first renews under the new one, and
`sparkwing_node_seconds_total{placement="cloud"}` dips by 40 seconds for each
such claim still open. No database migration is required.

## Schema 87: card billing and a team-wide runner count

`max_concurrent_runners` now counts a team's cloud runners across every token
of the team, and applies even when unset: zero or unset means 100 per team,
and `runner_cap` on the trust route raises one granted team. Schema 87 deletes the `runner_scale_base`,
`runner_scale_step_credits` and `runner_scale_ceiling` settings.
`Store.RunnerCapFor` is gone, and `GET /api/v1/compute-limits` no longer
reports `usage.derived_runner_cap` or `usage.recent_paid_micro`. Schema 87 also
adds card billing and the per-day spend buckets, backfilled from every past
charge; the upgrade reads the whole credit ledger once, so run it inside the
usual write freeze. The `card-billing-v1` requirement makes an older
controller refuse the upgraded database. The checkout service checks that the
controller reports the `card-billing-v1` capability before it saves a card,
reports a card payment, records a fraud warning or reverses a refund, so the
controller is upgraded first.

## Dashboard service probes

The dashboard no longer shows internal service names, URLs, latency, or
health details. Remove `--cache` from `sparkwing-web` startup arguments and
`web.cache.url` from full-chart values. The dashboard's
`GET /api/v1/health/services` route is gone. Operators can query each
service's own health endpoint through their private operations path.
Overview now reports pending approvals without treating service probes as
team-facing alerts.

## Browser session renewal

`Store.ExtendSession` has been removed. Replace a
`LookupSession`/`ExtendSession` pair with
`LookupSessionAndRenew(rawSession, now, idleTTL, maxLifetime)`. The new call
checks the session and renews its expiry in one database write. The caller
must reject an over-age session returned without renewal and revoke it.
Controller sessions now last seven days after their last use, with a 30-day
cap from sign-in. Dashboard session and CSRF cookies persist for 30 days;
server-side expiry and logout still end access on the next request.

## Schema 72: storage commit receipts

Back up the controller database before starting the upgraded controller.
Schema 72 records each committed storage reservation by team, store, and ID
for at least 24 hours. Hourly storage maintenance removes older receipts.
A repeated commit during that window counts once. A repeated renewal
returns its original next reservation while that reservation is active.
A replay after pruning counts as a new commit.

The first upgraded start records the `storage-commit-receipts-v1`
requirement. An older controller refuses the database because it would count
repeated commits again. To roll back, stop every controller, restore the
database backup taken before this upgrade, then start the older build.
Do not delete the requirement row from a live schema-72 database.

## Batched log append protocol

Upgrade the logs service before runners and pipeline binaries. New writers
send several newline-delimited records in one append and include the first
and last sequence numbers. An older logs service accepts the body but counts
only the first sequence number, so finished batched logs can appear
incomplete. The upgraded logs service accepts old single-line appends during
the rollout. No config or data migration is needed.

## Direct source bundles

Upgrade the controller, CLI and runner together before using Cloud
`--working-tree`. An older controller has no source capability, and an older
runner cannot read a source-bound trigger. Already pending working-tree
triggers made through the old cache seed path must finish before replacing
runners, or be retriggered with the new CLI. Rollback restores the old
controller and runner pair; newly uploaded source bundles cannot run on it.
A normal remote trigger now needs its commit pushed to origin. Use
`--working-tree` for an unpushed commit or a checkout with no cloud-reachable
origin. The CLI needs a user token with `runs.write` and a configured direct
S3 cache store and signed downloads. Bundles take the team's cache storage
share and are deleted 24 hours after the bound run finishes; retry uploads
again. `runs retry` and Cloud `RunAndAwait` children of a working-tree
run are refused before admission because they cannot inherit its one-run
source. Submit a fresh run from the checkout with
`sparkwing run <pipeline> --profile <cloud-profile>`. Local children and
local retries are unchanged.

## Schema 71: GitHub App cron identity

Back up the controller's PostgreSQL database or SQLite `state.db` before
starting the upgraded controller. Schema 71 adds installation and repository
identity to App-managed cron schedules. Existing local and manually pushed
schedules keep their URL identity; no URL or `armed_by` value is used to guess
an App identity. The first upgraded start records the
`github-app-cron-identity-v1` requirement, so an older controller refuses this
database rather than ticking schedules without the App removal safeguards.

To roll back to a schema-70 controller, stop every controller, restore the
database backup taken before this upgrade, then start the older build. Do not
delete the version or requirement row from a live schema-71 database: that
would leave App schedule identities in a shape the older controller cannot
manage safely.

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

v0.60.0 runs schema v47. This release migrates the database to its current
schema when the controller first starts, and a v0.60.0 binary cannot open it afterwards, so
the backup is the only way back.

1. Read the sections below for anything your deployment configures.
2. Stop the controller and back up its database: the Postgres database, or
   the SQLite `state.db` under the controller's `SPARKWING_HOME`.
3. Upgrade the controller and the CLI, in either order. A released CLI still
   works against the new controller.
4. Start the controller with the same `SPARKWING_SECRETS_KEY` it ran with. Its
   first start migrates the schema and reseals stored secrets, and logs how
   many it resealed.
5. Verify: the startup line reports the new `runs-store schema`,
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

- Delete `cache_token` from the agent settings (`agent.yaml`, or the `agent`
  section of `config.yaml`); the agent refuses to load settings that still
  carry it. The installer no longer writes it.
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

## Session methods take a context

`pkg/store` callers pass the request's context first:
`CreateSession(ctx, principal, scopes, ttl, now)`,
`LookupSession(ctx, raw, now)`,
`LookupSessionAndRenew(ctx, raw, now, ttl, maxLifetime)` and
`IdentityLinkStateKey(ctx)`. Behavior is otherwise unchanged.

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

## One agent matcher

Every claim route now asks `pkg/match` whether a runner may take a node.

- **Agent names.** `name=<agent>` matches the runner whose token principal is
  `agent:<agent>`. Every mint path refuses a second live runner token for the
  same name, so revoke the old one first. `RotateToken` is the one overlap:
  the replacement and its predecessor are both live until the grace ends.
- **Named claims.** A caller of `Client.ClaimNodeByID` passes the labels of
  the executor it runs the node on as the new last argument; pass `nil` for
  none. A labeled node is refused to a caller whose labels do not satisfy it,
  including a dispatcher older than this release, which sends no labels:
  upgrade Kubernetes dispatchers before their pipelines need labeled nodes run
  by name. The operator's metered pool keeps matching `location=cloud` nodes.
- **Store API.** `store.WithRepoFilter` is replaced by
  `store.WithClaimProfile(ctx, match.Profile{Accept: filter})`, and
  `RunnerPresence.AllowRepos` by `RunnerPresence.Profile.Accept`.
  `store.ExecutorResource` is now an alias of `match.Resources`.
- **Exclusion reasons.** Executor eligibility previews report `selector` where
  they reported `hard_capability`, and `shape` for a node whose request exceeds
  the executor's budget.


## Trusted proxy listener replaces trusted proxy CIDRs

`sparkwing-controller` and `sparkwing-web` no longer accept
`--trusted-proxy-cidrs`, and the full chart drops
`controller.trustedProxyCIDRs` and `web.trustedProxyCIDRs`. Neither process
takes a client address from `X-Forwarded-For` any more. A startup that still
passes the flag fails.

1. Start both processes with `--trusted-proxy-addr=<host:port>`, a second
   listener where `X-Real-IP` names the client.
2. Point the fronting proxy at the trusted listeners and have it overwrite
   `X-Real-IP` on every request; ingress-nginx does by default.
3. Point `sparkwing-web --controller` at the controller's trusted listener.
4. Keep every other caller off the trusted ports, for example with a
   NetworkPolicy.

Until then, login throttling, the bearer failure budget and audit `client_ip`
key on the TCP peer, so browsers behind one proxy share its budget.
