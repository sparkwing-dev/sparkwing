package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type ownerFixture struct {
	OwnerStore
	mu        sync.Mutex
	snapshot  store.RunSnapshot
	trace     []string
	finishErr error
	acceptErr error
	renewErr  error
	accepted  store.PlanAcceptance
	finished  store.FinishRunRequest
	changed   chan struct{}
}

func (f *ownerFixture) record(value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trace = append(f.trace, value)
}

func (f *ownerFixture) RenewOwner(context.Context, store.Actor) (store.LeaseBeat, error) {
	f.record("renew")
	return store.LeaseBeat{}, f.renewErr
}

func (f *ownerFixture) ReadSnapshot(context.Context, string) (store.RunSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snapshot
	s.Nodes = append([]store.NodeRow(nil), s.Nodes...)
	return s, nil
}

func (f *ownerFixture) ReadNode(_ context.Context, ref store.NodeRef) (store.NodeRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.snapshot.Nodes {
		if row.Ref == ref {
			return row, nil
		}
	}
	return store.NodeRow{}, store.ErrNotFound
}

func (f *ownerFixture) AcceptPlan(_ context.Context, input store.PlanAcceptance) (store.AcceptedPlan, error) {
	f.record("accept")
	f.accepted = input
	if f.acceptErr != nil {
		return store.AcceptedPlan{}, f.acceptErr
	}
	spec := runspec.Spec{Request: input.Seal.Request, Hash: "spec", PlanHash: runspec.Hash(strings.Repeat("a", 64)), Resolved: input.Seal.Resolved}
	f.snapshot.Run.Spec, f.snapshot.Run.PlanHash, f.snapshot.Run.Plan = &spec, strings.Repeat("a", 64), input.Seal.Plan
	return store.AcceptedPlan{Spec: spec, Plan: input.Seal.Plan, PlanHash: strings.Repeat("a", 64)}, nil
}

func (f *ownerFixture) TransitionRun(_ context.Context, change store.RunTransition) (store.Applied, error) {
	f.record("start")
	f.snapshot.Run.State = change.To
	return store.Applied{}, nil
}

func (f *ownerFixture) TransitionNode(_ context.Context, change store.NodeTransition) (store.Applied, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trace = append(f.trace, string(change.To))
	for i := range f.snapshot.Nodes {
		row := &f.snapshot.Nodes[i]
		if row.Ref != change.Ref {
			continue
		}
		row.StateSeq++
		if change.To == store.NodeReady {
			now := time.Now()
			row.ReadyAt = &now
		} else if change.To == store.NodeApprovalPending {
			row.Status = store.StatusApprovalPending
		} else {
			row.Status, row.Outcome = store.StatusDone, change.To
		}
	}
	select {
	case f.changed <- struct{}{}:
	default:
	}
	return store.Applied{}, nil
}

func (f *ownerFixture) ReconcileNode(_ context.Context, request store.ReconcileRequest) (store.ReconciledNode, error) {
	f.record("claim")
	expires := time.Now().Add(time.Hour)
	return store.ReconciledNode{Kind: store.Execute, Node: store.NodeRow{Ref: request.Ref, Status: store.StatusPending, ClaimedBy: "runner", CapabilityPrefix: "claim", Generation: 2, LeaseExpiresAt: &expires}, Claim: &store.ClaimAward{Holder: "runner", CapabilityPrefix: "claim", Generation: 2, LeaseExpiresAt: expires}}, nil
}

func (f *ownerFixture) FinishRunAndUnit(_ context.Context, request store.FinishRunRequest) (store.Applied, error) {
	f.record("finish")
	f.finished = request
	return store.Applied{}, f.finishErr
}

type ownerNodeFunc func(context.Context, NodeInput) error

func (fn ownerNodeFunc) Execute(ctx context.Context, in NodeInput) error { return fn(ctx, in) }

func newOwnerFixture(t *testing.T, resumed bool) (*ownerFixture, OwnerHost, RunInput) {
	t.Helper()
	request := runspec.Request{Pipeline: "build", PullRequest: runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{Number: 42}}}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	unit := store.OwnerLeaseRef{Kind: store.OwnerTrigger, Name: "trigger", RunID: "run", Holder: "owner", Generation: 4}
	f := &ownerFixture{changed: make(chan struct{}, 1), snapshot: store.RunSnapshot{Run: store.RunRow{ID: "run", State: store.RunPending, Request: runspec.SealedRequest{Request: request, Hash: "request"}, Owner: &unit}}}
	in := RunInput{RunID: "run", SealedRequest: raw, RequestHash: "request", Owner: store.Actor{Kind: store.ActorRunHost, Owner: unit}}
	h := OwnerHost{
		Mask:  func(value string) string { return value },
		Store: f, Lease: lease.Keeper{Interval: time.Minute, Lease: time.Hour},
		Acquire: func(context.Context, RunInput) (*source.Entry, error) {
			f.record("acquire")
			return &source.Entry{}, nil
		},
		Prepare: func(context.Context, RunInput, *source.Entry) (OwnerPlanning, error) {
			f.record("prepare")
			return OwnerPlanning{Planning: PlanningInput{Validation: nodeproto.ValidationContext{Secrets: planCacheSecretMatcher{}}}, Functions: PlanningFunctions{
				Plan: func(context.Context) (json.RawMessage, error) {
					f.record("plan")
					return json.RawMessage(`{"jobs":[]}`), nil
				},
				Validate: func(json.RawMessage, nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
					return nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}}, nil
				},
				Hash: func(any) (runspec.Hash, error) { return runspec.Hash(strings.Repeat("a", 64)), nil },
			}}, nil
		},
		Project: func(s store.RunSnapshot) (store.ScheduleSnapshot, error) {
			result := store.ScheduleSnapshot{}
			for _, row := range s.Nodes {
				result.Nodes = append(result.Nodes, store.ScheduleNode{ID: row.Ref.NodeID, Status: string(row.Status), Outcome: string(row.Outcome), Ready: row.ReadyAt != nil})
			}
			return result, nil
		},
		Decide: func(store.ScheduleSnapshot) store.ScheduleDecision { return store.ScheduleDecision{Outcome: "success"} },
	}
	follower := StateFollower{Store: f, Subscribe: func(context.Context, string) (StateSubscription, error) {
		return StateSubscription{Changed: f.changed, Close: func() error { return nil }}, nil
	}}
	h.Wait, h.WaitNode = follower.Wait, follower.WaitNode
	if resumed {
		spec := runspec.Spec{Request: f.snapshot.Run.Request, Hash: "spec", PlanHash: runspec.Hash(strings.Repeat("a", 64))}
		f.snapshot.Run.Spec, f.snapshot.Run.PlanHash, f.snapshot.Run.Plan = &spec, strings.Repeat("a", 64), json.RawMessage(`{"jobs":[]}`)
		accepted, err := ownerAccepted(f.snapshot.Run)
		if err != nil {
			t.Fatal(err)
		}
		in.Accepted = &accepted
	}
	return f, h, in
}

