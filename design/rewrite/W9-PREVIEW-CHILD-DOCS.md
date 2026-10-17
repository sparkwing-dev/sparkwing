# W9 review 3: preview, child secrets and migration docs

Findings 3, 5 and 6 were implemented in the preceding committed session.
Preview, describe and run help use shared selection, source.Acquire and SDKPlanner.
The old pipelineRun materializer and misleading parity comment are deleted.
Children no longer inherit LocalSecretNames. Migration instructions are corrected
or explicitly archived, and the checker normalizes unprefixed instructions.

The invariants remain:

- Selected code determines its verified, policy-checked engine before installation
  or execution.
- Children resolve their own secret requirements.
- Current migration instructions match dispatched commands and behavior.

This continuation fixes all four tests named in the supplied native failure report:

- TestScaffoldAliasThroughCLI and TestRegisteredNameThroughCLI now create real Git
  repositories using the existing fixture helper. They still invoke the public
  scaffold, describe, help and plan commands.
- TestRepositoryPlanAdapterUsesCurrentSelector initializes its fixture repository
  before calling the real command adapter.
- TestRunBuildPolicyReachesRealPreparationAndRetainedSource explicitly seeds the
  catalog cache from the first real preparation. It no longer depends on the
  deleted materializer's eager cache write. The later cases check that help and
  preparation use the selected build policy while preserving the catalog entry.
  Retry acquisition still compiles retained source and checks debug information.

The original failures reproduced locally before these changes. All four pass
as a group in 31.290s. After moving the cache check after preparation, the final
build-policy test passes in 11.991s.

Verification used GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
and TMPDIR/GOTMPDIR=/tmp/sw-astra-W9, with Go package parallelism at most four.

- Whole-repository go build ./... passes. Vet for cmd/sparkwing passes.
- Full doccheck, controller/client, clisurface and controllerwire tests pass.
- Inspection acquisition, generated policy refusal, local source selections,
  replacement-pipeline describe and real child admission/planning pass in
  cmd/sparkwing and controller (21.794s and 0.243s).
- TestSurface* and TestEngineAmendments* pass in tests/acceptance (0.085s), with
  their existing native coverage boundaries. No acceptance skip changed and no
  archived test was newly ported.
- Commentcheck against main is clean across 2,865 files. Embedded docs mirror
  and whitespace checks pass.

The lead's W9-burst-native-latest.md dated 2026-10-17 11:53 MDT still contains
FAIL for the preceding committed state. Local passes do not replace that report.
The lead must commit these fixture corrections and rerun the repository/native
suites before deciding completion.

This session deletes zero production lines and changes zero production lines.
W9 production versus main is +19,814/-25,468, net -5,654, within the -2,900
budget, using the lead's filters. This session changes only three CLI test files
and this report. No removed API has remaining callers to migrate in this session.

No contract changes, new mechanisms or cross-area edits were needed. Lead-owned
amendments and handoffs are unchanged. W3 still owns source/intake/native assembly
and webhook findings 1, 2 and 4. Native socket and platform/release proofs remain
with their owners. No new integration stub blocks these three assigned findings.

No Git writes or operational Sparkwing commands ran. Repository-local Git
configuration has no test user identity or core.worktree override. Scratch is
removed after checks join. The shared build cache is retained.

STATUS: CONTINUE
