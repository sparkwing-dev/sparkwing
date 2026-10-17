# W9 burst cache transport and claimed-host proof

Starting HEAD: `d5dfa6f6c`. Earlier five-part burst implementation is retained.
No Git writes, operational Sparkwing commands, installation, deployment or merge
ran. Lead-owned amendments and handoffs were read and left unchanged.

The only merge conflict was C2 credentials. Its file content now matches
integration stage 3 byte for byte. No integration deletion was restored. Its
index entry remains unmerged because the swarm brief prohibits Git writes.
The lead must stage that resolution when finishing the existing merge.

Implemented and verified:

- Compiled uploaded SDK execution now goes through ClaimedNodeHost, including
  the initial successful HTTP renewal, source acquisition, selected-engine
  installation, native SDK execution and joined source cleanup. The test first
  failed because its old fixture bypassed renewal. The fixture now observes
  actual computing activity and uses the filtered remote environment.
- SDK executions on here, personal-machine and exchanged Cloud awards declare
  a dependency cache. After execution the test removes the generated directory,
  restores it through DepcacheRecords and checks the exact dependency bytes.
  This reaches real controller authorization, SQLite, signed object transfer
  and record storage. Raw/base64 secret masking and artifacts remain checked.
- That test exposed two remaining production gaps. Cache writes carried
  pipeline/job fields that C1 rejects for repository-scoped caches. Cache lookup
  still returned policy_missing from the client. The original real-storage
  repro failed on every placement before these fixes.
- Cache save validates its complete job upload binding before dropping only
  pipeline/job from the persisted repository/ref/name/key selector. The upload
  retains its originating attempt and authority. Existing mutation properties
  still refuse changed run, job, generation, attempt, pipeline and repository.
- The dependency-cache client now calls the existing lookup endpoint. It checks
  the execution storage binding and validates returned kind, repository, name,
  content key, digest and own/PR-base/default ref. It transports no ReadSet,
  uploader or verified App facts. Signed download independently rechecks live
  entitlement in the controller transaction.

Invariant: dependency-cache reads and writes retain the live job's team,
repository, ref and attempt authority. Generated client response mutations and
native upload mutations cover this boundary. The compiled SDK also refuses an
ambient TOKEN and requires host PATH. No requester value is added to storage,
source bundles or process environment.

Contract changes made: C3 controller wire moves the existing dependency-cache
request/response shapes into controllerwire and documents execution lookup
without private proof transport. C3 protocol/SDK documents validated upload
provenance versus repository-scoped record keys and the claimed-host proof.
The old private response declaration is removed. OpenAPI and embedded docs are
regenerated. There are no new JSON fields, grants, limits, caps or services.
No foreign contract change is requested. The separate cross-profile memo and
verified-source contract remains outside this execution-cache fix.

Verification used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go,
TMPDIR=/tmp/sw-astra-W9 and at most four package/fuzz workers.

Passed:

- Whole-repository build and focused vet on CLI, controller/client, controller,
  controllerwire, nodehost, localcontroller and apispec.
- Complete short nodehost, localcontroller, controller/client, controllerwire,
  apispec and doccheck suites.
- CLI burst, route, source, native intake and overflow selections. Store burst,
  personal-machine ownership, overflow and requester-secret selections.
- Controller burst, requester-secret, native-data and personal enrollment paths.
  Nine SDK placement cases cover in-process, native-pipe and uploaded-source
  execution. All now verify real declared-cache restore as well as artifacts.
- Race checks on native-pipe and compiled-source execution, client lookup,
  native cache composition and scope properties.
- Three-second fuzz campaigns: client scope substitutions, 10,697 executions.
  Native upload scope, 2,437 executions.
- W9 surface grammar, query/prune, management authority, wire detail, observation
  and credential-policy acceptance, plus all DataCacheHTTP selections.
- API generation, embedded-doc drift, whitespace and commentcheck against HEAD.
  The independent focused review found no correctness/security issue.

No native matrix cell is newly unskipped. No archived test is newly ported.
Commentcheck against main still fails on the inherited untagged comment at
pkg/store/store.go:108. The first doccheck executable call omitted its required
arguments. The corrected invocation supplies docs and repository paths.

The five public CLI burst execution cases still skip because this sandbox
refuses loopback and local-controller sockets. Skipped children are not native
passes. Outside this sandbox, run the public probes with:

```bash
env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./cmd/sparkwing -run '^TestBurstPublicRun' \
  -count=1 -v -timeout=15m
```

Public CLI streaming, Ctrl-C, competing local admission and personal-runner
lifecycle therefore remain native verification points. The deployed Cloud
runner image, DinD, network isolation, fresh-machine signed engine distribution
and native platform/database lanes remain external. FIFO HTTP and fake
Kubernetes do not establish those properties. Requester values retain the
single-controller, volatile launch contract. Restart requires resubmission.
The prior retry-selection DESIGN remains with the lead.

Production delta this session: +67/-9, net +58. W9-owned production: +64/-2,
net +62. W9 versus main: +18,400/-23,824, net -5,424, within the -2,900 budget.
Counts exclude tests, testdata, acceptance and contract declarations. Deleted:
the dependency-cache refusal stub (2 lines) and duplicate response declaration
(7 lines). Earlier legacy cuts remain. No removed API leaves callers behind.

Shared files touched: internal/nodehost/depcache_archive.go and
host_scripted_storage_test.go; internal/wingd/localcontroller/native_caches_test.go;
internal/apispec/wire.go; pkg/controller/data_cache_routes.go;
pkg/controller/burst_sdk_execution_test.go, burst_sdk_source_test.go and
testdata/burst-sdk/main.go; CHANGELOG.md, generated api/openapi.yaml and pkg/docs
mirrors; the C2 conflict resolution and two C3 contract documents. Other edits
are W9's controllerwire/client implementation, client tests and run guide.

The changes use the existing upload validator, record namespace, lookup route,
shared DTOs, host and source owner. The client checks need no new proof service
because download reauthorizes in the controller. No smaller change would both
make the declared cache usable and retain its authorization boundary.

Friction: forbidden sockets, read-only Git metadata, the inherited commentcheck
finding and Overwing's source-lag notice. No approval was requested. Worker
scratch is removed after checks join. The shared build cache is retained.
Local Git identity and core.worktree overrides are absent.

STATUS: CONTINUE
