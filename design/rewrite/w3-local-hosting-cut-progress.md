# W3 local hosting cut

<!-- design:begin -->

This session removes **7,994 net production Go lines** across touched packages
(974 added, 8,968 deleted). W3 removes **8,167 net lines**
(712 added, 8,879 deleted). Against main, W3 is **-5,610**
(10,917 added, 16,527 deleted), **3,310 below** its -2,300 budget.
Counts follow the lead's exclusions for tests, testdata, acceptance and contract
files. Implemented host, plan-cache and remote-store files now count because they
have left their contract filenames. Orchestrator retains 10,313 production lines,
including its read surfaces, metrics and legacy local consumer.

## Implementation and deletion

Foreground local hosting constructs typed local intake with captured environment
identity, argument classifications, the invoked alias and registered entrypoint.
It submits through the controller, checks the sealed receipt, claims that run's
trigger unit and passes the award through W8 Hosting to RunHost. Local execution
uses a local trigger award. The here reservation and its credentials remain a
separate intake flow. Relative priority uses the existing queue projection and
wingwire resolver before its numeric value is sealed.

Run handles publish after accepted intake. CLI observation uses the shared attach
loop. Failed and cancelled completions retain their nonzero CLI status. Hosting
keeps the awarded HTTP transport, bearer, runner and owner fence. Preparation
cannot replace the captured seals. Partial preparation cleanup belongs to Hosting.
The small completingRunHost adapter captures RunCompletion because UnitHost.Host
returns only an error. No foreign interface was changed for that result.

The hidden local trigger command now accepts --run and uses the same native award
handoff. Its caller passes the run identity instead of a compiled program. Source
acquisition and SDK installation belong to the shared source package. The local
compiler, executable lease, checkout-selection dispatcher, unused child trigger
loop and trigger shepherd are removed.

Deleted Server and its bearer table, grant adapters, local output/secret/child
services, Config, SessionConfig, Attempts, Run, NodeRun, Result, RunResult,
Execute, the scheduler and its layer chain. This includes raw approval, memo,
retry-reuse, generator, admission, timeout, cache, artifact and step writers.
Session and StartSession were already gone.

Deleted orchestrator RunHosted, HostedEntry, HostClaimedTrigger and their local
policy, logging, rehydration and backend adapters. Removed the orphan backend
selection, HTTP retry transport, standalone store opener, setup-failure writer,
legacy Options and invocation builders. Native process cleanup keeps its shared
LineWriter. Raw protocol tests use ProtocolSession directly, without SessionConfig
or the legacy output queues.

Implemented declarations moved from host_contract.go, plan_cache_contract.go and
remote_contract.go to host.go, plan_cache.go and remote_store.go. The C3 contract
pointers are updated. Lead-owned amendment and handoff files are unchanged.

W9 now declares terminal result references. Remote reconciliation carries the
source run, job and state sequence in both directions instead of refusing that
source or losing its fence. Its generated test also checks that translations do
not alias the transport's mutable objects.

## Evidence and ports

The security properties exercised here are:

- A named local claim executes only its requested run, and preparation cannot
  replace the awarded seals, owner fence or runner identity.
- Registered secrets remain absent from native cleanup output across write
  boundaries, long-line splits and encoded forms.
- Remote reconciliation preserves the terminal result identity and state sequence.

The local handoff tests cover captured environments, aliases, numeric and relative
priority, accepted handles, success/failure/cancellation, missing native bindings,
forged prepared input and a foreign named award. The prepared-input fuzzer passed
2,204 generated executions in three seconds.

Retained raw-protocol session tests now start ProtocolSession directly. Their
stdout-forgery assertion inspects native log records. The cleanup-stream fuzz test
replaces the removed sink-queue masking fixture at the retained native boundary.
The CLI alias test keeps parsing/help assertions. Native pipeline_identity_test.go
continues to prove planning and execution of the invoked alias.

Tests for deleted adapters are removed with those APIs. Existing native SQLite
OwnerHost tests retain scheduling, recovery, approvals, debug, generated work,
events and retry reuse. Common executor tests retain retries, steps, drift,
startup/cleanup environments and persisted failures. Artifact, depcache, memo,
process-authority and service-router tests retain their contract checks. The
removed raw usage-record adapter still needs a native measurement binding.

