# Runner hosting

`NewManagedRunner` takes a `UnitHost`. `RuntimeConfig.NewController` opens the
claimant transport. The host owns each award's execution lifetime. An embedded
controller uses `NewStoreRunner` with its existing `EngineController` and store.
The local controller constructs that queue in `Config.Open` from its service
binder's `StoreRunnerConfig`.
An HTTP placement supplies `Hosting` with its award-specific controller factory,
source preparation and shared host adapters.

`RunnerCLI` serves the existing metrics address alongside its bound factory's
managed loop. Binding fails before any claim. Every claim records claimed,
empty or error without changing its returned award or error. Metrics failure
cancels and joins the loop. Runner exit stops and joins the listener, including
the HTTP shutdown worker. An empty address starts no listener.

`StoreRunnerConfig` supplies the claimant bearer provider, loop settings and
native service binder. The queue authenticates each claim through C2 and checks
every requested kind before calling the shared controller. Named trigger claims
use their run's ownership and named route. Other claims use the ordered runner
route. The controller still owns enrollment, signed version admission, matching
and issuance.
`StoreController` supplies the embedded policy adapter for both runners and
launchers. Launcher requests select the launcher policy and still require
`claims.launch` from a service credential. Synchronization reauthenticates before
calling the shared controller's persisted cleanup reader. Revocation and narrowed
grants take effect on the next request.

`StoreHosting` authenticates and authorizes the presented award through C2's
heartbeat policy before binding native services. It selects the authenticated
tenant, checks both persisted seals, and builds owner authority from the persisted
owner lease. Only original awards and exchanged capabilities enter hosting.
Narrowed reporting delegations cannot construct hosts. Jobs use
`nodehost.NewNodeInput` with the persisted row. The granted
runner and request bytes are captured before any service callback runs.

The service binder returns `NativeServices`. It supplies source, engine, process,
policy, logging and storage adapters for `nodehost.NewNativeHost`. The constructor
always receives the authenticated tenant over the controller's store. The shared
owner and claimed host own acquisition, lease renewal and joining. Services close
after those hosts return, including partial construction failures. Configured
storage still needs fresh per-attempt bindings in the shared executor.

`RunInput.SealedRequest` contains the captured `runspec.Request` body. Its hash
and run ID are separate fields. `GrantedRunner` is the server-granted identity,
with its own label slice. It reaches both planning and self-claimed work. A node's
selected executable is acquired by the shared host from the accepted engine
identity before SDK execution.

`TestNativeStoreOwnerW8` constructs the embedded queue through the local binding
and through `Config.Open`. It runs real C2 claim authentication, shared controller
issuance, the native owner and C1 completion before joining local workers. Enrollment, version policy, source entry and unused
process services are fixtures. `FuzzStoreQueueClaimAuthorityW8` checks named and
ordered claims against revoked, changed and narrowed credentials, duplicate and
unsupported kinds, and omitted selection. Refusals leave the queue unclaimed.
The direct-award case retains the runner-label mutation regression.
`TestNativeStorePartialBindingW8` covers
both owner and job construction. `FuzzNativeStoreAwardW8` rejects changed fences,
seals, settings and bearers before service construction. These checks do not
establish native placement, SDK-process or deployed Cloud behavior.

<!-- design:begin -->

The node-duration histogram still needs a committed-outcome observation from
the host integration. A host method's returned error does not identify the
terminal job verdict, so claim metrics do not manufacture that observation.

<!-- design:end -->
