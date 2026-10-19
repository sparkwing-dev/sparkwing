package main

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func newControllerServer(st *store.Store, root, version string, policy *http.Client) (*controller.Server, error) {
	engines, err := engine.NewResolver(engine.Config{BuildVersion: version, CacheDirectory: filepath.Join(root, "engines"), HTTPClient: policy})
	if err != nil {
		return nil, fmt.Errorf("engine version policy: %w", err)
	}
	return controller.New(st, nil).WithEngineController(&controller.EngineController{
		Store:         st,
		RunnerPolicy:  &controller.SignedRunnerAdmission{Resolver: engines},
		VersionPolicy: &controller.SignedEngineVersionPolicy{Resolver: engines},
	}), nil
}
