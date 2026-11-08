# Migrating to the next release

## Upgrade the store in place to schema baseline 37

Schemas 30 through 36 upgrade in place. Schema 37 drops the unused team
build-trust table and preserves secrets, teams, tokens, runner registrations,
GitHub App installations, schedules, subscriptions and Actions records, including
both direct team foreign keys. The team build-trust API is removed. Execution
trust still follows sealed provenance and host facts. Older binaries refuse the
upgraded store.
