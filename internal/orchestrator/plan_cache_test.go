package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type inheritedPlanHeartbeatFake struct {
	err       error
	supersede bool
}

func (f *inheritedPlanHeartbeatFake) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	return store.AcquireSlotResponse{}, errors.New("unexpected acquire")
}

func (f *inheritedPlanHeartbeatFake) HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (time.Time, bool, error) {
	return time.Now().Add(lease), f.supersede, f.err
}

func (f *inheritedPlanHeartbeatFake) ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error {
	return errors.New("unexpected release")
}

func (f *inheritedPlanHeartbeatFake) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string) (store.WaiterResolution, error) {
	return store.WaiterResolution{}, errors.New("unexpected resolve waiter")
}

func (f *inheritedPlanHeartbeatFake) ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error) {
	return nil, errors.New("unexpected force release")
}

func TestInheritedPlanSlotReleaseCancelsOnHeartbeatLoss(t *testing.T) {
	withFastInheritedPlanHeartbeat(t)
	fake := &inheritedPlanHeartbeatFake{
		err: store.ErrLockHeld,
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	release := makeInheritedPlanSlotRelease(Backends{Concurrency: fake}, "k", "parent/-", cancel)
	defer release("failed")

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not cancel run")
	}
	if cause := context.Cause(ctx); cause == nil || !strings.Contains(cause.Error(), "inherited admission lost") {
		t.Fatalf("cause = %v, want inherited admission lost", cause)
	}
}

func TestInheritedPlanSlotReleaseCancelsOnSupersededHeartbeat(t *testing.T) {
	withFastInheritedPlanHeartbeat(t)
	fake := &inheritedPlanHeartbeatFake{
		supersede: true,
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	release := makeInheritedPlanSlotRelease(Backends{Concurrency: fake}, "k", "parent/-", cancel)
	defer release("failed")

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not cancel run")
	}
	if cause := context.Cause(ctx); cause == nil || !strings.Contains(cause.Error(), "inherited admission superseded") {
		t.Fatalf("cause = %v, want inherited admission superseded", cause)
	}
}

func withFastInheritedPlanHeartbeat(t *testing.T) {
	t.Helper()
	prev := inheritedPlanHeartbeatInterval
	inheritedPlanHeartbeatInterval = time.Millisecond
	t.Cleanup(func() { inheritedPlanHeartbeatInterval = prev })
}
