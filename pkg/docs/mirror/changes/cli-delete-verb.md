### Changed

- **cli (Breaking):** `delete` is the one removal verb
  `pipeline sparks delete`, `profiles delete`, `repos delete` and
  `runners delete` take the place of their `remove` forms and keep the same
  flags. An old `remove` spelling is refused with a usage error that names the
  `delete` command. `tokens revoke`, the `uninstall` verbs and the `prune` verbs
  keep their names. See the [migration guide](docs/migrations/_unreleased.md#delete-is-the-one-removal-verb)
