### Fixed

- **runner:** Report stopped claim intake while held work drains
  Fatal claim failures immediately degrade runner readiness and log an error,
  with periodic warnings and the remaining held-unit count. Valid held work
  keeps running and the runner exits after it finishes.
