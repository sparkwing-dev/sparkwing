package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/quick"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type ownerFixture struct {
	OwnerStore
	mu        sync.Mutex
	snapshot  store.RunSnapshot
	trace     []string
	finishErr error
	acceptErr error
	renewErr  error
	nodeRenew func(context.Context, controllerwire.NodeRef, store.Actor) (store.LeaseBeat, error)
	accepted  store.PlanAcceptance
	finished  store.FinishRunRequest
	changed   chan struct{}
}

func (f *ownerFixture) record(value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trace = append(f.trace, value)
}

func (f *ownerFixture) RenewOwner(context.Context, store.Actor, bool) (store.LeaseBeat, error) {
	f.record("renew")
	return store.LeaseBeat{}, f.renewErr
}

func (f *ownerFixture) RenewNodeClaim(ctx context.Context, ref controllerwire.NodeRef, actor store.Actor) (store.LeaseBeat, error) {
	if f.nodeRenew != nil {
		return f.nodeRenew(ctx, ref, actor)
	}
	f.record("node_renew")
	return store.LeaseBeat{}, nil
}

func TestOwnerHostReportsCurrentComputeOnRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, host, input := newOwnerFixture(t, true)
		host.Lease.Interval = time.Second
		var computing atomic.Bool
		host.Computing = func(string) bool { return computing.Load() }
		beats := make(chan bool, 1)
		host.Renew = func(ctx context.Context, actor store.Actor, executing bool) (store.LeaseBeat, error) {
			if actor != input.Owner {
				t.Error("renewal changed owner authority")
			}
			select {
			case beats <- executing:
			case <-ctx.Done():
				return store.LeaseBeat{}, context.Cause(ctx)
			}
			return store.LeaseBeat{}, nil
		}
		host.Decide = func(store.ScheduleSnapshot) store.ScheduleDecision { return store.ScheduleDecision{} }
		waiting := make(chan struct{})
		host.Wait = func(ctx context.Context, _ store.RunSnapshot) error {
			close(waiting)
			<-ctx.Done()
			return context.Cause(ctx)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { _, err := host.Run(ctx, input); done <- err }()
		defer func() { cancel(); <-done }()
		if executing := <-beats; !executing {
			t.Fatal("source preparation was reported as idle")
		}
		<-waiting
		for _, want := range []bool{false, true, false} {
			computing.Store(want)
			if got := <-beats; got != want {
				t.Fatalf("renewal executing=%v, want %v", got, want)
			}
		}
	})
}

func TestOwnerHostCancellationStopsSchedulingWithARemoteAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state, host, input := newOwnerFixture(t, true)
		state.snapshot.Nodes = []store.NodeRow{{Ref: controllerwire.NodeRef{RunID: runspec.RunID(input.RunID), NodeID: "remote"}, Spec: json.RawMessage(`{"id":"remote"}`), Status: store.StatusRunning, ClaimedBy: "personal-machine"}}
		host.Lease.Interval = time.Second
		var beats atomic.Int64
		observed := make(chan struct{}, 1)
		host.Renew = func(context.Context, store.Actor, bool) (store.LeaseBeat, error) {
			return store.LeaseBeat{CancelRequested: beats.Add(1) > 1}, nil
		}
		host.Lease.Observe = func(lease.Beat) {
			if beats.Load() == 2 {
				observed <- struct{}{}
			}
		}
		host.Decide = func(snapshot store.ScheduleSnapshot) store.ScheduleDecision {
			if snapshot.Nodes[0].Status == string(store.StatusDone) {
				return store.ScheduleDecision{Outcome: "cancelled"}
			}
			return store.ScheduleDecision{}
		}
		host.Wait = func(ctx context.Context, _ store.RunSnapshot) error {
			now := time.Now()
			state.mu.Lock()
			state.snapshot.Run.CancelRequestedAt = &now
			state.mu.Unlock()
			<-observed
			if err := context.Cause(ctx); err != nil {
				return err
			}
			state.mu.Lock()
			state.snapshot.Nodes[0].Status, state.snapshot.Nodes[0].Outcome = store.StatusDone, store.NodeCancelled
			state.mu.Unlock()
			state.record("remote-joined")
			return nil
		}
		completion, err := host.Run(t.Context(), input)
		var cancelled *lease.CancelError
		if !errors.As(err, &cancelled) || completion.Outcome != "" || state.finished.Transition.RunID != "" || slices.Contains(state.trace, "remote-joined") {
			t.Fatalf("scheduling suppressed cancellation or finished live remote work: %+v %v trace=%v", completion, err, state.trace)
		}
	})
}

