package runners

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

// Hosting binds each award's controller to its shared host and lease keeper.
type Hosting struct {
	ControllerURL string
	HTTP          *http.Client
	NewController func(client.EngineOptions) (controllerwire.Controller, error)
	PrepareRun    func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.RunHost, nodehost.RunInput, func() error, error)
	PrepareNode   func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error)
	Observe       func(controllerwire.ClaimedUnit, lease.Beat)
	Lease         time.Duration
	Interval      time.Duration
	// StartKeeper supplies the shared lease runtime; nil uses lease.Keeper.Run.
	StartKeeper func(context.Context, lease.Keeper) (context.Context, func())
}

// Host keeps acquisition and execution under one keeper and joins it before
// releasing source resources. Lost authority remains an error even when the
// underlying host returns successfully while cancellation is being delivered.
func (h Hosting) Host(ctx context.Context, unit controllerwire.ClaimedUnit) (result error) {
	if err := validateHostedAward(unit); err != nil {
		return err
	}
	var execution nodeExecution
	if unit.Unit == capability.UnitNode {
		execution = executionFor(unit)
	}
	if err := limits.CheckRenewal(h.Interval, h.Lease); err != nil {
		return err
	}
	if strings.TrimSpace(h.ControllerURL) == "" || (unit.Unit == capability.UnitTrigger && h.PrepareRun == nil) || (unit.Unit == capability.UnitNode && h.PrepareNode == nil) {
		return errors.New("runner host requires a controller URL and host preparation")
	}
	deadline := unit.Binding.Deadline
	for _, bound := range []*time.Time{unit.OwnerDeadline, unit.ParentCapabilityExpiry} {
		if bound != nil && (deadline == nil || bound.Before(*deadline)) {
			deadline = bound
		}
	}
	if deadline != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, *deadline)
		defer cancel()
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	controller, err := h.controller(unit.Capability)
	if err != nil {
		return err
	}
	renew := func(ctx context.Context) (lease.Beat, error) {
		response, err := controller.Renew(ctx, unit.UnitID, controllerwire.RenewalRequest{})
		if err != nil {
			return lease.Beat{}, err
		}
		return lease.Beat{CancelRequested: response.CancelRequested, Watched: response.Watched, WatchedNodes: slices.Clone(response.WatchedJobs)}, nil
	}
	// safety: A delayed award must prove its lease is still live before source acquisition.
	beat, err := renew(ctx)
	if err != nil {
		return fmt.Errorf("confirm runner award: %w", err)
	}
	if beat.CancelRequested {
		return &lease.CancelError{Reason: "controller requested cancellation"}
	}
	observe := func(beat lease.Beat) {
		if h.Observe != nil {
			h.Observe(unit, beat)
		}
	}
	observe(beat)
	start := h.StartKeeper
	if start == nil {
		start = func(ctx context.Context, keeper lease.Keeper) (context.Context, func()) { return keeper.Run(ctx) }
	}
	unitCtx, stop := start(ctx, lease.Keeper{Renew: renew, Lease: h.Lease, Interval: h.Interval, Observe: observe})
	var closeSource func() error
	defer func() {
		// safety: Preserve lease loss before stop supplies its own cancellation cause.
		cause := context.Cause(unitCtx)
		stop()
		if cause == nil {
			cause = context.Cause(unitCtx)
			if cause == context.Canceled {
				cause = context.Cause(ctx)
			}
		}
		result = errors.Join(result, cause)
		if closeSource != nil {
			result = errors.Join(result, closeSource())
		}
	}()
	if err := context.Cause(unitCtx); err != nil {
		return err
	}
	if unit.Unit == capability.UnitTrigger {
		var host nodehost.RunHost
		var input nodehost.RunInput
		host, input, closeSource, err = h.PrepareRun(unitCtx, unit, controller)
		if err != nil {
			return err
		}
		if err := context.Cause(unitCtx); err != nil {
			return err
		}
		if host == nil {
			return errors.New("runner preparation returned no run host")
		}
		_, err = host.Run(unitCtx, input)
		return err
	}
	var host nodehost.NodeHost
	var input nodehost.NodeInput
	host, input, closeSource, err = h.PrepareNode(unitCtx, unit, controller)
	if err != nil {
		return err
	}
	if err := context.Cause(unitCtx); err != nil {
		return err
	}
	if host == nil {
		return errors.New("runner preparation returned no node host")
	}
	input, err = execution.input(input)
	if err != nil {
		return err
	}
	return host.Execute(unitCtx, input)
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
