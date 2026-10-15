package localcontroller

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	return s.binding.Store.DB().Ping()
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

func (s *localState) snapshot(ctx context.Context) (activity wingd.LocalActivity, err error) {
	team, now := s.binding.Tenant.Team(), time.Now().UnixNano()
	err = s.binding.Store.DB().QueryRowContext(ctx, `WITH executable AS (
 SELECT id FROM runs WHERE team=? AND intake_mode='execute' AND request_json IS NOT NULL
), active AS (
 SELECT r.id FROM runs r JOIN executable e ON e.id=r.id
 WHERE r.team=? AND r.status IN ('pending','running') AND r.finished_at IS NULL
), held AS (
 SELECT t.id FROM triggers t JOIN executable e ON e.id=t.id
 WHERE t.team=? AND t.status='claimed' AND t.lease_expires_at>?
 UNION ALL
 SELECT n.run_id FROM nodes n JOIN executable e ON e.id=n.run_id
 WHERE n.team=? AND n.claimed_by IS NOT NULL AND n.status<>'done' AND n.lease_expires_at>?
 UNION ALL
 SELECT u.run_id FROM eval_units u JOIN executable e ON e.id=u.run_id
 WHERE u.team=? AND u.claimed_by IS NOT NULL AND u.status IN ('claimed','running') AND u.lease_expires_at>?
 UNION ALL
 SELECT h.run_id FROM concurrency_holders h JOIN executable e ON e.id=h.run_id
 WHERE h.team=? AND h.lease_expires_at>?
)
SELECT (SELECT COUNT(*) FROM active),
 (SELECT COUNT(*) FROM triggers t JOIN active a ON a.id=t.id WHERE t.team=? AND t.status='pending'),
 (SELECT COUNT(*) FROM held)`, team, team, team, now, team, now, team, now, team, now, team).
		Scan(&activity.Runs, &activity.QueuedTriggers, &activity.HeldLeases)
	return activity, err
}
