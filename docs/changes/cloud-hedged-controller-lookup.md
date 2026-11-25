### Changed

- **cloud:** Cloud Pods reach the controller sooner after starting
  Strict network policy drops a new Pod's traffic until it attaches, so the first lookup of the controller's address was lost and retried only after 1 s, in both the planning Pod and each job Pod. The runner now sends a fresh lookup every 200 ms, up to five, and uses the first answer. The connection still verifies the controller's TLS certificate, and a failed lookup reports the same error as before.
