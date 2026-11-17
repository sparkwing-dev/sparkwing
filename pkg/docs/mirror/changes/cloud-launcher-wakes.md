### Changed

- **runner:** Cloud launchers wake on ready work and Pod changes
  Held claims and Pod watches avoid waiting for the next launcher poll. Coalesced signals keep reconciliation and claim searches bounded, and a missing watch permission warns. The safety poll still recovers missed signals, and placement keeps the same reservation, award and admission checks.
