# W9 local offline policy and describe isolation

Base HEAD: `f3fea2bec`. Decision 113 is implemented. Earlier W9 work was
retained. Lead-owned handoffs and amendments were read and left unchanged.

- Local engine selection, source acquisition and local hosting use host-owned
  `engine.Config.LocalOnly`. They read verified cached policy without refreshing
  it over the network. With no verified policy, execution continues with
  `version_policy_unavailable`. Cached restrictions, executable signatures and
  exact engine identities remain enforced. A failed cache read appears in the
  warning.
- The local controller selects that policy only for a persisted local subject.
  Machine and service claims retain the separate strict runner policy.
  `EvaluateRunner` refuses an unavailable policy even when handed a local
  resolver's result. Request version strings and labels cannot select the local
  exemption. The release-build production composition test completes intake,
  owner claim, node work, stored logs, finish and status reads with zero policy
  requests. Status retains the warning.
- Both SDKs return a canonical `PipelineDescription.Error` for a failed
  registration and continue describing the others. Failed entries contain an
  empty args array and no partial secret declarations or metadata. The host and
  CLI refuse the selected failed entry with its sanitized cause before planning.
  Healthy entries retain their own complete declarations. Any failed entry
  keeps describe-process output behind the existing secret gate.
- The local HTTP test exposed a preexisting disconnected compatibility route:
  `/capabilities?engine_version=...` returned browser features instead of the
  client's declared compatibility result. That query now reaches the existing
  engine compatibility operation. The ordinary browser query keeps its response.
  Local compatibility works without policy metadata.
- Source docs, the generated describe schema, its conformance snapshot and the
  embedded documentation mirror match the new behavior. No protocol version,
  credential format, user flag, environment override or compatibility shim was
  added.

Invariants under generated inputs: a pipeline gets no planning function without
its own complete secret declarations; describe output with unknown declarations
never reaches the log sink; request input cannot grant a machine claimant the
local policy exemption. SDK tests also retain masking across error fields and
encoded secret defaults.

Tests came first. The existing Go discovery regression failed because one
pipeline aborted discovery. The TypeScript describe regressions failed for the
same reason. The host property failed because partial describe output escaped.
The release-build controller regression failed on policy HTTP 403 during owner
claim. The CLI selection regression failed because a failed entry was accepted.
Each now passes. The compatibility HTTP assertion also failed before its route
was connected.

Verification used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four Go package workers.

- Whole repository `go build ./...` passes.
- Vet passes for cmd/sparkwing, engine, nodehost, nodeproto and conformance,
  sdkserve, sparkwing, controller, controller/client, source and doccheck.
- Full engine, nodehost, nodeproto/conformance, sdkserve, source,
  controller/client and doccheck package tests pass. Controller claim, runner
  policy and version-policy tests pass. CLI production local-controller,
  local-queue, engine-selection and describe-selection tests pass.
- Acceptance passes: `TestLocalVersionPolicyDoesNotNeedNetwork`,
  `TestProtocolSchemaContract` including `pipeline_describe_failure`, and all
  `TestEngineW8*` component tests. No existing skipped native cell was relabeled.
- Go SDK tests pass with the unrelated
  `TestSchedulingPrefersDocumentationStatesPlacementHold` excluded. The original
  full run failed only that test: unchanged scheduling.md lacks its expected
  `- **Prefers**` definition and holds-the-node-back wording. Neither scheduling
  code nor that page/test was changed here. The lead must reconcile that
  existing contract disagreement.
- TypeScript typecheck and all 20 test files pass. Existing Go/TypeScript
  diagnostic properties were migrated to inspect the per-pipeline inner error.
  No additional legacy test port was needed for this continuation.
- Three-second fuzz runs pass: complete Go SDK pipeline declarations (30,597
  executions), host isolation and withheld logs (5,590), and machine claim policy
  selection (6 real SQLite/controller executions).
- Commentcheck against main is clean across 2,573 files. Protocol schema drift,
  focused source-doc checks, documentation mirror check and git diff --check pass.

An expanded CLI docs selection could not complete because
TestDocsMigrationsValidatesWebBoundsBeforeFetching uses httptest.NewServer and
this sandbox refuses sockets. Native CLI/service/platform checks and full
pipelines remain lead work, as the swarm brief requires. No new foreign
implementation stub blocks decision 113. Earlier C1/C2 owner/eval metadata and
W8 Cloud-boundary handoffs remain in W9-REPLACEMENT-PIN-CONTINUATION.md and were
not reopened or represented as verified by this change.

No legacy files needed deletion in this continuation. Production Go changes
excluding contract declarations are +117/-26, net +91, across W9 and the
explicitly authorized shared integration edits. W9-owned production versus main
is +16,945/-23,817, net -6,872, below its -2,900 budget. No removed API needed a
caller migration.

Shared production files touched: internal/engine/{engine,index,policy}.go,
internal/nodehost/{sdk_description,sdk_planning}.go,
internal/nodeproto/describe_contract.go,
sparkwing/protocol_describe.go, sdk/typescript/src/describe.ts,
internal/controller/{engine,engine_claim,engine_version,identity}.go.
Shared tests cover those packages plus tests/acceptance. Shared generated files
are internal/nodeproto/conformance/testdata/v2-documents.schema.json and the
pkg/docs copies. Contract documentation changes are confined to
c3-engine-version.md and c3-protocol-sdk.md. New cross-area contract requests:
none. No changes to the wire client API were needed.

Friction: the socket restriction, the existing scheduling-doc disagreement and
Overwing's installed source lag. Git has no local user identity or core.worktree
override. No git writes, installation, deployed changes, pipeline or release ran.
Scratch is removed after checks join. The shared Go cache is retained.

STATUS: COMPLETE
