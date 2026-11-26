### Security

- **controller (Breaking):** Cap invitation emails at 100 per inviting account per day
  An account sends at most 100 invitation emails in any 24 hours, counted
  across every team it has invited from, including teams it has since deleted
  or left. Past the cap the invitation is still created and its link works,
  but no email is sent: the response carries `email_sent: false` and an
  `email_note` naming the limit and when the account can send again, and the
  console shows that note. The 5-a-day limit for one recipient address is
  unchanged. The schema baseline advances to version 42, which records a
  digest of the inviting account in the invitation email log. See
  [limits](docs/limits.md#invitation-limits) and the
  [migration guide](docs/migrations/_unreleased.md#upgrade-the-store-in-place-to-schema-baseline-42).
