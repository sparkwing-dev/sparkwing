# Runner hosting

`NewManagedRunner` takes a `UnitHost`. `RuntimeConfig.NewController` opens the
claimant transport. The host owns each award's execution lifetime. A local
controller can supply `StoreHosting` with its existing `Binding.Store` handle.
An HTTP placement supplies `Hosting` with its award-specific controller factory,
source preparation and shared host adapters.

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

`TestNativeStoreOwnerW8` runs the managed loop through real C2 authentication,
the native owner and real C1 completion. The claimant transport, source entry and
unused process services are fixtures. `TestNativeStorePartialBindingW8` covers
both owner and job construction. `FuzzNativeStoreAwardW8` rejects changed fences,
seals, settings and bearers before service construction. These checks do not
establish native placement, SDK-process or deployed Cloud behavior.
