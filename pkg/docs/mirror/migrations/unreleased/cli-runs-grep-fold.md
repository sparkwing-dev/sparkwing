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

`sparkwing runs grep --run RUN_ID --grep P` replaces a `--grep` on
`runs logs --run RUN_ID`. It applies the same Go regular expression on the controller to the
run's output lines, lifecycle events and their fields, and prints the matching
records in the `runs logs` output formats. With `--run`, `runs grep` takes only
`--job`, `--grep`, `--since` and `--profile`; the cross-run filters, `--limit`,
`--max-matches`, `--quiet` and `--cursor` are refused with `--run`. A `--grep`
on `runs logs` is refused with a usage error that names `runs grep`.

The per-run reading options of `runs logs` no longer combine with a pattern.
For `--follow`, `--head`, `--tail` and `--lines`, read the run with `runs logs`
and filter its plain output:

| Before | After |
|---|---|
| `sparkwing runs logs --run RUN_ID --grep P --follow` | `sparkwing runs logs --run RUN_ID --follow -o plain \| grep -E P` |
| `sparkwing runs logs --run RUN_ID --grep P --head 50` | `sparkwing runs logs --run RUN_ID --head 50 -o plain \| grep -E P` |
| `sparkwing runs logs --run RUN_ID --grep P --tail 50` | `sparkwing runs logs --run RUN_ID --tail 50 -o plain \| grep -E P` |
| `sparkwing runs logs --run RUN_ID --grep P --lines A:B` | `sparkwing runs logs --run RUN_ID --lines A:B -o plain \| grep -E P` |

The pipe is not the same search. It matches the rendered plain text with
`grep -E` rules, not the controller's Go regular expression, so it can miss
matches the old form found: lifecycle events and structured fields that plain
output leaves out or renders differently, and patterns that only Go's syntax
accepts, such as `\d` or `(?i)`. `--head`, `--tail` and `--lines` still count a
log's lines before the filter. With `--follow`, add `--line-buffered` to `grep`
when piping further.

`--tree`, `--events-only` and `--no-events` have no pattern form. `runs grep
--run RUN_ID` searches the run's output and events together and does not
descend into child runs; search each child run by its own ID.
