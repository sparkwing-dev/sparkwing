package runners

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

// RuntimeConfig composes one authenticated claim loop and its award-bound hosts.
type RuntimeConfig struct {
	Controller      client.EngineOptions
	Loop            LoopConfig
	Hosting         Hosting
	Versions        SoftwarePolicy
	Installation    RunnerInstallation
	Target          releaseasset.Target
	Notices         func([]engine.Notice)
	RequestCapacity func() (float64, int64)
	OnError         func(error)
}

// NewManagedRunner shares the HTTP transport while keeping claimant and award bearers separate.
func NewManagedRunner(ctx context.Context, cfg RuntimeConfig) (*ManagedRunner, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Controller.URL) == "" || cfg.Controller.Bearer == nil || cfg.Controller.Launcher || cfg.Versions == nil || cfg.Target.Binary != releaseasset.SparkwingRunner || cfg.Loop.Version == "" || cfg.Loop.MaxConcurrent < 1 {
		return nil, errors.New("runner composition requires claimant transport, signed policy, runner target and loop settings")
	}
	host := cfg.Hosting
	kinds := cfg.Loop.Kinds
	if kinds == nil {
		kinds = []capability.UnitKind{capability.UnitTrigger}
		if cfg.Loop.RunID == "" {
			kinds = append(kinds, capability.UnitNode)
		}
	}
	if (slices.Contains(kinds, capability.UnitTrigger) && host.PrepareRun == nil) || (slices.Contains(kinds, capability.UnitNode) && host.PrepareNode == nil) {
		return nil, errors.New("runner composition requires the shared hosts for its requested unit kinds")
	}
	if err := limits.CheckRenewal(host.Interval, host.Lease); err != nil {
		return nil, err
	}
	host.ControllerURL, host.HTTP = cfg.Controller.URL, cfg.Controller.HTTP
	open := host.NewController
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
		Loop:     &Loop{Config: cfg.Loop, Controller: controller, Host: host, RequestCapacity: cfg.RequestCapacity, OnError: cfg.OnError},
		Versions: cfg.Versions, Installation: cfg.Installation, Target: cfg.Target, Notices: cfg.Notices,
	}, nil
}
