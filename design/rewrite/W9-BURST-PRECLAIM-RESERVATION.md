# W9 burst reservation and native secret continuation

Starting HEAD: `6fc8a0ffc`. No Git writes, merge, deployment, installation or
operational Sparkwing invocation. Lead-owned amendments and handoffs are unchanged.
Earlier route configuration, offline fallback, source capture, overflow controls
and personal enrollment remain in place.

## Implemented

- Fixed the remaining observation-to-claim contention race. The regression first
  showed an unstarted job claimed by the owner while machine admission waited.
  Burst self-execution now acquires compute before requesting an award. The job
  remains ready for other claimants. A joined state follower cancels admission
  when another claimant advances it. Logical concurrency remains held once.
- The actual granted resources accompany reconciliation as optional `reserved`.
  The store credits that reservation in the eligibility and claim transaction.
  Ordinary heartbeats and private snapshots retain free capacity only. A
  concurrent heartbeat cannot erase the reservation or expose it to another job.
  Physical capacity, placement, repository and claim checks still apply.
- Added real SDK subprocess masking properties with generated multiline,
  quoted, binary-derived and Unicode requester values. Real intake, claims,
  secret release and process delegation precede execution. Captured output
  contains neither plaintext nor base64 secrets. Existing SQL/WAL isolation,
  loss, empty-value and stale-claim proofs still pass.
- The native HTTP probe reports socket restrictions as explicit skips instead
  of panicking inside httptest. Other listen failures remain failures.
- API discovery now excludes testdata execution programs. Its failing fixture
  reproduced the burst SDK sample appearing as a public package. Regenerated
  API references, store snapshot and embedded docs. Snapshot generation also
  catches up the already committed trusted-proxy method and depcache parameter
  names. No product API was added for those inherited snapshot differences.

Invariant: waiting for local compute never consumes an unstarted burst claim.
The real-store property generates fractional CPU and memory requests while a
second machine claims and finishes the job. All leases and waiters join.

Invariant: a reservation cannot widen physical capacity, placement or claim
identity. The real reconciliation property generates oversized, negative,
nonfinite and over-memory reservations, foreign owners, stale generations and
incompatible selectors. Refused requests leave the job unclaimed.

## Contract changes made

- C1/C3: optional `ReconcileRequest.Reserved` / JSON `reserved` carries actual
  machine resources already held for this job. It is transaction-local and is
  not a persisted availability observation. Omitted inputs preserve existing
  reconciliation. Wire, controller, remote owner and store adapters migrate.
- C3/C4: compute admission precedes a burst self-claim. The internal Lease adds
  Resources(); machineLease, fakeLease and pipelineAdmission implement it.
  Preclaim transport failure returns an error without claiming or finishing
  the job. Independently awarded jobs retain their fenced setup-result path.

No schema, cap, timeout, grant, environment variable, controller owner or secret
persistence was added. No contract amendment from another worker is needed.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR=/tmp/sw-astra-W9/go, TMPDIR=/tmp/sw-astra-W9 and at most four workers.

Passed:

- Whole-repository build and focused vet of every changed production package.
- Complete short nodehost, localcontroller, controllerwire and controller/client
  suites; complete cmd/apidiff suite.
- Controller burst acceptance, native SDK pipe execution, uploaded SDK build,
  fake-Kubernetes Cloud Job, requester-secret and source selections.
- Store burst placement, overflow, local-secret and registry-loss selections.
- CLI burst routing, source, native preparation and offline-secret selections.
  W9 surface, credential-policy, query and observation acceptance selections.
- Race checks of burst admission, reservations, requester secret release and
  native SDK pipe execution. New admission and reservation fuzz seeds pass under race checking.
- Three-second fuzz campaigns: machine contention 49 executions; reservation
  fences 118 executions; native requester masking 7 executions.
- Doccheck, commentcheck against main, API snapshot drift, embedded-doc drift
  and whitespace checks.

No native matrix cell was newly unskipped. No archived test was newly ported.
The existing composed acceptance flows remain component evidence, with actual
SDK subprocesses and real HTTP handlers/store. The Cloud Job uses fake
Kubernetes. Socket HTTP and public CLI probes compile, then skip their native
cases because this sandbox refuses loopback sockets. Those skips are not passes.

## Remaining real integration

The lead must execute outside this sandbox:

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

Verify public here/personal execution, parallel local saturation, masking,
Ctrl-C, artifacts and offline fallback. Run a real shipped Cloud runner Job
with DinD, source service, object backend and the deployed network boundary.
Released fresh-machine CLI startup, native declared-cache service composition,
Windows, Darwin and PostgreSQL lanes remain separate native evidence.
Deploy the controller accepting `reserved` before native burst verification.

Requester secrets work in the supported one-replica/Recreate topology. A
restart loses volatile values and requires a new submission. Multi-replica
support still needs verified affinity or an unpersisted relay. No relay is
added. The earlier retry-selection DESIGN remains with the lead and was not
patched again or claimed closed.

## Size, deletions and shared files

Production Go this session: +158/-54, net +104. Deleted the post-claim compute
admission block and the now-unreachable reconcileExecution loop. The one caller
uses FollowReconciliation directly. No removed API has an unmigrated caller.
W9-owned production is +4/-3, net +1. W9 versus main is +18,435/-23,824,
net -5,389, within the -2,900 budget. Counts exclude tests, testdata, acceptance
and contract declaration files. Earlier legacy cuts remain.

Shared files changed:

- internal/nodehost: admission_test.go, burst_admission_test.go,
  burst_availability_test.go, burst_store_admission_test.go, burst_execution.go,
  local_admission.go, owner_schedule.go and remote_store.go.
- internal/wingd/localcontroller: machine_admission.go and node_pipeline_test.go.
- internal/store: burst_placement_test.go, owner_placement.go, state_contract.go,
  state_owner_read.go, state_reconcile.go and unit_matching.go.
- internal/controller: engine.go, burst_acceptance_test.go,
  burst_sdk_execution_test.go, burst_sdk_native_test.go and
  burst_sdk_http_integration_test.go.
- cmd/apidiff/main.go and main_test.go; CHANGELOG.md; api/openapi.yaml;
  .apidiff/pkg_store.txt, pkg_controller.txt and pkg_controller_client.txt;
  pkg/docs generated mirrors; C1/C3/C4 contract files.

Mechanisms changed: admission order, one joined follower around compute
acquisition, one Lease resource accessor and one optional reconciliation input.
Checking availability alone retains the race. Crediting a heartbeat alone loses
reservation identity when another heartbeat arrives. A new claim-release API
would add a lifecycle transition. The selected change reuses the queue,
follower, fenced matcher and claim transaction. Reverting these additions and
restoring the old post-claim admission restores the previous behavior.

Friction: forbidden sockets, installed standards-tool source lag, and the API
snapshot discovery treating SDK test programs as public packages (fixed).
No approval requested. No merge. Worker scratch is removed after joining checks;
the shared build cache is retained.

STATUS: CONTINUE
