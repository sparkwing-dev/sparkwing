### Security

- **controller:** Seal legacy plaintext run and schedule arguments automatically on startup
  A background pass retries operational failures, reports unreadable rows, and
  leaves existing envelopes unchanged. Upgrades need no manual reseal step;
  retiring a secrets key still requires operator rotation.
  Missing keys produce one skipped summary and audit event until restart.
  Operator rotation and verification skip malformed argument documents and
  count them as unreadable instead of aborting the pass.
