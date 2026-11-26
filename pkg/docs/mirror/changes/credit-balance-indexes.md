### Changed

- **controller (Breaking):** Claim polls read a team's credit balance from an index
  Each claim poll sums the claiming team's credit charges and its unsettled Cloud awards. On PostgreSQL those sums
  scanned every team's charges or awards whenever one team held a large share of the ledger, so their cost grew
  with all teams' billing history. The balance now reads an index on team, payment mode and amount, and the active
  Cloud vCPU sum reads a partial index of unsettled awards, so each read costs what the polling team's own
  history costs. The schema baseline advances to version 43, which adds both indexes and drops the
  `credit_charges(team, amount_micro)` index they replace. See the
  [migration guide](docs/migrations/_unreleased.md#upgrade-the-store-in-place-to-schema-baseline-43).
