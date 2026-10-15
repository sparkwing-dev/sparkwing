package runners

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

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
func (h Hosting) Host(ctx context.Context, unit controllerwire.ClaimedUnit) (result error) {
	if err := validateHostedAward(unit); err != nil {
		return err
	}
	if strings.TrimSpace(h.ControllerURL) == "" || h.Bind == nil {
		return errors.New("runner host requires a controller URL and native service binding")
	}
	data, err := json.Marshal(unit)
	if err != nil {
		return err
	}
	var captured controllerwire.ClaimedUnit
	if err := json.Unmarshal(data, &captured); err != nil {
		return err
	}
	input, err := runInputFor(captured)
	if err != nil {
		return err
	}
	deadline := captured.Binding.Deadline
	for _, bound := range []*time.Time{captured.OwnerDeadline, captured.ParentCapabilityExpiry} {
		if bound != nil && (deadline == nil || bound.Before(*deadline)) {
			deadline = bound
		}
	}
	if deadline != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, *deadline)
		defer cancel()
	}
	defer func() { result = errors.Join(result, context.Cause(ctx)) }()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	controller, err := h.controller(captured.Capability)
	if err != nil {
		return err
	}
	beat, err := controller.Renew(ctx, captured.UnitID, controllerwire.RenewalRequest{})
	if err != nil {
		return err
	}
	if beat.CancelRequested {
		return &lease.CancelError{Reason: beat.CancelReason}
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	services, principal, err := h.Bind(ctx, unit, controller)
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
	if !principalMatches(captured, principal) || services.Config.Store == nil {
		return awardMismatch()
	}
	inputs, err := nativeInputs(ctx, services.Config.Store, captured, principal, input)
	if err != nil {
		return err
	}
	return hostNative(ctx, captured, services, inputs)
}

func hostNative(ctx context.Context, unit controllerwire.ClaimedUnit, services NativeServices, inputs storeInputs) error {
	owner, claimed, _, err := nodehost.NewNativeHost(services.Config)
	if err != nil {
		return err
	}
	if unit.Unit == capability.UnitNode {
		inputs.node.Storage = services.Storage
		return claimed.Execute(ctx, inputs.node)
	}
	inputs.run.Storage = services.Storage
	_, err = owner.Run(ctx, inputs.run)
	return err
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
	if err := validateUnit(unit); err != nil {
		return err
	}
	if unit.Delivery != capability.DeliveryDirect {
		return errors.New("runner hosting requires a directly usable award")
	}
	binding := unit.Binding
	if binding.RunID != unit.RunID || binding.Holder == "" || binding.Generation <= 0 {
		return errors.New("runner award has an invalid run or claim fence")
	}
	if unit.Unit == capability.UnitNode {
		if unit.Purpose != capability.PurposeWork || binding.Kind != capability.BindingNode || unit.JobID == "" || binding.NodeID != unit.JobID || binding.Attempt < 0 {
			return errors.New("runner award has an invalid job or attempt binding")
		}
		if unit.Engine == nil || unit.Accepted == nil || unit.Request.RunID != unit.RunID || unit.Request.Request.Team == "" || unit.Accepted.Spec.Request.RunID != unit.RunID || unit.Accepted.Spec.Request.Request.Team != unit.Request.Request.Team || unit.Accepted.Spec.Resolved.Engine != *unit.Engine {
			return errors.New("runner award is missing its matching accepted engine and run")
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
