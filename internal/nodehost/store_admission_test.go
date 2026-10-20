package nodehost

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type capacityProbe struct {
	bindings []store.CapacityBinding
	admit    func(context.Context) (store.AcquireSlotResponse, error)
}

func (s *capacityProbe) AdmitCapacity(ctx context.Context, binding store.CapacityBinding, _ store.Actor) (store.AcquireSlotResponse, error) {
	s.bindings = append(s.bindings, binding)
	return s.admit(ctx)
}

func capacityFixture(t *testing.T) (*storeAdmission, *capacityProbe, AcceptedRun, store.NodeRow) {
	t.Helper()
	probe := &capacityProbe{admit: func(context.Context) (store.AcquireSlotResponse, error) {
		return store.AcquireSlotResponse{Kind: store.AcquireGranted}, nil
	}}
	accepted := localAdmissionSeal(t, localAdmissionOptions())
	row := localAdmissionRow("job")
	row.Generation = 4
	row.Execution.Concurrency = []runspec.Concurrency{{Group: "db", Scope: "global", Capacity: 2, Cost: 1, OnLimit: "queue"}}
	admission := &storeAdmission{Store: probe, Owner: store.Actor{Kind: store.ActorRunHost, Owner: store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: accepted.RunID, Holder: "owner", Generation: 3}}}
	return admission, probe, accepted, row
}

func TestStoreAdmissionQueuePromotionAndPolicies(t *testing.T) {
	for _, kind := range []store.AcquireKind{store.AcquireQueued, store.AcquireSkipped, store.AcquireFailed} {
		t.Run(string(kind), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				admission, probe, accepted, row := capacityFixture(t)
				probe.admit = func(context.Context) (store.AcquireSlotResponse, error) {
					result := kind
					if len(probe.bindings) > 1 {
						result = store.AcquireGranted
					}
					return store.AcquireSlotResponse{Kind: result}, nil
				}
				ran := false
				err := admission.Node(t.Context(), accepted, row, func(context.Context) error { ran = true; return nil })
				refusal := kind != store.AcquireQueued
				if (err != nil) != refusal || ran == refusal {
					t.Fatalf("kind=%s ran=%v err=%v", kind, ran, err)
				}
				for _, binding := range probe.bindings {
					if binding != (store.CapacityBinding{RunID: accepted.RunID, NodeID: row.Ref.NodeID, Generation: row.Generation}) {
						t.Fatalf("changed binding: %+v", binding)
					}
				}
			})
		})
	}
}

func TestStoreAdmissionUsesPersistedQueueDeadline(t *testing.T) {
	admission, probe, accepted, row := capacityFixture(t)
	probe.admit = func(context.Context) (store.AcquireSlotResponse, error) {
		return store.AcquireSlotResponse{Kind: store.AcquireFailed, FailureReason: store.FailureQueueTimeout}, nil
	}
	err := admission.Node(t.Context(), accepted, row, func(context.Context) error { t.Error("ran an expired waiter"); return nil })
	var failure *AdmissionFailure
	if !errors.As(err, &failure) || !errors.Is(err, errQueueTimeout) || failure.Reason != store.FailureQueueTimeout {
		t.Fatalf("deadline restarted: %v", err)
	}
}

func TestStoreAdmissionStopsPollingAfterOwnerLoss(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		admission, probe, accepted, row := capacityFixture(t)
		probe.admit = func(context.Context) (store.AcquireSlotResponse, error) {
			return store.AcquireSlotResponse{Kind: store.AcquireQueued}, nil
		}
		ctx, cancel := context.WithCancel(t.Context())
		admission.Queued = func(context.Context, controllerwire.NodeRef, store.AcquireSlotResponse) error { cancel(); return nil }
		err := admission.Node(ctx, accepted, row, func(context.Context) error { t.Error("ran after owner loss"); return nil })
		if !errors.Is(err, context.Canceled) || len(probe.bindings) != 1 {
			t.Fatalf("polled after owner loss: %v", err)
		}
	})
}

func TestStoreAdmissionOutageBudget(t *testing.T) {
	for _, outage := range []time.Duration{4*time.Minute + 10*time.Second, 7 * time.Minute} {
		t.Run(outage.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				admission, probe, accepted, row := capacityFixture(t)
				started := time.Now()
				probe.admit = func(context.Context) (store.AcquireSlotResponse, error) {
					if len(probe.bindings) == 1 {
						return store.AcquireSlotResponse{Kind: store.AcquireQueued}, nil
					}
					if time.Since(started) < outage {
						return store.AcquireSlotResponse{}, errors.New("database is locked")
					}
					return store.AcquireSlotResponse{Kind: store.AcquireGranted}, nil
				}
				ran := false
				err := admission.Node(t.Context(), accepted, row, func(context.Context) error { ran = true; return nil })
				want := outage < storeOutageBudget
				if ran != want || (err == nil) != want {
					t.Fatalf("outage=%s ran=%v err=%v", outage, ran, err)
				}
			})
		})
	}
}
