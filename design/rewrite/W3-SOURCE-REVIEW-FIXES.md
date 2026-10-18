# W3 source-selection review fixes

All seven findings are implemented. The changed tree still needs the lead's
native suite and commit. The latest W3-native-latest.md has no FAIL, but its
session-3 result covers the previous committed state.

## Implementation

1. Configured local secrets reach pipeline processes only through authorized
   release. Help, describe, plan, preparation and burst share a prepared captured
   environment before selection, acquisition or SDK installation. Run dispatch
   retains that environment through preparation. Held values and their masker
   stay together. Acquire binds dependency tools to the installation snapshot. An
   explicit Go toolchain environment replaces a differing inherited session,
   retaining its notice sink. Sparks Go-setting probes and fallback resolution
   consume that captured session too. Each entry point has a SIGNING_MATERIAL regression through its
   real SDK path, including Sparks preparation. Only the external module download
   is faked in that fixture. SDK compilation and protocol execution remain real.
2. Replay preserves admission's private-input validation. ReplayRun and AdmitRun
   share name/value validation, registry equality/availability and submitted-env
   verification. The replay branch only reads the registry. HTTP tests cover
   changed and missing values, changed submitted bytes and registry loss on a
   second Store process. A generated-input property covers empty, binary and
   escaped secret values. Repeated unavailable requests never restore values.
3. Withholding follows the platform's environment-name identity. procenv.NameKey
   owns the comparison used by procenv and withholding. Platform-parameterized
   property coverage includes lowercase signing_material against configured
   SIGNING_MATERIAL on Windows, Unix case sensitivity and declaration-name
   preservation for release.
4. Source refs keep the same namespace across placements. Shared normalization
   resolves origin/main and refs/remotes/origin/main as the origin branch locally
   and refs/heads/main remotely. App intake and remote-schedule fixtures now use
   origin/main. Native Git tests cover both lookup locations.
5. Accepted depth governs checkout after bundle seeding. One checkout finalizer
   applies the shared depth rule, checks out the selected commit and materializes
   submodules/LFS. Its local file-protocol fetch uses seeded objects, so unpushed
   execution and code commits require no remote access. Bundle tests check both
   checkouts at depth 1, 2 and 0 and preserve the owner's history.
6. Only a replay miss proceeds to source resolution. Controller replay preserves
   errors, including 409 conflicts after removal of the source repository. The
   false fall-through comment is deleted.
7. Explicit source hints are checked even when their roots are unknown or retired.
   Tests cover retired/unknown roots, quoted and unquoted instructions, and an
   arrow in ordinary prose. Recovery strings use runners, schedules and operator
   object-store. The obsolete dashboard cloud-connect claim is removed from its
   comment and controller help.

The merged CLI no longer calls the old orchestrator source resolver. Deleted
pipeline_source.go and ref_resolve.go (86 production lines), plus
ref_resolve_test.go (84 test lines). Remote-only ref and option-injection
coverage moved to internal/source. No production caller needed migration.

## Verification

Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR=/tmp/sw-astra-W3/gotmp. Tests used -p 4.
All paths below are relative to this worktree. Reproduce commands with go -C
followed by the absolute worktree path.

Passed:

- go build ./... after the legacy API deletion.
- go vet for cmd/sparkwing, cmd/sparkwing-controller, internal/source,
  internal/procenv, internal/gotoolchain, internal/doccheck, internal/agentconfig,
  internal/objectguard, internal/crons, internal/orchestrator, pkg/controller
  and pkg/store.
- Full internal/source, internal/procenv, internal/doccheck,
  internal/agentconfig, internal/gotoolchain and internal/crons tests.
- CLI tests TestLocalEntryPointsWithholdSigningMaterial,
  TestBurstPreparationWithholdsConfiguredLocalSecretFromInstallAndPlan,
  TestInspectionAcquiresSelectedCodeAndEngine,
  TestLocalRunSelectionsReachPreparationSealingAndReplan and the withholding
  property seeds. These ran together in 52.558s. The expanded dependency-
  preparation entry-point tests passed in 12.270s.
