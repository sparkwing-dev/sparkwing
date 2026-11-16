## runs grep is the one log search

**Before:** `sparkwing runs logs --grep PATTERN` without `--run` searched across
runs with the same filters as `runs grep`, and `runs logs --run ID --grep
PATTERN` filtered one run's lines.

**After:** `runs logs` reads one run and requires `--run`. `runs grep` does all
searching:

| Before | After |
|---|---|
| `sparkwing runs logs --grep fatal --pipeline check` | `sparkwing runs grep --grep fatal --pipeline check` |
| `sparkwing runs logs --run RUN_ID --grep fatal` | `sparkwing runs grep --run RUN_ID --grep fatal` |

With `--run`, `runs grep` prints the run's matching records in the `runs logs`
output formats. The cross-run filters, `--limit`, `--max-matches`, `--quiet` and
`--cursor` do not apply to one run and are refused with `--run`. A `--grep` on
`runs logs` is refused with a usage error that names `runs grep`.
