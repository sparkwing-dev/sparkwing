### Fixed

- **Example pipeline:** Allow two minutes for the simulated deploy job and its controller reporting acknowledgements
  The previous 30-second job deadline could expire while an annotation was awaiting acknowledgement on a loaded host. Apply and rollout still wait for their reports to finish, and a stalled job remains bounded.
