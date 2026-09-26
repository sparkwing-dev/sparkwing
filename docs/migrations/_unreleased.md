# Migrating to the next release

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

## Secrets are scoped to pipelines

Schema 48 renames `secrets.repo` to `secrets.pipeline` and `runs.repo` to
`runs.declared_repo`, preserving the stored values. Older binaries cannot
use this schema. Back up and rehearse the upgrade using the
[upgrade procedure](../backup-restore.md#rehearse-the-migration-on-a-copy).
Upgrade the CLI, daemon, controller, runners, and pipeline SDK together;
rebuild pipeline binaries before resuming work. Rollback requires restoring
the pre-upgrade database with the previous binaries.

Replace `--repo` with `--pipeline` in secret commands and `repo` with
`pipeline` in secret API bodies and query parameters. Replace client methods
`CreateSecretForRepo`, `GetSecretForRepo`, and `DeleteSecretForRepo` with their
`ForPipeline` counterparts.

An old repository scope keeps its string value; the migration cannot choose
the intended pipeline. Re-create each affected secret under its pipeline
name from the original secret source, verify a run can read it, then remove
the old scope. Unscoped secrets keep their existing sharing rules. See the
[secret commands](../cli-secrets.md).

## Repository metadata grants no access

A run's declared repository no longer authorizes secret reads or Git cache
access. Connect the repository to the pipeline with a GitHub webhook
binding. Runner access through the claimed-run Git cache proxy requires
a run created from a verified delivery for that binding.

For Go callers, replace `store.Run.Repo` with `store.Run.DeclaredRepo` and
`store.RunFilter.Repos` with `store.RunFilter.DeclaredRepos`. Replace
`RepoForClaimedRun` and `ReposForClaimant` with `PipelineForClaimedRun` and
`PipelinesForClaimant`, and treat their results as pipeline names. Run JSON
and the run-list `repo` query parameter remain unchanged.

## Operator tokens require runs.control

Grant `runs.control` to operator and dashboard tokens that retry or cancel
runs, bounce nodes, release debug pauses, or change cron schedules.
`runs.write` still permits trigger submission and Git cache refresh;
runner tokens do not need the operator scope. Update integrations that used
`runs.write` for these control operations before resuming them.
