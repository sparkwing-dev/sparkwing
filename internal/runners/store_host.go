package runners

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// StoreBinding supplies authenticated, tenant-scoped facts to native service construction.
type StoreBinding struct {
	Tenant    *store.Tenant
	Principal capability.Principal
}

// NativeServices supplies the per-award host services and their joined cleanup.
type NativeServices struct {
	Config  nodehost.NativeHostConfig
	Storage nodehost.StorageBinding
	Close   func() error
}

// StoreHosting constructs the shared hosts over the controller's existing store handle.
type StoreHosting struct {
	Store *store.Store
	Bind  func(context.Context, StoreBinding) (NativeServices, error)
}

// Host authenticates before binding services; shared hosts own acquisition, renewal and joining.
func (h StoreHosting) Host(ctx context.Context, unit controllerwire.ClaimedUnit) (result error) {
	if h.Store == nil || h.Bind == nil {
		return errors.New("store hosting requires the owned store and native services")
	}
	if err := validateHostedAward(unit); err != nil {
		return err
	}
	input, err := runInputFor(unit)
	if err != nil {
		return err
	}
	deadline := unit.Binding.Deadline
	for _, bound := range []*time.Time{unit.OwnerDeadline, unit.ParentCapabilityExpiry} {
		if bound != nil && (deadline == nil || bound.Before(*deadline)) {
			deadline = bound
		}
	}
	if deadline != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, *deadline)
		defer cancel()
	}
	inputs, err := h.inputs(ctx, unit, input)
	if err != nil {
		return err
	}
	services, err := h.Bind(ctx, inputs.binding)
	defer func() {
		if services.Close != nil {
			result = errors.Join(result, services.Close())
		}
	}()
	if err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	services.Config.Store = inputs.binding.Tenant
	owner, claimed, _, err := nodehost.NewNativeHost(services.Config)
	if err != nil {
		return err
	}
	if unit.Unit == capability.UnitNode {
		inputs.node.Storage = services.Storage
		return claimed.Execute(ctx, inputs.node)
	}
	inputs.run.Storage = services.Storage
	_, err = owner.Run(ctx, inputs.run)
	return err
}

type storeInputs struct {
	binding StoreBinding
	run     nodehost.RunInput
	node    nodehost.NodeInput
}

func (h StoreHosting) inputs(ctx context.Context, unit controllerwire.ClaimedUnit, input nodehost.RunInput) (storeInputs, error) {
	principal, err := h.authenticate(ctx, unit)
	if err != nil {
		return storeInputs{}, err
	}
	tenant, err := h.Store.ForTeam(ctx, store.Team(principal.Capability.Team))
	if err != nil {
		return storeInputs{}, err
	}
	snapshot, err := tenant.ReadSnapshot(ctx, unit.RunID)
	if err != nil {
		return storeInputs{}, err
	}
	if !reflect.DeepEqual(snapshot.Run.Request, unit.Request) || (snapshot.Run.Spec == nil) != (unit.Accepted == nil) {
		return storeInputs{}, awardMismatch()
	}
	if snapshot.Run.Spec != nil {
		accepted, err := acceptedInput(unit.RunID, controllerwire.PlanAccepted{Spec: *snapshot.Run.Spec, Plan: snapshot.Run.Plan, PlanHash: runspec.Hash(snapshot.Run.PlanHash)})
		if err != nil {
			return storeInputs{}, err
		}
		if !reflect.DeepEqual(accepted, *input.Accepted) {
			return storeInputs{}, awardMismatch()
		}
	}
	var node nodehost.NodeInput
	if unit.Unit == capability.UnitNode {
		row, err := tenant.ReadNode(ctx, store.NodeRef{RunID: unit.RunID, NodeID: unit.JobID})
		if err != nil {
			return storeInputs{}, err
		}
		node, err = nodehost.NewNodeInput(*input.Accepted, row, engine.Resolved{Identity: *unit.Engine}, input.GrantedRunner)
		if err != nil {
			return storeInputs{}, err
		}
		if row.CapabilityPrefix != principal.Capability.Prefix || row.ClaimedBy != unit.Binding.Holder || row.Generation != unit.Binding.Generation || row.Attempt != unit.Binding.Attempt || !reflect.DeepEqual(row.Execution, *unit.Execution) {
			return storeInputs{}, awardMismatch()
		}
	} else {
		binding := principal.Capability.Binding
		owner := snapshot.Run.Owner
		if owner == nil || owner.Kind != store.OwnerTrigger || owner.RunID != binding.RunID || owner.Holder != binding.Holder || owner.Generation != binding.Generation {
			return storeInputs{}, awardMismatch()
		}
		input.Owner = store.Actor{Kind: store.ActorRunHost, Authority: principal.Authority, CapabilityPrefix: principal.Capability.Prefix, Holder: binding.Holder, Generation: binding.Generation,
			Owner: *owner}
	}
	return storeInputs{binding: StoreBinding{Tenant: tenant, Principal: principal}, run: input, node: node}, nil
}

func (h StoreHosting) authenticate(ctx context.Context, unit controllerwire.ClaimedUnit) (capability.Principal, error) {
	if err := context.Cause(ctx); err != nil {
		return capability.Principal{}, err
	}
	pattern := "/api/v1/runs/{run}/heartbeat"
	if unit.Unit == capability.UnitNode {
		pattern = "/api/v1/runs/{run}/jobs/{job}/heartbeat"
	}
	route, ok := capability.LookupRoute("POST", pattern)
	if !ok {
		return capability.Principal{}, errors.New("store hosting renewal policy is unavailable")
	}
	target := capability.Target{Team: unit.Request.Request.Team, RunID: unit.RunID, NodeID: unit.JobID}
	authenticated, err := capability.NewAuthenticator(h.Store).Authenticate(ctx, capability.Presentation{
		Raw: unit.Capability, Class: capability.CredentialBearer, Evidence: capability.RequestEvidence{Target: target},
	}, route)
	if err != nil {
		return capability.Principal{}, err
	}
	authorized, err := capability.Authorize(ctx, h.Store, route, authenticated, target)
	if err != nil {
		return capability.Principal{}, err
	}
	principal := authorized.Principal
	c := principal.Capability
	if (c.Issuance != capability.Award && c.Issuance != capability.Exchange) || c.Team != target.Team || c.Unit != unit.Unit || c.UnitID != unit.UnitID || c.Purpose != unit.Purpose || c.Delivery != unit.Delivery || !reflect.DeepEqual(c.Binding, unit.Binding) {
		return capability.Principal{}, awardMismatch()
	}
	return principal, nil
}

func awardMismatch() error {
	return &apierr.Error{Code: apierr.LockHeld, Message: "runner award differs from its authenticated claim"}
}

var _ UnitHost = StoreHosting{}
