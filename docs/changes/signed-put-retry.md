### Fixed

- **runner:** Retry transient signed object uploads before failing jobs
  Seekable blob, source snapshot and log uploads make at most three attempts,
  reuse valid signing tickets and renew expired ones. Streaming readers get
  one attempt. Errors retain the operation, host, failure class and attempt
  count while redacting signed URLs.
