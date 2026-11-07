# Local dashboard behind Tailscale Serve

Design note for the remaining piece of local-controller step 5
([local-controller-restart.md](local-controller-restart.md), "Dashboard, CLI
ownership and landing"). Not implemented. Stored cookie sessions, the always-on
loopback bind and the removal of `--allow-remote` have landed. A browser on
another machine currently reaches the dashboard only through an SSH port forward.

## What exists to build on

- `originguard.Policy.AllowOrigins` already admits extra exact origins and
  their hosts; `guardDashboard` passes none today.
- `requestOrigin` in `internal/wingd/local_login.go` is the single place that
  turns a request into the origin used for code redemption, session lookup,
  the write Origin check and cookie naming. It returns `http://` + Host.
- `sessionCookieName` and the login/logout handlers already switch to
  `__Host-sw_dashboard` with `Secure` for an `https://` origin.
- Codes and sessions store their exact origin, so a session redeemed at the
  loopback origin never authorizes the Serve origin, or the reverse.

## Proposed change (about 150 lines plus tests)

1. Config: `dashboard.allowed_hosts: [workstation.example.ts.net]`. Each entry
   is one DNS name, no scheme, port, path or wildcard. Validation refuses
   loopback names and IP literals. Read at controller start like `address`.
2. Guard: pass `https://<host>` for each entry as `AllowOrigins`.
3. Origin: `requestOrigin` returns `https://` + Host when Host exactly matches
   an allowed host, else `http://` + Host. The proxy terminates TLS, so the
   scheme comes from configuration, never from `X-Forwarded-Proto`.
4. Minting: `sparkwing dashboard open --host workstation.example.ts.net` sends
   the target origin in the socket mint request. The controller refuses a host
   that is not configured. Without `--host` the link stays on loopback.
5. Identity: ignore `Tailscale-User-*` and every forwarded header. The tailnet
   identity is not authentication here. The sign-in code minted by the socket
   peer is, the same as on loopback.
6. Docs: replace the SSH-forward guidance in `machine-config.md` with a Serve
   recipe (`tailscale serve --bg 4343`) plus the shared-backend warning from
   Tailscale's Serve guide.

## Open questions

1. **Host header.** The design assumes Serve forwards the browser's Host
   (`workstation.example.ts.net`) to `127.0.0.1:4343`. If Serve rewrites Host
   to the backend address, Host-based origin detection cannot work. The
   fallback would be to trust `X-Forwarded-Host` only from loopback peers,
   which any local process can also send. This needs a real Serve check before
   writing code. It is the reason this note exists instead of an
   implementation.
2. **Other users on the tailnet.** Anyone who can reach the Serve URL reaches
   the sign-in page. A link is still required, so reaching it grants nothing,
   but the static shell and its bundle are visible to them. Is that acceptable,
   or should non-loopback requests without a session get 401 for every path?
3. **Funnel.** Should configuration refuse a host when Tailscale Funnel exposes
   it publicly? Sparkwing cannot detect Funnel. The proposal is to document
   "Serve only, never Funnel" rather than enforce it.
4. **Read-only default.** Should a Serve origin force `read_only` sessions, or
   inherit `dashboard.read_only`? The proposal is to inherit it, because the
   link is minted by the machine's own account.
5. **Acceptance.** Real Serve routing needs a tailnet in CI or a manual
   runbook. The proposal is a manual runbook in the operator vault plus unit
   tests with a forged Host, consistent with keeping infrastructure checks out
   of every startup.
