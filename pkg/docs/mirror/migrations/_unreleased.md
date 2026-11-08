# Migrating to the next release

## Bound object presence requests

`POST /api/v1/data/has-many` requires a nonzero RFC3339 `deadline` alongside
`digests`. Send the caller's query deadline; a missing deadline returns `400`.
Both Go data clients require an explicit presence budget. The server stops
presence work at the earlier of the caller's deadline and its 30-second response
window. Requests that expire or are cancelled return an error without partial
presence results.

## Upgrade the store in place to schema baseline 37

Schemas 30 through 36 upgrade in place. Schema 37 drops the unused team
build-trust table and preserves secrets, teams, tokens, runner registrations,
GitHub App installations, schedules, subscriptions and Actions records, including
both direct team foreign keys. The team build-trust API is removed. Execution
trust still follows sealed provenance and host facts. Older binaries refuse the
upgraded store.
