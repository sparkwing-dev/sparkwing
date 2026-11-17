### Security

- **controller (Breaking):** Encrypt schedule arguments and job defaults at rest
  Keyed stores seal schedule declarations, overrides, consent and fire history,
  and `WithArgs` defaults in plans, node specs and expansion bodies. Existing
  plaintext rows remain readable and the operator secrets rotation seals them.
  Store schema 40 makes older binaries refuse the changed argument format.
  Node metadata stays readable while argument defaults are sealed.
  Upgrade all controller readers before resuming writes. See the
  [migration guide](docs/migrations/_unreleased.md#schedule-and-job-argument-encryption).
