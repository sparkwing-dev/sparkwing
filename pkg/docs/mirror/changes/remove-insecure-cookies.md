### Removed

- **controller (Breaking):** Remove `--insecure-cookies` and Helm `ingress.allowInsecure`
  All dashboard cookies keep Secure and the `__Host-` prefix. The web client
  reads only `__Host-sw_csrf`, matching session CLI enrollment. A remote HTTP
  connection exposes bare session cookies in clear text. On loopback, other
  ports share bare cookies, so a local service can receive the session or plant
  the CSRF cookie, leaving CSRF protection to the Origin check. Browser login
  requires HTTPS or loopback. Ingress rendering requires `ingress.tls`.
  See [migration guide](docs/migrations/_unreleased.md#secure-dashboard-cookies).
