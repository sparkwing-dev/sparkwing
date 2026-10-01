package orchestrator_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var supersessionPipelineID atomic.Uint64

type supersededPlanPipe struct {
	sparkwing.Base
	started     chan struct{}
	waitForSlot bool
}

func (p *supersededPlanPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("parcel-publish", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.CancelOthers,
	}))
	node := sparkwing.Job(plan, "publish", func(ctx context.Context) error {
		close(p.started)
		<-ctx.Done()
		return ctx.Err()
	})
	if p.waitForSlot {
		node.Concurrency(sparkwing.NewConcurrencyGroup("parcel-node", sparkwing.ConcurrencyLimit{Capacity: 1}))
	}
	return nil
}

type supersededPlanStore struct {
	fakeConcurrency
	started      chan struct{}
	waitForSlot  bool
	withdrawn    bool
	heartbeatErr error
}

func (s *supersededPlanStore) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	if s.waitForSlot && req.NodeID != "" {
		close(s.started)
		return store.AcquireSlotResponse{Kind: store.AcquireQueued}, nil
	}
	return s.fakeConcurrency.AcquireSlot(ctx, req)
}

func (s *supersededPlanStore) CancelWaiter(context.Context, string, string, string) (bool, error) {
	s.withdrawn = true
	return true, nil
}

func (s *supersededPlanStore) HeartbeatSlot(ctx context.Context, _, _ string, lease time.Duration) (time.Time, bool, error) {
	select {
	case <-s.started:
		return time.Now().Add(lease), s.heartbeatErr == nil, s.heartbeatErr
	case <-ctx.Done():
		return time.Time{}, false, ctx.Err()
	}
}

func TestPlanSupersessionCancelsNode(t *testing.T) {
	for _, tc := range []struct {
		name         string
		waitForSlot  bool
		heartbeatErr error
	}{
		{name: "superseded/executing"},
		{name: "superseded/waiting", waitForSlot: true},
		{name: "lease-lost/executing", heartbeatErr: store.ErrLockHeld},
		{name: "lease-lost/waiting", waitForSlot: true, heartbeatErr: store.ErrLockHeld},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := make(chan struct{})
				pipelineName := fmt.Sprintf("parcel-publish-supersession-%d", supersessionPipelineID.Add(1))
				register(pipelineName, func() sparkwing.Pipeline[sparkwing.NoInputs] {
					return &supersededPlanPipe{started: started, waitForSlot: tc.waitForSlot}
				})
				fakes := newFakeBackends()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				coordination := &supersededPlanStore{started: started, waitForSlot: tc.waitForSlot, heartbeatErr: tc.heartbeatErr}
				result, err := orchestrator.Run(ctx, orchestrator.Backends{
					State:       fakes.state,
					Logs:        fakes.logs,
					Concurrency: coordination,
				}, orchestrator.Options{Pipeline: pipelineName})
				if ctx.Err() != nil {
					t.Fatalf("superseded plan waited for caller timeout instead of cancelling: result=%+v err=%v", result, err)
				}
				if result == nil || result.Status != "cancelled" {
					t.Fatalf("superseded plan result=%+v err=%v, want cancelled", result, err)
				}
				if tc.waitForSlot && !coordination.withdrawn {
					t.Fatal("superseded node remained in the concurrency queue")
				}
			})
		})
	}
}