- Sparks toolchain-floor and resolver tests (0.495s).
- Platform-name fuzzing passed 2,136 cases with two workers in 3.071s.
- Store tests matching TestLocalSecrets.* and TestSubmittedEnvironment.*.
- Controller tests TestHTTPIntakeReplayValidatesPrivateInputs,
  FuzzHTTPIntakeReplayValidatesPrivateInputs, the fresh-child replay/source-outage
  test, requester-secret intake/claim/release and the origin/main remote schedule.
- Acceptance TestEngineIntakeHTTP, TestHereResumeClaimContract,
  TestSurfaceInitialTriggerIntakeTransport, TestSurfacePipelineIdentityContract
  and TestSurfacePlanArgumentsContract. No acceptance skip changed.
- Objectguard ceiling and recovery-remedy tests.
- Retained run/trigger SDK build-policy tests (39.000s).
- TestExplicitSessionEnvironmentReplacesPreviousSnapshot first reproduced an
  inherited session restoring SIGNING_MATERIAL, then passed with the full
  gotoolchain suite.
- go run ./internal/commentcheck -base main . and git diff --check.

The new regressions ran red before fixes: inspection leaked the configured
secret, HTTP replay accepted changed values, child conflicts depended on Git,
retired roots were skipped and bundles retained excess history.

The App fixture, Sparks proxy tests and objectguard middleware listener tests
cannot run in
this sandbox. The lead's native suite must execute the App test and whole suite
against the changed tree. Whole-repository test runs remain with the harness.

## Contract changes made

C1 records shared private-input replay validation and replay-error precedence.
C4 records mandatory prepared-environment consumption and procenv.NameKey.
The context argument on sparks.NewResolverFromEnv replaces its ambient-only
constructor. Production callers in sparks.Resolve, sparks.ResolveAndWrite and
the spark-list command, plus the toolchain test, are migrated. The captured
snapshot accessor on gotoolchain serves those probes.
The W3 source-selection continuation records origin-qualified ref normalization
and common post-seed depth enforcement. No schema, wire field, route, command,
setting or environment variable was added. No contract change is needed in
another area. Lead-owned amendments and handoffs are untouched.

Mechanisms added: one private prepared-environment value replaces independent
captures; one checkout finalizer replaces separate checkout/materialization
branches. The file-protocol fetch is confined to an acquisition-owned checkout
because applying depth through the remote would lose unpushed commits. No
alternate source resolver or compatibility layer remains.

## Shared files and limits

Shared production edits outside W3 are cmd/sparkwing/{action_plan,burst_native,
local_run,main,run_dispatch,local_run_environment}.go,
cmd/sparkwing-controller/main.go, internal/procenv/build.go,
internal/gotoolchain/toolchain.go, internal/sparks/{resolve,sparks}.go,
cmd/sparkwing/spark.go,
internal/agentconfig/agentconfig.go, internal/crons/{crons,health,pushed,tick}.go,
internal/doccheck/sourcehints.go, internal/objectguard/{ceiling,objectguard}.go,
pkg/controller/{engine_intake,engine_routes,services}.go and
pkg/store/{intake_runtime,local_secrets,run_spec}.go. Related tests and the named
contract/inventory documents changed too. Store edits affect intake replay only;
no lock-order machinery changed.

Session production delta across all touched areas: +32 lines (225 added,
193 removed). W3's session delta is -49. W3's production delta against main is
-13,498, below its -2,300 budget. Counts exclude tests, testdata, acceptance and
contract declaration files.

The earlier SQLite follower-cancellation DESIGN and the cross-language SDK Git
helper ownership question remain lead handoffs. This slice has no stub-backed
integration left. The native run, including App and Sparks proxy fixtures, is the remaining
verification point.

All checks joined before cleanup. W3 scratch is removed and the shared build
cache is retained.

Friction: socket and DNS restrictions prevent some native fixtures. Overwing's
installed binary reports ten commits of source drift, but card matching and
reading work. No Git write, operational Sparkwing execution or installation ran.
The lead owns commit, merge and native confirmation.

STATUS: CONTINUE