func TestOwnerHostWaitsForAlreadyRequestedRemoteCancellation(t *testing.T) {
	state, host, input := newOwnerFixture(t, true)
	now := time.Now()
	state.snapshot.Run.CancelRequestedAt = &now
	state.snapshot.Nodes = []store.NodeRow{{Ref: controllerwire.NodeRef{RunID: runspec.RunID(input.RunID), NodeID: "remote"}, Spec: json.RawMessage(`{"id":"remote"}`), Status: store.StatusRunning, ClaimedBy: "personal-machine", CancelRequestedAt: &now}}
	host.Project = func(snapshot store.RunSnapshot) (store.ScheduleSnapshot, error) {
		row := snapshot.Nodes[0]
		return store.ScheduleSnapshot{CancelRequested: true, Nodes: []store.ScheduleNode{{ID: string(row.Ref.NodeID), Status: string(row.Status), Claimed: row.ClaimedBy != "", Outcome: string(row.Outcome)}}}, nil
	}
	host.Decide = store.Decide
	host.Wait = func(context.Context, store.RunSnapshot) error {
		state.mu.Lock()
		state.snapshot.Nodes[0].Status, state.snapshot.Nodes[0].Outcome = store.StatusDone, store.NodeCancelled
		state.mu.Unlock()
		state.record("remote-joined")
		return nil
	}
	completion, err := host.Run(t.Context(), input)
	if err != nil || completion.Outcome != "cancelled" || !slices.Contains(state.trace, "remote-joined") || slices.Contains(state.trace, "cancelled") {
		t.Fatalf("owner repeated cancellation instead of waiting for the holder: %+v %v trace=%v", completion, err, state.trace)
	}
}

func TestOwnerHostAuthorityLossStopsWhileWaitingForRemoteAttempt(t *testing.T) {
	property := func(selector uint8) bool {
		synctest.Test(t, func(t *testing.T) {
			state, host, input := newOwnerFixture(t, true)
			host.Lease.Interval = time.Second
			var beats atomic.Int64
			host.Renew = func(context.Context, store.Actor, bool) (store.LeaseBeat, error) {
				if beats.Add(1) > 1+int64(selector%4) {
					codes := []apierr.Code{apierr.LockHeld, apierr.Unauthenticated, apierr.Forbidden}
					return store.LeaseBeat{}, &apierr.Error{Code: codes[selector%3]}
				}
				return store.LeaseBeat{}, nil
			}
			host.Decide = func(store.ScheduleSnapshot) store.ScheduleDecision { return store.ScheduleDecision{} }
			host.Wait = func(ctx context.Context, _ store.RunSnapshot) error {
				<-ctx.Done()
				return context.Cause(ctx)
			}
			_, err := host.Run(t.Context(), input)
			if !errors.Is(err, lease.ErrLeaseLost) || slices.Contains(state.trace, "finish") {
				t.Fatalf("lost owner finished its run: %v trace=%v", err, state.trace)
			}
		})
		return !t.Failed()
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerHostCancellationStopsSourcePreparation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state, host, input := newOwnerFixture(t, false)
		host.Lease.Interval = time.Second
		var beats atomic.Int64
		host.Renew = func(context.Context, store.Actor, bool) (store.LeaseBeat, error) {
			return store.LeaseBeat{CancelRequested: beats.Add(1) > 1}, nil
		}
		host.Acquire = func(ctx context.Context, _ RunInput) (*source.Entry, error) {
			<-ctx.Done()
			return nil, context.Cause(ctx)
		}
		_, err := host.Run(t.Context(), input)
		var cancelled *lease.CancelError
		if !errors.As(err, &cancelled) || state.finished.Transition.Reason != string(apierr.ReasonCancelled) || slices.Contains(state.trace, "prepare") {
			t.Fatalf("cancelled preparation reached planning: %v trace=%v", err, state.trace)
		}
	})
}

func (f *ownerFixture) ReadSnapshot(context.Context, string) (store.RunSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snapshot
	s.Nodes = append([]store.NodeRow(nil), s.Nodes...)
	return s, nil
}

