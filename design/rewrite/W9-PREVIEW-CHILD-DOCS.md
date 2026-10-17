# W9 review 3: preview, child secrets and migration docs

Findings 3, 5 and 6 are implemented. This continues the uncommitted edits present
at session start. W3's source library, native assembly, intake identities and
App webhook resolution are unchanged.

The invariants are:

- Selected code determines its verified, policy-checked engine before installation
  or execution. Preview, describe and run help now use selectLocalRunSource,
  source.ForRequest, source.Acquire and SDKPlanner. Acquisition detects the SDK
  from selected code. Planning rechecks the acquired engine before each process.
  The execution checkout supplies bindings and the author working directory.
- Children resolve their own secret requirements. StartChild no longer copies
  LocalSecretNames from its parent. A real controller admission test starts and
  plans Deploy with TOKEN declared on its Build parent. The request property
  fuzzes parent secret names and repository/branch inputs.
- Current migration instructions obey the dispatched grammar. The checker expands
  abbreviated invocations in prose, wrapped code spans, shell examples and tables.
  Explicit Before sections remain historical. A regression proves that an archive
  does not exempt the current section after it. v0.67.0 now has current guidance
  above an explicit archive of its pre-rewrite notes. Remaining current examples
  in _unreleased are corrected. Both embedded mirrors are regenerated.

Removed pipelineRun, newPipelineRun, its Go/TypeScript materializers, its schema
resolver, setupRefWorktree, the eager describe-cache writer and the unused
pipelineBinary wrapper. The misleading parity comment is gone. compileOptions
is removed because only the sparks no-update boolean remained. Run help uses
the same acquisition lifetime as preview and describe. Existing cancellation,
toolchain, alias, masking and cache tests use the surviving contracts. Two tests
specific to the retired eager-cache/source-cache path are removed. No archived
acceptance test was newly ported and no acceptance skip was changed.

Production Go versus HEAD is +192/-360, net -168 across all touched packages.
W9-owned production is +140/-359, net -219. W9 versus main is
+19,812/-25,468, net -5,656, within the -2,900 budget. Counts exclude tests,
testdata, acceptance and contract declarations, using the lead's area filters.

Verification used GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
and TMPDIR/GOTMPDIR=/tmp/sw-astra-W9, with go test -p 4 at most.

- Whole-repository go build ./... passes.
- Vet passes for cmd/sparkwing, controller/client, controller, doccheck and docs.
- Full doccheck and controller/client tests pass. clisurface and controllerwire
  short suites pass. Embedded migration/catalog/read selections pass.
- Focused CLI regressions pass, including selected Go code under an invoking
  TypeScript checkout, policy refusal before installation, public describe and
  preview of configured source, execution/source binding, secret masking,
  alias arguments, toolchain overrides and cancellation. The selected-source
  integration rerun passes in 29.954s. The broader focused CLI selection passed
  in 35.157s before the common inspection-lifetime extraction.
- TestNativeChildBuildToDeployPlansItsOwnSDKEntrypoint passes through real HTTP
  routing and SQLite admission into SDK planning. Child-secret fuzzing passes
  1,677 executions with four workers. Reintroducing parent-name inheritance
  through a Go overlay makes the real admission regression fail with
  "local secret names and resolved values must match".
- TestSurface* and TestEngineAmendments* pass with their existing native coverage
  boundaries. Native acceptance remains the lead's responsibility under A1-53.
- rewrite_integration builds for cmd/sparkwing and controller pass. The final
  cache/dispatch/describe/plan selection passes in 2.051s.
- Commentcheck against main is clean across 2,865 files. Whitespace and embedded
  mirror checks pass.

The broad short CLI suite stops at TestResolveSparks_WithManifest_WritesOverlay,
and the docs suite stops at TestWebClient_Versions_HappyPath, because the sandbox
refuses their httptest listeners. These are not native passes. The supplied
W9-burst-native-latest.md has no FAIL and is dated 2026-10-17 10:58 MDT. It
covers the prior committed state. The lead must commit and run the repository
and socket-dependent native suites for these edits before deciding completion.

No contract changes are needed. Lead-owned amendments and handoffs are unchanged.
No removed API had callers outside cmd/sparkwing. Shared files touched are
internal/doccheck/cliverbs.go, internal/doccheck/cliverbs_test.go,
pkg/controller/w9_mechanisms_test.go and the two generated pkg/docs migration
mirrors. W3's remaining review findings and native platform/release evidence
remain their owners' integrations. No new product setting, limit, wire field,
credential rule or runtime environment variable was added.

No Git writes or operational Sparkwing commands ran. Repository-local Git
configuration has no test user identity or core.worktree override. Scratch is
removed after checks join. The shared build cache is retained.

STATUS: CONTINUE
