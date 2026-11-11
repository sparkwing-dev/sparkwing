# Runner hosting

`NewManagedRunner` takes a `UnitHost`. `RuntimeConfig.NewController` opens the
claimant transport. The host owns each award's execution lifetime. The local
controller's service binder supplies `Services.Runner`, which constructs a
`Loop` over the authenticated controller API socket after it is listening.
An HTTP placement supplies `Hosting` with its award-specific controller factory
and native service binder. The binder returns `NativeServices` with an authenticated
`NativeHostStore` and the current C2 principal. It must derive that principal from
the authenticated transport. Decoding the opaque bearer does not establish authority.
`Hosting` confirms renewal before binding and captures the wire award before callbacks.
Both placements share award capture, deadlines, partial-service cleanup,
persisted-input checks and `nodehost.NewNativeHost`.
Node awards require their accepted plan, engine and execution settings before
confirmation or service binding. `FuzzHostingRejectsIncompleteNodeAwards` checks
every combination of missing inputs through the remote hosting entrypoint.
The native owner and claimed hosts own source acquisition and their only lease keeper.

Terminal step state and its diagnostic use the claim's execution lifetime,
including after requested cancellation. Claim expiry stops reporting.
`TestStepCompletionUsesTheLiveClaimLifetime` delays step persistence and diagnostics
past the heartbeat RPC budget and checks both live and expired claims.

An ordered runner loop asks for trigger, plan, eval and job units. A run-scoped
loop asks only for its trigger. Plan and eval awards for run-anywhere
runs execute in-process through the native owner, as a Cloud plan pod does.

`MaxConcurrent` bounds plan, eval and job awards. Trigger owners have a separate
bound of the same size, because their hosting waits for jobs to finish. Claims
offer only kinds with available capacity and stop while job capacity is full.
Owner scheduler waits and child awaits release their slots so nested run owners
can be admitted. A returning await reacquires its slot before the SDK
continues, waiting without consuming the no-progress budget. Cancellation
unblocks slot acquisition. `TestLoopAwaitResumeWaitsForRunnerSlot` covers
resumption behind unrelated work and cancellation at concurrency one.
`TestLoopOwnerDoesNotOccupyItsJobsOnlySlot` covers a waiting owner and its job
at concurrency one. `TestOwnerHostAwaitingDescendantsReleasesRunnerSlot` covers
three nested run owners on a runner with two owner slots. Claims report the configured limit in the optional
`X-Sparkwing-Max-Concurrent` header, independently of `free_slots`. Older
controllers ignore this header and continue awarding work. Renewals retain
that observation. Personal-runner `active_jobs` lists job awards and excludes
trigger owners. Older runners that omit the limit report zero (unknown).
`TestRunnerInventoryRecordsConcurrencyFromClaimHTTP`,
`TestRunnerInventoryRetainsConfiguredConcurrencyAcrossRenewal` and
`TestRunnerInventoryExcludesWaitingPersonalTriggerOwners` cover reporting.

Empty HTTP claims retain `X-Sparkwing-Poll-After`. The loop waits at least
that interval (up to eight seconds) or its configured poll, whichever is longer;
host completion preserves the controller's floor. Claim throttling retains
`retry_after_ms`. Here attachment waits and repeats an explicitly rate-limited
claim; a lost transport reply is never replayed because it may contain an award.
`TestClaimRetainsIdlePollAdvice`, `TestLoopHonorsIdlePollAdvice` and
`TestHereResumeWaitsForRateLimitedClaim` guard those paths. Idle-poll budgets
separate authenticated personal machine identities and here runs when no runner
header is supplied.

The loop has one event wait for pending claims, host completions, poll timing,
policy refresh and shutdown. A policy refresh leaves a pending claim in place.
Stopping cancels and joins that request, takes responsibility for a late award,
and joins every held host. Drain and policy denial preserve held host contexts.
Caller cancellation reaches those hosts and remains in the returned error.
A newer signed release defers automatic replacement until no units are held.
Claims stay live while work is held because an awaiting host may need child
claims, including children in another run. Continuous work can postpone a
routine update; no drain timeout cancels work. Revocation and minimum-version
refusals still stop claims immediately. Update drain entry and exit log the
current and target versions, reason, held units and elapsed time.
`TestManagedRunnerUpdateKeepsClaimsLiveForAwaitedChild` guards this boundary.

Replacement-pin source resolution retains the selected executable by SHA-256
under `runners/builds`. Later jobs and children resolve their accepted digest
there after an installed binary swap; new intake selects the installed bytes.
Both the CLI and standalone runner use the same retained executable owner.
`TestReplacementResolutionSurvivesInstalledEngineSwap` guards byte retention
and rejects substituting the new installation under the accepted identity.

