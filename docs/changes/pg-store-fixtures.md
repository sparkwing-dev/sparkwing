### Changed

- **tests:** Reuse initialized PostgreSQL templates for ready-store fixtures
  Each fixture keeps its own database and teardown. Empty targets still exercise
  fresh schema creation and migrations, and restricted roles retain the existing
  schema path. Test selection, iterations and seeds stay unchanged.
