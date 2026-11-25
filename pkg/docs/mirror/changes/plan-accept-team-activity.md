### Changed

- **controller:** A team's runners return to their own poll cadence when its plan is accepted
  When the hosted controller accepts a run's plan, the jobs it releases count as new work for that team, so the team's idle runners stop waiting on the widest advised poll interval. A refused plan changes nothing.
