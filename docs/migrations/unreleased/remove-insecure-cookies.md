## Secure dashboard cookies

**Before:** `sparkwing-controller --insecure-cookies` dropped Secure and the
`__Host-` prefix. Helm `ingress.allowInsecure=true` permitted an ingress without
TLS. The web client could read `sw_csrf`.

**After:** Remove `--insecure-cookies` from controller invocations and delete
`ingress.allowInsecure` from Helm values. The removed flag fails as unknown.
The chart rejects the removed value, including `false`. All controllers set Secure `__Host-sw_session` and `__Host-sw_csrf` cookies, and
the web client reads only `__Host-sw_csrf`. Sign in again after changing the
browser origin. Custom browser clients must use the prefixed names.

**Why:** Without Secure, a session cookie crosses a remote HTTP connection in
clear text. Cookies have no port isolation on loopback, so another local HTTP
service can receive `sw_session` and read or plant `sw_csrf`. That leaves CSRF
protection to the Origin check. Session CLI enrollment already sends the
prefixed CSRF cookie and failed against the removed mode.

Choose a supported browser route:

- Localhost or `127.0.0.1` needs no certificate setup in browsers that accept
  Secure cookies on loopback. Secure does not add port isolation, so keep local
  services trusted.
- Disable ingress and use `kubectl port-forward`, or use `ssh -L`, then open the
  loopback URL.
- Use `tailscale serve` for an HTTPS URL within a tailnet.
- Set chart `ingress.tls`, for example with cert-manager and Let's Encrypt.
  An enabled ingress with an empty TLS list fails rendering.
- Put Caddy in front of the native controller's `--trusted-proxy-addr` listener.
  Restrict that listener to a proxy that overwrites `X-Real-IP` and forwards
  `X-Forwarded-Proto: https`.

See [Serving the dashboard over HTTPS](../self-hosting.md#serving-the-dashboard-over-https)
for commands and chart values.
