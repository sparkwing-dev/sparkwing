# Go test impact experiment

## Question

Could a package-level selector make Sparkwing's pull request feedback take about one minute while the full gate runs less often?

## Method

`bin/test-impact-probe.py` reads the current Go import and test-import graph from both committed modules. For each of the 80 most recent non-merge commits at `341f7b6c5`, it finds packages containing changed Go files, then adds packages that import changed production packages, transitively. Changes to Go module files or CI workflows select all 133 packages. This replays old diffs against one current graph; it is a sizing sample, not historical validation.

Reproduce the selection with `python3 bin/test-impact-probe.py --history 80`. The repository's `sparkwing run test --sw-local-only` passed the root module's full Go suite in 300 seconds. Another worktree's race suite was active on the same host, so this run does not measure an idle baseline. Its `cmd/sparkwing`, `internal/orchestrator`, and `pkg/store` package times were 278, 164, and 86 seconds, respectively.

The selector does not map embedded files, test fixtures, generated code, or runtime dependencies. It also does not select frontend tests or Sparkwing's contract checks. Its selected counts are therefore an optimistic ceiling on speed, not a safe CI policy.

## Result

| Selected Go packages | Commits |
| --- | ---: |
| 0 | 15 |
| 1–2 | 23 |
| 3–13 | 3 |
| 69–72 | 28 |
| All 133 | 11 |

At least one of `cmd/sparkwing`, `internal/orchestrator`, or `pkg/store` appears in 49 of 80 selections. Each has an idle-host full-suite duration above one minute in [Delivery](../../DELIVERY.md#heavy-packages). In 39 of 80 commits the selector chooses roughly half the repository or all of it. Package-level selection can make small, local Go changes cheap, but it does not make a one-minute test lane the common case for this sample.

## Next experiment

For a faster PR lane, measure selection at the test level on a shadow run. Keep the existing gate while recording selected tests, skipped tests, added instrumentation cost, and full-gate failures the selected run misses. Track non-Go inputs and always-run tests before allowing selection to decide a merge. Compare end-to-end CI wall time, including checkout, build, lint, browser checks, and queueing; package counts alone cannot establish the one-minute target.
