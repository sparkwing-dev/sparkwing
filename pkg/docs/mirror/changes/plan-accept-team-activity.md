### Changed

- **controller:** A team's runners return to their own poll cadence when its plan is accepted
  When the hosted controller newly accepts a run's plan, the jobs it releases count as new work for that team, so the team's idle runners get no widened poll interval on their next claim request. A runner already waiting honors the interval it was given. A refused or replayed plan leaves the team's idle clock unchanged.