func (f *ownerFixture) ReadNode(_ context.Context, ref controllerwire.NodeRef) (store.NodeRow, error) {
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

func (f *ownerFixture) TransitionNode(_ context.Context, command store.NodeCommand) (store.Applied, error) {
	change := captureNodeCommand(command)
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

func (f *ownerFixture) ReconcileNode(ctx context.Context, request store.ReconcileRequest) (store.ReconciledNode, error) {
	f.record("claim")
	expires := time.Now().Add(time.Hour)
	row, _ := f.ReadNode(ctx, request.Ref)
	row.Ref, row.Status, row.ClaimedBy, row.CapabilityPrefix = request.Ref, store.StatusPending, "runner", "claim"
	row.Generation, row.LeaseExpiresAt = 2, &expires
	return store.ReconciledNode{Kind: store.Execute, Node: row, Claim: &store.ClaimAward{Holder: "runner", CapabilityPrefix: "claim", Generation: 2, LeaseExpiresAt: expires}}, nil
}

func (f *ownerFixture) FinishRunAndUnit(_ context.Context, request store.FinishRunRequest) (store.Applied, error) {
	f.record("finish")
	f.finished = request
	if f.finishErr != nil {
		return store.Applied{}, f.finishErr
	}
	graph := store.ScheduleSnapshot{CancelRequested: f.snapshot.Run.CancelRequestedAt != nil}
	for _, row := range f.snapshot.Nodes {
		graph.Nodes = append(graph.Nodes, store.ScheduleNode{ID: string(row.Ref.NodeID), Status: string(row.Status), Outcome: string(row.Outcome)})
	}
	decision := store.Decide(graph)
	f.snapshot.Run.State = store.RunState(decision.Outcome)
	reason := decision.Reason
	if graph.CancelRequested {
		reason = f.snapshot.Run.CancelReason
	}
	return store.Applied{Outcome: f.snapshot.Run.State, Reason: reason}, nil
}

type ownerNodeFunc func(context.Context, NodeInput) error

func (fn ownerNodeFunc) Execute(ctx context.Context, in NodeInput) error { return fn(ctx, in) }

type ownerDelegatorFixture struct{ actor store.Actor }

func (f ownerDelegatorFixture) ResolveAuthority(context.Context, capability.AuthorityRef) (capability.Principal, error) {
	owner := f.actor.Owner
	return capability.Principal{Authority: f.actor.Authority, Capability: capability.Capability{Binding: capability.Binding{Kind: capability.BindingTrigger, RunID: string(owner.RunID), Holder: owner.Holder, Generation: int64(owner.Generation)}}}, nil
}

func (ownerDelegatorFixture) DelegateCapability(_ context.Context, spec capability.DelegateSpec) (capability.Issued, error) {
	return capability.Issued{Capability: capability.Capability{Prefix: "expansion", Parent: &spec.Parent, Audience: spec.Audience, Grants: spec.Grants, Binding: spec.Binding}}, nil
}

func newOwnerFixture(t *testing.T, resumed bool) (*ownerFixture, OwnerHost, RunInput) {
	t.Helper()
	request := runspec.Request{Pipeline: "build", PullRequest: runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{Number: 42}}}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	unit := store.OwnerLeaseRef{Kind: store.OwnerTrigger, Name: "trigger", EvalFence: runspec.EvalFence{RunID: "run", Holder: "owner", Generation: 4}}
	f := &ownerFixture{changed: make(chan struct{}, 1), snapshot: store.RunSnapshot{Run: store.RunRow{ID: "run", State: store.RunPending, Request: runspec.SealedRequest{Request: request, Hash: "request"}, Owner: &unit}}}
	in := RunInput{RunID: "run", SealedRequest: raw, RequestHash: "request", Owner: store.Actor{Kind: store.ActorRunHost, Owner: unit}}
	in.GrantedRunner = nodeproto.RunnerIdentity{Name: "granted", Type: "agent", Labels: []string{}}
	selected := engine.Resolved{Identity: engine.Identity{Version: "v1.0.0", ManifestDigest: "manifest", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.0.0"}}}
	h := OwnerHost{
		Masker: secrets.NewMasker(),
		Store:  f, Renew: f.RenewOwner, Lease: lease.Keeper{Interval: time.Minute, Lease: time.Hour},
		Delegator: ownerDelegatorFixture{actor: in.Owner},
		Acquire: func(context.Context, RunInput) (*source.Entry, error) {
			f.record("acquire")
			return &source.Entry{Engine: selected}, nil
		},
		Prepare: func(context.Context, RunInput, *source.Entry) (OwnerPlanning, error) {
			f.record("prepare")
			return OwnerPlanning{Planning: PlanningInput{Disabled: true, Validation: nodeproto.ValidationContext{Secrets: planCacheSecretMatcher{}}}, Functions: PlanningFunctions{
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
				result.Nodes = append(result.Nodes, store.ScheduleNode{ID: string(row.Ref.NodeID), Status: string(row.Status), Outcome: string(row.Outcome), Ready: row.ReadyAt != nil})
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
		spec := runspec.Spec{Request: f.snapshot.Run.Request, Hash: "spec", PlanHash: runspec.Hash(strings.Repeat("a", 64)), Resolved: runspec.Resolved{Engine: selected.Identity}}
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
	f.snapshot.Nodes = []store.NodeRow{{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Spec: json.RawMessage(`{"id":"job"}`), Status: store.StatusPending, SpecHash: "job-hash"}}
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
	h.Layer = func(_ context.Context, layer LayerSpec, _ EvaluationInput, _ store.NodeRow, binding StorageBinding) (OwnerLayerResult, error) {
		f.record(layer.Name)
		return OwnerLayerResult{}, nil
	}
	h.Executor = ownerNodeFunc(func(ctx context.Context, n NodeInput) error {
		f.record("execute")
		_, err := f.TransitionNode(ctx, store.ReportNode{Ref: controllerwire.NodeRef{RunID: runspec.RunID(n.Accepted.RunID), NodeID: runspec.NodeID(n.NodeID)}, Outcome: store.NodeSuccess})
		return err
	})
	if _, err := h.Run(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	want := []string{"renew", "acquire", "start", "debug", "skip_if", "approval", "memo", "admission", "ready", "claim", "node_renew", "execute", "success", "finish"}
	if !reflect.DeepEqual(f.trace, want) {
		t.Fatalf("trace=%v", f.trace)
	}
}

func TestOwnerHostRetainsIntakeSecretClassifications(t *testing.T) {
	f, host, input := newOwnerFixture(t, false)
	f.snapshot.Run.Request.Request.RequestedArgs = map[string]string{"token": "private-value"}
	f.snapshot.Run.Request.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"token"}}
	input.SealedRequest, _ = json.Marshal(f.snapshot.Run.Request.Request)
	masker := secrets.NewMasker()
	host.Masks = NewMaskRegistry(masker)
	host.Masker = masker
	prepare := host.Prepare
	host.Prepare = func(ctx context.Context, in RunInput, entry *source.Entry) (OwnerPlanning, error) {
		if masker.Mask("private-value") != "***" {
			t.Fatal("intake secret was unregistered at preparation")
		}
		planning, err := prepare(ctx, in, entry)
		planning.Resolved.SecretArgs = []string{"sdk-secret"}
		return planning, err
	}
	if _, err := host.Run(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.accepted.Seal.Resolved.SecretArgs, []string{"sdk-secret", "token"}) {
		t.Fatalf("secret classification=%v", f.accepted.Seal.Resolved.SecretArgs)
	}
}

// Self-claimed jobs retain their own funded deadline before the controller can disappear.
func TestOwnerHostStopsASelfClaimedJob(t *testing.T) {
	for _, lost := range []bool{true, false} {
		name := "funding refused"
		if lost {
			name = "controller connection lost"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f, h, in := newOwnerFixture(t, true)
				f.snapshot.Nodes = []store.NodeRow{{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Spec: json.RawMessage(`{"id":"job"}`), Status: store.StatusPending, SpecHash: "job-hash"}}
				h.Lease = lease.Keeper{Interval: time.Second, Lease: time.Hour}
				h.Decide = func(s store.ScheduleSnapshot) store.ScheduleDecision {
					if s.Nodes[0].Ready {
						return store.ScheduleDecision{}
					}
					return store.ScheduleDecision{Nodes: []store.NodeDecision{{NodeID: "job", Kind: store.DecisionReady}}}
				}
				h.Local = func(store.NodeRow) bool { return true }
				h.Layer = func(context.Context, LayerSpec, EvaluationInput, store.NodeRow, StorageBinding) (OwnerLayerResult, error) {
					return OwnerLayerResult{}, nil
				}
				deadline := time.Now().Add(time.Minute)
				started := make(chan struct{})
				stopped := make(chan time.Duration, 1)
				h.Executor = ownerNodeFunc(func(ctx context.Context, _ NodeInput) error {
					close(started)
					<-ctx.Done()
					stopped <- time.Until(deadline)
					return nil
				})
				var renewed bool
				h.Renew = func(context.Context, store.Actor, bool) (store.LeaseBeat, error) {
					return store.LeaseBeat{ExpiresAt: time.Now().Add(3 * time.Minute)}, nil
				}
				f.nodeRenew = func(context.Context, controllerwire.NodeRef, store.Actor) (store.LeaseBeat, error) {
					if lost && renewed {
						return store.LeaseBeat{}, &apierr.Error{Code: apierr.Unavailable, Message: "controller unreachable"}
					}
					cancelled := !lost && renewed
					renewed = true
					return store.LeaseBeat{ExpiresAt: deadline, CancelRequested: cancelled, CancelReason: "out of credits"}, nil
				}
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
				defer cancel()
				done := make(chan error, 1)
				go func() { _, err := h.Run(ctx, in); done <- err }()
				<-started
				left := <-stopped
				err := <-done
				if lost && (left > 0 || left < -time.Second) {
					t.Fatalf("self-claimed job stopped %v before its funded deadline", left)
				}
				if !lost && left < 59*time.Second {
					t.Fatalf("scheduling delayed cancellation by %v", time.Minute-left)
				}
				if err == nil {
					t.Fatal("owner finished normally after its execution stopped")
				}
			})
		})
	}
}