Controller connection failures, timeouts, explicit 5xx unavailability and
5xx responses without a JSON error envelope allow fresh claim polls for up to five seconds of consecutive
failures. Exponential jitter adds to the configured polling floor and caps the
wait at five seconds, or the floor when longer. Retry-After seconds and HTTP
dates can extend that wait. If advice or backoff exceeds the recovery window,
claims stop at the deadline without an early retry. In-flight recovery claims
carry that deadline too. A successful response resets
that window. Each poll is a new request because a lost claim reply may
already contain an award. Authentication, permission and protocol refusals
remain fatal. Signed version policy keeps its existing denial behavior.
`Loop.Drain` stops new claims and returns immediately. `Run` joins held hosts,
including uploads and terminal acknowledgements, before it returns.
`TestLoopPollsThroughControllerRestarts`,
`TestLoopControllerRestartKeepsHeldHostAliveThroughDrain`,
`TestLoopBoundsControllerTransportRecovery`,
`TestLoopTransportRecoveryKeepsRefusalsFatal`,
`TestLoopClaimsAfterGatewayOutage`, `TestLoopGatewayRetryTiming`,
`TestLoopBoundsSlowGatewayRecovery`,
`TestLoopGatewayOutageKeepsHeldHost`, `TestLoopGatewayProtocolFailuresStayFatal`
and `TestMissingSocketDoesNotReplayClaim` guard recovery and request replay.
The shared controller decoder preserves gateway status and retry advice.
RetryableClaimError gives the runner its fresh-claim recovery classification;
RetryableTransportError retains its transport-only meaning. One-shot operations
return unavailable replies without extra retries unless the server advises a
retry or the operation already owns intake recovery. Launcher gateway failures
remain visible at WARN, including when shutdown interrupts an advised retry.
Held reads retain their award-bound recovery. The client never replays a claim.
A non-JSON success, an invalid error envelope in a JSON object or a protocol,
authentication or permission refusal remains fatal. Lease keepers retain their expiry and renewal policy.

Job reports and application retries carry optional `X-Sparkwing-Exited-At`,
captured after the execution process tree joins and before final log
acknowledgement, cleanup and artifact/cache publication. Older controllers ignore
this header. The store uses it for the job and attempt finish time, bounded by
report receipt and, when clocks permit, attempt start. Reports without it use
receipt time. The observation does not change the persisted result replay digest.
Event, lease and billing clocks still belong to the controller.
`TestNodeExecutorReportsExitBeforePublication`,
`TestReportedExitTimePreservesDelayedObservation`,
`TestRetriedAttemptPreservesObservedExitAndOwnStart` and
`TestLocalHostReportsObservedExitToRestartedController` guard that boundary.
`TestProtocolSessionObservesExitBeforeOutputAcknowledgement` holds log
acknowledgement after a real child exits and checks the observation stays fixed.
`TestNodeExitObservationPreservesLegacyCommandBody` checks that older
controllers can still decode a new runner's reports.

`RuntimeConfig.Repositories` carries the machine owner's parsed `runner.repos`.
`NewManagedRunner` checks the captured intake and accepted source locations before
calling its host. Both must project valid sealed remote source admitted by that
list. An empty list admits nothing. Machine-local checkout paths cannot enter a
remote runner. The caller supplies the configured list even when the controller
already authorized the claim. Controller authorization cannot grant use of the
machine owner's credentials.
`TestMachineRunnerAdmitsSealedSourceBeforeHosting` and
`FuzzMachineRunnerRepositoryFence` cover trigger/job execution, URL spellings,
empty lists, changed accepted locations, missing SHA and escaping directories.
The managed native-store fixture supplies the same list at composition.

`RunnerCLI` captures the configured controller URL, credential provider, loop
selection and repository list before calling its service factory. The factory
returns `RuntimeConfig` services. The CLI applies the captured settings and calls
`NewManagedRunner`, so every machine runner uses the repository fence. Factory
edits to its options or returned claimant settings cannot replace that capture.
The factory receives the Actions bearer callback and expiry without the raw
issued capability. `FuzzRunnerCLIEnforcesRepositoryFence` checks these boundaries
against foreign intake and accepted sources and factory mutation.

Runner `version` uses the same registry parser as the execution commands.
Its help, output formats and offline flag come from `clisurface`.

`RunnerCLI` serves the existing metrics address alongside the managed loop.
Binding fails before any claim. Every claim records claimed,
empty or error without changing its returned award or error. Metrics failure
cancels and joins the loop. Runner exit stops and joins the listener, including
the HTTP shutdown worker. An empty address starts no listener.

The local queue obtains a claim-only delegate through `POST /api/v1/tokens`
using verified socket peer authority. It asks for exactly `triggers.claim` and
`nodes.claim`, which expire within seven days; the runner rolls after one. The queue uses the shared HTTP claim client and revokes its
delegate after `Loop.Run` joins all hosts, while the API still serves cleanup.
`ClaimProfile` owns captured account and configured-label policy without holding
a store or issuing credentials. Each HTTP claim rechecks authority, signed local
version policy and placement in the controller. The production store claimant
and hosting adapters are removed.

