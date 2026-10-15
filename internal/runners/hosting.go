package runners

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
)

// Hosting dispatches awards to the shared host library. Preparation binds each
// host's controller to the award capability, acquires its source and constructs
// its immutable input. Cleanup also runs on partially completed preparation.
// Renew must use the award capability rather than the runner's root credential.
type Hosting struct {
	PrepareRun  func(context.Context, controllerwire.ClaimedUnit) (nodehost.RunHost, nodehost.RunInput, func() error, error)
	PrepareNode func(context.Context, controllerwire.ClaimedUnit) (nodehost.NodeHost, nodehost.NodeInput, func() error, error)
	Renew       func(context.Context, controllerwire.ClaimedUnit) (lease.Beat, error)
	Observe     func(controllerwire.ClaimedUnit, lease.Beat)
	Lease       time.Duration
	Interval    time.Duration
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
	if err := limits.CheckRenewal(h.Interval, h.Lease); err != nil {
		return err
	}
	if h.Renew == nil || (unit.Unit == capability.UnitTrigger && h.PrepareRun == nil) || (unit.Unit == capability.UnitNode && h.PrepareNode == nil) {
		return errors.New("runner host requires award-bound renewal and host preparation")
	}
	if deadline := unit.Binding.Deadline; deadline != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, *deadline)
		defer cancel()
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	renew := func(ctx context.Context) (lease.Beat, error) { return h.Renew(ctx, unit) }
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
		host, input, closeSource, err = h.PrepareRun(unitCtx, unit)
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
	host, input, closeSource, err = h.PrepareNode(unitCtx, unit)
	if err != nil {
		return err
	}
	if err := context.Cause(unitCtx); err != nil {
		return err
	}
	if host == nil {
		return errors.New("runner preparation returned no node host")
	}
	return host.Execute(unitCtx, input)
}

func validateHostedAward(unit controllerwire.ClaimedUnit) error {
	if err := validateUnit(unit); err != nil {
		return err
	}
	binding := unit.Binding
	if binding.RunID != unit.RunID || binding.Holder == "" || binding.Generation <= 0 {
		return errors.New("runner award has an invalid run or claim fence")
	}
	if unit.Unit == capability.UnitNode {
		if binding.Kind != capability.BindingNode || unit.JobID == "" || binding.NodeID != unit.JobID || binding.Attempt < 0 {
			return errors.New("runner award has an invalid job or attempt binding")
		}
	} else if binding.Kind != capability.BindingTrigger || unit.JobID != "" || binding.NodeID != "" {
		return errors.New("runner award has an invalid trigger binding")
	}
	if unit.LeaseExpiresAt.IsZero() {
		return errors.New("runner award is missing its lease expiry")
	}
	return nil
}

var _ UnitHost = Hosting{}
