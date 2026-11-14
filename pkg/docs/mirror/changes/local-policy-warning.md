### Fixed

- **cli:** The CLI no longer warns about the version policy once it is cached
  `sparkwing pipeline plan`, `pipeline describe` and the CLI side of `sparkwing run`
  treated a readable cached policy as a fallback, so they printed a warning on every
  command, and `sparkwing update` could not clear it. They now stay quiet when the
  policy is cached, and warn only when no policy is cached or the cached policy does
  not list the running release. `sparkwing run` also prints the warning from its
  engine, and runs store it on the run; that stops once `sparkwing update` installs
  an engine release that contains this fix. The hint reads "Run `sparkwing update`
  to cache the signed version policy.", and the `sparkwing doctor` line for a
  missing policy names the same command.