func TestOwnerHostSealsBeforeScheduling(t *testing.T) {
	f, h, in := newOwnerFixture(t, false)
	result, err := h.Run(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "success" || f.finished.Unit != in.Owner.Owner {
		t.Fatalf("result=%+v finish=%+v", result, f.finished)
	}
	if !reflect.DeepEqual(f.accepted.Seal.Request, f.snapshot.Run.Request) {
		t.Fatal("intake changed during planning")
	}
	if !reflect.DeepEqual(f.trace, []string{"renew", "acquire", "prepare", "plan", "accept", "start", "finish"}) {
		t.Fatalf("trace=%v", f.trace)
	}
}

func TestOwnerHostResumeDoesNotReplan(t *testing.T) {
	f, h, in := newOwnerFixture(t, true)
	h.Prepare = func(context.Context, RunInput, *source.Entry) (OwnerPlanning, error) {
		t.Fatal("resume read mutable planning inputs")
		return OwnerPlanning{}, nil
	}
	if _, err := h.Run(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.trace, []string{"renew", "acquire", "start", "finish"}) {
		t.Fatalf("trace=%v", f.trace)
	}
}

func TestOwnerHostRefusedAcceptanceAndFinishAreLoud(t *testing.T) {
	for _, stage := range []string{"accept", "finish"} {
		t.Run(stage, func(t *testing.T) {
			f, h, in := newOwnerFixture(t, false)
			sentinel := errors.New("store refused")
			if stage == "accept" {
				f.acceptErr = sentinel
			} else {
				f.finishErr = sentinel
			}
			result, err := h.Run(t.Context(), in)
			if !errors.Is(err, sentinel) || result.Outcome != "" {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if stage == "accept" && f.finished.Transition.RunID != "" {
				t.Fatal("finished unaccepted run")
			}
		})
	}
}

func TestOwnerHostRejectedReattachNeverAcquires(t *testing.T) {
	f, h, in := newOwnerFixture(t, true)
	f.renewErr = &apierr.Error{Code: apierr.LockHeld}
	_, err := h.Run(t.Context(), in)
	if !errors.Is(err, lease.ErrLeaseLost) {
		t.Fatalf("error=%v", err)
	}
	if !reflect.DeepEqual(f.trace, []string{"renew"}) {
		t.Fatalf("trace=%v", f.trace)
	}
}

func TestOwnerHostDispatchesOnlyAfterSelfClaim(t *testing.T) {
	f, h, in := newOwnerFixture(t, true)
	f.snapshot.Nodes = []store.NodeRow{{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Status: store.StatusPending}}
	h.Decide = func(s store.ScheduleSnapshot) store.ScheduleDecision {
		if s.Nodes[0].Outcome == "success" {
			return store.ScheduleDecision{Outcome: "success"}
		}
		if s.Nodes[0].Ready {
			return store.ScheduleDecision{}
		}
		return store.ScheduleDecision{Nodes: []store.NodeDecision{{NodeID: "job", Kind: store.DecisionReady}}}
	}
	h.Local = func(store.NodeRow) bool { return true }
	h.Layer = func(_ context.Context, layer LayerSpec, _ AcceptedRun, _ store.NodeRow) (OwnerLayerResult, error) {
		f.record(layer.Name)
		return OwnerLayerResult{}, nil
	}
	h.Bind = func(_ context.Context, a AcceptedRun, r store.ReconciledNode, e *source.Entry) (NodeInput, error) {
		return NodeInput{Accepted: a, Entry: e, NodeID: r.Node.Ref.NodeID, Generation: r.Node.Generation, Authority: ClaimAuthority{Holder: r.Claim.Holder, CapabilityPrefix: r.Claim.CapabilityPrefix}}, nil
	}
	h.Executor = ownerNodeFunc(func(ctx context.Context, n NodeInput) error {
		f.record("execute")
		_, err := f.TransitionNode(ctx, store.NodeTransition{Ref: store.NodeRef{RunID: n.Accepted.RunID, NodeID: n.NodeID}, To: store.NodeSuccess})
		return err
	})
	if _, err := h.Run(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	want := []string{"renew", "acquire", "start", "debug", "skip_if", "approval", "memo", "admission", "ready", "claim", "execute", "success", "finish"}
	if !reflect.DeepEqual(f.trace, want) {
		t.Fatalf("trace=%v", f.trace)
	}
}
