package runners

import (
	"context"
	"errors"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// StoreBinding supplies authenticated, tenant-scoped facts to native service construction.
type StoreBinding struct {
	Tenant    *store.Tenant
	Principal capability.Principal
	Refresh   func(context.Context) (capability.Principal, error)
	Bearer    func(context.Context) (string, error)
}

// NativeServices supplies the per-award host services and their joined cleanup.
type NativeServices struct {
	Config  nodehost.NativeHostConfig
	Storage nodehost.StorageBinding
	Close   func() error
}

// StoreHosting constructs the shared hosts over the controller's existing store handle.
type StoreHosting struct {
	Controller *controller.EngineController
	router     *controller.PolicyRouter
	Bind       func(context.Context, StoreBinding) (NativeServices, error)
}

// Host authenticates before binding services; shared hosts own acquisition, renewal and joining.
func (h StoreHosting) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	if h.Controller == nil || h.Bind == nil {
		return errors.New("store hosting requires the owned store and native services")
	}
	router, err := h.Controller.ClaimPolicyRouter()
	if err != nil {
		return err
	}
	h.router = router
	return hostAward(ctx, unit, h.bind)
}

type storeInputs struct {
	run  nodehost.RunInput
	node nodehost.NodeInput
}

func (h StoreHosting) bind(ctx context.Context, unit, _ controllerwire.ClaimedUnit, input nodehost.RunInput) (NativeServices, storeInputs, error) {
	ctx, principal, err := h.authenticate(ctx, unit)
	if err != nil {
		return NativeServices{}, storeInputs{}, err
	}
	tenant, err := controller.ScopedTenantPort[*store.Tenant](ctx, store.Team(principal.Capability.Team), h.Controller.ResolveTenant)
	if err != nil {
		return NativeServices{}, storeInputs{}, err
	}
	inputs, err := nativeInputs(ctx, tenant, unit, principal, input)
	if err != nil {
		return NativeServices{}, inputs, err
	}
	lifetime, retire := context.WithCancel(context.WithoutCancel(ctx))
	binding := StoreBinding{Tenant: tenant, Principal: principal, Refresh: func(ctx context.Context) (capability.Principal, error) {
		_, p, err := h.authenticate(ctx, unit)
		return p, err
	}, Bearer: func(request context.Context) (string, error) {
		if err := errors.Join(context.Cause(lifetime), context.Cause(request)); err != nil {
			return "", err
		}
		return unit.Capability, nil
	}}
	services, err := h.Bind(ctx, binding)
	closeServices := services.Close
	services.Close = func() error {
		defer retire()
		if closeServices != nil {
			return closeServices()
		}
		return nil
	}
	if services.Config.Store == nil {
		services.Config.Store = tenant
	}
	return services, inputs, err
}

