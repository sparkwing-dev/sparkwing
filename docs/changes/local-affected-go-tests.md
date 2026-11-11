### Changed

- **ci:** Run affected short and critical Go tests before local main pushes and queued landings
  Local and hosted checks share the same dependency and file-reader selection.
  The fast checks keep their one-minute warning budget, and affected tests have
  a separate 45-minute execution deadline with package and test timings.
