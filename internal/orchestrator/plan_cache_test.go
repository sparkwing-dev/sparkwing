package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type inheritedPlanObserveFake struct {
	err       error
	supersede bool
}

func (f *inheritedPlanObserveFake) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	return store.AcquireSlotResponse{}, errors.New("unexpected acquire")
}

func (f *inheritedPlanObserveFake) ObserveSlot(ctx context.Context, key, holderID string) (*store.ConcurrencyHolder, error) {
	return nil, errors.New("unexpected observe")
}

func (f *inheritedPlanObserveFake) HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (time.Time, bool, error) {
	if f.err != nil {
		return time.Time{}, false, f.err
	}
	return time.Now().Add(store.DefaultConcurrencyLease), f.supersede, nil
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

type inheritedPlanAcquireFake struct {
	request store.AcquireSlotRequest
	err     error
}

func (f *inheritedPlanAcquireFake) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	f.request = req
	if f.err != nil {
		return store.AcquireSlotResponse{}, f.err
	}
	return store.AcquireSlotResponse{
		Kind:           store.AcquireGranted,
		HolderID:       req.HolderID,
		LeaseExpiresAt: time.Now().Add(store.DefaultConcurrencyLease),
	}, nil
}

func (f *inheritedPlanAcquireFake) ObserveSlot(ctx context.Context, key, holderID string) (*store.ConcurrencyHolder, error) {
	return nil, errors.New("unexpected observe")
}

func (f *inheritedPlanAcquireFake) HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (time.Time, bool, error) {
	return time.Now().Add(store.DefaultConcurrencyLease), false, nil
}

func (f *inheritedPlanAcquireFake) ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error {
	return nil
}

func (f *inheritedPlanAcquireFake) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (store.WaiterResolution, error) {
	return store.WaiterResolution{}, errors.New("unexpected resolve waiter")
}

func (f *inheritedPlanAcquireFake) ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error) {
	return nil, errors.New("unexpected force release")
}

func (f *inheritedPlanAcquireFake) CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error) {
	return false, errors.New("unexpected cancel waiter")
}

func TestAcquirePlanSlotInheritedAdmissionReturnsChildHolder(t *testing.T) {
	plan := sparkwing.NewPlan()
	plan.Concurrency(sparkwing.NewConcurrencyGroup("shared", sparkwing.ConcurrencyLimit{Capacity: 10}))
	key := scopedGroupKey(plan.ConcurrencyGroupRef(), "child")
	fake := &inheritedPlanAcquireFake{}

	release, outcome, active, err := acquirePlanSlot(
		context.Background(),
		Backends{Concurrency: fake},
		"child",
		plan,
		planAdmission{Key: key, HolderID: "parent/-"},
		func(error) {},
	)
	if err != nil {
		t.Fatalf("acquirePlanSlot: %v", err)
	}
	defer release("success")
	if outcome != planCacheProceed {
		t.Fatalf("outcome = %q, want proceed", outcome)
	}
	if fake.request.InheritedHolderID != "parent/-" {
		t.Fatalf("inherited holder = %q, want parent/-", fake.request.InheritedHolderID)
	}
	if fake.request.HolderID != "child/-" {
		t.Fatalf("child holder request = %q, want child/-", fake.request.HolderID)
	}
	if holderID, ok := active.holderFor(key); !ok || holderID != "child/-" {
		t.Fatalf("active admission holder = %q, %v; want child/-", holderID, ok)
	}
}

func TestAcquirePlanSlotInheritedSupersededReturnsEvicted(t *testing.T) {
	plan := sparkwing.NewPlan()
	plan.Concurrency(sparkwing.NewConcurrencyGroup("shared", sparkwing.ConcurrencyLimit{Capacity: 10}))
	key := scopedGroupKey(plan.ConcurrencyGroupRef(), "child")
	fake := &inheritedPlanAcquireFake{err: store.ErrConcurrencySuperseded}

	release, outcome, active, err := acquirePlanSlot(
		context.Background(),
		Backends{Concurrency: fake},
		"child",
		plan,
		planAdmission{Key: key, HolderID: "parent/-"},
		func(error) {},
	)
	if err != nil {
		t.Fatalf("acquirePlanSlot: %v", err)
	}
	if release != nil {
		t.Fatal("release is non-nil on eviction")
	}
	if outcome != planCacheEvicted {
		t.Fatalf("outcome = %q, want evicted", outcome)
	}
	if len(active.HolderIDs) != 0 {
		t.Fatalf("active admission = %+v, want empty", active)
	}
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
