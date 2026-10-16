# W9 pipeline aliases through the real CLI

Invariant: the invoked project name and registered SDK entrypoint remain distinct
through discovery, parsing and execution. The CLI resolves the configured pair
through `clisurface.ResolvePipeline` and carries it into SDK selection and intake.

The native preparation regression reproduced the reported error before the fix:
`describe failed: unknown_pipeline (details withheld: secrets not yet known)`.
In this worktree, preparation already passed `Example`, as configured. The
repository registered `example`. All 26 YAML declarations had the same mismatch.
Their registrations now match their configured entrypoints. The existing
registration test now checks entrypoints instead of project aliases. No fallback
to a different registration was added.

All five built-in scaffolds also registered the project name while writing the
generated type name as the YAML entrypoint. They now register the entrypoint.
The public CLI regression creates a pipeline through `pipeline new`, then calls
`pipeline describe`, `run --help` and `pipeline plan` through the built executable.
Its project name is `sample-report` and its entrypoint is `SampleReport`. It
failed before the scaffold fix with `unknown pipeline "SampleReport"`.

That regression also found that the top-level help dispatcher bypassed selected
pipeline help. Selected run help now reaches the existing alias resolver and live
SDK schema. Plain output and the JSON help record both retain the project name.
The common help writer preserves the existing output envelope. The native
production-execution probe now runs `example` instead of the absent `ci`.

Verification used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache` and `GOTMPDIR=/tmp/sw-astra-W9/gotmp`.

- Root and `.sparkwing`: `go build ./...` and `go vet ./...` passed.
- Both new regressions passed. Native preparation uses real source acquisition,
  engine selection, SDK installation and protocol pipes, without a supplied
  identity pair or substituted source/engine factories.
- Existing CLI alias, describe, discovery, dispatch, template and trigger
  inspection tests passed. The full `internal/clisurface` suite passed.
- `TestSurfacePipelineIdentityContract` and `TestSurfacePlanArgumentsContract`
  passed. No acceptance cell was unskipped and no archived T1 test was ported.
- Repository registration and Kubernetes pipeline declaration tests passed.
- Embedded-doc drift, changelog mirror and privacy checks passed.
- `rewrite_integration` compiled. Its daemon probes were not run because this
  sandbox blocks sockets. The lead still owns native execution of
  `TestReviewProductionExecutionBindings/local_run`.
- Commentcheck against main and `git diff --check` passed.

Shared files touched are the 21 pipeline registration source files under
`.sparkwing/jobs`, plus `pre_commit_test.go` and `k8s_e2e_test.go`. All other
changes are W9 CLI, documentation and its embedded mirror. No foreign contract
change or open design decision is needed. The shared resolver was already
correct, so the fix changes the producers that violated its contract and makes
the existing selected-help path reachable.

Production changes are +47/-41, net +6 lines. No whole legacy file was deleted.
W9-owned production versus main is +16,903/-23,818, net -6,915. No repository Git
metadata was written. Task scratch is removed after checks join, and the shared
Go cache is retained. Friction was the repository/scaffold registration mismatch,
the unreachable selected-help path, and the sandbox's read-only default engine
cache, which the CLI regression isolates under its temporary cache directory.
