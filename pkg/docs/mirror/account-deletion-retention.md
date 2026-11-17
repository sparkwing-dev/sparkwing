# Account deletion retention

On Sparkwing Cloud, PostgreSQL archive dumps expire 35 days after creation under an S3 lifecycle rule. Noncurrent PostgreSQL versions expire 35 days after becoming noncurrent. Accounts which existed on or before 2026-10-02 may remain in encrypted operator archives until expiry. The SQLite pre-cutover archive from 2026-09-15 follows its existing 365-day lifecycle plus 30 days for noncurrent versions, ending around 2027-10-15. Accounts created after 2026-10-02 are not in any of these archives.

The PostgreSQL rule covers the `postgres/` prefix, including the controller and
billing dumps from 2026-10-02. The SQLite rule covers `sqlite/`. S3 rounds expiry
up to midnight UTC and deletes asynchronously. Current-object expiry in this
versioned bucket adds a delete marker. The noncurrent-version rule later removes
the retained bytes. Account deletion does not scrub either archive. The
seven-day automated RDS recovery window does not describe every copy.

Live account identities and sessions are removed immediately, and held or
created tokens are revoked, including runner and CI credentials in shared
teams. Arrange replacements for those credentials before deleting the account.
Sole-member team data uses the retrying team purge. Deletion is refused if you
are the sole owner of a team with other members. Transfer ownership or delete
that team first. Shared-team runs, logs, artifacts and other team data remain
with that team.

Selected execution actor columns, including `runs.created_principal`,
`triggers.claim_principal`, `nodes.claim_principal`, `approvals.approver`,
`cron_schedules.armed_by`, `debug_pauses.released_by` and `secrets.principal`,
are relabeled as "deleted user". This is not a rewrite of every creator field.
Opaque account IDs remain in `git_credentials.created_by`,
`github_app_triggers.created_by`, `github_app_extra_repos.created_by`,
`pool_access_rules.created_by`, `billing_pages.account_id`,
`billing_awards.requester_account` and `signup_admissions.account_id`.
These identifiers follow each record's existing retention policy. Account
deletion does not rewrite them.
User-supplied run contents may still name the former member.

Financial records and reserved team slugs remain. Sparkwing Cloud keeps account
IDs in audit records for 400 days for security and abuse investigation. Self-hosted
operators configure their own audit retention. OIDC audit records retain keyed
audience and subject digests, their key ID and fixed tracing identifiers after
schema 40 reconciliation. An operator with the external key and a candidate
can still confirm a match. See [audit investigation](oidc.md#audit-investigation).

See [Deleting a team or account](auth.md#deleting-a-team-or-account) for
confirmation, ownership and sign-in requirements, and
[Deleted data in backups](backup-restore.md#deleted-data-in-backups) for
self-hosted retention duties and restore handling.
