### Added

- **controller:** Add an `operator.metrics` grant that reads only `/metrics`
  A Prometheus scraper previously needed `operator.limits`, which also changes limits, signups, warm capacity and Actions dispatch retries. `sparkwing operator tokens create --scope operator.metrics` now issues a service credential that `GET /metrics` accepts on either listener and every other route refuses. `operator.limits` and operator-console sessions still read `/metrics`.

### Docs

- **chart:** Say that `controller.metricsPort` still requires a bearer
  The `sparkwing-full` values comment said the dedicated metrics port served `/metrics` without one. Both listeners require `operator.metrics` or `operator.limits`.
