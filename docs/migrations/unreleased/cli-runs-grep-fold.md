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

With `--run`, `runs grep` takes only `--job`, `--grep`, `--since` and `--profile`. The
per-run reading options of `runs logs` no longer combine with a pattern. Read the
run with `runs logs` and filter the plain output with `grep -E`, because the
pattern is a regular expression:

| Before | After |
|---|---|
| `sparkwing runs logs --run RUN_ID --grep P --follow` | `sparkwing runs logs --run RUN_ID --follow -o plain \| grep -E P` |
| `sparkwing runs logs --run RUN_ID --grep P --head 50` | `sparkwing runs logs --run RUN_ID --head 50 -o plain \| grep -E P` |
| `sparkwing runs logs --run RUN_ID --grep P --tail 50` | `sparkwing runs logs --run RUN_ID --tail 50 -o plain \| grep -E P` |
| `sparkwing runs logs --run RUN_ID --grep P --lines A:B` | `sparkwing runs logs --run RUN_ID --lines A:B -o plain \| grep -E P` |
| `sparkwing runs logs --run RUN_ID --grep P --tree` | `sparkwing runs logs --run RUN_ID --tree -o plain \| grep -E P` |
| `sparkwing runs logs --run RUN_ID --grep P --events-only` | `sparkwing runs logs --run RUN_ID --events-only -o plain \| grep -E P` |
| `sparkwing runs logs --run RUN_ID --grep P --no-events` | `sparkwing runs logs --run RUN_ID --no-events -o plain \| grep -E P` |

`--head`, `--tail` and `--lines` count a log's lines before any pattern applies,
as they did with `--grep`, so the pipe selects the same lines. With `--follow`,
`grep` prints matches as they arrive; add `--line-buffered` when piping further.
