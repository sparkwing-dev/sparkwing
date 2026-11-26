## Upgrade the store in place to schema baseline 43

Schema 43 adds `idx_credit_charges_team_mode_amount` on
`credit_charges(team, payment_mode, amount_micro)` and the partial index
`idx_billing_awards_unsettled` on `billing_awards(team)` for unsettled awards,
and drops `idx_credit_charges_team_amount`, in one transaction on the
recognized schema 42 baseline. No rows change. Earlier recognized baselines
run their existing upgrades first, and SQLite creates its usual backup.

On PostgreSQL the upgrade takes locks that last until it commits:

- Each index build holds a SHARE lock on its table, which blocks writes to
  `credit_charges` or `billing_awards` but not reads. The `credit_charges`
  build took 205 ms at 113,800 rows on PostgreSQL 17; by linear
  extrapolation, expect about 2 seconds per million rows.
- Dropping `idx_credit_charges_team_amount` takes an ACCESS EXCLUSIVE lock on
  `credit_charges`. It waits for every open transaction that has read the
  table, and from then until commit it blocks all reads and writes of it.

The controller applies the upgrade when it opens the store, before it serves,
so no request of its own waits on these locks. Another client reading the
store at the time, such as an operator's query, can delay the drop until it
finishes. Older binaries refuse the upgraded store; use a matching binary or
restore the pre-upgrade backup into a separate store.

No CLI, SDK or pipeline configuration change is required.
