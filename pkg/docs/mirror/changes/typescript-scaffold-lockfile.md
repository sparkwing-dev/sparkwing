### Changed

- **cli (Breaking):** `pipeline new --template typescript` writes the npm lockfile itself
  The TypeScript scaffold runs `npm install --package-lock-only` in `.sparkwing/`,
  so `sparkwing run` works right after scaffolding. Without npm on PATH, or when
  npm fails, the command keeps the written files, exits non-zero and prints the
  npm command to run. JSON and plain output no longer carry the setup command.
  See the [migration guide](docs/migrations/_unreleased.md#the-typescript-scaffold-writes-its-own-lockfile)
