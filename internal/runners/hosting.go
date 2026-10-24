package runners

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	award "github.com/sparkwing-dev/sparkwing/internal/runners/award"
)

// Hosting binds remote awards to the shared native hosts and their authenticated store adapter.
type Hosting struct {
	ControllerURL string
	HTTP          *http.Client
	NewController func(client.EngineOptions) (controllerwire.Controller, error)
	Bind          func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NativeServices, capability.Principal, error)
}

// Host confirms live authority before binding services; native hosts own acquisition and renewal.
func (h Hosting) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	if strings.TrimSpace(h.ControllerURL) == "" || h.Bind == nil {
		return errors.New("runner host requires a controller URL and native service binding")
	}
	return HostAward(ctx, unit, h.bind)
}

// HostAward captures and validates an award before entering the shared native host.
func HostAward(ctx context.Context, unit controllerwire.ClaimedUnit, bind func(context.Context, controllerwire.ClaimedUnit, controllerwire.ClaimedUnit, nodehost.RunInput) (nodehost.NativeServices, HostInputs, error)) (result error) {
	if err := award.ValidateAward(unit, capability.DeliveryDirect); err != nil {
		return err
	}
	if unit.Capability == "" {
		return errors.New("runner hosting requires a directly usable award")
	}
	captured, err := captureUnit(unit)
	if err != nil {
		return err
	}
	input, err := nodehost.NewRunInput(captured)
	if err != nil {
		return err
	}
	ctx, cancel := captured.HostContext(ctx)
	defer cancel()
	defer func() { result = errors.Join(result, context.Cause(ctx)) }()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	services, inputs, err := bind(ctx, captured, unit, input)
	defer func() {
		if services.Close != nil {
			result = errors.Join(result, services.Close())
		}
	}()
	if err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	owner, claimed, _, err := nodehost.NewNativeHost(services.Config)
	if err != nil {
		return err
	}
	if captured.Unit == capability.UnitNode {
		inputs.Node.Storage = services.Storage
		return claimed.Execute(ctx, inputs.Node)
	}
	inputs.Run.Storage = services.Storage
	if captured.Unit == capability.UnitPlan {
		_, err = owner.Plan(ctx, inputs.Run)
		return err
	}
	if captured.Unit == capability.UnitEval {
		return owner.Evaluate(ctx, inputs.Run, captured.JobID, captured.SourceGeneration, *captured.Binding.Expansion)
	}
	_, err = owner.Run(ctx, inputs.Run)
	return err
}

func captureUnit(unit controllerwire.ClaimedUnit) (controllerwire.ClaimedUnit, error) {
	data, err := json.Marshal(unit)
	var captured controllerwire.ClaimedUnit
	if err == nil {
		err = json.Unmarshal(data, &captured)
	}
	return captured, err
}

func (h Hosting) bind(ctx context.Context, unit, original controllerwire.ClaimedUnit, input nodehost.RunInput) (nodehost.NativeServices, HostInputs, error) {
	controller, err := h.controller(unit.Capability)
	if err != nil {
		return nodehost.NativeServices{}, HostInputs{}, err
	}
	beat, err := controller.Renew(ctx, unit.UnitID, controllerwire.RenewalRequest{})
	if err != nil {
		return nodehost.NativeServices{}, HostInputs{}, err
	}
	if beat.CancelRequested {
		return nodehost.NativeServices{}, HostInputs{}, &lease.CancelError{Reason: beat.CancelReason}
	}
	if err := context.Cause(ctx); err != nil {
		return nodehost.NativeServices{}, HostInputs{}, err
	}
	services, principal, err := h.Bind(ctx, original, controller)
	if err == nil {
		err = context.Cause(ctx)
	}
	if err != nil {
		return services, HostInputs{}, err
	}
	if !principalMatches(unit, principal) || services.Config.Store == nil {
		return services, HostInputs{}, awardMismatch()
	}
	inputs, err := NativeInputs(ctx, services.Config.Store, unit, principal, input)
	return services, inputs, err
}

func (h Hosting) controller(bearer string) (controllerwire.Controller, error) {
	open := h.NewController
	if open == nil {
		open = client.NewEngineClient
	}
	controller, err := open(client.EngineOptions{URL: h.ControllerURL, HTTP: h.HTTP, Bearer: func(ctx context.Context) (string, error) {
		if err := context.Cause(ctx); err != nil {
			return "", err
		}
		return bearer, nil
	}})
	if err != nil {
		return nil, err
	}
	if controller == nil {
		return nil, errors.New("runner controller factory returned no client")
	}
	return controller, nil
}

var _ UnitHost = Hosting{}
