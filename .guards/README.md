# Architecture guard allow-lists

Each file here lists the violations of one guard that predate the guard. A
list only shrinks. `go run ./internal/guardcheck .` fails when:

- a finding is missing from its list (`NEW`): fix the code, do not add a line;
- a line no longer matches a finding (`GONE`): delete the line;
- a list holds a line its version at the merge base with `origin/main` lacks
  (`GREW`). Set `SPARKWING_GUARD_BASE` to judge against another ref.

`-fast` judges only the depguard lists, which is what `pre-push` runs. `gate`
runs everything. Neither uses the `origin/main` baseline that `.golangci.yml`
applies, so both judge the whole tree.

| File | Guard | Line format |
|---|---|---|
| `depguard-<rule>.allow` | the depguard rule of that name in `.golangci-guards.yml` | `<importing package dir><TAB><imported package>` |
| `forbidigo-f1.allow` | F1: `(*store.Store).ForTeam`, `AsOperator` or `SetTestClock` outside `cmd/`, `internal/wingd/localcontroller`, `internal/controller/composition.go`, `internal/web/browserfixture/main.go` and the test-only `internal/store/teststore` and `internal/store/internal/storetest`, and `TestOnly*` outside `_test.go` | `<file><TAB><method>`, one line per call |
| `forbidigo-f2.allow` | F2: `(*license.License).Allows` outside `internal/deployment`, so the deployment kind is decided once at startup; it has no entries and should keep none | `<file><TAB><method>`, one line per call |
| `unexport.allow` | exported identifiers in an `internal/` package that no other package uses, that satisfy no interface and that no used type exposes | `<package><TAB><count>` |
| `acceptance-skips.allow` | required acceptance cells that skip rather than execute; `tests/acceptance` enforces it on linux | `<scenario>/<target>` |

`guardcheck` prints the identifiers behind a rising unexport count. The
acceptance list is judged by the `tests/acceptance` package itself: a skipped
cell missing from the list fails that cell, and an unfiltered run fails when a
listed cell did not skip.

Moving the controller, store and storage trees under `internal/` adds their
existing exports to the unexport census. The move accepts those package counts
once and translates the existing depguard and F1 paths. It does not add a
standing growth exemption. Subsequent changes must keep shrinking these lists.

The typed spawn guard has no allow-list. It loads every root-module package
for Linux, macOS and Windows on amd64 and arm64 and rejects raw process-constructor references
(including aliases and function values), `exec.Cmd` value construction and
`Cmd.Env` access outside `internal/procenv`. Test files are exempt. `procenv`
`Command` constructors require an explicit environment and copy it; nil selects
an empty environment. Pipeline callers supply their existing `Build` or delegated
boundary environment. Trusted CLI, tooling and SDK control processes explicitly
use `HostCommand` to preserve host inheritance, or supply an explicit host
snapshot where they already selected one. The guard enforces the creation and environment
ownership seam; it does not infer the policy provenance of arbitrary slices.
