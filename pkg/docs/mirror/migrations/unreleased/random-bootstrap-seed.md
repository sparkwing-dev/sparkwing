## Random bootstrap administrator seeds

**Before:** a constant-byte `bootstrap-admin-token` could initialize a store.
**After:** that file stops controller startup, including after its bootstrap
credential was consumed or revoked. This check does not revoke credentials
already in the store. Replacing the file with a random seed cannot repair an
initialized store: its bootstrap gate remains consumed. Keep the existing store.

For a fresh store, use the generator in [authentication](../../auth.md#deployment-bootstrap).
For an existing store, complete one of the following handoffs and remove the
`bootstrap-admin-token` file from the credentials mount or projected Secret.

If a real deployment administrator already exists, verify that credential can
list operator tokens and that the old bootstrap credential is revoked. Remove
the mounted bootstrap file before restarting the upgraded controller.

If the weak bootstrap credential is still live, treat it as guessable. Before
upgrading, isolate the controller from untrusted traffic and rotate it with zero
grace using the existing CLI and a deployment profile authenticated by that seed:

```sh
sparkwing operator tokens list --profile deployment -o json
umask 077
sparkwing operator tokens rotate --profile deployment --token PREFIX --grace 0 -o plain > replacement-token
```

`PREFIX` is the weak bootstrap credential's prefix from the list, not its full
bearer. Rotation returns the replacement once, revokes the weak credential
immediately and ends its descendants' authority. Store the replacement securely,
update the profile to use it, and verify `operator tokens list` succeeds with it
and fails with the old credential. Remove the mounted bootstrap file, then
upgrade and restart. The replacement is an authentication credential, not a new
bootstrap seed; do not mount it as `bootstrap-admin-token`.

If the upgrade already refuses startup, stop public ingress first. Temporarily
remove the weak bootstrap file and start the same controller/store bound to
loopback (`--addr 127.0.0.1:PORT`) with `--require-auth` retained. Its existing
weak credential still authenticates, so perform the rotation above through that
private endpoint. Keep the bootstrap file removed and restore normal ingress
only after verifying the replacement and old-credential refusal. A loopback bind
is not protection from other users or processes on the host: use a trusted host
and keep the recovery window short.

After rotation, issue a named service administrator with `operator.tokens` and
any other required operator grants to finish setup; that transaction also
revokes remaining bootstrap setup credentials. If no live administrator remains,
use your established credential recovery procedure; generating a replacement
seed does not reopen the bootstrap gate.
