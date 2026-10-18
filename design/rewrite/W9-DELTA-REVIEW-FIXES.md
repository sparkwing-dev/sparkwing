# W9 delta review fixes

Base HEAD: `df4d93f27`. No Git writes. The integration merge's three contract
conflicts are resolved in file contents, retaining integration amendments and
W9 behavior. The lead must stage C1/C3/C4 and finish the merge because the swarm
brief prohibits worker Git writes. No integration deletion was restored.
Lead-owned handoffs and amendments are unchanged. Earlier completed assignments
remain intact. This session closes the four delta findings and the shared
pipeline-identity handoff through the paths below.

## Implemented

1. Environment storage follows sealed `Request.EnvironmentSource`, independently
   of invocation origin. Captured snapshots remain local. Submitted values retain
   bound encryption. Child intake inherits its parent's snapshot and provenance
   transactionally and refuses changed values or provenance. NativeProcessClient
   tests reach real controller intake with nonempty Env, including descendants.
   A generated adversarial case checks that children cannot replace that seal.
   Local schedule registration now records captured provenance too.
2. `DescribeDoc.Pipeline` selects configured SDK entrypoints for the shared
   planner and CLI schema binding. Initial plans, attempt replans and plan
   inspection send the entrypoint, while run context and presentation retain the
   alias. Scaffolds register their configured entrypoint. Native trigger and
   overflow preparation cover deploy-prod -> Deploy, including an unrelated
   deploy-prod registration. Real scaffold/describe/help/plan commands pass.
3. One local source boundary resolves both retained ref flags and configured
   source pins before engine selection, help, inspection or building. The
   dispatcher carries that selection into preparation. Source acquisition keeps
   code and execution checkouts separate, restores them from the seal and builds
   with the code checkout's selected engine. Explicit HEAD selects committed
   bytes even when the working tree differs. Help still works without Git when
   no revision was requested. OLD/HEAD tests cover dispatcher selection, native
   preparation, help, sealing, reacquisition, real SDK planning and author context.
   Source credential access admits only the exact captured execution or code
   projection. A property test refuses changed repositories, URLs and commits.
   Scheduled source refs naming the execution ref use its sealed SHA without
   looking up the branch again.
4. The docs checker parses migration After-table destinations and fenced After
   examples, including repeated examples, separately from historical Before
   material. Destination commands and hooks.md use the current grammar. All
   checked current recipes parse. API and embedded documentation were regenerated.

The invariants are: children retain their parent's environment source; only
captured source projections can request credentials; SDK selection uses the
configured entrypoint; requested revisions govern both acquisition and the seal;
current documentation destinations parse under the authoritative grammar.

## Verification

Tests reproduced the failures before their fixes. The overflow regression also
fails against an overlay of the original shared describe implementation. The
source credential code-projection seed failed before the adapter change.

All commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
TMPDIR=/tmp/sw-astra-W9 and GOTMPDIR=/tmp/sw-astra-W9/go, with go -C and -p 4 or
fewer workers.

Passed:

- Whole-repository `go build -p 4 ./...` and vet on cmd/sparkwing, nodehost,
  nodeproto, source, runspec, doccheck, controller and store.
- Focused CLI regressions, trigger preparation and inspection, native trigger and
  overflow preparation, required alias flags, describe isolation, retained build
  policy, ref selection, real SDK plans, and scaffold/describe/help/plan commands.
  The final scaffold/source command finished in 80.368s.
- Whole source, runspec, nodeproto, controller/client, clisurface and doccheck
  suites. Focused controller/store environment, retry, child and schedule intake
  tests pass through real HTTP handlers and store transactions.
- Existing TestSurface* and TestEngineAmendments* acceptance cells. No new cell
  was unskipped and no archived test was newly ported. Alias fixtures now register
  the configured SDK entrypoint. A synthetic help/ref test was replaced by the
  real OLD/HEAD path.
- Alias metadata fuzz: 4,045 executions. Source projection fuzz: 10,129 executions.
- Full doccheck, OpenAPI drift check, embedded docs/changelog mirror tests,
  commentcheck against main, gofmt and git diff --check.

Not claimed as passes:

- Full CLI and pkg/docs suites hit httptest's socket refusal in this sandbox.
  The lead owns native reruns. The latest W9-burst-native-latest.md has no FAIL,
  but records the previous committed state, not this uncommitted merge result.
- Whole nodehost has pre-existing failures in TestBurstSharedOwner* and
  FuzzBurstMachineWaitNeverConsumesClaim (requester/Cloud award refusal), and
  TestOwnerDrainsPendingSupersessionIntent (deadline). An overlay of the original
  nodehost implementation reproduces all of them. The rest of nodehost passes
  with exactly those names excluded. These are W3/store integration work, not
  implementations changed here.
- Remote split-revision execution, sockets, deployed Cloud/DinD, PostgreSQL and
  native Windows/Darwin remain lead integration evidence. The remote source
  credential boundary is covered through its contract and HTTP test transport.

## Contracts and shared files

Integration-phase amendments made in C1, C3-controller-wire and C4 record
`environment_source`, `source.working_tree`, `source.committed_checkout`, the
separate execution/code projections and entrypoint selection. No further
contract change is requested for these findings. No new route, grant, setting,
customer flag or limit was added.

Shared production files touched:

- internal/runspec/{contracts,environment}.go
- internal/source/{source,checkout,acquire,projection}.go
- internal/nodehost/{checkout_planning,sdk_description,sdk_planning,sdk_code}.go
- internal/nodeproto/describe.go
- pkg/store/{intake_runtime,submitted_environment}.go
- pkg/controller/schedule_source.go
- internal/doccheck/cliverbs.go
- api/openapi.yaml, CHANGELOG.md and embedded documentation mirrors.

Their focused callers/fixtures were migrated in controller, store and nodehost,
including nodehost/testdata/pipelinealias. No API was removed and no unrelated
caller was deleted. W9 changes include the dispatcher, run preparation/help,
source access client, schema binding, trigger preparation and scaffold templates.

## Accounting and handoff

W9 production Go versus main: +19,686/-25,174,
net -5,488 lines, within the -2,900 budget. This session removes
80 W9 production lines and adds 191
(net +111). Across shared production files the session is
+407/-115 (net +292).
Counts exclude tests, testdata, acceptance and paths containing contract, matching
the lead's budget filter. No whole production file was deleted. Removed code is
the separate help/source-selection flow and origin-based environment exceptions.

The lead must stage the resolved contract files, finish the merge, review/commit
and rerun the native harness. No W9 design question remains open. Overwing reports
its installed binary behind source. Scratch is removed after checks join and the
shared build cache is kept. No harness memory, Git identity or core.worktree was
written. Native verification of this state remains pending.

STATUS: CONTINUE
