package nodehost

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

type capacityProbe struct {
	bindings     []runstate.CapacityBinding
	admit        func(context.Context) (runstate.AcquireSlotResponse, error)
	relinquished int
}

func (s *capacityProbe) MayStart(ctx context.Context, binding runstate.CapacityBinding, _ runstate.Actor) (runstate.AcquireSlotResponse, error) {
	s.bindings = append(s.bindings, binding)
	return s.admit(ctx)
}

func (s *capacityProbe) RelinquishStart(context.Context, runstate.CapacityBinding, runstate.Actor) error {
	s.relinquished++
	return nil
}

func capacityFixture(t *testing.T) (StartGate, *capacityProbe, AcceptedRun, runstate.NodeRow) {
	t.Helper()
	probe := &capacityProbe{admit: func(context.Context) (runstate.AcquireSlotResponse, error) {
		return runstate.AcquireSlotResponse{Kind: runstate.AcquireGranted}, nil
	}}
	accepted := localAdmissionSeal(t, localAdmissionOptions())
	row := localAdmissionRow("job")
	row.Generation = 4
	return ComposeStartGate(probe, nil, false), probe, accepted, row
}

func TestStartGateRequestsPermitForGroupFreeWork(t *testing.T) {
	gate, probe, accepted, row := capacityFixture(t)
	ran := false
	err := gate.claimedNode(t.Context(), NodeInput{Accepted: accepted, NodeID: string(row.Ref.NodeID), Generation: row.Generation}, func(context.Context) error { ran = true; return nil })
	if err != nil || !ran || len(probe.bindings) != 1 {
		t.Fatalf("ran=%t requests=%d error=%v", ran, len(probe.bindings), err)
	}
	want := runstate.CapacityBinding{RunID: accepted.RunID, NodeID: string(row.Ref.NodeID), Generation: int64(row.Generation)}
	if probe.bindings[0] != want {
		t.Fatalf("binding=%+v", probe.bindings[0])
	}
}

func TestStartDecisionsAndPolling(t *testing.T) {
	for _, kind := range []runstate.AcquireKind{runstate.AcquireQueued, runstate.AcquireSkipped, runstate.AcquireFailed} {
		t.Run(string(kind), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				gate, probe, accepted, row := capacityFixture(t)
				probe.admit = func(context.Context) (runstate.AcquireSlotResponse, error) {
					k := kind
					if len(probe.bindings) > 1 {
						k = runstate.AcquireGranted
					}
					return runstate.AcquireSlotResponse{Kind: k}, nil
				}
				ran := false
				err := gate.claimedNode(t.Context(), NodeInput{Accepted: accepted, NodeID: string(row.Ref.NodeID), Generation: row.Generation}, func(context.Context) error { ran = true; return nil })
				if kind == runstate.AcquireQueued {
					if err != nil || !ran || len(probe.bindings) != 2 {
						t.Fatalf("ran=%t error=%v", ran, err)
					}
					return
				}
				var refusal *admissionFailure
				if ran || !errors.As(err, &refusal) || !refusal.committed {
					t.Fatalf("ran=%t error=%v", ran, err)
				}
			})
		})
	}
}

func TestStartRefusalDoesNotSettleTwice(t *testing.T) {
	input, state, host, _ := claimedNodeFixture(t)
	for _, kind := range []runstate.AcquireKind{runstate.AcquireSkipped, runstate.AcquireFailed} {
		probe := &capacityProbe{admit: func(context.Context) (runstate.AcquireSlotResponse, error) {
			return runstate.AcquireSlotResponse{Kind: kind}, nil
		}}
		host.Admission = ComposeStartGate(probe, nil, false)
		before := len(state.changes)
		err := host.Execute(t.Context(), input)
		if (err == nil) != (kind == runstate.AcquireSkipped) || len(state.changes) != before {
			t.Fatalf("decision=%s changes=%d error=%v", kind, len(state.changes)-before, err)
		}
	}
}

