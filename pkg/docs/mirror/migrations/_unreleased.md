# Migrating to the next release

## Upgrade the store in place to schema baseline 35

Schema 35 adds approved GitHub Actions workflow/image pairs and durable dispatch
receipts. Recognized schema 34 stores upgrade in place, preserving configured
secrets, teams, credentials, runners, installations and schedules. Older binaries
refuse the upgraded store. The real GitHub App permission update is a separate
operator action. Existing Actions workers need a matching dispatch receipt to
claim jobs whose repository placement explicitly lists GitHub Actions.

## Upgrade the store in place to schema baseline 36

Schemas 30 through 35 upgrade in place. Schema 36 drops the unused team
build-trust table and preserves secrets, teams, tokens, runner registrations,
GitHub App installations, schedules and subscriptions. The team build-trust
API is removed. Execution trust still follows sealed provenance and host facts.
Older binaries refuse the upgraded store.
