package launcher

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

// RuntimeConfig joins the launcher transport, Kubernetes client and enforced Cloud boundary.
type RuntimeConfig struct {
	Kube          kubernetes.Interface
	Boundary      CloudBoundary
	Jobs          CloudJobConfig
	Demand        controllerwire.ClaimRequest
	HTTP          *http.Client
	Bearer        func(context.Context) (string, error)
	NewController func(client.EngineOptions) (controllerwire.Controller, error)
	Poll          time.Duration
	Logger        *slog.Logger
}

// NewLauncher selects the launcher route independently of requested unit kinds.
func NewLauncher(ctx context.Context, cfg RuntimeConfig) (*Launcher, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if err := cfg.Jobs.Validate(); err != nil {
		return nil, err
	}
	if cfg.Kube == nil || cfg.Boundary == nil || cfg.Bearer == nil {
		return nil, errors.New("launcher composition requires Kubernetes, enforced boundary and claimant transport")
	}
	open := cfg.NewController
	if open == nil {
		open = client.NewEngineClient
	}
	wire, err := open(client.EngineOptions{URL: cfg.Jobs.ControllerURL, HTTP: cfg.HTTP, Bearer: cfg.Bearer, Launcher: true})
	if err != nil {
		return nil, err
	}
	control, err := NewControllerControl(wire, cfg.Demand)
	if err != nil {
		return nil, err
	}
	return &Launcher{Kube: cfg.Kube, Boundary: cfg.Boundary, Control: control, Config: cfg.Jobs, Poll: cfg.Poll, Logger: cfg.Logger}, nil
}
