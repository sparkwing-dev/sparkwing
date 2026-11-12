### Fixed

- **local runtime:** Keep lifecycle failures during runner shutdown
  Local runner watch and drain cleanup preserve genuine failures alongside
  cancellation. A cancellation from the cleanup owner remains a clean stop.
