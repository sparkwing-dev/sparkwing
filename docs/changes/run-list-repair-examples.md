### Fixed

- **cli + controller:** Include repair examples in run-list input refusals
  Invalid or negative `--since` durations show `--since 1h`, and invalid
  HTTP page limits show `limit=1`. Validation rules are unchanged.
