package nodehost

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestOwnerHostWorkersBoundLocalExecution(t *testing.T) {
	for _, limit := range []int{1, 2, 3} {
		t.Run(string(rune('0'+limit)), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				state, host, input := newOwnerFixture(t, true)
				state.snapshot.Run.Spec.Resolved.Settings.Options.Workers.Value = limit
				input.Accepted.Spec, _ = json.Marshal(state.snapshot.Run.Spec)
				for _, id := range []string{"one", "two", "three"} {
					state.snapshot.Nodes = append(state.snapshot.Nodes, runstate.NodeRow{Ref: runspec.NodeRef{RunID: "run", NodeID: runspec.NodeID(id)}, Spec: marshalEventFixture(t, nodewire.JobSpec{ID: id}), Status: runstate.StatusPending, SpecHash: "job"})
				}
				host.Decide = store.Decide
				host.Local = func(runstate.NodeRow) bool { return true }
				setTestPolicies(&host, func(context.Context, testPolicyName, EvaluationInput, runstate.NodeRow, StorageBinding) (ownerLayerResult, error) {
					return ownerLayerResult{}, nil
				})
				release := make(chan struct{})
				var started atomic.Int64
				host.Executor = ownerNodeFunc(func(ctx context.Context, node NodeInput) error {
					started.Add(1)
					<-release
					_, err := state.TransitionNode(ctx, store.ReportNode{Ref: runspec.NodeRef{RunID: "run", NodeID: runspec.NodeID(node.NodeID)}, Outcome: runstate.NodeSuccess})
					return err
				})
				done := make(chan error, 1)
				go func() { _, err := host.Run(t.Context(), input); done <- err }()
				synctest.Wait()
				if got := started.Load(); got != int64(limit) {
					t.Errorf("started %d jobs before any finished with workers=%d", got, limit)
				}
				close(release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if got := started.Load(); got != 3 {
					t.Fatalf("finished after starting %d jobs", got)
				}
			})
		})
	}
}

// safety: the fake owner store hands every subscriber one shared channel, so while the scheduler and a holder
// follower both wait, a notification would wake only one of them; each follower here gets its own copy, as each
// production subscriber polls on its own.
type changeFanout struct {
	mu          sync.Mutex
	subscribers map[chan struct{}]bool
}

func (h *changeFanout) subscribe(context.Context, string) (stateSubscription, error) {
	changed := make(chan struct{}, 1)
	h.mu.Lock()
	h.subscribers[changed] = true
	h.mu.Unlock()
	return stateSubscription{Changed: changed, Close: func() error {
		h.mu.Lock()
		delete(h.subscribers, changed)
		h.mu.Unlock()
		return nil
	}}, nil
}

func (h *changeFanout) notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for changed := range h.subscribers {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
}

type handoffReconciler struct {
	*ownerFixture
	requeued *atomic.Bool
}

func (s handoffReconciler) ReconcileNode(ctx context.Context, request store.ReconcileRequest) (runstate.ReconciledNode, error) {
	if request.Ref.NodeID == "followed" && !s.requeued.Load() {
		row, err := s.ReadNode(ctx, request.Ref)
		return runstate.ReconciledNode{Kind: runstate.WaitHolder, Node: row}, err
	}
	return s.ownerFixture.ReconcileNode(ctx, request)
}

func TestOwnerHostFollowingAnotherHolderLeavesTheWorkerSlotToASibling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state, host, input := newOwnerFixture(t, true)
		state.snapshot.Run.Spec.Resolved.Settings.Options.Workers.Value = 1
		input.Accepted.Spec, _ = json.Marshal(state.snapshot.Run.Spec)
		ready := time.Now()
		state.snapshot.Nodes = []runstate.NodeRow{
			{Ref: runspec.NodeRef{RunID: "run", NodeID: "followed"}, Spec: marshalEventFixture(t, nodewire.JobSpec{ID: "followed"}), Status: runstate.StatusPending, SpecHash: "job", ReadyAt: &ready},
			{Ref: runspec.NodeRef{RunID: "run", NodeID: "sibling"}, Spec: marshalEventFixture(t, nodewire.JobSpec{ID: "sibling"}), Status: runstate.StatusPending, SpecHash: "job"},
		}
		requeued := &atomic.Bool{}
		host.Store = handoffReconciler{ownerFixture: state, requeued: requeued}
		host.Decide = func(graph store.ScheduleSnapshot) store.ScheduleDecision {
			decision := store.Decide(graph)
			decision.Nodes = slices.DeleteFunc(decision.Nodes, func(node store.NodeDecision) bool { return node.Kind == store.DecisionReady })
			return decision
		}
		host.Local = func(runstate.NodeRow) bool { return true }
		fanout := &changeFanout{subscribers: map[chan struct{}]bool{}}
		follower := stateFollower{Store: state, Subscribe: fanout.subscribe}
		host.Wait, host.WaitNode = follower.wait, follower.waitNode
		forwarding, stopForwarding := context.WithCancel(t.Context())
		defer stopForwarding()
		go func() {
			for {
				select {
				case <-forwarding.Done():
					return
				case <-state.changed:
					fanout.notify()
				}
			}
		}()
		release := make(chan struct{})
		var mu sync.Mutex
		var order []string
		host.Executor = ownerNodeFunc(func(ctx context.Context, node NodeInput) error {
			mu.Lock()
			order = append(order, node.NodeID+" started")
			mu.Unlock()
			if node.NodeID == "sibling" {
				<-release
			}
			mu.Lock()
			order = append(order, node.NodeID+" finished")
			mu.Unlock()
			_, err := state.TransitionNode(ctx, store.ReportNode{Ref: runspec.NodeRef{RunID: "run", NodeID: runspec.NodeID(node.NodeID)}, Outcome: runstate.NodeSuccess})
			return err
		})
		done := make(chan error, 1)
		go func() { _, err := host.Run(t.Context(), input); done <- err }()
		synctest.Wait()

		state.mu.Lock()
		now := time.Now()
		state.snapshot.Nodes[1].ReadyAt, state.snapshot.Nodes[1].StateSeq = &now, state.snapshot.Nodes[1].StateSeq+1
		state.mu.Unlock()
		state.notify()
		synctest.Wait()
		mu.Lock()
		started := slices.Equal(order, []string{"sibling started"})
		mu.Unlock()
		if !started {
			t.Fatalf("the sibling did not start while the other job followed a holder with --workers=1: %v", order)
		}

		requeued.Store(true)
		state.mu.Lock()
		state.snapshot.Nodes[0].StateSeq++
		state.mu.Unlock()
		state.notify()
		synctest.Wait()
		mu.Lock()
		early := slices.Contains(order, "followed started")
		mu.Unlock()
		if early {
			t.Fatalf("the requeued job started while the sibling held the only worker slot: %v", order)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if want := []string{"sibling started", "sibling finished", "followed started", "followed finished"}; !slices.Equal(order, want) {
			t.Fatalf("order %v, want %v", order, want)
		}
	})
}
