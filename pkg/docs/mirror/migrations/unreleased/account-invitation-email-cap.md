## Upgrade the store in place to schema baseline 42

Schema 42 adds an `inviter` column and an index to `invitation_email_log` on
the recognized schema 41 baseline, in one transaction. The column holds a
SHA-256 digest of the inviting account, so the per-account invitation email
cap keeps counting after the account or its teams are deleted. Rows written
before the upgrade carry an empty value and count toward no account; they
leave the 24-hour window within a day. Earlier recognized baselines run their
existing upgrades first, and SQLite creates its usual backup. Older binaries
refuse the upgraded store; use a matching binary or restore the pre-upgrade
backup into a separate store.

The invitation response adds an optional `email_note` string. No CLI, SDK or
pipeline configuration change is required.
