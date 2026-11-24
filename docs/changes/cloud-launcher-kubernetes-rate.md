### Changed

- **cloud:** Cloud jobs reach their Pods sooner after planning
  The Cloud launcher no longer uses client-go's default limit of 5 Kubernetes requests per second for each API group. That limit added about 200 ms to each node-reservation and Pod request during the first-job handoff.
