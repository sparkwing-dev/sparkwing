# Release and scaffold grid verification

Original baseline: `1f4998a3e`. First-session changes: `abf8c7167`, `4cd03c0db`.
Continuation baseline: `8a2af4ebe`. Release fix: `7470ccfd3`.
R02C16 was already fixed at the continuation baseline and passed again.

All candidate CLI invocations used private `HOME` and `SPARKWING_HOME`
under `/tmp/sw-codex-grid-release`. Go used `GOWORK=off`,
`GOFLAGS=-buildvcs=false`, `GOCACHE=/tmp/sw-gocache`,
`GOPATH=/home/bombuntu/.go`, `GOMODCACHE=/home/bombuntu/.go/pkg/mod`,
`ASDF_DATA_DIR=/home/bombuntu/.asdf`, and Go 1.26.9 directly on PATH.
The temporary directory was removed after verification. No real store,
cluster, database service, publication, push or release was used.

## R02C16: already fixed at continuation baseline

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

## R01C15: fixed before intake

Rebuilt the CLI from this worktree with Go 1.26.9, without a release stamp.
On continuation baseline `8a2af4ebe`, ran:

```text
HOME=/tmp/sw-codex-grid-release/home
SPARKWING_HOME=/tmp/sw-codex-grid-release/home/.sparkwing
SPARKWING_TOOLCHAIN=local
sparkwing -C /var/tmp/sw-fix-release run release --sw-dry-run --sw-allow-unadmitted
```

The initially absent store acquired one run, `run-219f81f79a20578c5116735d`.
The transcript included `job_started` and `job_failed` for
`require-isolated-home`, the operational-store refusal, and `run_finish`
with status `failed`. The standalone controller stopped when the command
returned. No release job beyond the home guard executed.

The regression `TestReleaseRefusesOperationalHomeDuringPlanning` failed on
that source: both an unset home and an explicit `$HOME/.sparkwing` produced
`release planning = <nil>`. The fix captures validation in the registered
factory and returns its error from Plan. The late guard job is deleted.
No CLI feature, wrapper, configuration option or limit was added.

After the fix, the same CLI command failed during discovery, before intake.
The populated store retained one run and its SHA-256 stayed identical:

```text
before: 47ec0dace3c04c0c4f1203d3371d7f1b083a396cc838c48680be663133b1b8a6
after:  47ec0dace3c04c0c4f1203d3371d7f1b083a396cc838c48680be663133b1b8a6
```

Repeated `run release --sw-dry-run` without the standalone flag under
`/tmp/sw-codex-grid-release/fresh-home`, with SPARKWING_HOME set to that
home's `.sparkwing`: the command refused and created no `state.db`.

The existing secret policy withholds discovery failure details:

```text
{"error":{"code":"plan_failed","message":"describe failed: plan_failed (details withheld: secrets not yet known)"}}
```

This also withholds discovery logs for other pipelines while this release
error exists, but does not block their plans. Checked with the built CLI's
`pipeline plan --pipeline example`, which returned the plan successfully.
The regression also verifies Build still plans in the operational home and
Release plans with an isolated home. DELIVERY.md explains the isolated-home
remedy for the generic refusal. Secret policy was not changed.

Focused checks passed:

```text
go -C .sparkwing test ./jobs -run '^TestRelease' -count=1
go -C .sparkwing test -tags qatrust ./jobs -run '^TestReleaseRefusesOperationalHomeDuringPlanning$' -count=1
go -C .sparkwing vet ./jobs
sparkwing pipeline lint --pipeline release
go test ./pkg/docs ./internal/doccheck -count=1
go run ./internal/commentcheck -base HEAD .
bash bin/sync-docs.sh --check
```

The check commands used the absolute checkout paths and the environment
above. The comment check's HEAD was the continuation baseline. Hook status
reported current pre-commit and pre-push hooks, but a stale
prepare-commit-msg forwarder. No hook or git configuration was changed.

## R04C15: fixture fix verified, external commit pending

In both sessions, copied `/var/tmp/sw-mig/sparkwing-ci-smoke/.sparkwing` into the temporary
directory and replaced only its SDK dependency with this worktree for
verification. The built CLI listed the fixture pipelines successfully.
`go test -p 2 -count=1 ./...` failed with:

```text
jobs/regex_sweep_test.go:59:12: undefined: sparkwing.SetWorkDir
jobs/regex_sweep_test.go:60:31: undefined: sparkwing.SetWorkDir
```

In that temporary copy, replacing the saved WorkDir, SetWorkDir and cleanup
calls with `t.Chdir(root)` and removing the unused SDK import made the full
author test package pass again in 1.835 seconds. Its existing regex sweep tests
exercise the fixture directory and caught the original compile failure.
The external repository remains clean and unchanged because its requested
edit path conflicts with the harness allow-list. Clarification was requested
again during this session; no answer had arrived. It is on branch
`bw-3095-v068`, HEAD `223385a`.

The complete required edit to `.sparkwing/jobs/regex_sweep_test.go` is:

```diff
-       "github.com/sparkwing-dev/sparkwing/sparkwing"

-       prev := sparkwing.WorkDir()
-       sparkwing.SetWorkDir(root)
-       t.Cleanup(func() { sparkwing.SetWorkDir(prev) })
+       t.Chdir(root)
```

Remove the empty import group when removing the SDK import, then run gofmt.
The existing author tests fail to compile before this edit and pass after it.
No replacement shim is needed.

The migration table, SDK guide, changelog and embedded copies now explain
`SetWorkDir` removal and the `t.Chdir` replacement.

## Remaining coverage

The lead owns the full gate and its test-output file. No full gate,
PostgreSQL suite, release cut, publication, or independent agent review
was run in this lane.
