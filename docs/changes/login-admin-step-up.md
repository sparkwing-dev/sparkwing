### Added

- **auth:** `sparkwing login --admin` gives a team owner an hour of administration
  An owner who signed in with `sparkwing login` can now run `secrets set`,
  `team settings set`, `team delete` and other team-admin commands without
  minting a token by hand. The request is sent with the profile's member token, so
  only the same account can approve it, only an owner can, and only from a
  browser sign-in made in the last 10 minutes. Approval saves a `team.admin` token
  that expires after one hour beside a re-issued member token. The profile uses it
  until it expires and then falls back to the member token. Ordinary
  `sparkwing login` tokens still never carry `team.admin`.

### Changed

- **auth:** Team-admin refusals name the step-up
  A `missing_grant` refusal for `team.admin` now tells owners to run
  `sparkwing login --admin` and other members to ask a team owner.
