# W9 burst native cache continuation

Starting HEAD: `e95c6e770`. Earlier routing, source, requester-secret,
contention, overflow-control and personal-machine implementation is retained.
No Git writes, merge, deployment, installation or operational Sparkwing
execution ran. Lead-owned handoffs and amendments are unchanged.

## Implemented

- Reproduced the production native host's refusal of every declared dependency
  cache. The shared services constructor now binds the existing DepcacheRecords
  adapter for local, here, registered-machine and Cloud execution. Cache uploads
  reuse the current output binding. Package-manager paths use the initialized
  session's persisted job environment. No new cache implementation is added.
- Added native-services directory save/restore and package-manager resolution
  tests. The storage and listener boundaries are fakes. The generated publication
  property mutates run, job, generation, attempt, pipeline, repository and ref
  and proves those changes cannot upload under another award.
- Replaced the compiled-source proof's fake resolver and in-process installer
  override with the real engine resolver, replacement-pin executable verification
  and the default selected-engine `__sdk-install` subprocess. Execution also
  rechecks the accepted engine with the real resolver. Closing source removes
  the installation-owned executable. The test executable dispatches the hidden
  installer entrypoint to the production installer.
- Here, personal and exchanged Cloud compiled-source cases still execute the
  uploaded SDK, release requester secrets, mask raw/base64 logs and publish and
  retrieve artifacts through real controller/store storage. FIFOs replace HTTP
  sockets. Kubernetes and the machine catalog remain fixture boundaries.
- The public native CLI fixture now declares a cache on its producer and
  consumer and requires the consumer's cache-restored marker. These native
  probes remain socket-blocked here.
- Updated the local execution guide, changelog, embedded mirrors and C3 contract.

Invariant: native cache reads and writes retain the live job's team, repository,
ref and attempt authority. The round-trip checks lookup, download and commit
authority. The generated property checks invalid execution identities before
upload. Existing requester-secret persistence and masking properties are retained.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package or fuzz workers.

Passed:

- Whole-repository `go build -p 4 ./...`.
- Focused vet on CLI, controller, localcontroller, nodehost, cluster and runners.
- Complete short localcontroller, nodehost, cluster and runners suites.
- Controller burst and requester-secret selections, including compiled uploaded
  source on all three placements through selected-engine installation IPC.
- CLI burst, routing, source, native preparation and environment properties.
- Race checks on cache composition and scope properties and the three compiled
  source placements. The expanded cache property also passes under race.
- Three-second final cache-scope fuzz campaign, 1,283 executions.
- W9 surface grammar, query/prune, management authority, wire detail and run
  observation acceptance selections. No native matrix cell is newly unskipped.
- Full doccheck executable, embedded mirror/changelog checks, focused docs tests,
  whitespace and commentcheck against HEAD across eight Go files.

The cache round-trip first failed with `executor layer caches has no storage
adapter`. It passes after wiring. The selected-engine proof first failed because
the test executable did not dispatch `__sdk-install`. Its test-only dispatch now
uses the production installer. No production engine policy was weakened.

Commentcheck against main still reports the inherited untagged comment at
`internal/store/store.go:108`. No archived test was newly ported. The existing W9
legacy cuts and port reports remain in force.

## Contract changes made

C3 protocol/SDK records native cache composition, authenticated upload-key
derivation and late access to the initialized SDK environment. It also records
the real selected-engine resolver/install path and remaining fake boundaries in
the compiled-source proof. No foreign API amendment, SQL schema, wire field,
flag, environment variable, cap, secret store or runtime service is added.

Mechanisms added: test-only installer dispatch and cache storage/listener fakes.
Production reuses the existing cache adapter, environment projection and output
authority binding. No smaller implementation was needed beyond wiring them.

## Remaining native integration

All five public CLI execution cases still skip at
`listen tcp 127.0.0.1:0: socket: operation not permitted`. The cancellation
parent reports PASS with skipped children. This is unverified coverage.
Requester-secret and contention component proofs pass, but these skips cannot
establish full native end-to-end completion.

The lead should run outside this sandbox:

```bash
env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./cmd/sparkwing -run '^TestBurstPublicRun' \
  -count=1 -v -timeout=15m

env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./internal/controller \
  -run '^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$' -count=1 -v
```

Real integration points remain public CLI/controller startup, terminal streaming,
Ctrl-C, competing local admission, personal-runner lifecycle and declared-cache
transfers through native sockets. Signed release download on fresh machines,
the deployed Cloud runner image, DinD, object service, network isolation and
native Windows/Darwin/PostgreSQL lanes remain separate verification points.

Requester values remain volatile within the supported single-controller
topology. Restart requires resubmission. Multiple replicas need an affinity or
ephemeral-relay contract. The prior retry-selection DESIGN remains with the
lead and was not patched again. No new open contract question is introduced.

## Size, shared files and friction

Production this session: +9/-2, net +7 in the shared native-services constructor.
The cache-refusal stub is removed. The source proof removes its fake resolver
and in-process installer override. W9-owned production this session is unchanged.
W9 versus main remains +18,329/-23,824, net -5,495, within the -2,900 budget.
Counts exclude tests, testdata, acceptance and contract declarations. No API was
removed, so no callers in other packages required migration.

Shared files touched:

- `internal/wingd/localcontroller/native.go`, `native_artifacts_test.go` and
  new `native_caches_test.go`.
- `internal/controller/burst_sdk_source_test.go`, `burst_sdk_execution_test.go` and
  `leak_test.go`.
- `design/rewrite/contracts/c3-protocol-sdk.md`, `CHANGELOG.md` and generated
  `pkg/docs` mirrors.

Other edits are W9's two public burst probe files and local execution guide.
Friction is the forbidden socket boundary, the inherited main commentcheck
finding and Overwing's source-lag notice. All checks are joined. The process
sweep found no scratch executables. Worker scratch is removed at the end and
the shared build cache is retained. Local Git identity/core.worktree overrides
are absent.

STATUS: CONTINUE
