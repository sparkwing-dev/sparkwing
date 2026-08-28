# Migrating to the next release

Staging ground for the breaking changes sitting in `[Unreleased]`. The
pre-release manicuring agent moves these sections into
`docs/migrations/v<X.Y.Z>.md` when the version is cut; until then the
CHANGELOG links here.

## Authenticated dashboard proxy sessions

- **Before:** A request carrying any bearer token bypassed the dashboard's
  login middleware, and the dashboard embedded its controller service bearer
  in authenticated HTML for browser JavaScript to send back.
- **After:** A login-gated dashboard accepts its session cookie only. Its
  browser stays on the dashboard origin, and the server-side proxy adds the
  service bearer to upstream controller requests. Automation should call the
  controller API directly with its own credential instead of using the
  browser-facing dashboard proxy.
- **Why:** A shared service credential in browser HTML could be copied and used
  after logout, collapsing the boundary between a user session and the
  dashboard pod's controller identity.
