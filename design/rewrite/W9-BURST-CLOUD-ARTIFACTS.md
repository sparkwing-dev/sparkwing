# W9 burst Cloud allocation and artifact continuation

Starting HEAD: `a7c568e28`. Earlier burst work is retained. No Git writes,
installation, deployment or release ran. Lead-owned amendments and handoffs
are unchanged.

## Implemented

- The production native host now publishes declared artifacts and stages
  declared producer artifacts through `ArtifactRecords`. A new regression
  first failed because native services supplied no artifact layer. The adapter
  derives uploads from the existing authenticated output binding and resolves
  producer manifests through the shared store. Missing cache composition still
  fails explicitly. No cache behavior is silently skipped by artifact wiring.
- A composed Cloud launcher regression first failed on a resource-free job.
  The launcher required explicit CPU and memory settings even though billing
  had already selected a class. Cloud awards now carry the allocation from
  their committed billing reservation. Author settings and their presence stay
  sealed independently. Job construction and bootstrap exchange consume that
  allocation. Price-table changes cannot resize the award. Here and customer
  machine awards carry no Cloud allocation.
- The existing burst subprocess test now drives real controller/store intake,
  launcher Job and bootstrap publication, source initialization, one-use
  exchange, SDK execution, secret masking, artifact publication/restoration and
  Job/bootstrap teardown. Kubernetes and measured isolation are infrastructure
  fakes. The existing SDK subprocess uses native request/frame pipes and a
  test-only FIFO HTTP transport where listeners are refused.
- The native-services two-process test now publishes an artifact in the first
  job and reads its staged bytes in the dependent job. It retains independent
  node awards, real controller storage and committed terminal selectors.
- Public here/personal CLI probes now publish from the edited uploaded SDK and
  consume the artifact in a dependent job. The native probes still require
  sockets. Product docs, changelog, OpenAPI, API snapshot and embedded mirrors
  are updated.

Invariants: a Cloud allocation comes from the award's immutable billing
reservation, independently of later prices or bootstrap exchange. Generated
allocation changes cannot reach execution. Artifact publication cannot use a
foreign run, job, generation, attempt, pipeline or repository. Properties cover
these boundaries through the new store reader, bootstrap host and production
native service adapter.

## Contract changes made

- C1: `Store.CloudAwardResources(ctx, authority)` reads live ancestry and the
  committed reservation in one snapshot. It projects the existing CPU class and
  `CPUClassMemoryBytes`. No schema, price, tier or customer cap changes.
- C3 controller wire: `ClaimedUnit.cloud_resources` carries the physical Cloud
  allocation on claims, synchronization and exchange. `Execution` keeps author
  settings. Launcher construction requires the allocation and bootstrap refuses
  a changed allocation. The old derivation from author settings is removed.
- C3 protocol/SDK: native artifact publication and consumption use the existing
  shared adapter and current storage authority. Missing caches still refuse.
- C4: records the same resource distinction and artifact composition, and the
  expanded native probe's verification boundary.

No foreign contract change is requested. The earlier retry-selection DESIGN
remains with the lead as recorded in W9-BURST-FINALIZATION.md.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package/fuzz workers.

Passed:

- Whole-repository `go build ./...`.
- Vet on cmd/sparkwing, controllerwire, runners and launcher, native local
  controller, controller/client, controller, store and acceptance. Vet was
  repeated for the final native-process and acceptance fixture changes.
- Complete launcher, native-local-controller and cluster suites, followed by
  the expanded native two-process artifact test.
- CLI burst/source/fallback/preparation tests and property seeds. Real source
  preparation still builds the uploaded edited SDK.
- Controller burst/source/requester-secret tests, including here, personal,
  saturated, Cloud retry and SDK subprocess paths. The existing New-tier Cloud
  award limit regression also passes.
- Store allocation property with 24 generated table changes and exchanges,
  plus the customer-machine allocation regression. Both pass with the race
  detector. Artifact composition and burst SDK tests also pass with race.
- Three-second fuzz campaigns: bootstrap allocation, 5,105 executions. Native
  artifact scope, 1,791 executions.
- W9 surface grammar, run-query/prune, launcher-sync and credential-policy
  acceptance. Affected Cloud/bootstrap/launcher/runner-identity acceptance
  selections pass after migrating the allocation fixtures.
- Clisurface, controllerwire, controller/client, API schema and doccheck suites.
  Embedded docs/sections/search selections and the doccheck executable pass.
  Generated mirrors and changelog pass their drift check.
- Commentcheck against HEAD is clean across 21 Go files. Diff whitespace passes.

Commentcheck against main reports the inherited pinnedClock comment at
pkg/store/store.go:108. The complete pkg/docs suite cannot run because its web
client tests open forbidden loopback listeners. Its embedded-doc checks pass.
The public CLI probes compile and explicitly skip on socket refusal. The native
SDK HTTP probe also compiled, then its httptest listener was refused before
native execution. No native placement matrix cell was newly unskipped, and no archived test was newly
ported. New tests cover the burst integration findings.

## Remaining native integration

The lead should run outside the socket-restricted sandbox:

```bash
env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./cmd/sparkwing -run '^TestBurstPublicRun' \
  -count=1 -v -timeout=15m

env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./pkg/controller \
  -run '^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$' -count=1 -v
```

The fake-Kubernetes proof does not execute a real Kubernetes Job, DinD, network
canary or release runner image. The production Cloud runner/service composition,
public CLI behavior under competing local admission, separate-machine admission
startup and signed engine distribution still need native verification. The
personal native fixture shares the local admission daemon and executable with
its requester. Local SDK replacements still use this host's absolute checkout.
Requester secrets retain the documented single-controller/restart boundary.
PostgreSQL, Windows and Darwin are separate native acceptance cells.

## Size, shared files and assumptions

Production Go this session: +82/-7, net +75. The seven replaced lines are the
old launcher resource projection/checks and native executor construction. No
legacy subsystem was added or duplicated. W9 production versus main is
+18,345/-23,824, net -5,479, within the -2,900 budget. No removed API required
caller migration.

Shared implementation/test files:

- internal/runners/launcher/{bootstrap,controller}.go and their tests, including
  controller_resources_test.go.
- internal/wingd/localcontroller/native.go, native_artifacts_test.go and
  node_pipeline_test.go.
- pkg/store/cloud_resources.go and cloud_resources_test.go.
- pkg/controller/engine_claim.go and burst acceptance/SDK/Cloud Job tests.
- tests/acceptance/bootstrap_store_w8_test.go, bootstrap_w8_test.go and
  runner_identity_w8_test.go. These migrate the existing Cloud award consumers
  to physical allocations while retaining their author-setting assertions.
- C1, C3 controller-wire, C3 protocol/SDK and C4 contracts, CHANGELOG.md,
  api/openapi.yaml, .apidiff/pkg_store.txt and generated pkg/docs mirrors.

Other changes are W9 wire/CLI/docs files. Mechanisms added: one store projection
and one optional award field, because author settings can be absent and cannot
represent an immutable physical billing allocation. Both derive from existing
reservation state. No new table, price selector, grant, configuration key,
service, environment variable or persisted secret mechanism is added. Removing
these two additions would restore the resource-free Cloud launch failure.

Friction: sandbox listener refusal, inherited main commentcheck finding and
Overwing's reported source lag. The initial wider docs suite retains its socket
failure as evidence. Initial caller failures were fixed by migrating the exact
Cloud fixture consumers listed above. All launched checks are joined before
worker scratch cleanup. The shared Go cache is retained. Local Git identity and
core.worktree overrides are absent.

STATUS: CONTINUE
