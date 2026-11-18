### Security

- **controller:** Seal legacy plaintext run and schedule arguments automatically on startup
  A background pass retries operational failures, reports unreadable rows, and
  leaves existing envelopes unchanged. Upgrades need no manual reseal step;
  retiring a secrets key still requires operator rotation.
