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
	return nil
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
	request  store.AcquireSlotRequest
	requests []store.AcquireSlotRequest
	releases []string
	kinds    map[string]store.AcquireKind
	err      error
}

func (f *inheritedPlanAcquireFake) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	f.request = req
	f.requests = append(f.requests, req)
	if f.err != nil {
		return store.AcquireSlotResponse{}, f.err
	}
	kind := store.AcquireGranted
	if f.kinds != nil && f.kinds[req.Key] != "" {
		kind = f.kinds[req.Key]
	}
	return store.AcquireSlotResponse{
		Kind:           kind,
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
	f.releases = append(f.releases, key+"\x00"+holderID)
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
	plan.Concurrency(sparkwing.NewConcurrencyGroup("shared", sparkwing.ConcurrencyLimit{Capacity: 10}), 4)
	key := scopedGroupKey(plan.ConcurrencyGroupRef(), "child")
	fake := &inheritedPlanAcquireFake{}

	release, outcome, _, active, err := acquirePlanSlot(
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
	if fake.request.Cost != 4 {
		t.Fatalf("child holder cost = %d, want plan cost 4", fake.request.Cost)
	}
	if holderID, ok := active.holderFor(key); !ok || holderID != "child/-" {
		t.Fatalf("active admission holder = %q, %v; want child/-", holderID, ok)
	}
}

func TestAcquirePlanSlot_ComposesMultiplePlanGates(t *testing.T) {
	plan := sparkwing.NewPlan()
	land := sparkwing.NewConcurrencyGroup("land", sparkwing.ConcurrencyLimit{Capacity: 1})
	memory := sparkwing.NewConcurrencyGroup("memory-gb", sparkwing.ConcurrencyLimit{
		Capacity: 32,
		Scope:    sparkwing.ScopeBox,
	})
	plan.Concurrency(land)
	plan.Concurrency(memory, 8)
	fake := &inheritedPlanAcquireFake{}

	release, outcome, _, active, err := acquirePlanSlot(
		context.Background(),
		Backends{Concurrency: fake},
		"run-multi",
		plan,
		planAdmission{},
		func(error) {},
	)
	if err != nil {
		t.Fatalf("acquirePlanSlot: %v", err)
	}
	if outcome != planCacheProceed {
		t.Fatalf("outcome = %q, want proceed", outcome)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("AcquireSlot calls = %d, want 2", len(fake.requests))
	}
	expectedMemoryKey := scopedGroupKey(memory, "run-multi")
	if fake.requests[0].Key != expectedMemoryKey || fake.requests[0].Cost != 8 {
		t.Fatalf("first request = %+v, want memory cost 8", fake.requests[0])
	}
	if fake.requests[1].Key != "g:land" || fake.requests[1].Cost != 1 {
		t.Fatalf("second request = %+v, want land cost 1", fake.requests[1])
	}
	if holderID, ok := active.holderFor("g:land"); !ok || holderID != "run-multi/-" {
		t.Fatalf("active land holder = %q, %v", holderID, ok)
	}
	memoryKey := expectedMemoryKey
	if holderID, ok := active.holderFor(memoryKey); !ok || holderID != "run-multi/-" {
		t.Fatalf("active memory holder = %q, %v", holderID, ok)
	}
	release("success")
	if len(fake.releases) != 2 {
		t.Fatalf("ReleaseSlot calls = %d, want 2", len(fake.releases))
	}
	if fake.releases[0] != "g:land\x00run-multi/-" || fake.releases[1] != memoryKey+"\x00run-multi/-" {
		t.Fatalf("releases = %+v, want reverse acquisition order", fake.releases)
	}
}

func TestAcquirePlanSlotUsesCanonicalGateOrder(t *testing.T) {
	plan := sparkwing.NewPlan()
	land := sparkwing.NewConcurrencyGroup("land", sparkwing.ConcurrencyLimit{Capacity: 1})
	memory := sparkwing.NewConcurrencyGroup("memory-gb", sparkwing.ConcurrencyLimit{
		Capacity: 32,
		Scope:    sparkwing.ScopeBox,
	})
	plan.Concurrency(memory, 8)
	plan.Concurrency(land)
	fake := &inheritedPlanAcquireFake{}

	release, outcome, _, _, err := acquirePlanSlot(
		context.Background(),
		Backends{Concurrency: fake},
		"run-canonical",
		plan,
		planAdmission{},
		func(error) {},
	)
	if err != nil {
		t.Fatalf("acquirePlanSlot: %v", err)
	}
	defer release("success")
	if outcome != planCacheProceed {
		t.Fatalf("outcome = %q, want proceed", outcome)
	}
	expectedMemoryKey := scopedGroupKey(memory, "run-canonical")
	if got := []string{fake.requests[0].Key, fake.requests[1].Key}; got[0] != expectedMemoryKey || got[1] != "g:land" {
		t.Fatalf("acquire order = %+v, want [%s g:land]", got, expectedMemoryKey)
	}
}

func TestAcquirePlanSlotReportsGateThatRejectedAdmission(t *testing.T) {
	plan := sparkwing.NewPlan()
	land := sparkwing.NewConcurrencyGroup("land", sparkwing.ConcurrencyLimit{Capacity: 1})
	memory := sparkwing.NewConcurrencyGroup("memory-gb", sparkwing.ConcurrencyLimit{
		Capacity: 32,
		Scope:    sparkwing.ScopeBox,
		OnLimit:  sparkwing.Fail,
	})
	plan.Concurrency(land)
	plan.Concurrency(memory, 8)
	memoryKey := scopedGroupKey(memory, "run-fail")
	fake := &inheritedPlanAcquireFake{kinds: map[string]store.AcquireKind{
		memoryKey: store.AcquireFailed,
	}}

	release, outcome, outcomeGroup, _, err := acquirePlanSlot(
		context.Background(),
		Backends{Concurrency: fake},
		"run-fail",
		plan,
		planAdmission{},
		func(error) {},
	)
	if err != nil {
		t.Fatalf("acquirePlanSlot: %v", err)
	}
	if release != nil {
		t.Fatal("release is non-nil on failed admission")
	}
	if outcome != planCacheFailed {
		t.Fatalf("outcome = %q, want failed", outcome)
	}
	if outcomeGroup != "memory-gb" {
		t.Fatalf("outcome group = %q, want memory-gb", outcomeGroup)
	}
}

func TestAcquirePlanSlot_RejectsSecondHostAdmissionOwner(t *testing.T) {
	plan := sparkwing.NewPlan()
	parentKey := "b:1:parent-host"
	childGroup := sparkwing.NewConcurrencyGroup("child-host", sparkwing.ConcurrencyLimit{
		Capacity:      1,
		Scope:         sparkwing.ScopeBox,
		HostAdmission: true,
	})
	plan.Concurrency(childGroup)
	fake := &inheritedPlanAcquireFake{}

	_, _, _, _, err := acquirePlanSlot(
		context.Background(),
		Backends{Concurrency: fake},
		"child",
		plan,
		planAdmission{
			Key:              parentKey,
			HolderID:         "parent/-",
			HolderIDs:        map[string]string{parentKey: "parent/-"},
			HostAdmission:    true,
			HostAdmissionKey: parentKey,
		},
		func(error) {},
	)
	if err == nil || !strings.Contains(err.Error(), "already has host-admission key") {
		t.Fatalf("err = %v, want duplicate host-admission owner rejection", err)
	}
}

func TestAcquirePlanSlotSendsPlanCost(t *testing.T) {
	plan := sparkwing.NewPlan()
	plan.Concurrency(sparkwing.NewConcurrencyGroup("shared", sparkwing.ConcurrencyLimit{Capacity: 10}), 6)
	fake := &inheritedPlanAcquireFake{}

	release, outcome, _, _, err := acquirePlanSlot(
		context.Background(),
		Backends{Concurrency: fake},
		"run-costed",
		plan,
		planAdmission{},
		func(error) {},
	)
	if err != nil {
		t.Fatalf("acquirePlanSlot: %v", err)
	}
	defer release("success")
	if outcome != planCacheProceed {
		t.Fatalf("outcome = %q, want proceed", outcome)
	}
	if fake.request.Cost != 6 {
		t.Fatalf("fresh holder cost = %d, want plan cost 6", fake.request.Cost)
	}
}

func TestAcquirePlanSlotInheritedSupersededReturnsEvicted(t *testing.T) {
	plan := sparkwing.NewPlan()
	plan.Concurrency(sparkwing.NewConcurrencyGroup("shared", sparkwing.ConcurrencyLimit{Capacity: 10}))
	key := scopedGroupKey(plan.ConcurrencyGroupRef(), "child")
	fake := &inheritedPlanAcquireFake{err: store.ErrConcurrencySuperseded}

	release, outcome, _, active, err := acquirePlanSlot(
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

func TestInheritedPlanSlotReleaseReleasesChildHolder(t *testing.T) {
	withFastInheritedPlanObserve(t)
	fake := &inheritedPlanAcquireFake{}
	release := makeInheritedPlanSlotRelease(Backends{Concurrency: fake}, "g:shared", "child/-", func(error) {})
	release("success")
	if len(fake.releases) != 1 {
		t.Fatalf("ReleaseSlot calls = %d, want 1", len(fake.releases))
	}
	if fake.releases[0] != "g:shared\x00child/-" {
		t.Fatalf("release = %q, want child holder", fake.releases[0])
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
