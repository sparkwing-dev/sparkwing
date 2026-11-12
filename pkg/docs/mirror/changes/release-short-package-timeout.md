### Fixed

- **release:** Reuse the shared per-package deadline for broad short tests
  Local release cuts use the same bounded 25-minute package policy as the land
  gate, rather than Go's ten-minute default. Existing concurrency and the
  release job deadline remain unchanged.
