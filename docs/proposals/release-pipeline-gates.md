# Gate the `release` pipeline on `pre-commit` + `pre-push`

Status: implemented in v0.5.1.

## Problem

`.sparkwing/jobs/release.go` defines a 7-node DAG (`discover-version`, `validate-version`, `check-clean-tree`, `prepare-changelog`, `bump-self-replace`, `push-tag`, `restore-self-replace`) that gates a release on **shape** -- clean tree, free tag, non-empty `[Unreleased]` -- but not on **substance**. None of the checks the `pre-commit` pipeline runs (gofmt, vet, em-dashes, tracker-IDs) nor the ones `pre-push` runs (golangci-lint, `go test -race`, govulncheck, shellcheck, markdownlint) run during release.

A human pushing directly to `main` hits both pipelines. A release tag pushed via `sparkwing run release` skips both. That's how three em-dash violations shipped in v0.5.0 -- `pkg/projectconfig/projectconfig.go:138`, `internal/profile/resolve.go` (8 sites), and `DESIGN-shared-state.md` (~50 sites) -- all caught by `pre-commit em-dashes` on the next human push but invisible to the release path.

## Proposal

Add two new jobs to the release pipeline that run the existing `pre-commit` and `pre-push` pipelines as child runs (via `sparkwing.RunAndAwait`) and gate every mutating release step on their success.

### New DAG

```
discover-version ─┬→ validate-version ──────────────────────────────────┐
                  │                                                     │
check-clean-tree ─┼→ gate-pre-commit ──┬→ prepare-changelog ────────────┤
                  │                    │                                │
                  └→ gate-pre-push  ───┼→ bump-self-replace ────────────┴→ push-tag → restore-self-replace
                                       │
                                       (both gates block downstream)
```

Both gates run after `check-clean-tree`, in parallel with each other, *before* any release-time mutation (CHANGELOG rewrite, `.sparkwing/go.mod` self-replace strip, tag push). If either fails, no commit lands -- the working tree stays exactly as the operator left it.

### Implementation

Single new job type in `.sparkwing/jobs/release.go`:

```go
type runPipelineGateJob struct {
    sparkwing.Base
    Pipeline string
}

func (j *runPipelineGateJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
    sparkwing.Step(w, "run", j.run).SafeWithoutDryRun()
    return nil, nil
}

func (j *runPipelineGateJob) run(ctx context.Context) error {
    sparkwing.Info(ctx, "gating release on child pipeline %q", j.Pipeline)
    _, err := sparkwing.RunAndAwait[any, sparkwing.NoInputs](ctx, j.Pipeline, j.Pipeline)
    if err != nil {
        return fmt.Errorf("release gate %q failed: %w", j.Pipeline, err)
    }
    return nil
}
```

Plan wire-up:

```go
gatePreCommit := sparkwing.Job(plan, "gate-pre-commit", &runPipelineGateJob{Pipeline: "pre-commit"})
gatePreCommit.Needs(clean)

gatePrePush := sparkwing.Job(plan, "gate-pre-push", &runPipelineGateJob{Pipeline: "pre-push"})
gatePrePush.Needs(clean)

changelog.Needs(discover, gatePreCommit, gatePrePush)   // was: discover, clean
bumpSelf.Needs(discover, gatePreCommit, gatePrePush)    // was: discover, clean
```

`changelog` and `bumpSelf` lose their direct dep on `clean` because they now transitively depend on it through the gates.

## Why `RunAndAwait` over the alternatives

| Option | Verdict |
|--------|---------|
| **`RunAndAwait` child-run (chosen)** | Single source of truth (the gates are literally the same pipelines a human runs). Uses the v0.5.0-native `child_run_start`/`child_run_finish` events for parent-side observability. ~30 lines of code. |
| Shell out to `sparkwing run pre-{commit,push}` from an exec step | Trivial but recursive process spawn; loses event/log integration; no parent-child correlation in the stream. |
| Inline the checks (golangci-lint, race, vuln, ...) in `release.go` | Flat DAG, parallelizable per check -- but duplicates the source of truth across `pre-push` and `release`. Guaranteed drift. |
| Extract `pre-push` to a library function called by both | No nesting, no duplication -- but `pre-push` is itself a `sparkwing.Plan`, and extracting it loses the per-step granularity it currently has. Bigger refactor. |

## Cost & risk

- **Wall-clock:** release goes from ~15s to ~50s (gates add the slower of pre-commit ~6s and pre-push ~36s, in parallel). Releases are infrequent and the GitHub Actions workflow that follows the tag push is async anyway.
- **Code:** one job struct + run method + four wire-up lines in `release.go`. Zero changes to `pre-commit` / `pre-push`.
- **Backwards compatibility:** `sparkwing run release --version vX.Y.Z` semantics unchanged. The new behavior is "release can fail earlier with a clear `gate-pre-{commit,push}` reason" instead of shipping a hidden lint violation.
- **Dry-run:** gates use `SafeWithoutDryRun()` -- they execute the real child pipelines even in `--sw-dry-run`. This is intentional: a dry-run that skips its own gates can't catch the very failure mode the gates exist for. The child pipelines are themselves read-only.

## Out of scope

- `bin/pre-release-test.sh` references packages from the pre-refactor layout (`internal/controller`, `internal/cli`, `pkg/workflow`, `pkg/step`) that no longer exist. It would fail to run if invoked. Either delete or rewrite -- separate ticket.
- A `--sw-allow=skip-gates` escape hatch for emergency hot-fix releases. Defer until there's a real need; until then "no skip" is the safe default.
