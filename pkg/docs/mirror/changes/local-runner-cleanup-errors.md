### Fixed

- **local runtime:** Keep lifecycle failures during runner shutdown
  Local runner watch and drain cleanup preserve genuine failures alongside
  cancellation. Only cancellation caused by the cleanup owner's stop
  is clean; other cancellations remain errors when returned after shutdown.
