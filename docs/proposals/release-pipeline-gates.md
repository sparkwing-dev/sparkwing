# Gate the `release` pipeline on `pre-commit` + `pre-push`

Status: implemented in v0.5.1.

## Problem

`.sparkwing/jobs/release.go` defines a release DAG that gates a release on **shape** -- clean tree, free tag, non-empty `[Unreleased]` -- but not on **substance**. None of the checks the `pre-commit` pipeline runs (gofmt, vet, em-dashes, tracker-IDs) nor the ones `pre-push` runs (golangci-lint, `go test -race`, govulncheck, shellcheck, markdownlint) run during release.

A human pushing directly to `main` hits both pipelines. A release tag pushed via `sparkwing run release` skipped both. That's how three em-dash violations shipped in v0.5.0 -- `pkg/projectconfig/projectconfig.go:138`, `internal/profile/resolve.go` (8 sites), and `DESIGN-shared-state.md` (~50 sites) -- all caught by `pre-commit em-dashes` on the next human push but invisible to the release path.

## Solution

`PreCommit` and `PrePush` are exported job types in the same `package jobs` as `Release`. They compose directly into the release plan as regular nodes:

```go
gatePreCommit := sparkwing.Job(plan, "gate-pre-commit", &PreCommit{})
gatePreCommit.Needs(clean)

gatePrePush := sparkwing.Job(plan, "gate-pre-push", (&PrePush{}).run)
gatePrePush.Needs(clean)

changelog.Needs(discover, gatePreCommit, gatePrePush)   // was: discover, clean
bumpSelf.Needs(discover, gatePreCommit, gatePrePush)    // was: discover, clean
```

Total addition: 4 lines of wire-up. No new job type. The gate nodes use the same `PreCommit.Work` / `PrePush.run` check set a human invocation does -- single source of truth, zero drift risk. Release-line tags also fence the current branch against its remote ref before pushing the tag, so an intentional maintenance release is checked against the branch it will publish rather than unrelated newer default-branch work.

### Resulting DAG

```
discover-version ─┬→ validate-version ──────────────────────────┐
                  │                                             │
check-clean-tree ─┼→ gate-pre-commit ──┬→ prepare-changelog ────┤
                  │                    │                        │
                  └→ gate-pre-push  ───┼→ bump-self-replace ────┴→ push-tag → restore-self-replace
                                       │
                                       (both gates block downstream)
```

Both gates run in parallel after `check-clean-tree`, *before* any release-time mutation. If either fails, no commit lands -- the working tree stays exactly as the operator left it.

## Alternatives considered (and why not)

| Option | Verdict |
|--------|---------|
| **Direct job composition (chosen)** | 4 lines. Same package, same process, same orchestrator, native event integration. No new code paths. |
| `sparkwing.RunAndAwait` to spawn child runs | Looks elegant but the local orchestrator's `PipelineAwaiter` dispatches through the controller's trigger queue -- which routes to a warm-runner pod that fetches source via gitcache. The gate then fails whenever the gitcache is cold for the parent's SHA (observed: tried this first; release failed because the gitcache hadn't yet picked up the SHA pushed seconds earlier). Adds a remote infra dependency to what should be a local-only check. |
| Subprocess: shell out to `sparkwing run <pipeline>` | Bypasses the gitcache issue but spawns a process, loses event integration, and re-loads the pipeline registry. All cost, no benefit when direct composition is available. |
| Inline the checks (golangci-lint, race, vuln, ...) in `release.go` | Flat DAG -- but duplicates the source of truth across `pre-push` and `release`. Guaranteed drift the next time `pre-push` adds a check. |

The lesson: jobs are first-class composable units in this SDK. When you want pipeline A to enforce pipeline B's checks, **import B's job type and add it to A's plan**. The cross-pipeline `RunAndAwait` machinery exists for harder cases (different repo, different fleet, different schedule); a local gate is the easy case.

## Cost & risk

- **Wall-clock:** release goes from ~15s to ~50s (gates add the slower of pre-commit ~6s and pre-push ~36s, in parallel). Releases are infrequent and the GitHub Actions workflow that follows the tag push is async anyway.
- **Code:** 4 wire-up lines in `release.go`. Zero new code, zero changes to `pre-commit` / `pre-push`.
- **Backwards compatibility:** `sparkwing run release --version vX.Y.Z` semantics unchanged. The new behavior is "release can fail earlier with a clear `gate-pre-{commit,push}` reason" instead of shipping a hidden lint violation.
- **Dry-run:** `PreCommit` and `PrePush` jobs use the same dry-run semantics they always have -- their steps are read-only checks, so a dry-run executes them normally and reports the same outcome as a real run.
- **Coupling:** the gate wire-up assumes `PreCommit` and `PrePush` remain single-job pipelines. If either later grows a multi-node `Plan`, the gate would only invoke its `Work` method, missing the extra nodes. Cheap to update if/when that happens.

## Out of scope

- `bin/pre-release-test.sh` references packages from the pre-refactor layout (`internal/controller`, `internal/cli`, `pkg/workflow`, `pkg/step`) that no longer exist. It would fail to run if invoked. Either delete or rewrite -- separate ticket.
- An in-process `PipelineAwaiter` implementation for `RunAndAwait` -- a real refactor that would benefit cross-pipeline scenarios beyond this gate. Tracked separately.
- A `--sw-allow=skip-gates` escape hatch for emergency hot-fix releases. Defer until there's a real need; until then "no skip" is the safe default.
