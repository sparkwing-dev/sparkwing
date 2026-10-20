package localcontroller

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type localState struct {
	binding Binding
	cipher  controller.Cipher
	mu      sync.Mutex
	ready   bool
}

// NewState projects activity and opens secrets through the elected controller's store.
func NewState(binding Binding, cipher controller.Cipher) (State, error) {
	if binding.Store == nil || binding.Tenant == nil || binding.Tenant.Team() != store.DefaultTeam || cipher == nil {
		return nil, errors.New("wingd: local state requires the owned default-team store and secrets cipher")
	}
	return &localState{binding: binding, cipher: cipher}, nil
}

func (s *localState) Ready() error {
	s.mu.Lock()
	ready := s.ready
	s.mu.Unlock()
	if !ready {
		return errors.New("wingd: local state has not reconciled")
	}
	return s.binding.Store.AsOperator().Ping(context.Background())
}

func (s *localState) TypeSafeAPIKey(ctx context.Context) (string, error) {
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	secret, err := s.binding.Tenant.GetSecretRow("TYPESAFE_API_KEY", "")
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return controller.OpenSecretValue(s.cipher, s.binding.Tenant.Team(), secret)
}

func (s *localState) Reconcile(ctx context.Context, lease store.SingletonLease) (wingd.LocalActivity, error) {
	if lease.Name != "controller.reaper" || lease.Holder == "" || lease.Generation <= 0 {
		return wingd.LocalActivity{}, errors.New("wingd: local state requires the elected reaper fence")
	}
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
