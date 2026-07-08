package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type inheritedPlanObserveFake struct {
	err       error
	supersede bool
}

func (f *inheritedPlanObserveFake) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	return store.AcquireSlotResponse{}, errors.New("unexpected acquire")
}

func (f *inheritedPlanObserveFake) ObserveSlot(ctx context.Context, key, holderID string) (*store.ConcurrencyHolder, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &store.ConcurrencyHolder{
		Key:            key,
		HolderID:       holderID,
		RunID:          "parent",
		LeaseExpiresAt: time.Now().Add(store.DefaultConcurrencyLease),
		Superseded:     f.supersede,
	}, nil
}

func (f *inheritedPlanObserveFake) HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (time.Time, bool, error) {
	return time.Time{}, false, errors.New("unexpected heartbeat")
}

func (f *inheritedPlanObserveFake) ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error {
	return errors.New("unexpected release")
}

func (f *inheritedPlanObserveFake) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (store.WaiterResolution, error) {
	return store.WaiterResolution{}, errors.New("unexpected resolve waiter")
}

func (f *inheritedPlanObserveFake) ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error) {
	return nil, errors.New("unexpected force release")
}

func (f *inheritedPlanObserveFake) CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error) {
	return false, errors.New("unexpected cancel waiter")
}

func TestInheritedPlanSlotReleaseCancelsOnAdmissionLoss(t *testing.T) {
	withFastInheritedPlanObserve(t)
	fake := &inheritedPlanObserveFake{
		err: store.ErrLockHeld,
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	release := makeInheritedPlanSlotRelease(Backends{Concurrency: fake}, "k", "parent/-", cancel)
	defer release("failed")

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("admission observer did not cancel run")
	}
	if cause := context.Cause(ctx); cause == nil || !strings.Contains(cause.Error(), "inherited admission lost") {
		t.Fatalf("cause = %v, want inherited admission lost", cause)
	}
}

func TestInheritedPlanSlotReleaseCancelsOnSupersededAdmission(t *testing.T) {
	withFastInheritedPlanObserve(t)
	fake := &inheritedPlanObserveFake{
		supersede: true,
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	release := makeInheritedPlanSlotRelease(Backends{Concurrency: fake}, "k", "parent/-", cancel)
	defer release("failed")

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("admission observer did not cancel run")
	}
	if cause := context.Cause(ctx); cause == nil || !strings.Contains(cause.Error(), "inherited admission superseded") {
		t.Fatalf("cause = %v, want inherited admission superseded", cause)
	}
}

func withFastInheritedPlanObserve(t *testing.T) {
	t.Helper()
	prev := inheritedPlanObserveInterval
	inheritedPlanObserveInterval = time.Millisecond
	t.Cleanup(func() { inheritedPlanObserveInterval = prev })
}
