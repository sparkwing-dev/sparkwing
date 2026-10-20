package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type capacityProbe struct {
	CapacityStore
	request  store.AcquireSlotRequest
	requests []store.AcquireSlotRequest
	binding  store.CapacityBinding
	holder   *store.CapacityHolder
	waiter   *store.CapacityWaiter
	kind     store.AcquireKind
	adopt    func(store.CapacityAdoption) store.CapacityAdoption
	resolve  func(context.Context) (store.WaiterResolution, error)
}

func (s *capacityProbe) AdoptCapacity(_ context.Context, key, id string, binding store.CapacityBinding, _ store.Actor) (store.CapacityAdoption, error) {
	s.binding = binding
	result := store.CapacityAdoption{Kind: store.AcquireAgain}
	if s.holder != nil && s.holder.Key == key && s.holder.ID == id {
		result.Kind, result.Holder = store.AdoptHolder, s.holder
	}
	if s.waiter != nil && s.waiter.Key == key && s.waiter.ID == id {
		result.Kind, result.Waiter = store.AdoptWaiter, s.waiter
	}
	if s.adopt != nil {
		result = s.adopt(result)
	}
	return result, nil
}

func (s *capacityProbe) AcquireCapacity(_ context.Context, request store.AcquireSlotRequest, binding store.CapacityBinding, _ store.Actor) (store.AcquireSlotResponse, error) {
	s.binding = binding
	s.request = request
	s.requests = append(s.requests, request)
	kind := s.kind
	if kind == "" {
		kind = store.AcquireGranted
	}
	if kind == store.AcquireGranted || kind == store.AcquireCancellingOthers {
		s.holder = &store.CapacityHolder{Key: request.Key, ID: request.HolderID, Binding: s.binding, Cost: request.Cost}
	}
	if kind == store.AcquireQueued {
		s.waiter = &store.CapacityWaiter{Key: request.Key, ID: request.HolderID, Binding: s.binding, ArrivedAt: time.Now()}
	}
	return store.AcquireSlotResponse{Kind: kind, HolderID: request.HolderID}, nil
}

func (s *capacityProbe) ResolveCapacityWaiter(ctx context.Context, _ string, _ store.CapacityBinding, _ store.Actor) (store.WaiterResolution, error) {
	if s.resolve != nil {
		return s.resolve(ctx)
	}
	return store.WaiterResolution{Status: store.WaiterStillWaiting}, nil
}

func capacityFixture(t *testing.T) (*StoreAdmission, *capacityProbe, AcceptedRun, store.NodeRow) {
	t.Helper()
	probe := &capacityProbe{}
	accepted := localAdmissionSeal(t, localAdmissionOptions())
	row := localAdmissionRow("job")
	row.Generation = 4
	row.Execution.Concurrency = []runspec.Concurrency{{Group: "db", Scope: "global", Capacity: 2, Cost: 1, OnLimit: "queue"}}
	admission := &StoreAdmission{Store: probe, Owner: store.Actor{Kind: store.ActorRunHost, Owner: store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: accepted.RunID, Holder: "owner", Generation: 3}}}
	return admission, probe, accepted, row
}

func TestStoreAdmissionRetainsCapacityAfterOwnerWorkReturns(t *testing.T) {
	admission, probe, accepted, row := capacityFixture(t)
	for range 2 {
		err := admission.Node(t.Context(), accepted, row, func(context.Context) error { return errors.New("owner interrupted") })
		if err == nil || !strings.Contains(err.Error(), "owner interrupted") {
			t.Fatal(err)
		}
	}
	if len(probe.requests) != 1 || probe.holder == nil || probe.holder.Binding.Generation != row.Generation {
		t.Fatalf("capacity continuity lost: %+v", probe)
	}
}

func TestStoreAdmissionQueuePromotionAndPolicies(t *testing.T) {
	for _, policy := range []store.AcquireKind{store.AcquireQueued, store.AcquireSkipped, store.AcquireFailed, store.AcquireCancellingOthers} {
		t.Run(string(policy), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				admission, probe, accepted, row := capacityFixture(t)
				probe.kind = policy
				probe.resolve = func(context.Context) (store.WaiterResolution, error) {
					probe.holder = &store.CapacityHolder{Key: probe.request.Key, ID: probe.request.HolderID, Binding: probe.binding, Cost: probe.request.Cost}
					probe.waiter = nil
					return store.WaiterResolution{Status: store.WaiterPromoted, HolderID: probe.request.HolderID}, nil
				}
				ran := false
				err := admission.Node(t.Context(), accepted, row, func(context.Context) error { ran = true; return nil })
				refusal := policy == store.AcquireSkipped || policy == store.AcquireFailed
				if (err != nil) != refusal || ran == refusal {
					t.Fatalf("kind=%s ran=%v err=%v", policy, ran, err)
				}
				if refusal {
					var failure *AdmissionFailure
					if !errors.As(err, &failure) || policy == store.AcquireSkipped && failure.Outcome != store.NodeSkipped || policy == store.AcquireFailed && failure.Outcome != store.NodeFailed {
						t.Fatal(err)
					}
				}
			})
		})
	}
}

