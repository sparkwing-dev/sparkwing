### Added

- **controller + cli:** Delete your own account from Account settings or `sparkwing account delete`
  Preview and confirm every sole-member team removed with the account. Deletion
  revokes all account sessions and tokens. Sole owners of teams with other
  members must transfer ownership or delete those teams first. Shared-team
  history remains with its execution actors relabeled as "deleted user".
  Financial records remain, and account IDs stay in audit records for 400 days
  on Sparkwing Cloud. Accounts which existed on or before 2026-10-02 may remain
  in encrypted operator archives. The SQLite archive from
  2026-09-15 follows a 365-day lifecycle plus 30 noncurrent days, ending around
  2027-10-15. PostgreSQL archive dumps expire 35 days after creation under an S3
  lifecycle rule. Noncurrent versions expire 35 days after becoming noncurrent. S3 expiry
  is asynchronous. Accounts created after 2026-10-02 are not in these archives.

### Fixed

- **controller + cli:** Compare confirmed team slugs independently of database collation
  Missing confirmation flags identify the required list. Plain preview includes
  blocked teams and fails when deletion is blocked. Previews warn that runner
  and CI credentials created in shared teams are revoked too.

### Security

- **controller:** Require a sign-in from the last ten minutes before account deletion
  The dashboard and CLI request a new sign-in when the session is older.
