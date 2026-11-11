### Removed

- **controller (Breaking):** Remove `--insecure-cookies` and Helm `ingress.allowInsecure`
  All dashboard cookies keep Secure and the `__Host-` prefix. The web client
  reads only `__Host-sw_csrf`, matching session CLI enrollment. Secure prevents
  session cookies crossing remote HTTP connections in clear text. Cookies still
  have no port scope: another loopback service can receive `__Host-sw_session`
  and read or plant `__Host-sw_csrf`. The Origin check and the CSRF token tied to
  the session protect writes there. Do not run untrusted loopback services
  alongside the dashboard. Browser login requires HTTPS or loopback.
  Ingress rendering requires `ingress.tls`.
  See [migration guide](docs/migrations/_unreleased.md#secure-dashboard-cookies).
