## The TypeScript scaffold writes its own lockfile

**Before:** `sparkwing pipeline new --template typescript` (and
`merge-queue-typescript`) printed an `npm --prefix .sparkwing install
--package-lock-only ...` command, also reported as `setup_command` in JSON and as
a second line in plain output, which you ran before the first `sparkwing run`.

**After:** the scaffold runs that npm command itself and writes
`.sparkwing/package-lock.json`. JSON output has only `pipeline` and `path`, and
plain output prints only the pipeline name. Scripts that ran `setup_command`
can drop that step. When npm is missing or fails, the command exits non-zero
after writing the pipeline files, and its error names the npm command to run.
