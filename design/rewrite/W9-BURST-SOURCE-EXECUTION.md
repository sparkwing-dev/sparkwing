# W9 burst source execution continuation

Starting HEAD: `cfdeab4ac`. Earlier burst work is retained. No Git writes,
installation, deployment or release ran. The required read-only Sparkwing info ran.

## Implemented

- The real CLI preparation now has a composed acceptance test. It captures
  tracked edits and a declared untracked file, compiles and describes the SDK
  with its selected engine, uploads through authorized source storage, admits
  the Cloud-owned run, claims its here owner, downloads the bundle and compiles
  and describes that SDK again. The edited SDK and both file contents survive.
  Undeclared `.env` is absent and persisted run data contains no secret value.
- That path exposed two production here-host gaps. Remote source options had
  no ambient environment, so installation lost PATH and could not find Go.
  The source workspace also did not exist on a fresh home. The CLI now prepares
  the private directory and supplies the host's environment through procenv.
  Only the existing minimal base plus GOCACHE and GOMODCACHE pass remote policy.
- SDK installation resolves the pipeline's imported packages before computing
  its binary-cache key. The old whole-graph archive download blocked builds on
  unused dependencies. A real offline installer regression failed before the
  change and now executes successfully. Importing that unavailable module
  still fails without an install receipt. Effective sparks overlays remain.
- A separate real-Go regression covers covering and unrelated go.work files.
  Preparation follows the compiler's workspace selection. The two cases failed
  with the first dependency-selection change and now execute successfully.
- The selected-engine test entrypoint handles pipeline describe through the CLI
  instead of reentering the Go test suite. Its subprocess help regression passes.
- Added a native public CLI probe for configured routing, the default uncapped
  allowance, setting a zero cap, here execution, edited source, requester-secret
  masking in terminal logs, the admitted handle, Cloud success and absence of a
  duplicate local run record. It uses production composition and real
  development-engine policy. It compiles but cannot run here because loopback
  listeners are refused. The test reports that as a skip, not execution proof.

The environment invariant is that captured requester values, arbitrary user
variables and credential-shaped names never enter the remote source/process
environment. A fuzz test generates adversarial values across Install, Git,
Plan, Attempt and Eval while preserving host PATH and the selected cache paths.

## Contract changes made

- C3 engine: dependency preparation uses go list over imported packages, with
  compilation's workspace and effective-modfile selection before cache hashing.
- C4: fresh private source workspace creation and the filtered host ambient
  environment for Cloud-owned here execution. Local snapshot policy is retained.
- C3 controller wire: the real source-installation acceptance and native public
  CLI probe, with their execution boundaries stated explicitly.

Run docs, changelog and embedded mirrors describe the source-build behavior.
No schema, wire field, grant, environment variable, limit or default cap changed.
No foreign contract change is requested. Lead-owned amendments and handoffs are
untouched. The earlier committed retry-selection DESIGN remains open.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package workers.

Passed:

- Whole-repository build and vet on cmd/sparkwing, internal/bincache and
  internal/engine/sdkinstall.
- Complete bincache and sdkinstall suites, including executable ownership,
  checksums, overlays, unavailable imported dependencies and real workspaces.
- CLI burst, source, here attachment/resume, local compile and selected-engine
  helper regressions. The new real source path and environment seeds also pass
  under the race detector.
- Environment fuzz campaign: 592 executions in three seconds.
- Complete short controllerwire, controller/client, clisurface and doccheck
  suites. The doccheck executable and embedded mirror drift check pass.
- Surface and credential-policy acceptance selections. No native matrix cell
  was newly unskipped. No archived test was newly ported.
- Commentcheck against HEAD across eight Go files and diff whitespace.

Commentcheck against main still fails on the unchanged pinnedClock comment at
internal/store/store.go:108. The native public CLI test compiles and skips on the
sandbox's explicit listener refusal. No whole-repository test pass is claimed.

## Remaining native integration

The lead should run TestBurstPublicRunBuildsEditedSourceAndStreamsMaskedSecrets
in cmd/sparkwing with tags rewrite_integration, plus the preceding
TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards in internal/controller.
Use an isolated home and the normal shared module/build caches.

Full launch proof still needs registered personal-runner and Cloud Job execution,
Ctrl-C, artifact retrieval and competing local admission through the public CLI.
The new source test uses a local SDK replacement under decision 111. It does
not establish signed release download or cross-machine portability of that
replacement. Existing component proofs for all five burst features remain.

## Size, shared files and friction

Production Go this session: +19/-6, net +13. W9 production versus main:
+18,349/-23,824, net -5,475, within the -2,900 budget. No legacy subsystem or
API was replaced this session. Earlier cuts stand. No caller migration is needed.

Shared implementation files are internal/bincache/materialize.go,
internal/bincache/materialize_test.go and
internal/engine/sdkinstall/materialize_test.go. Shared documentation files are
the three contract documents above, CHANGELOG.md, pkg/docs/changelog.md and
pkg/docs/mirror/local-execution.md. Other changed files are in W9's CLI/docs area.

Mechanisms added: none in production. The changes use the existing private
directory helper, procenv policy, Go dependency resolver and workspace helpers.
The test controller/source fixtures are shared by the two new CLI tests.

Friction: sandbox sockets, read-only module-cache archives that the old broad
download tried to materialize, the inherited main commentcheck finding, and
Overwing's reported source lag. Fixture setup errors were corrected before the
recorded passes. All checks are joined before cleanup. Worker scratch is removed
at the end and the shared Go cache is retained. Local Git identity/core.worktree
overrides are absent.

STATUS: CONTINUE
