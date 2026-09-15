# Migrating to the next release

This release adds a controller-owned hosted execution credential. Stop older
controller binaries before opening their shared runs store with this version.

## Bound hosted execution credentials

- **Before:** Every runner bearer authenticated as an ordinary token whose
  scopes and claim identity applied across the controller.
- **After:** Schema 48 persists a hosted execution credential's run, root node,
  and delegated pool claim identity. Older controllers refuse the store because
  they cannot enforce that binding.
- **Migration:** Stop all controller replicas that share the store, upgrade them
  together, then restart. Do not roll a replica back after schema 48 is applied.
- **Why:** A short-lived node bearer must preserve the pool's billing identity
  without becoming another credential that can poll or mutate unrelated work.
