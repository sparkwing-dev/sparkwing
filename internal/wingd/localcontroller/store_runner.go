package localcontroller

import (
	"context"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

// StoreRunnerConfig binds an embedded claimant and native hosts to the controller's store.
type StoreRunnerConfig struct {
	Bearer  func(context.Context) (string, error)
	Loop    runners.LoopConfig
	Bind    func(context.Context, StoreBinding) (nodehost.NativeServices, error)
	OnError func(error)
}

// NewStoreRunner constructs the shared queue without a transport or another store opener.
func NewStoreRunner(engine *controller.EngineController, cfg StoreRunnerConfig) (*runners.Loop, error) {
	if engine == nil || cfg.Bearer == nil || cfg.Bind == nil || engine.ResolveTenant == nil || engine.RunnerPolicy == nil {
		return nil, errors.New("embedded runner requires the owned controller, claimant, signed policy and native services")
	}
	if cfg.Loop.Version == "" || cfg.Loop.MaxConcurrent < 1 {
		return nil, errors.New("embedded runner requires its release version and positive concurrency")
	}
	cfg.Loop.Labels = slices.Clone(cfg.Loop.Labels)
	cfg.Loop.Kinds = slices.Clone(cfg.Loop.Kinds)
	router, err := engine.ClaimPolicyRouter()
	if err != nil {
		return nil, err
	}
	client := storeController{Engine: engine, Bearer: cfg.Bearer, Router: router}
	loop := &runners.Loop{
		Config: cfg.Loop, Controller: client,
		Host:    StoreHosting{Controller: engine, Bind: cfg.Bind},
		OnError: cfg.OnError,
	}
	loop.CheckVersion = func(ctx context.Context, version string) error {
		return client.checkVersion(ctx, version, loop.Config.Kinds)
	}
	return loop, nil
}

// storeController applies shared route policy to the embedded local queue.
type storeController struct {
	Router *routepolicy.Router
	Engine *controller.EngineController
	Bearer func(context.Context) (string, error)
}

func (c storeController) checkVersion(ctx context.Context, version string, kinds []capability.UnitKind) error {
	ctx, authorized, err := c.authorize(ctx, "/api/v1/runners/claim", capability.Target{ClaimKinds: slices.Clone(kinds)})
	if err != nil {
		return err
	}
	_, err = c.Engine.CheckRunner(ctx, authorized.Principal.Authority, version)
	return err
}

// Claim authorizes every selected kind before the controller issues an award.
func (c storeController) Claim(ctx context.Context, body controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	pattern := "/api/v1/runners/claim"
	if body.RunID != "" && body.JobID == "" && len(body.Kinds) == 1 && body.Kinds[0] == capability.UnitTrigger {
		pattern = "/api/v1/runs/{run}/claim"
	}
	target := capability.Target{RunID: body.RunID, NodeID: body.JobID, ClaimKinds: slices.Clone(body.Kinds)}
	ctx, authorized, err := c.authorize(ctx, pattern, target)
	if err != nil {
		return controllerwire.ClaimResponse{}, err
	}
	return c.Engine.Claim(ctx, authorized, body)
}

func (c storeController) authorize(ctx context.Context, pattern string, target capability.Target) (context.Context, capability.Authorized, error) {
	if err := context.Cause(ctx); err != nil {
		return ctx, capability.Authorized{}, err
	}
	if c.Engine == nil || c.Bearer == nil {
		return ctx, capability.Authorized{}, errors.New("embedded controller requires the owned store and claimant")
	}
	raw, err := c.Bearer(ctx)
	if err != nil {
		return ctx, capability.Authorized{}, err
	}
	if c.Router == nil {
		return ctx, capability.Authorized{}, errors.New("embedded controller requires the policy router")
	}
	return c.Router.Authorize(ctx, "POST", pattern, capability.Presentation{Raw: raw, Class: capability.CredentialBearer, Evidence: capability.RequestEvidence{Target: target}})
}
