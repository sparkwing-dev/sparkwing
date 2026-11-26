## Upgrade the store in place to schema baseline 43

Schema 43 adds `idx_credit_charges_team_mode_amount` on
`credit_charges(team, payment_mode, amount_micro)` and the partial index
`idx_billing_awards_unsettled` on `billing_awards(team)` for unsettled awards,
and drops `idx_credit_charges_team_amount`, in one transaction on the
recognized schema 42 baseline. No rows change. On PostgreSQL the builds hold a
lock that blocks writes to those two tables while they run, about 2 seconds
per million `credit_charges` rows; the controller applies the upgrade when it
opens the store, before it serves. Earlier recognized baselines run their
existing upgrades first, and SQLite creates its usual backup. Older binaries
refuse the upgraded store; use a matching binary or restore the pre-upgrade
backup into a separate store.

No CLI, SDK or pipeline configuration change is required.
