### Added

- **controller + cli:** Delete your own account from Account settings or `sparkwing account delete`
  Preview and confirm every sole-member team removed with the account. Deletion
  revokes all account sessions and tokens. Sole owners of teams with other
  members must transfer ownership or delete those teams first. Shared-team
  history, financial records and audit identifiers retain their documented
  exceptions to account erasure. On Sparkwing Cloud, accounts which existed
  on or before 2026-10-02
  may remain in encrypted operator archives. The SQLite archive from
  2026-09-15 follows a 365-day lifecycle plus 30 noncurrent days, ending around
  2027-10-15. PostgreSQL archives from 2026-10-02 have no scheduled expiry,
  pending an operator retention decision. Accounts created after 2026-10-02
  are not in any of these archives.

### Fixed

- **controller + cli:** Compare confirmed team slugs independently of database collation
  Missing confirmation flags identify the required list. Plain preview includes
  blocked teams and fails when deletion is blocked. Previews warn that runner
  and CI credentials created in shared teams are revoked too.

### Security

- **controller:** Require a sign-in from the last ten minutes before account deletion
  The dashboard and CLI request a new sign-in when the session is older.
