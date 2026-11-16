### Changed

- **cli:** A missing run, secret, schedule or other record names what was not found and exits 4
  `runs logs --run nope` and its siblings print run "nope" not found with a hint such as
  `list runs with sparkwing runs list`, carrying `--profile` when one was given, instead of the
  controller route. Every not-found refusal exits 4, the code `runs status` already used, so
  `runs cancel`, `secrets get` and `schedules show` no longer exit 1. JSON keeps code `not_found`.
- **cli:** `secrets set` refuses empty stdin or an empty `--file`
  An empty pipe stored an empty secret and exited 0. `--value ''` still stores an empty value.
- **cli:** An unadmitted run against a running local controller says how to proceed
  The refusal is a `conflict` that names `sparkwing daemon stop` or a private `SPARKWING_HOME`,
  instead of an internal error.

### Fixed

- **cli:** `daemon stop` without a reachable systemd user manager says so and how to repair it
- **cli:** `pipeline list` shows each pipeline's description once the pipelines are built
  The listing looked for the built binary under a different cache key than the build used, so
  descriptions never appeared. Before the first build it says so and names `sparkwing cache warm`.
- **cli:** `cache explain` help no longer promises a pipeline argument
  It explains the repository's `.sparkwing` directory, whose pipelines share one binary.
