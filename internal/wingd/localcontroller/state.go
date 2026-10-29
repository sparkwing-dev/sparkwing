package localcontroller

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type localState struct {
	binding Binding
	mu      sync.Mutex
	ready   bool
}

// NewState projects activity from the elected controller's store.
func NewState(binding Binding) (State, error) {
	if binding.Store == nil || binding.Tenant == nil || binding.Tenant.Team() != store.DefaultTeam {
		return nil, errors.New("wingd: local state requires the owned default-team store")
	}
	return &localState{binding: binding}, nil
}

func (s *localState) Ready() error {
	s.mu.Lock()
	ready := s.ready
	s.mu.Unlock()
	if !ready {
		return errors.New("wingd: local state has not reconciled")
	}
	return nil
}

func (s *localState) Reconcile(ctx context.Context) (wingd.LocalActivity, error) {
	activity, err := s.snapshot(ctx)
	if err == nil {
		s.mu.Lock()
		s.ready = true
		s.mu.Unlock()
	}
	return activity, err
}

func (s *localState) Watch(ctx context.Context, report func(wingd.LocalActivity) error) error {
	if report == nil {
		return errors.New("wingd: activity reporter is required")
	}
	if err := s.Ready(); err != nil {
		return err
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var previous wingd.LocalActivity
	first := true
	for {
		activity, err := s.snapshot(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if first || activity != previous {
			if err := report(activity); err != nil {
				return err
			}
			previous, first = activity, false
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func (s *localState) snapshot(ctx context.Context) (wingd.LocalActivity, error) {
	activity, err := s.binding.Tenant.ExecutionActivity(ctx)
	return wingd.LocalActivity{Runs: activity.Runs, QueuedTriggers: activity.QueuedTriggers, HeldLeases: activity.HeldLeases}, err
}
