# W9 repository pipeline discovery

`windows-verify` now declares `Requires("os=windows")` on its job. Planning
does not inspect the host OS. The placement matcher owns the Windows requirement.
The other `.sparkwing` platform branches run during execution: MinIO fixture
building, template executable selection and Kubernetes script execution. No other
platform refusal during planning was found.

The Windows planning regression fails on Linux with the former refusal restored
and passes with the placement declaration. Its plan retains the four-core pin.

The escape probe's no-network planning test now uses `sparkwingtest` for both an
empty run ID and a real run ID. It fails on planning errors and retains the dial
positive control. Removed registry enumeration, `Registration`, `Invoke`,
`SkipArgResolve` and panic swallowing are gone from that test.

Compiling the pipeline tests exposed additional removed SDK inspection, runtime
and scheduler exports. Those tests now inspect fake-host plan documents, execute
individual steps through the host, inject outputs through host options and use
`t.Chdir` for work directories. Scheduler-owned lifecycle log assertions were
replaced with declaration and per-step failure assertions. The shared test helper
contains consumer JSON projections because the pipeline module cannot import the
SDK's internal protocol types. No SDK API or compatibility shim was added.

`AGENTS.md` now teaches `commands --search`, `docs search --search`, positive
limits, cursors and command help. These flags and paths were checked against
`internal/clisurface/registry.go`. `CLAUDE.md` only imports `AGENTS.md`.

All Go checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache` and `GOTMPDIR=/tmp/sw-astra-W9/gotmp`.

- Root and `.sparkwing`: `go build ./...` and `go vet ./...` passed.
- `.sparkwing/jobs`: focused declaration, budget, escape probe, release, SDK
  output and directory tests passed. The Windows planning/order, escape planning,
  budget failure and pre-release failure tests also passed with `-race -p 4`.
- `internal/clisurface` and `cmd/sparkwing`: focused registry, command index,
  pagination and discovery tests passed.
- `go run ./internal/commentcheck -base main .` and `git diff --check` passed.
- The full pipeline short suite could not complete: an unchanged Git fixture
  test failed for an outside-checkout path, then an unchanged HTTP fixture
  panicked because this sandbox forbids listening sockets.
- The whole-tree home-resolution check failed on the unchanged
  `internal/settings/resolvers.go`. Its documentation-mirror check passed.

No acceptance cell was changed or claimed. No archived T1 test was ported. The
lead still owns the native local-run probe and native Windows execution. No
foreign contract change or decision is needed for this scope.

Shared files touched are `AGENTS.md` and `.sparkwing/jobs` production/tests.
Before this report, the patch removed 534 tracked lines, added 320 tracked lines
and added a 147-line test helper: net -67. Production changed by +1/-8, net -7.
W9-owned production versus main remains +16,886/-23,807, net -6,921. No whole
legacy file was deleted. No repository Git metadata was written. Task scratch
is removed after checks join; the shared Go cache is retained.
