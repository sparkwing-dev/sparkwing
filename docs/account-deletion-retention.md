# Account deletion retention

On Sparkwing Cloud, accounts which existed on or before 2026-10-02 may remain in encrypted operator archives. The SQLite pre-cutover archive from 2026-09-15 follows a 365-day lifecycle plus 30 days for noncurrent versions, ending around 2027-10-15. The PostgreSQL archives from 2026-10-02 have no scheduled expiry and remain until the operator sets a retention decision, which is pending. Accounts created after 2026-10-02 are not in any of these archives.

S3 lifecycle expiry is asynchronous, so the SQLite date is approximate. The
lifecycle rule covers only the SQLite prefix. It does not cover the PostgreSQL
controller and billing dumps. Account deletion does not scrub either archive.
The seven-day automated RDS recovery window does not describe every copy.

Live account identities and sessions are removed immediately, and held or
created tokens are revoked, including runner and CI credentials in shared
teams. Arrange replacements for those credentials before deleting the account.
Sole-member team data uses the retrying team purge. Shared-team runs, logs,
artifacts and other team data remain with that team.

Selected execution actor columns, including `runs.created_principal`,
`triggers.claim_principal`, `nodes.claim_principal`, `approvals.approver`,
`cron_schedules.armed_by`, `debug_pauses.released_by` and `secrets.principal`,
are relabeled as a deleted user. This is not a rewrite of every creator field.
Opaque account IDs remain in `git_credentials.created_by`,
`github_app_triggers.created_by`, `github_app_extra_repos.created_by`,
`pool_access_rules.created_by`, `billing_pages.account_id`,
`billing_awards.requester_account` and `signup_admissions.account_id`.
These identifiers follow each record's existing retention policy. Account
deletion does not rewrite them.
User-supplied run contents may still name the former member.

Financial records and reserved team slugs remain. Audit identifiers normally
remain for 400 days. Existing OIDC audit audience and subject strings also
remain until audit expiry and can contain personal text.

See [Deleting a team or account](auth.md#deleting-a-team-or-account) for
confirmation, ownership and sign-in requirements, and
[Deleted data in backups](backup-restore.md#deleted-data-in-backups) for
self-hosted retention duties and restore handling.
