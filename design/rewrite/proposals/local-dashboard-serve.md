# Local dashboard behind Tailscale Serve

The dashboard binds loopback. A configured reverse proxy can expose an exact
HTTPS origin without gaining authority from its network or identity headers.

## Configuration and routing

`dashboard.allowed_hosts` contains exact DNS names, without schemes, ports,
paths, trailing dots or wildcards. Loopback names and IP literals are refused.
The controller reads this configuration at startup. Its origin guard admits
`https://<host>` and the matching Host for each configured name. Every other
non-loopback Host is refused.

Tailscale Serve preserves the browser's Host and Origin while terminating TLS
before forwarding to the loopback backend. A real request between tailnet nodes
verified this behavior, including Serve replacing forged forwarded host and
scheme headers. Sparkwing derives HTTPS from its configured host list rather
than trusting any forwarded header. No Tailscale identity header authenticates
a dashboard request.

## Sign-in and sessions

`sparkwing dashboard open --host workstation.example.ts.net` asks api.sock to
mint a one-time code for that configured HTTPS origin. An unknown host is
refused. Omitting the flag selects the serving loopback origin.

The link carries the code in its fragment. The page exchanges it for a session
token kept in localStorage and sent as an Authorization bearer header. Codes
and sessions retain their exact origin, so neither a loopback credential nor
one issued for another configured host can authorize this origin. Sign-in,
sign-out and writes require the addressed origin. Restart and logout retain
the existing revocation behavior.

## Exposure and acceptance

The signed-out static shell remains visible to anyone who can reach the proxy.
API data requires a session. `dashboard.read_only` applies equally to loopback
and proxy sessions because the machine's socket peer mints both.

Use Serve, never Funnel. The dashboard must be the only app at its HTTPS name
and port, because anything else served there shares the browser origin and can
read the stored session. Sparkwing does not probe Tailscale or enforce its
network configuration. Review shared backend-port exposure using the
[Serve guide](https://tailscale.com/docs/features/tailscale-serve). Setup is in
[machine configuration](../../../docs/machine-config.md#tailscale-serve).

The configuration decoder and real local-controller HTTP tests cover exact-host
admission, sign-in, origin-bound reads and writes, and forged headers. The live
Serve forwarding probe is separate infrastructure evidence. It is not part of
every controller startup or a CI dependency on a tailnet.
