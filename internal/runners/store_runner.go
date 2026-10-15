package runners

import (
	"context"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

// StoreRunnerConfig binds an embedded claimant and native hosts to the controller's store.
type StoreRunnerConfig struct {
	Bearer          func(context.Context) (string, error)
	Loop            LoopConfig
	Bind            func(context.Context, StoreBinding) (NativeServices, error)
	RequestCapacity func() (float64, int64)
	OnError         func(error)
}

// NewStoreRunner constructs the shared queue without a transport or another store opener.
func NewStoreRunner(engine *controller.EngineController, cfg StoreRunnerConfig) (*Loop, error) {
	if engine == nil || engine.Store == nil || engine.RunnerPolicy == nil || cfg.Bearer == nil || cfg.Bind == nil {
		return nil, errors.New("embedded runner requires the owned controller, claimant, signed policy and native services")
	}
	if cfg.Loop.Version == "" || cfg.Loop.MaxConcurrent < 1 {
		return nil, errors.New("embedded runner requires its release version and positive concurrency")
	}
	cfg.Loop.Labels = slices.Clone(cfg.Loop.Labels)
	cfg.Loop.Kinds = slices.Clone(cfg.Loop.Kinds)
	return &Loop{
		Config: cfg.Loop, Controller: storeClaimant{engine: engine, bearer: cfg.Bearer},
		Host: StoreHosting{Store: engine.Store, Bind: cfg.Bind}, CheckVersion: engine.RunnerPolicy.CheckRunner,
		RequestCapacity: cfg.RequestCapacity, OnError: cfg.OnError,
	}, nil
}

type storeClaimant struct {
	engine *controller.EngineController
	bearer func(context.Context) (string, error)
}

func (c storeClaimant) Claim(ctx context.Context, body controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	if err := context.Cause(ctx); err != nil {
		return controllerwire.ClaimResponse{}, err
	}
	raw, err := c.bearer(ctx)
	if err != nil {
		return controllerwire.ClaimResponse{}, err
	}
	pattern := "/api/v1/runners/claim"
	if body.RunID != "" && body.JobID == "" && len(body.Kinds) == 1 && body.Kinds[0] == capability.UnitTrigger {
		pattern = "/api/v1/runs/{run}/claim"
	}
	route, ok := capability.LookupRoute("POST", pattern)
	if !ok {
		return controllerwire.ClaimResponse{}, errors.New("embedded runner claim policy is unavailable")
	}
	target := capability.Target{RunID: body.RunID, NodeID: body.JobID, ClaimKinds: slices.Clone(body.Kinds)}
	authenticated, err := capability.NewAuthenticator(c.engine.Store).Authenticate(ctx, capability.Presentation{
		Raw: raw, Class: capability.CredentialBearer, Evidence: capability.RequestEvidence{Target: target},
	}, route)
	if err != nil {
		return controllerwire.ClaimResponse{}, err
	}
	target.Team = authenticated.Principal.Capability.Team
	if pattern == "/api/v1/runs/{run}/claim" {
		team, err := c.engine.Store.AsOperator().RunTeam(ctx, body.RunID)
		if err != nil {
			return controllerwire.ClaimResponse{}, err
		}
		target.Team = string(team)
	}
	authorized, err := capability.Authorize(ctx, c.engine.Store, route, authenticated, target)
	if err != nil {
		return controllerwire.ClaimResponse{}, err
	}
	return c.engine.Claim(ctx, authorized, body)
}
