package runners

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

// Hosting binds remote awards to the shared native hosts and their authenticated store adapter.
type Hosting struct {
	ControllerURL string
	HTTP          *http.Client
	NewController func(client.EngineOptions) (controllerwire.Controller, error)
	Bind          func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (NativeServices, capability.Principal, error)
}

// Host confirms live authority before binding services; native hosts own acquisition and renewal.
func (h Hosting) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	if strings.TrimSpace(h.ControllerURL) == "" || h.Bind == nil {
		return errors.New("runner host requires a controller URL and native service binding")
	}
	return hostAward(ctx, unit, h.bind)
}

func hostAward(ctx context.Context, unit controllerwire.ClaimedUnit, bind func(context.Context, controllerwire.ClaimedUnit, controllerwire.ClaimedUnit, nodehost.RunInput) (NativeServices, storeInputs, error)) (result error) {
	if err := validateHostedAward(unit); err != nil {
		return err
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
		inputs.node.Storage = services.Storage
		return claimed.Execute(ctx, inputs.node)
	}
	inputs.run.Storage = services.Storage
	_, err = owner.Run(ctx, inputs.run)
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

func (h Hosting) bind(ctx context.Context, unit, original controllerwire.ClaimedUnit, input nodehost.RunInput) (NativeServices, storeInputs, error) {
	controller, err := h.controller(unit.Capability)
	if err != nil {
		return NativeServices{}, storeInputs{}, err
	}
	beat, err := controller.Renew(ctx, unit.UnitID, controllerwire.RenewalRequest{})
	if err != nil {
		return NativeServices{}, storeInputs{}, err
	}
	if beat.CancelRequested {
		return NativeServices{}, storeInputs{}, &lease.CancelError{Reason: beat.CancelReason}
	}
	if err := context.Cause(ctx); err != nil {
		return NativeServices{}, storeInputs{}, err
	}
	services, principal, err := h.Bind(ctx, original, controller)
	if err == nil {
		err = context.Cause(ctx)
	}
	if err != nil {
		return services, storeInputs{}, err
	}
	if !principalMatches(unit, principal) || services.Config.Store == nil {
		return services, storeInputs{}, awardMismatch()
	}
	inputs, err := nativeInputs(ctx, services.Config.Store, unit, principal, input)
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

func validateHostedAward(unit controllerwire.ClaimedUnit) error {
	if err := ValidateAwardIdentity(unit); err != nil {
		return err
	}
	if err := validateUnit(unit); err != nil {
		return err
	}
	if unit.Delivery != capability.DeliveryDirect {
		return errors.New("runner hosting requires a directly usable award")
	}
	binding := unit.Binding
	if binding.Holder == "" || binding.Generation <= 0 {
		return errors.New("runner award has an invalid run or claim fence")
	}
	if unit.Unit == capability.UnitNode {
		if unit.Purpose != capability.PurposeWork || binding.Kind != capability.BindingNode || unit.JobID == "" || binding.NodeID != unit.JobID || binding.Attempt < 0 {
			return errors.New("runner award has an invalid job or attempt binding")
		}
		if unit.Engine == nil || unit.Accepted == nil || unit.Execution == nil || unit.Accepted.Spec.Resolved.Engine != *unit.Engine {
			return errors.New("runner award is missing its matching accepted engine, execution settings and run")
		}
		if unit.Runner.Name == "" || unit.Runner.Type == "" {
			return errors.New("runner award is missing its granted runner identity")
		}
	} else if (unit.Purpose != capability.PurposeTrigger && unit.Purpose != capability.PurposeHere) || binding.Kind != capability.BindingTrigger || unit.JobID != "" || binding.NodeID != "" {
		return errors.New("runner award has an invalid trigger binding")
	}
	if unit.LeaseExpiresAt.IsZero() {
		return errors.New("runner award is missing its lease expiry")
	}
	return nil
}

var _ UnitHost = Hosting{}
