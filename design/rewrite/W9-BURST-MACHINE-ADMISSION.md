# W9 burst machine admission continuation

Starting HEAD: `0469f063f`. Earlier routing, source, requester secrets,
overflow controls, personal enrollment, Cloud allocation and artifact work is
retained. No Git writes, operational Sparkwing execution, installation,
deployment or release ran. Lead-owned amendments and handoffs are unchanged.

## Implemented

- The production registered-runner loop advertises physical capacity separately
  from free CPU and memory, including OS and architecture. It previously sent
  zero resources without a capacity callback. Contribution and keep-free
  settings now narrow the offer. An observation error stops before claiming.
  Explicit zero availability remains distinct from unknown physical capacity.
- Enrollment-enabled local admission observes the existing machine queue. If
  its controller is absent, the runner resolves its own release's verified CLI,
  starts the existing detached lifecycle and waits for readiness. An unreachable
  owner never causes a second owner to start. Runners without local admission
  use the existing host and container sampler.
- The real controller/store claim path now uses an unbound registered machine's
  supplied physical capacity. Before the fix, a newly enrolled machine with no
  profile override received a four-core job while offering zero free cores.
  The real enrollment/HTTP/store regression first reproduced that award and
  now verifies refusal, followed by an award when headroom returns.
- Independently claimed jobs acquire a job-specific machine lease before source
  preparation and retain it through SDK cleanup. Queued cancellation cannot
  acquire source or start an attempt. Admission refusal reports through the
  existing claim setup-result path. Jobs in one run use separate holders.
- Cloud-owned machine admission has a controller-scoped identity. The local
  controller neither looks up nor finalizes that identity in its own run store.
  This closes the real admission-handler failure where Cloud run IDs were
  rejected as missing local runs. Local lookup failures remain errors.
- The public personal-runner probe now uses its own HOME, runtime home, cache
  and admission controller. The development fixture starts that controller
  first. It still uses one development binary and an absolute SDK replacement.
- Run docs describe machine headroom and startup. An auth-guide command reference
  is now a complete runnable enrollment example. Mirrors and the wingwire API
  snapshot are regenerated.

Invariant: machine admission never reads, creates or cancels another
controller's run in the local run store. The real-store property checks remote
root and job holders, including a colliding run name, without changing the local
run or adding a run record. A separate fuzz property proves the holder codec
keeps controller/run tuples distinct for adversarial strings. Machine lease
properties keep two jobs separate and join every acquired lease.

## Contract changes made

- C3 controller wire: optional physical capacity, OS and architecture also apply
  to unbound registered machines. RequestCapacity takes context and returns
  physical resources, available resources and an error. All consumers migrate.
  Existing JSON fields are reused. Store profile ceilings still constrain awards.
- C3 protocol/SDK: ClaimedNodeHost.Admission and NativeHostConfig.ClaimAdmission
  compose the existing LocalAdmission around independently claimed jobs.
  Admission refusal before source reports without allocating an attempt.
- C4: documents runner contribution/keep-free projection, verified CLI startup,
  shared capacity parsing and detached readiness, independent job admission,
  and the controller-scoped remote admission codec. Remote machine leases do
  not create local run ownership. The codec's two exported APIs are snapshotted.

No SQL schema, new public JSON field, grant, price, cap, flag or environment
variable is added. No foreign contract amendment is requested for these fixes.

## Verification

All Go checks used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package or fuzz workers.

Passed:

- Whole-repository `go build -p 4 ./...` and focused vet on CLI, cluster,
  runners, nodehost, wingd/client/localcontroller, controller, wingwire and
  acceptance packages.
- Complete short cluster, runners, nodehost, localcontroller and wingwire suites.
  The source acquisition fixture expected eager credentials before bundle
  download. Its trace now follows the existing lazy-credential contract. Its
  two bundle readers and credential cleanup still pass.
- CLI burst preparation, source, routing, attachment, credential and here-host
  selections. Controller burst intake, source, personal, overflow, cancellation,
  SDK subprocess and fake-Kubernetes Cloud Job selections.
- New real enrollment/HTTP/store zero-headroom regression, source/admission
  lifecycle tests, separate job-holder properties and observer-error refusal.
- Race checks on the new CLI-runtime capacity, node admission, controller
  headroom and real-store remote ownership selections.
- Three-second fuzz campaigns: remote holder identity, 60,691 executions.
  Real-store remote ownership, 9 executions. Contribution/reserve projection,
  12,538 executions. The final run-record count assertion passes its seeds.
