package main

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func newControllerServer(st *store.Store, cfg deployment.Config, root, version string, policy *http.Client) (*controller.Server, error) {
	engines, err := engine.NewResolver(engine.Config{BuildVersion: version, CacheDirectory: filepath.Join(root, "engines"), HTTPClient: policy})
	if err != nil {
		return nil, fmt.Errorf("engine version policy: %w", err)
	}
	return controller.NewDeployment(st, cfg, nil).WithEngineController(&controller.EngineController{
		Store:         st,
		RunnerPolicy:  &controller.SignedRunnerAdmission{Resolver: engines},
		VersionPolicy: &controller.SignedEngineVersionPolicy{Resolver: engines},
	}), nil
}
