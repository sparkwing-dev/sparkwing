# Migrating to the next release

## Dashboard session and CSRF cookies carry the `__Host-` prefix

On a dashboard that keeps `Secure` cookies, the session and CSRF cookies are
now named `__Host-sw_session` and `__Host-sw_csrf` rather than `sw_session` and
`sw_csrf`. A browser refuses a `__Host-` cookie that carries a `Domain`
attribute, which is the write a sibling host under the same registrable domain
would use to plant a session on a victim.

Every signed-in browser is signed out once on upgrade, because the old cookie
names are no longer read. Signing in again is the whole of the recovery.

A custom browser client that reads the CSRF token out of `document.cookie` to
fill `X-CSRF-Token` reads `__Host-sw_csrf` first and falls back to `sw_csrf`,
because a deployment running `SPARKWING_WEB_INSECURE_COOKIES=1` drops the
prefix along with `Secure`. See [auth.md](../auth.md#dashboard-authorization) for
the cookie contract.

## BoundCipher takes the owning team

`controller.BoundCipher` binds an envelope to the team that owns its row.
`SealBound(name, scope, shared, masked, plain)` is now
`SealBound(team, name, scope, shared, masked, plain)`, and `OpenBound` gains the
same leading `team`. A custom cipher passed to `WithSecretsCipher` adds the
parameter and includes it in its additional authenticated data;
`ciphertest.TestBoundCipher` now fails an implementation whose envelope opens
under another team.

A custom cipher that also implements `controller.LegacyCipher`
(`OpenLegacy(name, scope, shared, masked, envelope)`) lets the controller
reseal the envelopes it wrote before this release. Without it those rows are
logged and left as they are at startup, and each read of one answers `500`
until the secret is set again.

A self-hosted controller using the built-in cipher needs no change. Its first
start reseals every row in place and logs how many it resealed; keep the same
`SPARKWING_SECRETS_KEY` across the upgrade.
