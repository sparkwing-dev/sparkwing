### Security

- **controller:** Count an invitation as a sign-up admission only while the sign-up gate admits
  A waitlisted account that accepts a team invitation still joins that team and
  stays on the waitlist. The acceptance counts as an admission toward the
  hourly and daily sign-up limits only while the operator's sign-up mode is
  open, fewer than 100 accounts are admitted and the acceptance stays within
  those limits. An acceptance that would cross a limit closes the gate and does
  not count. Only a counted acceptance increments `sparkwing_signups_total`
  with reason `invitation`. See [sign-up gate](docs/auth.md#sign-up-gate).
