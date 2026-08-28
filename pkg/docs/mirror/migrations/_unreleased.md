# Migrating to the next release

Staging ground for the breaking changes sitting in `[Unreleased]`. The
pre-release manicuring agent moves these sections into
`docs/migrations/v<X.Y.Z>.md` when the version is cut; until then the
CHANGELOG links here.

## Dashboard browser session hardening

- **Before:** `sparkwing-web --require-login` silently became login-free when
  no controller URL reached the web handler. Login and logout forms accepted
  cross-origin submissions, redirect targets were concatenated into the login
  URL without encoding, and a controller-revoked session could remain accepted
  from a web replica's cache for 60 seconds.
- **After:** Login-required mode refuses to start without `--controller URL` or
  a selected profile with `controller.url`. Browser forms require a same-origin
  CSRF cookie and hidden token, logout verifies the session-bound controller
  token, unsafe `/api/v1/*` mutations require the same token in
  `X-CSRF-Token`, redirect targets are encoded and restricted to same-origin
  paths, and every protected HTML/data/API request revalidates the controller
  session. The proxy removes browser cookies and CSRF headers before adding its
  service bearer. Controller outages and logout failures return `502` without
  clearing the browser cookies.
- **Migration:** Remove `--require-login` from controller-free trusted-network
  deployments, or add a controller session backend. Automation should continue
  to call the controller directly. Custom browser clients must read `sw_csrf`
  and send it as `X-CSRF-Token` on unsafe dashboard API requests. Serve a
  login-required dashboard over HTTPS; use
  `SPARKWING_WEB_INSECURE_COOKIES=1` only for a loopback-only development
  process.
- **Why:** A login flag must never publish an unauthenticated dashboard, and a
  successful logout or server-side revocation must be effective on the next
  request.
