### Changed

- **controller:** The PostgreSQL pool keeps up to 25 idle connections
  The controller's pool previously kept 5 idle connections and closed the rest after each burst of concurrent requests, so the next burst paid a new TLS handshake and login for each one. It now keeps every connection it opened, up to its existing limit of 25, until a connection has been idle for five minutes.
