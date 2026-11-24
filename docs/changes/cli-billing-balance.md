### Added

- **cli:** Show the team's prepaid balance with `sparkwing billing balance`
  The command reports the credits and their dollar value as the billing page shows them, rounded toward zero, and says when a hold or an empty balance stops Cloud work. JSON returns `team`, `credits`, `prepaid_cents`, `balance_micro`, `frozen` and any `hold_cause`; plain prints the credits.
