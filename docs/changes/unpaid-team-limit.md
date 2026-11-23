### Security

- **controller:** Limit each account to three owned teams that have never paid
  Every team carries its own free storage, download and request allowances, so creating teams without limit multiplied them. The fourth unpaid team is refused with `422 limit_exceeded` and reason `unpaid_team_limit`; adding funds to any owned team lifts it out of the count, and operator-created teams are exempt. See [team resource limits](docs/limits.md#team-resource-limits).