func nativeInputs(ctx context.Context, state nodehost.NativeHostStore, unit controllerwire.ClaimedUnit, principal capability.Principal, input nodehost.RunInput) (storeInputs, error) {
	snapshot, err := state.ReadSnapshot(ctx, unit.RunID)
	if err != nil {
		return storeInputs{}, err
	}
	if !reflect.DeepEqual(snapshot.Run.Request, unit.Request) || (snapshot.Run.Spec == nil) != (unit.Accepted == nil) {
		return storeInputs{}, awardMismatch()
	}
	if snapshot.Run.Spec != nil {
		persisted := unit
		persisted.Accepted = &controllerwire.PlanAccepted{Spec: *snapshot.Run.Spec, Plan: snapshot.Run.Plan, PlanHash: runspec.Hash(snapshot.Run.PlanHash)}
		accepted, err := nodehost.NewRunInput(persisted)
		if err != nil {
			return storeInputs{}, err
		}
		if !reflect.DeepEqual(accepted.Accepted, input.Accepted) {
			return storeInputs{}, awardMismatch()
		}
	}
	var node nodehost.NodeInput
	if unit.Unit == capability.UnitNode {
		row, err := state.ReadNode(ctx, controllerwire.NodeRef{RunID: unit.RunID, NodeID: unit.JobID})
		if err != nil {
			return storeInputs{}, err
		}
		node, err = nodehost.NewNodeInput(*input.Accepted, row, engine.Resolved{Identity: *unit.Engine}, input.GrantedRunner)
		if err != nil {
			return storeInputs{}, err
		}
		if row.Ref != (controllerwire.NodeRef{RunID: unit.RunID, NodeID: unit.JobID}) || row.CapabilityPrefix != principal.Capability.Prefix || row.ClaimedBy != unit.Binding.Holder || row.Generation != runspec.ClaimGeneration(unit.Binding.Generation) || row.Attempt != runspec.AttemptOrdinal(unit.Binding.Attempt) || !reflect.DeepEqual(row.Execution, *unit.Execution) {
			return storeInputs{}, awardMismatch()
		}
	} else if unit.Unit == capability.UnitEval {
		binding := principal.Capability.Binding
		row, err := state.ReadNode(ctx, controllerwire.NodeRef{RunID: unit.RunID, NodeID: unit.JobID})
		if err != nil {
			return storeInputs{}, err
		}
		if row.Generation != runspec.ClaimGeneration(unit.SourceGeneration) || row.Ref != (controllerwire.NodeRef{RunID: unit.RunID, NodeID: unit.JobID}) || row.Status != store.StatusDone || row.Outcome != store.NodeSuccess && row.Outcome != store.NodeMemoized || row.Expansion != store.ExpansionExpected {
			return storeInputs{}, awardMismatch()
		}
		input.Owner = store.Actor{Kind: store.ActorRunHost, Authority: principal.Authority, CapabilityPrefix: principal.Capability.Prefix, Holder: binding.Holder, Generation: runspec.ClaimGeneration(binding.Generation)}
	} else {
		binding := principal.Capability.Binding
		owner := snapshot.Run.Owner
		if owner == nil || owner.Kind != store.OwnerTrigger || owner.RunID != binding.RunID || owner.Holder != binding.Holder || owner.Generation != binding.Generation {
			return storeInputs{}, awardMismatch()
		}
		input.Owner = store.Actor{
			Kind: store.ActorRunHost, Authority: principal.Authority, CapabilityPrefix: principal.Capability.Prefix, Holder: binding.Holder, Generation: runspec.ClaimGeneration(binding.Generation),
			Owner: *owner,
		}
	}
	return storeInputs{run: input, node: node}, nil
}

func (h StoreHosting) authenticate(ctx context.Context, unit controllerwire.ClaimedUnit) (context.Context, capability.Principal, error) {
	if err := context.Cause(ctx); err != nil {
		return ctx, capability.Principal{}, err
	}
	target := capability.Target{Team: unit.Request.Request.Team, RunID: unit.RunID, NodeID: unit.JobID, ClaimID: unit.UnitID}
	ctx, authorized, err := h.router.Authorize(ctx, "POST", "/api/v1/claims/{claim}/heartbeat", capability.Presentation{Raw: unit.Capability, Class: capability.CredentialBearer, Evidence: capability.RequestEvidence{Target: target}})
	if err != nil {
		return ctx, capability.Principal{}, err
	}
	if err := ValidateAwardPrincipal(unit, authorized.Principal); err != nil {
		return ctx, capability.Principal{}, err
	}
	return ctx, authorized.Principal, nil
}

// ValidateAwardPrincipal checks an award against its authenticated persisted authority.
func ValidateAwardPrincipal(unit controllerwire.ClaimedUnit, principal capability.Principal) error {
	if !principalMatches(unit, principal) {
		return awardMismatch()
	}
	return nil
}

func principalMatches(unit controllerwire.ClaimedUnit, principal capability.Principal) bool {
	c := principal.Capability
	binding := c.Binding
	if unit.Unit == capability.UnitNode && unit.Binding.Kind == capability.BindingNode && unit.Binding.Expansion == nil && unit.Binding.Attempt == 0 && binding.Attempt > 0 {
		binding.Attempt = 0
	}
	if binding.ClaimBy != nil && unit.Binding.ClaimBy != nil && binding.ClaimBy.Equal(*unit.Binding.ClaimBy) {
		binding.ClaimBy = unit.Binding.ClaimBy
	}
	if binding.Deadline != nil && unit.Binding.Deadline != nil && binding.Deadline.Equal(*unit.Binding.Deadline) {
		binding.Deadline = unit.Binding.Deadline
	}
	return c.Prefix != "" && principal.Authority == (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}) && (c.Issuance == capability.Award || c.Issuance == capability.Exchange) && c.Team == unit.Request.Request.Team && c.Unit == unit.Unit && c.UnitID == unit.UnitID && c.Purpose == unit.Purpose && c.Delivery == unit.Delivery && reflect.DeepEqual(binding, unit.Binding)
}

func awardMismatch() error {
	return &apierr.Error{Code: apierr.LockHeld, Message: "runner award differs from its authenticated claim"}
}

var _ UnitHost = StoreHosting{}
