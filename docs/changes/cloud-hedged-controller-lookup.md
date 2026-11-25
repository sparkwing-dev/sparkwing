### Changed

- **cloud:** Cloud Pods reach the controller sooner after starting
  Strict network policy drops a new Pod's traffic until it attaches, so the first lookup of the controller's address was lost and retried only after 1 s, in both the planning Pod and each job Pod. The runner now starts a fresh controller lookup every 200 ms, up to five lookups, and once one answers it connects to the controller as before. The connection still verifies the controller's TLS certificate, and a failed lookup or connection reports the same error as before.
