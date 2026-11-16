### Security

- **controller:** Require fresh approval when a failed-only retry re-executes protected work
  Approval gates are reused only with all transitive dependents. An unreadable
  source-output scope now emits a warning before reuse falls back to execution.
