### Security

- **controller (Breaking):** Encrypt schedule arguments and job defaults at rest
  Keyed stores seal schedule declarations, overrides, consent and fire history,
  and `WithArgs` defaults in plans, node specs and expansion bodies. Existing
  plaintext rows remain readable and the operator secrets rotation seals them.
  The schema baseline advances to version 41 so older binaries refuse the
  changed argument format.
  Dependency, memo, child-pipeline and declared-step metadata stay readable
  without the argument key. Full job specs require the key to restore defaults.
  Upgrade all controller readers before resuming writes. See the
  [migration guide](docs/migrations/_unreleased.md#schedule-and-job-argument-encryption).