func TestStoreAdmissionAdoptedWaiterKeepsOriginalQueueDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		admission, probe, accepted, row := capacityFixture(t)
		budget := int64(10)
		row.Execution.Concurrency[0].QueueTimeoutMS = &budget
		probe.waiter = &store.CapacityWaiter{Key: "global:db", ID: accepted.RunID + "/job", Binding: store.CapacityBinding{RunID: accepted.RunID, NodeID: "job", Generation: row.Generation}, ArrivedAt: time.Now().Add(-time.Second)}
		probe.resolve = func(context.Context) (store.WaiterResolution, error) {
			return store.WaiterResolution{Status: store.WaiterExpired}, nil
		}
		err := admission.Node(t.Context(), accepted, row, func(context.Context) error { t.Error("ran an expired waiter"); return nil })
		var failure *AdmissionFailure
		if !errors.As(err, &failure) || !errors.Is(err, errQueueTimeout) || failure.Reason != store.FailureQueueTimeout {
			t.Fatalf("deadline restarted: %v", err)
		}
	})
}

func TestStoreAdmissionWaiterSurvivesOwnerLoss(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		admission, probe, accepted, row := capacityFixture(t)
		probe.kind = store.AcquireQueued
		ctx, cancel := context.WithCancel(t.Context())
		admission.Queued = func(context.Context, controllerwire.NodeRef, store.AcquireSlotResponse) error { cancel(); return nil }
		err := admission.Node(ctx, accepted, row, func(context.Context) error { t.Error("ran after owner loss"); return nil })
		if !errors.Is(err, context.Canceled) || probe.waiter == nil {
			t.Fatalf("lost continuous waiter: %v", err)
		}
	})
}

func TestStoreAdmissionOutageBudget(t *testing.T) {
	for _, outage := range []time.Duration{4*time.Minute + 10*time.Second, 7 * time.Minute} {
		t.Run(outage.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				admission, probe, accepted, row := capacityFixture(t)
				probe.kind = store.AcquireQueued
				started := time.Now()
				probe.resolve = func(context.Context) (store.WaiterResolution, error) {
					if time.Since(started) < outage {
						return store.WaiterResolution{}, errors.New("database is locked")
					}
					probe.waiter = nil
					probe.holder = &store.CapacityHolder{Key: probe.request.Key, ID: probe.request.HolderID, Binding: probe.binding, Cost: probe.request.Cost}
					return store.WaiterResolution{Status: store.WaiterPromoted, HolderID: probe.request.HolderID}, nil
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

func TestNativeAdmissionCombinesStoreAndLocalGroups(t *testing.T) {
	admission, probe, accepted, row := capacityFixture(t)
	daemon := newFakeDaemon()
	var spec runspec.Spec
	if err := json.Unmarshal(accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Resolved.Settings.Concurrency = []runspec.Concurrency{{Group: "deploy", Scope: "global", Capacity: 1, Cost: 1, OnLimit: "queue"}}
	accepted.Spec, _ = json.Marshal(spec)
	row.Execution.Concurrency = append(row.Execution.Concurrency, runspec.Concurrency{Group: "local", Scope: "run", Capacity: 1, Cost: 1, OnLimit: "queue"})
	var owner OwnerHost
	owner.Local = func(store.NodeRow) bool { return true }
	configureNativeAdmission(&owner, &LocalAdmission{Daemon: daemon}, admission, false)
	_, err := owner.Around(localAdmissionContext(t), accepted, func(ctx context.Context) (RunCompletion, error) {
		return RunCompletion{}, owner.Admit(ctx, accepted, row, func(context.Context) error {
			if probe.holder == nil {
				t.Error("job ran before store admission")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(probe.requests) != 2 || probe.requests[0].Key != "global:deploy" || probe.requests[0].HolderID != accepted.RunID+"/-" || probe.request.Key != "global:db" || probe.request.HolderID != accepted.RunID+"/job" {
		t.Fatalf("store groups=%+v", probe.requests)
	}
	requests := daemon.requests()
	if len(requests) != 2 || len(requests[1].Semaphores) != 1 || requests[1].Semaphores[0].Name != scopedConcurrencyKey("local", "run", accepted.RunID, "") {
		t.Fatalf("local groups=%+v", requests)
	}
	if live, granted, released := daemon.counts(); live != 0 || granted != released {
		t.Fatal("local admission leaked")
	}
}

func FuzzStoreAdmissionFencesAdoptedCapacity(f *testing.F) {
	f.Add("other-run", uint8(0))
	f.Add("job", uint8(1))
	f.Add("key", uint8(2))
	f.Add("holder", uint8(3))
	f.Add("cost", uint8(4))
	f.Add("superseded", uint8(5))
	f.Add("expired", uint8(6))
	f.Add("owner", uint8(7))
	f.Fuzz(func(t *testing.T, value string, field uint8) {
		admission, probe, accepted, row := capacityFixture(t)
		expected := store.CapacityHolder{Key: "global:db", ID: accepted.RunID + "/job", Binding: store.CapacityBinding{RunID: accepted.RunID, NodeID: row.Ref.NodeID, Generation: row.Generation}, Cost: 1}
		changed := expected
		switch field % 8 {
		case 0:
			changed.Binding.RunID += value + "x"
		case 1:
			changed.Binding.NodeID += value + "x"
		case 2:
			changed.Key += value + "x"
		case 3:
			changed.ID += value + "x"
		case 4:
			changed.Cost++
		case 5:
			changed.Superseded = true
		case 6:
			changed.Binding.Generation++
		case 7:
			admission.Owner.Owner.RunID += value + "x"
		}
		probe.adopt = func(store.CapacityAdoption) store.CapacityAdoption {
			return store.CapacityAdoption{Kind: store.AdoptHolder, Holder: &changed}
		}
		ran := false
		err := admission.Node(t.Context(), accepted, row, func(context.Context) error { ran = true; return nil })
		if err == nil || ran || len(probe.requests) != 0 || reflect.DeepEqual(expected, changed) && field%8 != 7 {
			t.Fatalf("accepted changed capacity: %v", err)
		}
	})
}
