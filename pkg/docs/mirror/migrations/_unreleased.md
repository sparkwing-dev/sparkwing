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
