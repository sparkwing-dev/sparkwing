# W9 continuation: local integration and runner transport

Latest continuation: [W9-BURST-DETACHED-CACHE-CONSTRUCTION.md](W9-BURST-DETACHED-CACHE-CONSTRUCTION.md).
The queued cache constructor no longer requires default-branch metadata that
intake clears. It reads the hint from the authorized checkout while retaining
the sealed repository and server-side verified-default fence. The real-intake
regression and generated Git-override property pass, including with -race.
The authoritative native file still contains the detached-cache failure. The
lead must rerun the native family before completion can be claimed.

Earlier continuation: [W9-BURST-DETACHED-DIAGNOSTICS.md](W9-BURST-DETACHED-DIAGNOSTICS.md).
The lead's latest native run passes attached profile-cache execution and both
publication modes. Detached profile-cache execution still hangs in log following.
Its cause is unproven. Background host errors now reach the controller log, and
native failures include controller status, run status and that log. The harness
must rerun the native family before completion can be claimed.

Earlier continuation: [W9-BURST-CANCELLATION-PROFILE-CONTINUATION.md](W9-BURST-CANCELLATION-PROFILE-CONTINUATION.md).
It fixes owner cancellation proof ordinals, avoids repeating persisted remote
cancellation, and corrects the profile-cache native fixture's Git metadata.
Profile credentials and attached/detached publication are implemented. The lead's
latest native file still contains failures, so native completion remains open.

Earlier memo continuation: [W9-MEMO-CONTINUATION.md](W9-MEMO-CONTINUATION.md).
It implements native memo lookup/copy through the existing controller routes,
with the authenticated reader fence and no private ancestry projection. Real
router/SQLite and signed-byte materialization pass. The prior burst fixes and
the remaining native rerun commands are retained below and in that report.

Prior native failure fixes: [W9-BURST-NATIVE-FAILURES.md](W9-BURST-NATIVE-FAILURES.md).
Commit `3c97acfb1` addresses the lead's native burst failures: session enrollment
fixture configuration, execution-award validation, pipeline argument separation,
attached local logs and local cache namespaces. The continuation rechecks that
commit and gives the exact native rerun commands. Native execution remains
unverified because the worker cannot open sockets. Retry selection is explicitly
handed back to the lead with a failing regression and a recommendation.

Earlier reports below describe their own bases. Their superseded missing-owner,
here, source, local-cache and SDK replacement statements are not new blockers.
Cross-profile remote cache credentials and one-way publication under A1-50 are
implemented and covered through real router, store and signed-byte component
tests. Attached and detached publication and attached profile-cache execution
passed the lead's native run. Detached profile-cache completion remains open.
Full native and deployed acceptance remains lead work under A1-53.

The next continuation fixes background queue service binding and log finalization.
See [W9-QUEUE-CONTINUATION.md](W9-QUEUE-CONTINUATION.md) for its evidence and scope.

Base: `9129db2e9`. Priorities (1)-(3) are implemented. Native execution remains
with the lead. The area remains CONTINUE because the additional owner/eval and
launcher assignments need the contracts listed below.

## Implemented

- The daemon startup timeout was a probe entrypoint defect. Detached startup
  selects `os.Executable()`, which selected the test executable and reran its
  fixture compilation instead of serving `wingd run`. TestMain now dispatches
  that exact subprocess entrypoint through the real CLI. A separate regression
  starts no pipeline fixtures. An overlay removing the fix fails in 48 ms with
  `daemon reentered the test suite: PASS`. The real local runtime also passes a
  controlled test that keeps consumer preparation blocked while its API becomes
  ready. No startup deadline was increased.
- Public doctor starts/reuses the elected controller and invokes its private,
  authenticated session sweep. Successful cleanup with a missing run produces
  `unrecorded_step_outcome` and exits successfully. Dry-run preserves ledger
  entries. Findings survive another cleanup's failure, and that failure still
  fails the public command. Requests cannot substitute headers or bearer text
  for a verified OS peer. Concurrent sweeps share one controller lock.
- The native command probes register daemon shutdown in `t.Cleanup`. The
  in-process probe also cancels and joins its runtime on every exit. Native
  daemon shutdown waits for the socket and election lock to be released after
  worker and child cleanup.
- Service installation is a lifecycle transition. Both lifecycle gates and the
  existing interprocess startup claim cover old-owner drain/join, native unit
  activation, and successor readiness. The result verifies a new PID, installed
  unit, version and stay mode. Failures return an error without a competing
  fallback owner. OS service-manager ancestry still needs native verification.
- The elected controller supplies the shared filesystem object backend, signed
  transfers, records and log readers. Failed construction closes both backend
  and claimant. Shared storage maintenance runs in the existing worker after
  readiness, first at lease renewal and then at its existing hourly cadence.
