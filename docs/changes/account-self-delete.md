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
  2027-10-15. PostgreSQL archive copies are removed within about 70 days of being
  taken:
  they expire after 35 days, then S3 permanently deletes the old version up to
  35 days later. For dumps taken before the rule took effect, count from the
  date the rule was applied. S3 rounds expiry up to midnight UTC and deletes
  asynchronously, so these timings are approximate. Accounts created after
  2026-10-02 are not in the 2026-09-15 or 2026-10-02 archives.

### Fixed

- **web:** Reach Account from the waitlist and before joining a team
  The top navigation keeps an Account link available for both states.

- **cli:** Explain account deletion before listing confirmation requirements
  Account deletion help includes the plain introduction shown by other commands.

- **controller + cli:** Compare confirmed team slugs independently of database collation
  Missing confirmation flags identify the required list. Plain preview includes
  blocked teams and fails when deletion is blocked. Previews warn that runner
  and CI credentials created in shared teams are revoked too.

### Security

- **controller:** Require a sign-in from the last ten minutes before account deletion
  The dashboard and CLI request a new sign-in when the session is older.
