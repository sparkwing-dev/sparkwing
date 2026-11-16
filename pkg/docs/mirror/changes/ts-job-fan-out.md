### Added

- **sdk (TypeScript):** `plan.jobFanOut(id, items, fn)` declares one job per item:
  `fn` returns an `[id, body]` pair for each item, and the jobs are grouped under
  `id`, as Go's `JobFanOut` does. The returned group accepts `.needs`, `.cache`,
  `.retry` and the other group modifiers. A dependent waits for every member, and
  an empty list gives an empty group. Generators can also call
  `generated.jobFanOut`.