- Native process services use authenticated controller callbacks and canonical
  byte/record authority. Node attempts refresh their independent storage and
  process bindings. The private JSONL log writer is gone. Shared logseg sinks
  commit segments and final manifests before report/retry/finish ends their
  writer's authority. Actual HTTP tests cover output upload, record commit,
  lookup, verified download and a public persisted-log read.
- Owner evaluation has its own binder and never asks nodeClaims for a work
  award. A fake contract binder proves evaluation can start before a work
  award. The production owner/eval credential contract remains unavailable.
- The shipped runner now composes start, warm, fetch-source and run-node factories with
  shared runtime owners, signed engine selection, credential/source transport,
  installation hooks and private execution state. Host-state glue moved from
  the CLI into nodehost.RemoteHostStore. Private execution endpoints now serve
  authenticated hosted controllers as well as api.sock. Doctor remains local.
  Independent node renewal uses the retained node bearer and unit ID.
- Committing result-proof validation accepts the exact private transition and
  finish routes while retaining its actor, body digest and claim fences. Real
  hosted HTTP probes now exercise node start/step/result, owner completion,
  cancellation drain, scoped snapshots/authentication and node renewal.
- Peer intake already used the shared local relation and committing authority
  gate. A new real HTTP-to-SQLite property checks 32 generated peer identities,
  actor forgery, replay, foreign teams and revocation before commit/replay.
- The Data client and schema mappings now use the server's actual byte and
  record DTOs. Incompatible authority selectors and union scaffolding were
  removed. The SDK guide states the accepted cross-host runtime-mask residual.

## Contract changes and real integrations still needed

1. **C2/W3 owner/eval process identity.** CredentialNodeProcess requires a node
   binding with a positive attempt. ProcessAuthorities/NativeProcesses also tie
   the service job ID to that node binding. An owner/eval lease has a different
   lifetime and must retain its own fence. Supply a supported owner/eval process
   binding and source-job relation. NativeConfig.Evaluate is separate and
   returns policy_missing without that adapter. No node attempt was invented.
2. **W8 Cloud launcher boundary.** There is no production implementation of the
   KubernetesBoundary network-enforcement probes and reservation-scoped cleanup.
   Launch remains unbound. The rewrite_integration installed-runner regression
   must remain red for launch until W8 supplies this boundary. Cloud plan/eval
   dispatch also needs the shared Hosting implementation for those unit kinds.
3. **C3/C1 placement eligibility.** Environment locality does not decide whether
   a row executes on its owner. The awarded RunnerIdentity lacks trusted
   location, pools and the canonical profile needed by match.Evaluate. Remote
   owner hosting needs a server-owned eligibility decision/profile. Do not
   reconstruct authority from runner labels.
4. **C2/W7 canonical cache and cross-run output transport.** Current public
   record/cache responses omit canonical source/uploader/ref and read-set proof.
   The adapter refuses to fabricate these proofs. Same-run output is bound to
   canonical storage. Latest-result and child-result reads still need an
   authorized cross-run record selector, and cross-repository/branch children
   need the shared source resolver. No removed output route was reintroduced.
5. **Paged log wire.** Public node log reads return bytes. W9's LogReader expects
   tagged LogPage records and cursor/finalization facts. Raw public reads now
   prove persisted local logs, but the structured CLI reader still needs that
   server contract. Do not infer subjects or completion from rendered text.
6. **Bootstrap/source integration.** Cloud source-init checkout consumption,
   unaccepted TypeScript SDK selection, native platform/process replacement and
   actual Docker/Kubernetes execution remain integration work.
7. Earlier unresolved integrations still apply: foreground first-claim
   reservation versus the background queue, W1's scheduled sealed intake,
   TypeScript pinning of the full executable dependency closure, and the named
   here/debug/publication/cache/native acceptance boundaries in earlier reports.
   Materialized result copying and terminal replay have since landed in their
   owners; they are not repeated as open contracts here.

The native rerun should select TestReviewProductionExecutionBindings,
TestReviewPublicDoctorSweepsAndWarnsForPrunedRun and
TestLocalClaimantW8UsesPersistedPeerAuthority. Keep a release-version stamp and
isolated HOME/config/state. Also rerun TestInstalledRunnerRuntimeComposition;
launch is expected to remain red until its deployment boundary is implemented.

## Verification and scope

No Git write, candidate installation, operational Sparkwing run, pipeline,
deployment or release ran. The only installed Sparkwing invocation was the
required read-only info. All Go commands used GOWORK=off,
GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache and at most four workers. Scratch
stayed under /tmp/sw-astra-W9 and is removed after checks join. The shared cache
is retained.

Whole-repository go build passes. Vet passes for every changed package and
its direct dependents, including store. Full short suites pass for cluster,
controllerwire, clisurface, runner services, localcontroller and controller/client.
Full apispec and doccheck suites pass after API/OpenAPI and mirror regeneration.
Focused public doctor/entrypoint, private hosted execution, peer-intake and
storage/log tests pass. The W9 surface/engine acceptance selection passes.
Lifecycle/service and doctor/peer race selections pass. The tagged CLI and
runner suites compile; native command assertions were not executed here.

