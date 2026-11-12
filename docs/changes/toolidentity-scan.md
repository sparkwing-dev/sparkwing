### Fixed

- **cache:** Avoid repeated ancestor scans for missing compiler tools on PATH
  Pipeline cache identity checks stat missing candidates once before resolving symlinks. Present tools keep the same resolved path, size and modification-time identity.
