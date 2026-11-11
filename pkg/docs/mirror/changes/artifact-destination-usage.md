### Fixed

- **cli:** Treat an occupied artifact download destination as invalid usage
  The command exits 2 instead of the incomplete-observation exit 3, names the
  occupied path and asks for a distinct `--dest`. Retrying the same destination
  keeps refusing it.
