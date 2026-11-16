### Changed

- **cli:** Root help groups commands into Local, Cloud, and Admin and help sections
  JSON help adds a `section` field to each top-level subcommand.
- **cli:** `run --help` opens with a plain description, common flags and examples
  `run --help-all` adds the routing, publication and standalone CI rules as details and lists
  every flag. JSON help carries the rules in a `details` field.

### Added

- **cli:** Common guesses suggest the real command
  Typing runs show suggests `runs status`, pipelines list suggests `pipeline list`, secrets add
  suggests `secrets set`, logout suggests `profiles delete`, and similar. The command still fails
  with a usage error; nothing runs under the guessed name.
- **cli:** Examples on the most-used commands
  `runs`, `pipeline`, `secrets`, `schedules`, `profiles add`, `login`, `dashboard open`,
  `daemon stop`, `docs search` and `cache explain` help pages show one to three invocations.