The doctor request-identity fuzz ran 5,518 executions. The shared native log
masking/authority fuzz ran 8,314. Commentcheck is clean across 2,335 files and
git diff --check passes. The before-fix startup overlay fails exactly as the
regression requires. Independent review found the partial doctor-warning loss;
that finding is fixed and the expanded real HTTP matrix passes review.

Broader checks retain explicit failures outside this continuation's fixes:
TestEngineBootstrapExchangeReturnsOriginalUnitOnce has a launcher fixture with
an empty repository scope and fails claim admission. The serving-inventory test
still names four absent controller registrations (job output, child job output,
operator refunds and claim input); its hosted-only enumeration also cannot count
the local-only doctor operation. The declaration table includes the new routes
and passes independently. No server grant was weakened to satisfy a fixture.
Sleepcheck against main reports internal/source/checkout_test.go:141 and
tests/acceptance/session_flow_http_test.go:102. Neither file changed here.

Two whole production files were deleted: cmd/sparkwing/host_state.go (114 lines,
replaced by the shared state adapter) and pkg/controller/client/data_values.go
(93 lines of obsolete conversion). The private JSONL writer removed another
35 lines. Incompatible Data wire scaffolding and private-format tests were also
removed. The following production counts include untracked Go source and exclude
tests, testdata, acceptance and contract-declaration paths.


This continuation: all touched areas +1,785 / -582, net +1,203; W9-owned paths +803 / -488, net +315. W9 versus main: +16,835 / -23,797, net -6,962, within its -2,900 budget.

Friction: native sockets and platform services are unavailable in this sandbox.
The installed Overwing reports it trails its source. Foreign owner/eval, launcher,
placement, cache-proof and log-page contracts limit end-to-end completion. No
error string was changed merely to turn the installed-runner probe green.

The lead-owned handoffs and amendment files were read and left unchanged.
Shared production edits are limited to capability route entries, controller
registration and private projections, store result-proof route matching,
wingd lifecycle/controller maintenance, shared nodehost state, cluster runtime
composition, runner service registration and localhostwire DTOs. W9 source,
client, CLI, API schema and documentation paths contain the other changes.
Acceptance fixtures changed only the owner actor kind and the required
remove_reservations response field. No archived test was newly ported and no
native acceptance cell was marked passed.

Shared Go files (including their tests):

- `internal/apispec/wire.go`
- `internal/capability/routes.go`
- `internal/cluster/main.go`
- `internal/cluster/production_installation.go`
- `internal/cluster/production_restart_unix.go`
- `internal/cluster/production_restart_windows.go`
- `internal/cluster/production_runtime.go`
- `internal/cluster/production_runtime_integration_test.go`
- `internal/cluster/runner_cli.go`
- `internal/localhostwire/doctor.go`
- `internal/localhostwire/private_host.go`
- `internal/nodehost/native_host.go`
- `internal/nodehost/native_processes.go`
- `internal/nodehost/native_store_test.go`
- `internal/nodehost/remote_host_store.go`
- `internal/runners/service/refresh.go`
- `internal/runners/service/refresh_test.go`
- `internal/wingd/client/local_install_test.go`
- `internal/wingd/client/local_lifecycle.go`
- `internal/wingd/client/local_process.go`
- `internal/wingd/lifecycle.go`
- `internal/wingd/local_lifecycle.go`
- `internal/wingd/local_runtime_test.go`
- `internal/wingd/localcontroller/claims.go`
- `internal/wingd/localcontroller/claims_test.go`
- `internal/wingd/localcontroller/controller.go`
- `internal/wingd/localcontroller/controller_test.go`
- `internal/wingd/localcontroller/native.go`
- `internal/wingd/localcontroller/native_client.go`
- `internal/wingd/localcontroller/native_config_test.go`
- `internal/wingd/localcontroller/native_logs.go`
- `internal/wingd/localcontroller/native_logs_test.go`
- `internal/wingd/localcontroller/native_service_fixture_test.go`
- `internal/wingd/localcontroller/native_test.go`
- `pkg/controller/credential_route_expectations_test.go`
- `pkg/controller/local_doctor.go`
- `pkg/controller/local_doctor_test.go`
- `pkg/controller/local_http.go`
- `pkg/controller/native_host_storage_test.go`
- `pkg/controller/peer_intake_native_test.go`
- `pkg/controller/private_host.go`
- `pkg/controller/private_host_data.go`
- `pkg/controller/private_host_test.go`
- `pkg/controller/private_node_host_test.go`
- `pkg/controller/private_node_renewal_test.go`
- `pkg/controller/server.go`
- `pkg/store/result_proof.go`
- `tests/acceptance/engine_transactions_test.go`
- `tests/acceptance/launcher_sync_contract_test.go`

STATUS: CONTINUE
