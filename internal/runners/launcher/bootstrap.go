package launcher

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

// BootstrapHost exchanges private bootstrap authority before dispatching its Cloud unit.
type BootstrapHost struct {
	ControllerURL string
	HTTP          *http.Client
	NewController func(client.EngineOptions) (controllerwire.Controller, error)
	Host          runners.UnitHost
}

// Run presents a bootstrap once and leaves renewal, source acquisition and execution to Host.
func (h BootstrapHost) Run(ctx context.Context, award CloudJobAward) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := validateAward(award); err != nil {
		return err
	}
	if strings.TrimSpace(h.ControllerURL) == "" || h.Host == nil {
		return errors.New("Cloud bootstrap requires a controller URL and unit host")
	}
	binding, bootstrap := award.Binding, string(award.Bootstrap)
	var identity *engine.Identity
	if award.Engine != nil {
		value := *award.Engine
		identity = &value
	}
	open := h.NewController
	if open == nil {
		open = client.NewEngineClient
	}
	controller, err := open(client.EngineOptions{URL: h.ControllerURL, HTTP: h.HTTP, Bearer: func(ctx context.Context) (string, error) {
		if err := context.Cause(ctx); err != nil {
			return "", err
		}
		return bootstrap, nil
	}})
	if err != nil {
		return err
	}
	if controller == nil {
		return errors.New("Cloud bootstrap controller factory returned no client")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	response, err := controller.Exchange(ctx)
	if err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	unit := response.Claim
	exchanged, err := cloudBinding(unit, capability.DeliveryDirect)
	if err != nil {
		return err
	}
	if !sameBinding(binding, exchanged) || unit.Capability == "" || unit.Capability == bootstrap || unit.LeaseExpiresAt.IsZero() {
		return errors.New("Cloud exchange returned different binding or unusable authority")
	}
	if identity != nil && (unit.Engine == nil || *unit.Engine != *identity) {
		return errors.New("Cloud exchange changed the accepted engine")
	}
	if binding.Kind != CloudPlan && (unit.Accepted == nil || unit.Engine == nil || unit.Accepted.Spec.Request.RunID != binding.RunID || unit.Accepted.Spec.Request.Request.Team != binding.Team || unit.Accepted.Spec.Resolved.Engine != *unit.Engine) {
		return errors.New("Cloud exchange is missing its matching accepted run")
	}
	if unit.Execution == nil || !unit.Execution.Resources.Set || !unit.Execution.Resources.Value.Cores.Set || !unit.Execution.Resources.Value.MemoryBytes.Set || unit.Execution.Resources.Value.Cores.Value != award.CPUCores || unit.Execution.Resources.Value.MemoryBytes.Value != award.Memory {
		return errors.New("Cloud exchange changed awarded execution resources")
	}
	return errors.Join(h.Host.Host(ctx, unit), context.Cause(ctx))
}
