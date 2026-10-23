package main

import (
	"fmt"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func newControllerServer(st *store.Store, cfg deployment.Config, root, version string, policy engine.Config) (*controller.Server, error) {
	policy.BuildVersion, policy.CacheDirectory = version, filepath.Join(root, "engines")
	engines, err := engine.NewResolver(policy)
	if err != nil {
		return nil, fmt.Errorf("engine version policy: %w", err)
	}
	return controller.NewDeployment(st, cfg, nil).WithEngineController(controller.NewEngineController(st, controller.EngineController{
		RunnerPolicy:  &controller.SignedRunnerAdmission{Resolver: engines},
		VersionPolicy: &controller.SignedEngineVersionPolicy{Resolver: engines},
	})), nil
}
