### Changed

- **cli (Breaking):** Controller admin actions select pretty output on a terminal and JSON when piped
  Enrollment pipes must request `--output plain` to pass the raw session to `sparkwing runners add --session-stdin`.
  See the [migration guide](docs/migrations/_unreleased.md#admin-session-output) for before/after commands.
  Freeze, unfreeze and grant accept all output modes. Admin help validates output flags and renders to stdout.
  Internal `__complete` output also follows the stdout terminal default. Generated shell callers already request plain output.
