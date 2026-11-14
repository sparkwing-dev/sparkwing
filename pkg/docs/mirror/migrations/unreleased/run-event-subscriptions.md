## Upgrade the store in place to schema baseline 39

Store schema 39 adds the `run_event_subscriptions` and `run_event_deliveries`
tables. The controller upgrades a schema 38 store automatically; SQLite creates
its usual backup first. Existing data is unchanged and no team has a
subscription until an administrator creates one. Binaries without schema 39
support refuse the upgraded store; use a matching binary or restore its
pre-upgrade backup into a separate store.

Rotating the operator secrets key re-seals subscription signing secrets along
with team secrets and git credentials. No pipeline configuration change is
required.
