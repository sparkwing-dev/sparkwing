package localcontroller

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// StoreBinding supplies authenticated, tenant-scoped facts to native service construction.
type StoreBinding struct {
	Tenant    *store.Tenant
	Principal capability.Principal
	Refresh   func(context.Context) (capability.Principal, error)
	Bearer    func(context.Context) (string, error)
}

// StoreHosting constructs the shared hosts over the controller's existing store handle.
type StoreHosting struct {
	Controller *controller.EngineController
	router     *routepolicy.Router
	Bind       func(context.Context, StoreBinding) (nodehost.NativeServices, error)
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
	return runners.HostAward(ctx, unit, h.bind)
}

func (h StoreHosting) bind(ctx context.Context, unit, _ controllerwire.ClaimedUnit, input nodehost.RunInput) (nodehost.NativeServices, runners.HostInputs, error) {
	ctx, principal, err := h.authenticate(ctx, unit)
	if err != nil {
		return nodehost.NativeServices{}, runners.HostInputs{}, err
	}
	tenant, err := h.Controller.RunnerTenant(ctx, store.Team(principal.Capability.Team))
	if err != nil {
		return nodehost.NativeServices{}, runners.HostInputs{}, err
	}
	inputs, err := runners.NativeInputs(ctx, tenant, unit, principal, input)
	if err != nil {
		return nodehost.NativeServices{}, inputs, err
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

func (h StoreHosting) authenticate(ctx context.Context, unit controllerwire.ClaimedUnit) (context.Context, capability.Principal, error) {
	if err := context.Cause(ctx); err != nil {
		return ctx, capability.Principal{}, err
	}
	target := capability.Target{Team: unit.Request.Request.Team, RunID: unit.RunID, NodeID: unit.JobID, ClaimID: unit.UnitID}
	ctx, authorized, err := h.router.Authorize(ctx, "POST", "/api/v1/claims/{claim}/heartbeat", capability.Presentation{Raw: unit.Capability, Class: capability.CredentialBearer, Evidence: capability.RequestEvidence{Target: target}})
	if err != nil {
		return ctx, capability.Principal{}, err
	}
	if err := runners.ValidateAwardPrincipal(unit, authorized.Principal); err != nil {
		return ctx, capability.Principal{}, err
	}
	return ctx, authorized.Principal, nil
}
