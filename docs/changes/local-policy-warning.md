### Fixed

- **cli:** Local runs no longer warn about the version policy once it is cached
  `sparkwing run`, `pipeline plan`, `pipeline describe` and `runs status` treated a
  readable cached policy as a fallback, so they printed a warning and stored it on
  every run, and `sparkwing update` could not clear it. They now stay quiet when the
  cache is readable, and warn only when no policy is cached, the cache cannot be
  read, or the cached policy does not list the running release. The hint reads
  "Run `sparkwing update` to cache the signed version policy.", and the
  `sparkwing doctor` line for a missing policy names the same command.
