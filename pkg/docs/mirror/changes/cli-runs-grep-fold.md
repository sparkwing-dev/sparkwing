### Changed

- **cli (Breaking):** `runs grep` is the one log search; `runs logs` reads one run
  `runs logs` loses `--grep` and the cross-run filters (`--pipeline`,
  `--status`, `--branch`, `--sha`, `--started-after`, `--started-before`,
  `--limit`, `--max-matches` and `--quiet`) and requires `--run`. Search across
  runs with `runs grep --grep PATTERN`, or search one run's logs with
  `runs grep --run RUN_ID --grep PATTERN`. A `--grep` on `runs logs` is refused
  with a usage error that names `runs grep`. `--follow`, `--head`, `--tail`,
  `--lines`, `--tree`, `--events-only` and `--no-events` no longer combine
  with a pattern; pipe `runs logs --run RUN_ID ... -o plain` into `grep -E`. See the [migration guide](docs/migrations/_unreleased.md#runs-grep-is-the-one-log-search)
