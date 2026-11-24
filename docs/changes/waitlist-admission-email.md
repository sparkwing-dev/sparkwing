### Added

- **controller:** Email waitlisted people when an operator admits them
  The controller makes one attempt to email the account's verified address, with a link to the console sign-in page. A retried approval or an account that is already admitted sends nothing. `/api/v1/me` returns `admission_email`, and the waitlist screen promises the email only when the deployment has a sender and the address is verified. See [sign-up gate](docs/auth.md#sign-up-gate).

### Fixed

- **controller:** Send each email in one SES attempt and keep addresses out of send-failure logs
  SES `SendEmail` takes no idempotency key, so an SDK retry after a lost response could deliver an invitation, billing notice or admission email twice. A failed send now logs a fixed failure class instead of the provider's error text, which can quote the recipient address.
