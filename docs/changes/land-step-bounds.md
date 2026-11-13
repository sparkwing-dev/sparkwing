### Added

- **sdk:** Expose step bounds to Plan
  `RunContext.StartAt` and `RunContext.StopAt` carry the run's `--sw-start-at`
  and `--sw-stop-at` values, and the protocol run context sends them as
  `start_at` and `stop_at`. A `Plan` can refuse a selection it cannot honor.

### Fixed

- **land:** Refuse step bounds that would skip checks and still push
  Ordinary and queued Land fail at plan time, before any job or Git command,
  when `--sw-start-at` or `--sw-stop-at` would skip checks while the land job
  still pushes. `--sw-stop-at verify` and `--sw-dry-run` keep working.
