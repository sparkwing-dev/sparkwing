### Fixed

- **runner:** Keep the Windows profile directory available while installing pipeline SDKs
  Windows personal runners can plan uploaded pipelines without failing with `USERPROFILE` missing. The executing runner supplies the directory; pipeline requests cannot override it.
