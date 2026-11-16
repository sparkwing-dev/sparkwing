### Changed

- **cli:** `runs list`, `runs logs` and `runs grep` refuse an unknown `--status`
  The error lists the stored run states (pending, running, success, dry_run,
  failed, cancelled) and suggests the nearest one. An unknown value used to
  match nothing and exit 0.

### Fixed

- **cli:** Common mistakes now name what went wrong and the command that fixes it
  A pipeline name that sparkwing.yaml does not list and the SDK does not
  register reads `no pipeline named "biuld"` with the nearest listed name,
  instead of telling you to register the typo. A misspelled flag suggests the
  nearest one. `runs logs` with
  no arguments asks for `--run` or `--grep`. A stray positional names the flag
  that takes it, such as `--run RUN_ID`. An empty `runs list --pipeline` result
  suggests a close sparkwing.yaml name. A run outside a git repository says to
  run `git init`.
- **secrets:** Refusals name the secret and the fix
  `secrets get` of a masked secret says only a run that declares it can read
  the value and that `--variable` stores readable values. A run reading an
  undeclared secret names it and the `sw` tag to add. The missing-secret
  preflight shows the exact optional tag instead of `NAME`. A secret another
  team holds still reads exactly like a missing one.
- **sdk:** A job reading another job's output without depending on it names both jobs
  The refusal says to add `.Needs()`, in place of "resource is outside the
  authorized binding". Reads of jobs outside the caller's run keep the generic
  refusal.
- **child runs:** An awaited child that fails names its run ID, the failed job and its error
  Pipeline-name advice is kept for children that could not plan.
- **config:** sparkwing.yaml errors give the real reason and line
  Cron values report the field and range, unknown `@` aliases list the
  supported ones, and an unknown time zone names the value. YAML syntax errors
  carry the parser's problem and the line where the document went wrong, and
  the file is named once. A config file with a broad mode prints the `chmod`
  that fixes it.