- W9 surface grammar, query/prune, observation and credential-policy acceptance.
  Migrated native-store owner, delegated-owner refusal, partial binding and
  award-property selections pass. No native matrix cell was newly unskipped.
- Embedded docs, doccheck, auth example grammar, mirror drift and whitespace.
  Commentcheck against HEAD is clean. Against main it still reports the
  inherited pinnedClock comment at pkg/store/store.go:108.

The first broad test expression also selected listener-based docs, Google-auth,
daemon-capacity and daemon-spawn tests. Those could not run because this sandbox
refuses TCP and Unix sockets. The public burst and installed-runner probes
compile and explicitly skip at that boundary. These are not native passes.
No archived test was newly ported. The CLI capacity test moved to the shared
client parser, and the source-order fixture follows its existing contract.

## Remaining integration and decisions

The lead should run these outside the socket-restricted sandbox:

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

- Verify released fresh-machine CLI resolution and admission startup. The unit
  test uses the runner startup helper with resolver/lifecycle fakes.
  The development native probe starts its separate controller explicitly.
- Verify here and personal output, masking, Ctrl-C, artifacts and parallel local
  contention. Existing here availability is observed before local admission.
  It is not an atomic reservation with Cloud placement. Owner release still
  waits for admission before making a locally selected job ready, so a competing
  local holder can delay overflow. This session does not close that placement
  design question or claim the native saturation behavior is verified.
- Execute a real Cloud Job with the shipped runner image, DinD, source service,
  object backend and network boundary. The composed Cloud subprocess test uses
  fake Kubernetes and isolation evidence. Native declared-cache service
  composition remains an inherited integration point.
- Requester secrets still require one Store process for intake and release.
  Multiple controller replicas or restart need verified affinity or an
  unpersisted relay. Values must not enter SQL or the source bundle. The earlier
  committed retry-selection DESIGN remains with the lead and was not rechecked.
- PostgreSQL, Windows and Darwin need their native acceptance lanes.

## Size, deletions and shared files

Production Go this session: +321/-72, net +249. The removed lines include the
duplicate CLI capacity parser and unused productionHosting wrapper. The old
callback signature is cut and every consumer is migrated. Earlier legacy
deletions remain. W9-owned production this session is +12/-28, net -16.
W9 versus main is +18,329/-23,824, net -5,495, within the -2,900 budget.
Counts exclude tests, testdata, acceptance and contract declaration files.

Shared implementation/test files:

- internal/cluster/production_runtime.go, production_capacity_test.go,
  runner_capacity.go, runner_capacity_test.go, production_transport_test.go and
  production_runtime_integration_test.go.
- internal/nodehost/claimed_node.go, local_admission.go, native_host.go,
  claimed_admission_test.go and host_source_test.go.
- internal/runners/loop.go, loop_test.go, runtime.go and store_runner.go.
- internal/wingd/machine_sampler.go, client/capacity.go, client/capacity_test.go,
  client/local_lifecycle.go and client/local_process.go.
- internal/wingd/localcontroller/admission.go and remote_admission_test.go.
- pkg/controller/engine_claim.go and runner_capacity_test.go.
- pkg/wingwire/identity.go and remote_identity_test.go.
- tests/acceptance/native_store_w8_test.go, .apidiff/pkg_wingwire.txt,
  CHANGELOG.md, generated pkg/docs mirrors and the three contract files above.

Callback callers in cmd/sparkwing/local_services.go and its test migrate too.
The removed productionHosting wrapper's two test callers use the real prepared
host constructor directly. No removed API has an unmigrated caller.

Mechanisms added: one error-aware resource observation callback, reuse of the
existing sampler and detached lifecycle, one independently claimed-job entry
into LocalAdmission, and one remote holder codec. A raw CPU count would leave
contribution and contention ignored. Treating missing local runs as live would
conflate owners and leave colliding local IDs exposed to finalization. A second
parent run lease for each remote job would conflate sibling jobs. These additions
use the existing resource and controller owners. Reverting the new callback,
admission field and codec returns to the previous composition. No new service
or persistent registry is added.

Friction: sandbox sockets, the inherited main commentcheck finding and the
standards tool's reported source lag. No approval was requested. Checks are
joined before scratch cleanup. Worker scratch is removed and the shared cache
is retained. Local Git identity and core.worktree overrides are absent.

STATUS: CONTINUE
