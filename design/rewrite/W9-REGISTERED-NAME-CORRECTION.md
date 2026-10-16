# W9 registered pipeline names

This supersedes the registration interpretation in
`W9-ALIAS-CLI-CONTINUATION.md`. The authoring contract matches main:
the YAML `name` matches SDK registration, and `entrypoint` names the Go type.

Restored all 26 registration renames and all five scaffold templates. Restored
the repository tests that plan each declaration by its registered name.

CLI schema lookup, plan requests, catalog metadata and completion now use the
registered name. Trigger inspection preserves that name instead of replacing
it with the type. Shared-host describe, initial planning and execution replan
also use the registered name. Source metadata and plan-cache inputs retain the
Go type identity. Schedules already preserve the registered pipeline name and
reach the corrected shared host.

`TestRegisteredNameThroughCLI` builds the CLI and a real SDK program with
`Register("example")`, YAML `name: example` and `entrypoint: Example`. It exercises
named describe, selected run help and plan inspection. Restoring the old lookup
makes it fail with `unknown pipeline "Example"`; the corrected lookup passes.
The native-run integration probe now enters through the top-level dispatcher
with `run example`. It compiles, but socket-dependent execution remains for
the lead outside the sandbox.

Updated identity fixtures, negative registration cases, trigger preparation,
live-schema properties and the secret/risk metadata fuzz test. Registrations
sharing a Go type each have their own registered name. No acceptance cell was
unskipped and no archived T1 test was ported.

Corrected pipelines, CLI, protocol and migration documentation and their embedded
mirrors. The authoring guide's registration sentence is unchanged from main.
The C3 narrative now describes the same contract. No wire type or foreign
contract signature changed. No open design question or new mechanism remains.

Verification used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache` and `GOTMPDIR=/tmp/sw-astra-W9/gotmp`.

- Root and `.sparkwing`: `go build ./...` and `go vet ./...` pass.
- The full `internal/nodehost` and `internal/clisurface` suites pass.
- Focused CLI registration, scaffold, describe, help, plan, native preparation,
  local-run, schedule and trigger tests pass.
- `TestSurfacePipelineIdentityContract` and `TestSurfacePlanArgumentsContract`
  pass, as do repository registration and Kubernetes declaration tests.
- Secret/risk metadata fuzzing passes 2,037 executions with four workers.
- Embedded-doc drift, changelog mirror, privacy, commentcheck against main and
  `git diff --check` pass. `rewrite_integration` compiles.

Broader optional checks exposed unrelated failures: legacy `internal/crons`
fixtures use schedule mappings rejected by the sequence-only config, inherit
unavailable GPG signing, or supply obsolete repository identities. The CLI's
cached-toolchain fixture also fails at dependency materialization. These files
were not changed. The new SDK fixture runs `go mod tidy` before materialization;
an isolated writable module cache with the host's cached module proxy permits
the real CLI regression without network access.

Shared files touched: 21 registration source files and two registration tests
under `.sparkwing/jobs`; W3's `sdk_description.go`, `sdk_planning.go`,
`sdk_code.go`, `pipeline_identity.go`, two host tests and the pipelinealias fixture.
The remaining edits belong to W9. Lead-owned handoff and amendment files were
not edited. No repository Git metadata was written.

Production changes are +40/-46, net -6 lines. No whole legacy file was deleted.
W9-owned production versus main is +16,894/-23,811, net -6,917 lines.
Task scratch is removed after checks join. The shared Go cache is retained.
