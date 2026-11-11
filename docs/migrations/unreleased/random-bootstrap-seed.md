## Random bootstrap administrator seeds

**Before:** a constant-byte `bootstrap-admin-token` could initialize a store.
**After:** that file stops controller startup, including after its bootstrap
credential was consumed or revoked. This check does not revoke credentials
already in the store. Replacing the file with a random seed cannot repair an
initialized store: its bootstrap gate remains consumed. Keep the existing store.

For a fresh store, use the generator in [authentication](../../auth.md#deployment-bootstrap).
For an existing store, follow [weak bootstrap credential recovery](../../auth.md#weak-bootstrap-credential-recovery).
It covers zero-grace rotation before upgrade, private recovery after a refused
restart, and removal of the mounted file. The replacement credential must not
be mounted as a new bootstrap seed.