func TestStartBoundaryErrorsStayErrors(t *testing.T) {
	gate, probe, accepted, row := capacityFixture(t)
	denied := errors.New("award fence refused")
	probe.admit = func(context.Context) (runstate.AcquireSlotResponse, error) {
		return runstate.AcquireSlotResponse{}, denied
	}
	err := gate.claimedNode(t.Context(), NodeInput{Accepted: accepted, NodeID: string(row.Ref.NodeID), Generation: row.Generation}, func(context.Context) error { t.Fatal("ran after refusal"); return nil })
	if !errors.Is(err, denied) {
		t.Fatal(err)
	}
}

func TestQueuedAdmissionLeavesRunnerSlotForAwaitedChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		slots := make(chan struct{}, 1)
		slots <- struct{}{}
		childPlanned, childStopped := make(chan struct{}), make(chan struct{})
		ctx = WithAwaitSlot(ctx, func() func(context.Context) {
			<-slots
			return func(ctx context.Context) {
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
				}
			}
		})
		go func() {
			defer close(childStopped)
			select {
			case slots <- struct{}{}:
				close(childPlanned)
				<-slots
			case <-ctx.Done():
			}
		}()
		defer func() { cancel(); <-childStopped }()
		gate, probe, accepted, row := capacityFixture(t)
		probe.admit = func(context.Context) (runstate.AcquireSlotResponse, error) {
			select {
			case <-childPlanned:
				return runstate.AcquireSlotResponse{Kind: runstate.AcquireGranted}, nil
			default:
				return runstate.AcquireSlotResponse{Kind: runstate.AcquireQueued}, nil
			}
		}
		ran := false
		err := gate.claimedNode(ctx, NodeInput{Accepted: accepted, NodeID: string(row.Ref.NodeID), Generation: row.Generation}, func(context.Context) error {
			ran = true
			if len(slots) != 1 {
				t.Error("job resumed without reacquiring runner inventory")
			}
			return nil
		})
		if err != nil || !ran {
			t.Fatalf("queued sibling starved the awaited child's planning: ran=%t error=%v", ran, err)
		}
	})
}

func TestQueuedAdmissionBacksOffAndRefusalDoesNotReacquire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		resumed := false
		ctx = WithAwaitSlot(ctx, func() func(context.Context) {
			return func(context.Context) { resumed = true }
		})
		gate, probe, accepted, row := capacityFixture(t)
		probe.admit = func(context.Context) (runstate.AcquireSlotResponse, error) {
			return runstate.AcquireSlotResponse{Kind: runstate.AcquireQueued}, nil
		}
		err := gate.claimedNode(ctx, NodeInput{Accepted: accepted, NodeID: string(row.Ref.NodeID), Generation: row.Generation}, func(context.Context) error {
			t.Error("queued job executed")
			return nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || resumed || len(probe.bindings) > 5 {
			t.Fatalf("queued wait did not back off or reacquired after refusal: polls=%d resumed=%t error=%v", len(probe.bindings), resumed, err)
		}
	})
}

func TestQueuedAdmissionRefusalLeavesInventoryReleased(t *testing.T) {
	for _, kind := range []runstate.AcquireKind{runstate.AcquireSkipped, runstate.AcquireFailed, "error"} {
		t.Run(string(kind), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				resumed := false
				ctx := WithAwaitSlot(t.Context(), func() func(context.Context) {
					return func(context.Context) { resumed = true }
				})
				gate, probe, accepted, row := capacityFixture(t)
				probe.admit = func(context.Context) (runstate.AcquireSlotResponse, error) {
					if len(probe.bindings) == 1 {
						return runstate.AcquireSlotResponse{Kind: runstate.AcquireQueued}, nil
					}
					if kind == "error" {
						return runstate.AcquireSlotResponse{}, errors.New("admission unavailable")
					}
					return runstate.AcquireSlotResponse{Kind: kind}, nil
				}
				err := gate.claimedNode(ctx, NodeInput{Accepted: accepted, NodeID: string(row.Ref.NodeID), Generation: row.Generation}, func(context.Context) error {
					t.Error("refused job executed")
					return nil
				})
				if err == nil || resumed {
					t.Fatalf("refusal reacquired runner inventory: resumed=%t error=%v", resumed, err)
				}
			})
		})
	}
}