Launchers use the shared HTTP client and controller handlers for claim and
synchronization. Each request rechecks its service credential and
`claims.launch` authority. Only original awards and exchanged capabilities enter
hosting. Narrowed reporting delegations cannot construct hosts.

The service binder returns `NativeServices`. It supplies source, engine, process,
policy, logging and storage adapters for `nodehost.NewNativeHost`. HTTP hosting
uses the bound `NativeHostStore` adapter. The shared
owner and claimed host own acquisition, lease renewal and joining. Services close
after those hosts return, including partial construction failures. Configured
storage still needs fresh per-attempt bindings in the shared executor.

`nodehost.NewRunInput` captures the request, accepted plan and granted runner for
remote and embedded hosting. The execution run ID can differ from the
original request seal on retry. `runners.ValidateAward` checks that relationship
for hosting, launcher claim and bootstrap exchange. The accepted request must
retain the origin run, team and hash. Persisted rows and authenticated claim
bindings still name the execution run. Hosting uses `ClaimedUnit.HostContext`
for the shared authority deadline.
`RunInput.SealedRequest` contains the captured `runspec.Request` body. Its hash
and run ID are separate fields. `GrantedRunner` is the server-granted identity,
with its own label slice. It reaches both planning and self-claimed work. A node's
selected executable is acquired by the shared host from the accepted engine
identity before SDK execution.

`TestNativeStoreOwnerW8` supplies an HTTP claimant through the local binding
and through `Config.Open`. It runs real C2 claim authentication, shared controller
issuance, the native owner and C1 completion before joining local workers. The remote-award and managed-runner variants use the same native owner, with an
in-process controller transport and tenant standing in for the remote store adapter.
They retain awarded labels and seals across service-binding mutation. The
remote-renewal case blocks acquisition until the shared native keeper renews
again. The remote-cancellation case leaves the live claim unfinished and joins
its service cleanup. Enrollment, version
policy, source entry and unused process services are fixtures. `FuzzStoreQueueClaimAuthorityW8` checks named and
ordered claims against revoked, changed and narrowed credentials, duplicate and
unsupported kinds, and omitted selection. Refusals leave the queue unclaimed.
The direct-award case retains the runner-label mutation regression.
`TestNativeStorePartialBindingW8` covers
both owner and job construction. `FuzzNativeStoreAwardW8` rejects changed fences,
seals, settings and bearers before service construction. These checks do not
establish native placement, SDK-process or deployed Cloud behavior.

Claimed hosting rejects an accepted award without a sealed source location
before acquisition. Cancelling source acquisition joins git and removes its
owned checkout. `TestClaimedRunnerRefusesAwardWithoutSourceBeforeAcquisition`
uses the persisted-input constructor and shared claimed host.
`TestRunnerSourceCancellationJoinsFetchAndRemovesCheckout` blocks a git fetch
through a process fixture and checks cancellation and cleanup. It exercises
Unix process handling without network or controller transport.

The orphan orchestrator trigger host and its claimed-node/pod executor are
removed. Local execution retains its placement and guard helpers in
`orchestrator/hosted_policy.go`. Runner installation uses the selected engine,
with the source mask registry preserved through its in-memory request. The
shared environment builder masks unsplit proxy credentials and consumer-visible
fragments before any process starts. `TestInstallSDKPreservesRegisteredSecretsProperty`
and the runner environment ports cover the former install-output and allow-list
regressions. The retired native fixture does not establish final native placement
coverage. Installed runner commands bind source authorization, private state,
process services and storage to the awarded credential. Cloud plan units stop
after acceptance. Eval units submit the awarded source expansion under their
independent lease. Both share the native acquisition and SDK implementation.

The shared host's orphan pod executor and row-polling dispatcher are removed.
`OwnerHost`, `ClaimedNodeHost` and `NodeExecutor` supply the claim and execution
paths. The local planner remains in `nodehost/plan_document.go`. Generator ports
exercise recorded recovery, nested ancestor order, one evaluation per ancestor,
attempt timeout and a 70-generation lineage through the final executor contract.
The error-masking property covers both outer layers and output publication.
These use scripted process and store boundaries. The remote native-store and
Cloud bootstrap components use the shared HTTP client, controller handlers and
real store for renewal and exchange. A component transport invokes the handlers
without sockets. Production entrypoint probes reach those adapters, while native
SDK-process execution and deployed Cloud placement require the native environment.

The orphan `GCWarmRoot` sweeper and orchestrator repository-admission helpers
are removed. Source projection and machine admission consume sealed source
locations instead of rebuilding them from legacy trigger fields. The source
contract describes a Git-bundle seed but has no separate marker for an uploaded
working-tree snapshot. Final `accept_working_tree` transport and consent remain
an integration obligation. A committed-source test does not close that obligation.

`Loop.OnClaimsStopped` notifies the host owner before pending awards and held
hosts join. The local runner uses it to persist draining independently of host
cleanup, including when a controller outage exhausts claim recovery. Cancellation
of that persistence worker follows host joining, so a permanently absent API
does not hold an idle runner open.
