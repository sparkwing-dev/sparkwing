### Fixed

- **CLI:** Preserve UTF-8 rune boundaries in Spark resolver-error summaries
  Pretty output applies the shared display limit before a multibyte character can be split. JSON keeps the full original error.
