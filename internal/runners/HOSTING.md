# Runner hosting

`NewManagedRunner` takes a `UnitHost`. `RuntimeConfig.NewController` opens the
claimant transport. The host owns each award's execution lifetime. An embedded
controller uses `NewStoreRunner` with its existing `EngineController` and store.
The local controller constructs that queue in `Config.Open` from its service
binder's `StoreRunnerConfig`.
An HTTP placement supplies `Hosting` with its award-specific controller factory
and native service binder. The binder returns `NativeServices` with an authenticated
`NativeHostStore` and the current C2 principal. It must derive that principal from
the authenticated transport. Decoding the opaque bearer does not establish authority.
`Hosting` confirms renewal before binding and captures the wire award before callbacks.
Both placements share award capture, deadlines, partial-service cleanup,
persisted-input checks and `nodehost.NewNativeHost`.
The native owner and claimed hosts own source acquisition and their only lease keeper.

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

`RunnerCLI` serves the existing metrics address alongside the managed loop.
Binding fails before any claim. Every claim records claimed,
empty or error without changing its returned award or error. Metrics failure
cancels and joins the loop. Runner exit stops and joins the listener, including
the HTTP shutdown worker. An empty address starts no listener.

`StoreRunnerConfig` supplies the claimant bearer provider, loop settings and
native service binder. The queue authenticates each claim through C2 and checks
every requested kind before calling the shared controller. Named trigger claims
use their run's ownership and named route. Other claims use the ordered runner
route. The controller still owns enrollment, signed version admission, matching
and issuance.
`StoreController` supplies the embedded local queue's policy adapter. Launchers
use the shared HTTP client and controller handlers for claim and synchronization.
Each request rechecks its service credential and `claims.launch` authority.

`StoreHosting` authenticates and authorizes the presented award through C2's
heartbeat policy before binding native services. It selects the authenticated
tenant, checks both persisted seals, and builds owner authority from the persisted
owner lease. Only original awards and exchanged capabilities enter hosting.
Narrowed reporting delegations cannot construct hosts. Jobs use
`nodehost.NewNodeInput` with the persisted row. The granted
runner and request bytes are captured before any service callback runs.

The service binder returns `NativeServices`. It supplies source, engine, process,
policy, logging and storage adapters for `nodehost.NewNativeHost`. Embedded hosting
overrides the constructor's store with the authenticated tenant. Remote hosting
uses the bound `NativeHostStore` adapter. The shared
owner and claimed host own acquisition, lease renewal and joining. Services close
after those hosts return, including partial construction failures. Configured
storage still needs fresh per-attempt bindings in the shared executor.

`RunInputForAward` captures the request, accepted plan and granted runner for
remote, embedded and CLI here hosting. The execution run ID can differ from the
original request seal on retry. Persisted rows and authenticated claim bindings
still name the execution run.
`RunInput.SealedRequest` contains the captured `runspec.Request` body. Its hash
and run ID are separate fields. `GrantedRunner` is the server-granted identity,
with its own label slice. It reaches both planning and self-claimed work. A node's
selected executable is acquired by the shared host from the accepted engine
identity before SDK execution.

`TestNativeStoreOwnerW8` constructs the embedded queue through the local binding
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
coverage. Production source authorization, service factories and Cloud plan/eval
hosting remain integration work.

The shared host's orphan pod executor and row-polling dispatcher are removed.
`OwnerHost`, `ClaimedNodeHost` and `NodeExecutor` supply the claim and execution
paths. The local planner remains in `nodehost/plan_document.go`. Generator ports
exercise recorded recovery, nested ancestor order, one evaluation per ancestor,
attempt timeout and a 70-generation lineage through the final executor contract.
The error-masking property covers both outer layers and output publication.
These use scripted process and store boundaries. The remote native-store and
Cloud bootstrap components use the shared HTTP client, controller handlers and
real store for renewal and exchange. A component transport invokes the handlers
without sockets. They do not establish native Cloud plan/eval hosting or deployed
placement.

The orphan `GCWarmRoot` sweeper and orchestrator repository-admission helpers
are removed. Source projection and machine admission consume sealed source
locations instead of rebuilding them from legacy trigger fields. The source
contract describes a Git-bundle seed but has no separate marker for an uploaded
working-tree snapshot. Final `accept_working_tree` transport and consent remain
an integration obligation. A committed-source test does not close that obligation.
