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
  environment contains the pool's controller bearer. Complete the
  [cache-only runner credential](#cache-only-runner-credential) migration
  before a hosted pod can receive it.
- **Why:** A short-lived node bearer must preserve the pool's billing identity
  without becoming another credential that can poll or mutate unrelated work.

## Cache-only runner credential

- **Before:** `controller.tokenSecret` supplied the runner's controller bearer,
  the runner and controller cache-client bearer, and the cache server bearer.
- **After:** `controller.tokenSecret` supplies only controller authority.
  `cache.tokenSecret` supplies `SPARKWING_CACHE_TOKEN` to cache clients and
  `SPARKWING_API_TOKEN` to the cache server. This includes runner and controller
  clients pointed at an external cache while the bundled cache is disabled.
  The chart refuses an authenticated cache with no cache Secret and refuses the
  exact same Secret key for both roles.
- **Migration:** Create a cache credential that is not a Sparkwing controller
  token. Store it in an existing Kubernetes Secret, then set
  `cache.tokenSecret.name` and, when needed, `cache.tokenSecret.key`. For
  `sparkwing-full`, put these values under `sparkwing-runner-bundle.cache`.
  Keep `controller.tokenSecret` configured for runner and logs authorization.
- **Limit:** The trigger-compiled Plan process and node processes still inherit
  the shared cache bearer. This split prevents that bearer from authenticating
  to the controller; it does not isolate cache content between repositories.
- **Why:** A hosted node can receive cache access beside its bound controller
  credential without also receiving the pool's controller bearer through the
  cache setting.
