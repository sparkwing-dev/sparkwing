package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type planAcquireFake struct {
	request  store.AcquireSlotRequest
	requests []store.AcquireSlotRequest
	releases []string
	kinds    map[string]store.AcquireKind
	err      error
}

func (f *planAcquireFake) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
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

func (f *planAcquireFake) ObserveSlot(ctx context.Context, key, holderID string) (*store.ConcurrencyHolder, error) {
	return nil, errors.New("unexpected observe")
}

func (f *planAcquireFake) State(ctx context.Context, key string) (*store.ConcurrencyState, error) {
	return nil, errors.New("unexpected state")
}

func (f *planAcquireFake) HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (time.Time, bool, error) {
	return time.Now().Add(store.DefaultConcurrencyLease), false, nil
}

func (f *planAcquireFake) ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error {
	f.releases = append(f.releases, key+"\x00"+holderID)
	return nil
}

func (f *planAcquireFake) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (store.WaiterResolution, error) {
	return store.WaiterResolution{}, errors.New("unexpected resolve waiter")
}

func (f *planAcquireFake) ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error) {
	return nil, errors.New("unexpected force release")
}

func (f *planAcquireFake) CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error) {
	return false, errors.New("unexpected cancel waiter")
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
	fake := &planAcquireFake{}

	release, outcome, _, err := acquirePlanSlot(
		context.Background(),
		Backends{Concurrency: fake},
		"run-multi",
		plan,
		false,
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
	release("success")
	if len(fake.releases) != 2 {
		t.Fatalf("ReleaseSlot calls = %d, want 2", len(fake.releases))
	}
	if fake.releases[0] != "g:land\x00run-multi/-" || fake.releases[1] != expectedMemoryKey+"\x00run-multi/-" {
		t.Fatalf("releases = %+v, want reverse acquisition order", fake.releases)
	}
}

func TestAcquirePlanSlot_DaemonModeSkipsBoxAndRunScopes(t *testing.T) {
	plan := sparkwing.NewPlan()
	global := sparkwing.NewConcurrencyGroup("fleet", sparkwing.ConcurrencyLimit{Capacity: 2})
	box := sparkwing.NewConcurrencyGroup("box-only", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		Scope:    sparkwing.ScopeBox,
	})
	perRun := sparkwing.NewConcurrencyGroup("run-only", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		Scope:    sparkwing.ScopeRun,
	})
	plan.Concurrency(global)
	plan.Concurrency(box)
	plan.Concurrency(perRun)
	fake := &planAcquireFake{}

	release, outcome, _, err := acquirePlanSlot(
		context.Background(),
		Backends{Concurrency: fake},
		"run-daemon",
		plan,
		true,
	)
	if err != nil {
		t.Fatalf("acquirePlanSlot: %v", err)
	}
	defer release("success")
	if outcome != planCacheProceed {
		t.Fatalf("outcome = %q, want proceed", outcome)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("AcquireSlot calls = %d, want only the global gate", len(fake.requests))
	}
	if fake.requests[0].Key != "g:fleet" {
		t.Fatalf("store acquire key = %q, want g:fleet", fake.requests[0].Key)
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
	fake := &planAcquireFake{}

	release, outcome, _, err := acquirePlanSlot(
		context.Background(),
		Backends{Concurrency: fake},
		"run-canonical",
		plan,
		false,
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
	fake := &planAcquireFake{kinds: map[string]store.AcquireKind{
		memoryKey: store.AcquireFailed,
	}}

	release, outcome, outcomeGroup, err := acquirePlanSlot(
		context.Background(),
		Backends{Concurrency: fake},
		"run-fail",
		plan,
		false,
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

func TestAcquirePlanSlotSendsPlanCost(t *testing.T) {
	plan := sparkwing.NewPlan()
	plan.Concurrency(sparkwing.NewConcurrencyGroup("shared", sparkwing.ConcurrencyLimit{Capacity: 10}), 6)
	fake := &planAcquireFake{}

	release, outcome, _, err := acquirePlanSlot(
		context.Background(),
		Backends{Concurrency: fake},
		"run-costed",
		plan,
		false,
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
