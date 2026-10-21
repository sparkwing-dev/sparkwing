package runners

import (
	"context"
	"errors"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// RuntimeConfig composes one authenticated claim loop and its award-bound hosts.
type RuntimeConfig struct {
	Controller    client.EngineOptions
	NewController func(client.EngineOptions) (controllerwire.Controller, error)
	Loop          LoopConfig
	Hosting       UnitHost
	Repositories  sourceurl.RepoAllowlist
	Versions      SoftwarePolicy
	Installation  RunnerInstallation
	Target        releaseasset.Target
	Notices       func([]engine.Notice)
	OnError       func(error)
}

// NewManagedRunner shares the HTTP transport while keeping claimant and award bearers separate.
func NewManagedRunner(ctx context.Context, cfg RuntimeConfig) (*ManagedRunner, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Controller.URL) == "" || cfg.Controller.Bearer == nil || cfg.Controller.Launcher || cfg.Versions == nil || cfg.Target.Binary != releaseasset.SparkwingRunner || cfg.Loop.Version == "" || cfg.Loop.MaxConcurrent < 1 || cfg.Hosting == nil {
		return nil, errors.New("runner composition requires claimant transport, signed policy, runner target and loop settings")
	}
	open := cfg.NewController
	if open == nil {
		open = client.NewEngineClient
	}
	controller, err := open(cfg.Controller)
	if err != nil {
		return nil, err
	}
	if controller == nil {
		return nil, errors.New("runner controller factory returned no client")
	}
	return &ManagedRunner{
		Loop:     &Loop{Config: cfg.Loop, Controller: controller, Host: machineHost{host: cfg.Hosting, repositories: cfg.Repositories}, OnError: cfg.OnError},
		Versions: cfg.Versions, Installation: cfg.Installation, Target: cfg.Target, Notices: cfg.Notices,
	}, nil
}