No additional archived inventory row or native acceptance cell is claimed
complete. The in-package host acceptance checks remain in the passing nodehost
suite. Lead 09:40 items 1-3 remain done: no host_* harness files remain in
internal/acceptance, scripted interface fakes remain consolidated and duplicate
reconciliation/keeper checks remain removed. Lead 07:40 item 9 remains done.
No scenario catalog, Harness or boundary registry was extended.

Verification passed with GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and Go package parallelism at most four:

- Whole-repository go build ./....
- go vet on nodehost, lease, orchestrator, the CLI and runners.
- The full nodehost suite, with 416 top-level test/fuzz entries, and the lease suite.
- Focused CLI local/here handoff and alias tests.
- Focused native host, protocol-session and local handoff race tests.
- Direct-dependent orchestrator, CLI and acceptance test compilation.
- commentcheck -base main, Go formatting, documentation sync and whitespace checks.

A focused orchestrator selection fails TestConsumer_CancelRequestedBeforeClaimNeverDispatches
and TestConsumer_FailureRacingACancelStaysFailed. Both fail while seeding the
removed triggers.pipeline column, before dispatch. A HEAD source overlay reproduces
both failures. The fixtures remain. That selection is not green. Socket-backed
orchestrator and native placement suites were not run in this sandbox.

## Remaining work and contract changes needed

The default resolveHostRuntime still reports unavailable. The local caller refuses
before CreateRun when that binding is missing. Component fakes do not establish
native execution. The remaining composition must join the controller/store,
process authority, source access, storage bindings, unit renewal, persistent state
subscription, owner policy adapters, native usage recording and the selected
engine's actual host executable.

W3 still owns replacing the raw local consumer and its old submission-environment
adapter. It must not claim legacy trigger rows and then attempt a second native
claim. Local detached intake also needs the sealed request and digest-based
snapshot path. This cut removes the old source compiler, not that queue migration.

Foreign contracts and bindings still needed:

- W9: a private execution snapshot with the sealed request/spec, current owner,
  execution rows and accepted expansion bodies. ScheduleSnapshot is insufficient
  for OwnerStore. The run-start transaction also needs its native wire binding.
- W9 with W6: Lead 07:40 item 1. Options still lacks StartAt, StopAt and Only.
  Native local intake returns policy_missing for those selections rather than
  silently dropping them. Finish shared environment/machine setting projection
  for local intake too. This is an integration gap, not a new product policy.
- W2/W8/W9: native local/remote composition, process delegation, storage/source
  authority, renewal and state subscriptions. The declared __host-trigger
  state-db/env-file/standalone-reason bootstrap fields still need agreement with
  this final claim path. No foreign grammar declaration was changed.
- W8/W9: the selected verified engine's typed host invocation. InstallSDK alone
  does not prove that the selected release owns host execution.
- W1/W9: result-record references for native output, generator replay and memo
  reads. Terminal result-source transport is now implemented. Native authenticated
  result reads and the complete remote owner/executor adapters remain open.

Native Windows/Darwin, HTTP/api.sock, actual placement/storage integration,
PostgreSQL and full acceptance evidence remain with their owning integrations.

## Shared files and friction

Caller edits outside W3 ownership are cmd/sparkwing/run_host.go,
trigger_host.go and here_resume.go. Shared test edits are controller_socket_test.go,
here_attach_test.go, here_resume_test.go, trigger_intake_test.go and
pipeline_args_test.go. run_host_native_test.go is new. Source documentation changed
in docs/local-execution.md with its generated pkg/docs/mirror copy. The C3
protocol contract links this report and uses the implemented source filenames.

Friction was the socket sandbox, legacy consumer fixtures against the final schema
and the native bindings/contracts listed above. No new service, customer limit,
compatibility path or approval flow was added. No installation, Sparkwing runtime,
pipeline, Git write or release was performed. The lead owns review and landing.
Worker scratch is removed after all checks join. The shared build cache stays.

<!-- design:end -->

STATUS: CONTINUE
