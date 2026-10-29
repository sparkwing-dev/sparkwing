# Release and scaffold grid verification

Baseline: `1f4998a3e`. Candidate changes: `abf8c7167`, `4cd03c0db`.
No assigned cell was already fixed on the baseline.

All candidate CLI invocations used private `HOME` and `SPARKWING_HOME`
under `/tmp/sw-codex-grid-release`. Go used `GOWORK=off`,
`GOFLAGS=-buildvcs=false`, `GOCACHE=/tmp/sw-gocache`,
`GOPATH=/home/bombuntu/.go`, `GOMODCACHE=/home/bombuntu/.go/pkg/mod`,
`ASDF_DATA_DIR=/home/bombuntu/.asdf`, and Go 1.26.9 directly on PATH.
The temporary directory was removed after verification. No real store,
cluster, database service, publication, push or release was used.

## R02C16: fixed

Built the CLI from this worktree with `-tags qatrust` and
`-ldflags '-X main.Version=v0.68.0-qa.16'`, then ran:

```text
sparkwing-qa -C <fresh-directory> pipeline new --pipeline hello --template minimal
```

On the baseline, dependency setup failed with:

```text
github.com/sparkwing-dev/sparkwing@v0.68.0-qa.16:
reading https://sum.golang.org/lookup/github.com/sparkwing-dev/sparkwing@v0.68.0-qa.16:
404 Not Found; invalid version: unknown revision v0.68.0-qa.16
```

On the fixed source, the same command exited zero and emitted the pipeline
receipt. The generated module retained `v0.68.0-qa.16`. No QA proxy or
checksum overrides were set in the invoking shell.

`TestScaffoldTidyResolvesQAPin` failed against the original source in a
qatrust build because tidy saw the public proxy and no checksum exemption.
`TestScaffoldPinMatchesBuildKind` failed against the original source through
a Go source overlay because a production build selected the QA pin instead
of `v0.67.0`. Both tests pass on the fixed source in both build flavours.
The fix reuses `engine.QAModuleEnvironment`; it adds no setting or limit.

Focused checks, all passed:

```text
go test ./cmd/sparkwing -run 'TestScaffold(PinMatchesBuildKind|TidyResolvesQAPin)$|TestPipelineModuleCommandsHonorToolchainFloor|TestPipelineScaffoldDoesNotRequireGoPatch' -count=1
go test -tags qatrust ./cmd/sparkwing -run 'TestScaffold(PinMatchesBuildKind|TidyResolvesQAPin)$|TestPipelineModuleCommandsHonorToolchainFloor|TestPipelineScaffoldDoesNotRequireGoPatch' -count=1
go test ./pkg/docs ./internal/doccheck
bash bin/sync-docs.sh --check
```

## R01C15: reproduced, implementation pending

Built an unstamped CLI from the worktree and ran `run release --sw-dry-run`
with `SPARKWING_TOOLCHAIN=local` and both homes isolated. `SPARKWING_HOME`
was explicitly `$HOME/.sparkwing`, the case the release guard must refuse.

The command persisted `run-76d352912ed1e8f479ddd997`, emitted `run_started`,
and claimed and started `require-isolated-home`. The initially absent
`state.db` then contained one run. The local handoff timed out before a
final job result, so this attempt establishes intake before the guard,
not the final refusal error. The isolated controller was stopped.

The five focused `TestRelease*Home*` tests in `.sparkwing/jobs` passed,
including the test that executes the guard and observes its isolated-home
error. Those tests protect job ordering, not the CLI's pre-intake boundary.

A decision is pending between a repository release launcher that checks
the home before starting Sparkwing, with a hard cut of the unsafe direct
entrypoint, and a reusable CLI facility for checks before intake.
The CLI must not impose a home policy on unrelated customer pipelines
merely because they are named `release`.

## R04C15: fixture fix verified, external commit pending

Copied `/var/tmp/sw-mig/sparkwing-ci-smoke/.sparkwing` into the temporary
directory and replaced only its SDK dependency with this worktree for
verification. The built CLI listed the fixture pipelines successfully.
`go test -p 2 -count=1 ./...` failed with:

```text
jobs/regex_sweep_test.go:59:12: undefined: sparkwing.SetWorkDir
jobs/regex_sweep_test.go:60:31: undefined: sparkwing.SetWorkDir
```

In that temporary copy, replacing the saved WorkDir, SetWorkDir and cleanup
calls with `t.Chdir(root)` and removing the unused SDK import made the full
author test package pass in 1.603 seconds. Its existing regex sweep tests
exercise the fixture directory and caught the original compile failure.
The external repository remains unchanged because its requested edit path
conflicts with the harness allow-list. It is on branch `bw-3095-v068`.

The migration table, SDK guide, changelog and embedded copies now explain
`SetWorkDir` removal and the `t.Chdir` replacement.

## Remaining coverage

The lead owns the full gate and its test-output file. No full gate,
PostgreSQL suite, release cut, publication, or independent agent review
was run in this lane.
