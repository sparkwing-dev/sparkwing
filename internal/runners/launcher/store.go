package launcher

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

// StoreRuntimeConfig binds the launcher to an existing controller and its store.
type StoreRuntimeConfig struct {
	Kube     kubernetes.Interface
	Boundary CloudBoundary
	Jobs     CloudJobConfig
	Demand   controllerwire.ClaimRequest
	Bearer   func(context.Context) (string, error)
	Poll     time.Duration
	Logger   *slog.Logger
}

// NewStoreLauncher shares controller authorization and issuance without another transport or store.
func NewStoreLauncher(ctx context.Context, engine *controller.EngineController, cfg StoreRuntimeConfig) (*Launcher, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if err := cfg.Jobs.Validate(); err != nil {
		return nil, err
	}
	if engine == nil || engine.Store == nil || engine.RunnerPolicy == nil || cfg.Kube == nil || cfg.Boundary == nil || cfg.Bearer == nil {
		return nil, errors.New("embedded launcher requires the owned controller, signed policy, Kubernetes, boundary and claimant")
	}
	control, err := NewControllerControl(runners.StoreController{Engine: engine, Bearer: cfg.Bearer, Launcher: true}, cfg.Demand)
	if err != nil {
		return nil, err
	}
	return &Launcher{Kube: cfg.Kube, Boundary: cfg.Boundary, Control: control, Config: cfg.Jobs, Poll: cfg.Poll, Logger: cfg.Logger}, nil
}
