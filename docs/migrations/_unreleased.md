# Migrating to the next release

This release adds a controller-owned hosted execution credential. Stop older
controller binaries before opening their shared runs store with this version.

## Bound hosted execution credentials

- **Before:** Every runner bearer authenticated as an ordinary token whose
  scopes and claim identity applied across the controller.
- **After:** Schema 48 persists a hosted execution credential's run, root node,
  delegated pool claim identity, exact claim generation and run-owner quota
  identity, plus the one child output its parent may read. Older controllers
  refuse the store because they cannot enforce those bounds. Existing active
  metered claims are assigned to the principal that created their run during
  migration.
- **Migration:** Stop all controller replicas that share the store, upgrade them
  together, update Go callers of `Client.FinalizeNodeReady` to pass an empty
  policy for the existing offer-first behavior, then restart. An older HTTP
  client may continue sending an empty finalization body. Do not roll a replica
  back after schema 48 is applied.
- **Shipping dependency:** Do not enable hosted dispatch while a pod's cache
  environment contains the pool's controller bearer. Replace the shared
  cache/controller token with a cache-only credential that the controller
  rejects before a hosted pod can receive it.
- **Why:** A short-lived node bearer must preserve the pool's billing identity
  without becoming another credential that can poll or mutate unrelated work.
