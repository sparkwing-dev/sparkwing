# Migrating to the next release

No breaking changes so far.

## Upgrade the store in place to schema baseline 35

Schema 35 adds approved GitHub Actions workflow/image pairs and durable dispatch
receipts. Recognized schema 34 stores upgrade in place, preserving configured
secrets, teams, credentials, runners, installations and schedules. Older binaries
refuse the upgraded store. The real GitHub App permission update is a separate
operator action. Existing Actions workers need a matching dispatch receipt to
claim jobs whose repository placement explicitly lists GitHub Actions.
