### Changed

- **local runs:** A local run starts within half a second of intake after any idle period
  The local controller no longer advises its idle runner to poll less often. Before,
  a run submitted after a pause waited up to 6.25 s for the runner's next claim.
  An idle local runner now sends two empty claims a second over its private socket.
- **typescript:** Unchanged TypeScript pipelines skip npm during preparation
  A verified installation records a receipt of the inputs npm reads but does not
  write. Later preparations whose inputs match, and whose tree passes npm's own
  hidden-lockfile check, skip `npm ci --dry-run` and `npm install`, saving 1-2 s
  each time the CLI, trigger or job prepares the pipeline. Root lifecycle scripts
  still run on every preparation. Version-manager shims hide node and npm switches
  from the receipt. See [versioning](../versioning.md).
