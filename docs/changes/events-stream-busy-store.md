### Fixed

- **controller:** Answer a busy or interrupted event stream open with a retryable 503
  Opening a run's event stream (`/api/v1/runs/{run}/events/stream`) answered 500 whenever reading the run failed
  for a reason other than a missing run, and the run lookup that every `/api/v1/runs/{run}/...` route makes first
  answered 500 when SQLite was busy. A store lock conflict or store deadline during either read now answers 503
  with `Retry-After` and the `unavailable` error envelope. A run read that a stopping controller cancels answers
  the same retryable 503 instead of an empty response, and is logged at info. Other errors still answer 500. Lock
  conflicts are recognized by the database drivers' error codes, not by error text.
