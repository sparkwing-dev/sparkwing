### Added

- **sdk:** Expose step bounds to Plan
  `RunContext.StartAt` and `RunContext.StopAt` carry the run's `--start-at`
  and `--stop-at` values when the host forwards them. The protocol run context
  defines them as the optional `start_at` and `stop_at` fields. A `Plan` can
  refuse a selection it cannot honor.
