# W9 SDK replacement continuation

Base HEAD: `04a0ddbe2`. Decision 111 is implemented. Remaining execution
integration depends on the foreign contracts and production bindings below.
Lead-owned handoffs and amendments were read and left unchanged.

## Implemented and deleted

`ReadPin` retains the Go SDK requirement and records its applicable replacement
in `Pin.Replacement`. Version-specific replacements take precedence over the
wildcard regardless of file order. Unrelated and nonmatching replacements do
not disable pinning. A replacement without an SDK requirement remains invalid.

Replacement pins select the installed executable, with its embedded version and
SHA256, and emit `engine_pin_disabled`: engine pinning is off for this run.
Selection performs no release download. Ensure and installation require that
same executable identity. Ordinary release pins retain signed policy, signature
verification and exact version matching. Both paths share executable-version
inspection; the duplicate parsing logic was removed.

The shared Go installer builds the recorded replacement and checks native
module selection against it. Undeclared overlay replacements remain refused.
SDK hello continues checking protocol and language. A replacement's reported
SDK version can differ from the replaced requirement. The existing CLI
selection and local preparation render the warning without new CLI plumbing.
Real offline Go builds execute the replacement and observe changed replacement
source on the next build.

Invariants: replacement selection cannot substitute another executable for the
accepted installed engine; disabling version pinning cannot disable protocol or
language negotiation. Generated-input tests cover both. Manifest requirements,
replacement selectors and input digests remain in the accepted identity.

The engine contract, versioning/local/API guides, migration guide, changelog,
OpenAPI and embedded copies describe the exception. No API was removed, so no
production caller migration was required. No whole production file was deleted.
Production Go this session: **+124/-14, net +110**. The deleted lines include
the unconditional replacement refusal and duplicated version parsing.
W9-owned production versus main remains **+16,886/-23,807, net -6,921**,
within the -2,900 budget. Counts exclude tests, testdata, acceptance and contract
declarations. The implementation includes 61 new lines in installed.go.

## Evidence

Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package workers.

Passed:

- Whole-repository build; focused vet of engine, installer, nodehost and CLI.
- Full short suites for engine, installer, nodehost, source, localcontroller,
  cluster, controller/client, controllerwire, clisurface, apispec, apiref and
  doccheck.
- Replacement selection, precedence, real compilation and CLI warning tests;
  replacement tests and generated-input seeds under race.
- Engine-identity fuzzing: 64 final executions, including platform mutations.
  SDK hello fuzzing: 4,679 executions.
- Persisted peer intake, private host state/finish, shared storage/log readers,
  numeric selectors, metadata transport and service-installation handover tests.
- Production local controller, schedule and queue construction; readiness,
  consumer ordering and drain-acknowledgement regressions.
- Surface and engine-amendment acceptance selections, engine W8 components,
  native-store owner modes and cancelled-host log finalization.
- Embedded-doc/privacy/symbol/changelog selections, generated spec and mirror
  checks, commentcheck against main and diff whitespace checks.

The new replacement tests ran red on the published-pin refusal before the fix.
A precedence regression also ran red before the specific-over-wildcard rule.
The first SDK hello fixture omitted required features; it was corrected before
the successful property and fuzz runs. No archived test was newly ported and no
native acceptance cell was newly claimed. Native public local_run verification
remains with the lead, using the existing rewrite_integration probes and isolated
HOME/config/state. This worker did not start a daemon or run operational CLIs.

## Foreign contracts and real integration points

1. **DESIGN: C1/C2 owner/eval metadata commits.**
   TestReviewOwnerEvaluationReportsBeforeWorkAward still returns HTTP 409
   lock_held for annotations and summary, instead of 204. Authorization succeeds;
   checkResultWriterTx then requires a live node holder and positive attempt.
   The invariant is that owner/eval metadata retains its live lease and source
   relation independently of a work attempt. The previously reported two-strikes
   design handoff remains open. A separate metadata gate must recheck that
   authority at commit while step reports retain their attempt fence. No
   synthetic attempt or broader holder right was added.
2. **C2 owner-source and eval services.**
   bindingPipelineReferenceTx still requires UnitNode. The owner source checker
   cannot make its parent transport authorize a latest-reference request.
   UnitEval's fixed grant table also omits NodeReport for mask/cleanup routes.
   These require the owning area's authority contract before further adapters.
3. **W8 production Cloud boundary.**
   The complete TestInstalledRunnerRuntimeComposition was run. Start and warm
   reach GET sparkwing.dev/versions.json; fetch-source reaches the source-
   credential POST; run-node reaches the claim-exchange POST. Launch still fails
   with launcher runtime is unavailable. KubernetesBoundary.Probe and Clear
   still have only test implementations. W8 must provide enforced network
   evidence and reservation-scoped cleanup through CloudBoundary, after which
   ProductionRuntime.Launcher can use NewLauncher and the authenticated client.
   Existing component tests exercise that contract with fakes. No no-op or
   error-only production factory was added to conceal the missing binding.

The earlier foreign placement, cross-run record/cache provenance, publication,
Cloud plan/eval/source consumption, scheduler intake and Kubernetes debug-target
requests remain recorded in W9-PRUNE-SELECTOR-CONTINUATION.md and its linked
reports. This session changes none of those contracts. Completed findings 6-9,
peer intake and numeric/debug projections were retained. The private JSONL
writer remains deleted. No actionable W9 change remains without those foreign
contracts or the lead's native execution evidence.

## Scope and friction

Shared production files: internal/engine/engine.go, pin.go, resolver.go,
install.go, release.go, the new installed.go, internal/engine/sdkinstall/install.go
and the one-line SDK hello condition in internal/nodehost/sdk_code.go.
Shared tests: engine and installer tests plus nodehost/sdk_replacement_test.go.
Shared documentation: c3-engine-version.md, api/openapi.yaml and generated
pkg/docs copies. CLI changes are confined to its engine-selection test.

Mechanisms added: the explicit replacement-pin identity and disabled-pinning
notice implement decision 111. Ordinary signed-release resolution cannot serve
an unpublished replacement. No flag, environment variable, retry loop, service
or authority bypass was added. Removing the replacement branch reverses it.

No Git write, candidate install, pipeline, deployment or release ran. Git has no
local user identity or core.worktree override. Scratch is removed after checks
join; /tmp/sw-gocache is retained. Friction: sandbox sockets, the foreign design
handoffs above and Overwing's installed-binary source lag.

STATUS: COMPLETE
