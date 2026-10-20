package launcher

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

// BootstrapHost exchanges private bootstrap authority before dispatching its Cloud unit.
type BootstrapHost struct {
	ControllerURL string
	HTTP          *http.Client
	NewController func(client.EngineOptions) (controllerwire.Controller, error)
	Host          runners.UnitHost
}

// Run presents a bootstrap once and leaves renewal, source acquisition and execution to Host.
func (h BootstrapHost) Run(ctx context.Context, award CloudJobAward) (result error) {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := validateAward(award); err != nil {
		return err
	}
	if strings.TrimSpace(h.ControllerURL) == "" || h.Host == nil {
		return errors.New("Cloud bootstrap requires a controller URL and unit host")
	}
	award, err := captureAward(award)
	if err != nil {
		return err
	}
	deadline, err := cloudDeadline(time.Now(), award.Binding)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	defer func() { result = errors.Join(result, context.Cause(ctx)) }()
	binding, bootstrap := award.Binding, string(award.Bootstrap)
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
	if !reflect.DeepEqual(unit.Engine, award.Engine) {
		return errors.New("Cloud exchange changed the accepted engine")
	}
	projected, err := cloudSource(unit)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(projected, award.Source) {
		return errors.New("Cloud exchange changed the awarded source")
	}
	if unit.CloudResources == nil || unit.CloudResources.Cores != award.CPUCores || unit.CloudResources.MemoryBytes != award.Memory {
		return errors.New("Cloud exchange changed the committed allocation")
	}
	return h.Host.Host(ctx, unit)
}
