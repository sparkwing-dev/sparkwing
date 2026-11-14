### Changed

- **local runs:** A local run starts within half a second of intake after any idle period
  The local controller no longer advises its idle runner to poll less often. Before,
  a run submitted after a pause waited up to 6.25 s for the runner's next claim.
  An idle local runner now sends two empty claims a second over its private socket.
- **typescript:** Unchanged TypeScript pipelines skip npm during preparation
  A verified installation records a receipt of every npm input. Later preparations
  whose inputs match skip `npm ci --dry-run` and `npm install`, saving 1-2 s each
  time the CLI, trigger or job prepares the pipeline. Root lifecycle scripts still
  run on every preparation. See [versioning](../versioning.md).
