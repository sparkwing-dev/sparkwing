### Changed

- **cloud:** Cloud jobs reach their Pods sooner after planning
  The Cloud launcher no longer uses client-go's default limit of 5 Kubernetes requests per second for each API group. Reconciliation used up that budget, so node-reservation and Pod requests during the first-job handoff waited for it to refill, adding about 1 s.
