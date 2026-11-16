### Docs

- **sdk + docs:** Document the trigger sources a run actually carries
  `TriggerInfo.Source` listed `manual`, `push`, `schedule` and `webhook`; runs
  carry `local`, `trigger`, `github_app`, `schedule` or `child`. The guard docs
  now say that a local run from `main` passes `when: [git:branch=main]` and that
  `where:controller` keeps a pipeline off developer machines.
