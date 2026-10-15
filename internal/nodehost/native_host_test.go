//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type nativeHostState struct {
	OwnerStore
	ClaimStore
}

type nativeOwnerState struct {
	OwnerStore
	*acquiredClaimStore
	mu       sync.Mutex
	owner    store.Actor
	changed  chan struct{}
	finished store.FinishRunRequest
}

func (s *nativeOwnerState) ReadSnapshot(ctx context.Context, runID string) (store.RunSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquiredClaimStore.ReadSnapshot(ctx, runID)
}

func (s *nativeOwnerState) ReadNode(ctx context.Context, ref store.NodeRef) (store.NodeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquiredClaimStore.ReadNode(ctx, ref)
}

func (s *nativeOwnerState) RenewOwner(_ context.Context, actor store.Actor, _ bool) (store.LeaseBeat, error) {
	if actor != s.owner {
		return store.LeaseBeat{}, errors.New("changed owner renewal")
	}
	return store.LeaseBeat{}, nil
}

func (s *nativeOwnerState) ReconcileNode(_ context.Context, request store.ReconcileRequest) (store.ReconciledNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if request.Ref != s.row.Ref || request.Actor != s.owner || s.row.ReadyAt == nil || s.row.ClaimedBy != "" {
		return store.ReconciledNode{}, errors.New("changed self claim")
	}
	expires := time.Now().Add(time.Hour)
	s.row.Generation++
	s.row.ClaimedBy, s.row.CapabilityPrefix, s.row.LeaseExpiresAt = "runner", "claim", &expires
	return store.ReconciledNode{Kind: store.Execute, Node: s.row, Claim: &store.ClaimAward{
		Holder: s.row.ClaimedBy, CapabilityPrefix: s.row.CapabilityPrefix, Generation: s.row.Generation, LeaseExpiresAt: expires,
	}}, nil
}

func (s *nativeOwnerState) TransitionNode(ctx context.Context, change store.NodeTransition) (store.Applied, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.acquiredClaimStore.TransitionNode(ctx, change)
	if err != nil {
		return result, err
	}
	s.row.StateSeq++
	switch change.To {
	case store.NodeReady:
		now := time.Now()
		s.row.ReadyAt = &now
	case store.NodeRunning:
		now := time.Now()
		s.row.Status, s.row.StartedAt = store.StatusRunning, &now
	default:
		now := time.Now()
		s.row.Status, s.row.Outcome, s.row.FinishedAt = store.StatusDone, change.To, &now
		s.row.ClaimedBy, s.row.CapabilityPrefix, s.row.LeaseExpiresAt = "", "", nil
	}
	select {
	case s.changed <- struct{}{}:
	default:
	}
	return result, nil
}

func (s *nativeOwnerState) FinishRunAndUnit(_ context.Context, request store.FinishRunRequest) (store.Applied, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if request.Unit != s.owner.Owner || s.row.Status != store.StatusDone || s.row.Outcome != store.NodeSuccess {
		return store.Applied{}, errors.New("unfinished owner")
	}
	s.finished = request
	return store.Applied{}, nil
}

func (s nativeHostState) ReadSnapshot(ctx context.Context, runID string) (store.RunSnapshot, error) {
	return s.ClaimStore.ReadSnapshot(ctx, runID)
}

func (s nativeHostState) TransitionNode(ctx context.Context, change store.NodeTransition) (store.Applied, error) {
	return s.ClaimStore.TransitionNode(ctx, change)
}

func nativeHostFixture(t *testing.T) (NativeHostConfig, NodeInput, *acquiredClaimStore, ClaimedNodeHost) {
	t.Helper()
	input, state, claimed, _ := claimedNodeFixture(t)
	execution := claimed.Executor.(*nodeExecutor).deps
	execution.Bind = func(context.Context, NodeInput) (ProcessGrant, error) {
		return ProcessGrant{}, errors.New("fixture delegation unavailable")
	}
	authorities, _, _, _, _ := processAuthorityFixture(t)
	t.Cleanup(func() { _ = authorities.Close() })
	config := NativeHostConfig{
		Store: nativeHostState{ClaimStore: state}, Lease: claimed.Lease,
		Source:       source.Options{Engines: claimEngine{resolved: input.Engine}},
		Masks:        NewMaskRegistry(secrets.NewMasker()),
		Router:       &serviceRegistrar{},
		Processes:    &NativeProcesses{Authorities: authorities, Log: func(context.Context, string, string, nodeproto.FrameLog) error { return nil }},
		ValidatePlan: execution.ValidatePlan, CanonicalHash: execution.CanonicalHash,
		Executor: execution,
		Evaluation: EvaluationDependencies{
			Bind: func(context.Context, EvaluationInput) (ProcessGrant, error) {
				return ProcessGrant{}, errors.New("fixture delegation unavailable")
			},
			ReadOutput: func(context.Context, store.NodeRef, int64) (json.RawMessage, error) {
				return json.RawMessage("null"), nil
			},
		},
		Planner: SDKPlanner{Log: func(context.Context, nodeproto.FrameLog) error { return nil }},
		Owner:   OwnerHost{Renew: func(context.Context, store.Actor, bool) (store.LeaseBeat, error) { return store.LeaseBeat{}, nil }, Wait: func(context.Context, store.RunSnapshot) error { return nil }},
	}
	return config, input, state, claimed
}

func TestNativeHostRunsSharedTransportForOwnerAndClaimedWork(t *testing.T) {
	for _, role := range []string{"owner", "claimed"} {
		t.Run(role, func(t *testing.T) {
			config, input, state, _ := nativeHostFixture(t)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			entry := &source.Entry{Engine: input.Engine, Directory: t.TempDir(), Command: []string{exe, "-test.run=^TestProtocolSessionChild$", "--", "claimed_node"}, SHA: state.run.Spec.Request.Request.Git.SHA, RepoKey: string(state.run.Spec.Request.Request.Git.Repository.Key)}
			authorities, parent, lookup, delegation, services := processAuthorityFixture(t)
			t.Cleanup(func() { _ = authorities.Close() })
			config.Processes.Authorities, config.Processes.URL = authorities, "http://host"
			config.Source.Base = procenv.Base{Captured: []string{"GORACE=atexit_sleep_ms=0"}}
			config.Processes.Session = ProtocolSessionConfig{Environments: sdkTestEnvironment(nil), ParseFrame: protocolTestCodec, ValidateRequest: func(nodeproto.Request) error { return nil }}
			delegated := 0
			config.Executor.Bind = func(ctx context.Context, input NodeInput) (ProcessGrant, error) {
				delegated++
				delegation.Binding = capability.Binding{Kind: capability.BindingNode, RunID: input.Accepted.RunID, NodeID: input.NodeID, Holder: input.Authority.Holder, Generation: input.Generation, Attempt: input.Attempt}
				parent.snapshot.Authorities[0].Principal.Capability.Binding = delegation.Binding
				parent.snapshot.Binding.Current = delegation.Binding
				services.RunID, services.Job = input.Accepted.RunID, input.Accepted.Plan.Jobs[0]
				return config.Processes.Delegate(ctx, lookup, delegation, *services)
			}
			var lifecycle *nativeOwnerState
			var run RunInput
			if role == "owner" {
				actor := store.Actor{Kind: store.ActorRunHost, Owner: store.OwnerLeaseRef{
					Kind: store.OwnerTrigger, Name: "trigger", RunID: input.Accepted.RunID, Holder: "owner", Generation: 2,
				}}
				state.run.Spec.Request.Hash = "request"
				state.run.Request = state.run.Spec.Request
				state.run.State, state.run.Owner = store.RunRunning, &actor.Owner
				state.row.ClaimedBy, state.row.CapabilityPrefix, state.row.LeaseExpiresAt = "", "", nil
				input.Accepted.Spec, err = json.Marshal(state.run.Spec)
				if err != nil {
					t.Fatal(err)
				}
				request, err := json.Marshal(state.run.Request.Request)
				if err != nil {
					t.Fatal(err)
				}
				run = RunInput{RunID: input.Accepted.RunID, Owner: actor, RequestHash: "request", SealedRequest: request, Accepted: &input.Accepted, GrantedRunner: input.GrantedRunner}
				lifecycle = &nativeOwnerState{acquiredClaimStore: state, owner: actor, changed: make(chan struct{}, 1)}
				config.Store = lifecycle
				config.Owner.Renew = lifecycle.RenewOwner
				config.Owner.Acquire = func(context.Context, RunInput) (*source.Entry, error) { return entry, nil }
				config.Owner.Local = func(store.NodeRow) bool { return true }
				config.Owner.Layer = func(context.Context, LayerSpec, EvaluationInput, store.NodeRow, StorageBinding) (OwnerLayerResult, error) {
					return OwnerLayerResult{}, nil
				}
				follower := StateFollower{Store: lifecycle, Subscribe: func(context.Context, string) (StateSubscription, error) {
					return StateSubscription{Changed: lifecycle.changed, Close: func() error { return nil }}, nil
				}}
				config.Owner.Wait, config.Owner.WaitNode = follower.Wait, follower.WaitNode
			}
			owner, claimed, _, err := NewNativeHost(config)
			if err != nil {
				t.Fatal(err)
			}
			if owner.Executor != claimed.Executor {
				t.Fatal("owner and claimed execution use different executors")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			wantChanges := 2
			if role == "owner" {
				var completion RunCompletion
				completion, err = owner.Run(ctx, run)
				if completion.Outcome != "success" || lifecycle.finished.Transition.To != store.RunSuccess {
					t.Fatalf("owner completion=%+v finish=%+v error=%v", completion, lifecycle.finished, err)
				}
				wantChanges++
			} else {
				claimed.Acquire = func(context.Context, source.Spec, source.Options) (*source.Entry, error) { return entry, nil }
				err = claimed.Execute(ctx, input)
			}
			if err != nil || delegated != 1 || len(state.changes) != wantChanges || state.changes[wantChanges-1].Extra.Result.Outcome != store.NodeSuccess {
				t.Fatalf("delegations=%d transitions=%+v error=%v", delegated, state.changes, err)
			}
			if config.Processes.computing(input.Accepted.RunID) {
				t.Fatal("host returned with active SDK compute")
			}
			authorities.mu.RLock()
			live := len(authorities.live)
			authorities.mu.RUnlock()
			if live != 0 {
				t.Fatal("host returned with a live delegated process")
			}
		})
	}
}

func TestNativeHostDefaultExpansionUsesSharedEvaluator(t *testing.T) {
	config, _, state, _ := nativeHostFixture(t)
	input, evaluationState, _, _ := evaluationFixture(t)
	state.executorStore = evaluationState
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	state.run.Spec = &spec
	refused := errors.New("evaluation delegation refused")
	config.Evaluation.Bind = func(context.Context, EvaluationInput) (ProcessGrant, error) { return ProcessGrant{}, refused }
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	body, failure, err := owner.Expand(t.Context(), input, state.row, StorageBinding{})
	if err != nil || len(body) != 0 || failure == nil || failure.MaskedError != refused.Error() {
		t.Fatalf("body=%s failure=%+v error=%v", body, failure, err)
	}
}

func TestNativeHostOpensApprovalThroughOwnerTransition(t *testing.T) {
	config, _, state, _ := nativeHostFixture(t)
	input, policyState, _, _ := policyFixture(t, func(job *nodeproto.JobSpec) {
		job.Approval, job.Steps = &nodeproto.ApprovalSpec{Message: "ship?"}, nil
	})
	state.executorStore = policyState
	config.Owner.Local = func(store.NodeRow) bool {
		t.Fatal("approval entered compute admission")
		return false
	}
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	request, err := owner.release(t.Context(), RunInput{GrantedRunner: input.GrantedRunner}, input.Accepted, input.Entry, state.row, store.DecisionApproval)
	if err != nil || request != nil || len(state.changes) != 1 || state.changes[0].To != store.NodeApprovalPending || state.changes[0].Cause != store.CauseApprovalOpen {
		t.Fatalf("request=%+v changes=%+v error=%v", request, state.changes, err)
	}
}

func TestNativeHostRequiresOnlyActiveOwnerPolicyAdapters(t *testing.T) {
	for _, mode := range []string{"remote", "local", "concurrency", "memo", "debug"} {
		t.Run(mode, func(t *testing.T) {
			config, _, state, _ := nativeHostFixture(t)
			input, policyState, _, _ := policyFixture(t, func(job *nodeproto.JobSpec) {
				if mode == "memo" {
					job.Memo, job.Closures.MemoKey = &nodeproto.MemoSpec{}, "key"
				}
			})
			state.executorStore = policyState
			if mode == "local" {
				config.Owner.Local = func(store.NodeRow) bool { return true }
			}
			if mode == "concurrency" {
				state.row.Execution.Concurrency = []runspec.Concurrency{{Group: "db", Capacity: 1, Cost: 1, Scope: "global", OnLimit: "queue"}}
			}
			if mode == "debug" {
				var spec runspec.Spec
				if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
					t.Fatal(err)
				}
				spec.Resolved.Settings.Options.Pause.Value.Before.Value = []string{input.NodeID}
				var err error
				input.Accepted.Spec, err = json.Marshal(spec)
				if err != nil {
					t.Fatal(err)
				}
			}
			owner, _, _, err := NewNativeHost(config)
			if err != nil {
				t.Fatal(err)
			}
			request, err := owner.release(t.Context(), RunInput{GrantedRunner: input.GrantedRunner}, input.Accepted, input.Entry, state.row, store.DecisionReady)
			if request != nil || (err != nil) != (mode != "remote") {
				t.Fatalf("request=%+v error=%v", request, err)
			}
			if mode == "remote" && (len(state.changes) != 1 || state.changes[0].To != store.NodeReady) || mode != "remote" && len(state.changes) != 0 {
				t.Fatalf("changes=%+v", state.changes)
			}
		})
	}
}

func TestNativeHostSkipPolicyUsesSharedEvaluator(t *testing.T) {
	config, _, state, _ := nativeHostFixture(t)
	input, policyState, _, _ := policyFixture(t, func(job *nodeproto.JobSpec) {
		job.Closures.SkipIf = []string{"guard"}
	})
	state.executorStore = policyState
	refused := errors.New("evaluation delegation refused")
	config.Evaluation.Bind = func(context.Context, EvaluationInput) (ProcessGrant, error) {
		return ProcessGrant{}, refused
	}
	var layers []string
	config.Owner.Layer = func(_ context.Context, layer LayerSpec, _ EvaluationInput, _ store.NodeRow, _ StorageBinding) (OwnerLayerResult, error) {
		layers = append(layers, layer.Name)
		return OwnerLayerResult{}, nil
	}
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	run := RunInput{GrantedRunner: input.GrantedRunner}
	request, err := owner.release(t.Context(), run, input.Accepted, input.Entry, state.row, store.DecisionReady)
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.SDKError || failure.Message != refused.Error() || request != nil || !reflect.DeepEqual(layers, []string{"debug"}) || len(state.changes) != 0 {
		t.Fatalf("request=%+v layers=%v changes=%+v error=%v", request, layers, state.changes, err)
	}
}

func TestNativeHostSkipPolicyJoinsEvaluationBeforeTransition(t *testing.T) {
	config, _, state, _ := nativeHostFixture(t)
	input, policyState, _, _ := policyFixture(t, func(job *nodeproto.JobSpec) { *job = ownerPolicyJob() })
	state.executorStore = policyState
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input.Entry.Command = []string{exe, "-test.run=^TestProtocolSessionChild$", "--", "owner_policy"}
	authorities, parent, lookup, delegation, services := processAuthorityFixture(t)
	t.Cleanup(func() { _ = authorities.Close() })
	config.Processes.Authorities, config.Processes.URL = authorities, "http://host"
	config.Processes.Session = ProtocolSessionConfig{Environments: sdkTestEnvironment(nil), ParseFrame: protocolTestCodec}
	config.Source.Base = procenv.Base{Captured: []string{"GORACE=atexit_sleep_ms=0"}}
	deadlines := make(chan context.CancelCauseFunc, 2)
	config.Evaluation.ChildContext = func(ctx context.Context, _, _ time.Duration) (context.Context, context.CancelFunc, error) {
		bounded, cancel := context.WithCancelCause(ctx)
		deadlines <- cancel
		return bounded, func() { cancel(context.Canceled) }, nil
	}
	var diagnostics []nodeproto.FrameLog
	config.Processes.Log = func(_ context.Context, _, _ string, record nodeproto.FrameLog) error {
		if record.Message == "slow entered" {
			(<-deadlines)(context.DeadlineExceeded)
		}
		if record.Level == "warn" {
			diagnostics = append(diagnostics, record)
		}
		return nil
	}
	delegated := 0
	config.Evaluation.Bind = func(ctx context.Context, input EvaluationInput) (ProcessGrant, error) {
		delegated++
		delegation.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: input.Accepted.RunID, NodeID: input.NodeID, Holder: "owner", Generation: 4}
		parent.snapshot.Authorities[0].Principal.Capability.Binding = delegation.Binding
		parent.snapshot.Binding.Current = delegation.Binding
		services.RunID, services.Job = input.Accepted.RunID, input.Accepted.Plan.Jobs[0]
		return config.Processes.Delegate(ctx, lookup, delegation, *services)
	}
	var layers []string
	config.Owner.Layer = func(_ context.Context, layer LayerSpec, _ EvaluationInput, _ store.NodeRow, _ StorageBinding) (OwnerLayerResult, error) {
		layers = append(layers, layer.Name)
		return OwnerLayerResult{}, nil
	}
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	run := RunInput{GrantedRunner: input.GrantedRunner}
	request, err := owner.release(ctx, run, input.Accepted, input.Entry, state.row, store.DecisionReady)
	if err != nil || request != nil || delegated != 2 || !reflect.DeepEqual(layers, []string{"debug"}) || len(state.changes) != 1 || state.changes[0].To != store.NodeSkipped {
		t.Fatalf("request=%+v layers=%v changes=%+v delegations=%d error=%v", request, layers, state.changes, delegated, err)
	}
	if len(diagnostics) != 1 || string(diagnostics[0].Attrs) != `{"predicate":0}` {
		t.Fatalf("timeout diagnostics=%+v", diagnostics)
	}
	authorities.mu.RLock()
	live := len(authorities.live)
	authorities.mu.RUnlock()
	if live != 0 {
		t.Fatal("owner transition retained a delegated process")
	}
}

func TestNativeHostRegistersServicesAgainstLiveProcessAuthority(t *testing.T) {
	config, _, _, _ := nativeHostFixture(t)
	authorities, _, parent, delegation, services := processAuthorityFixture(t)
	t.Cleanup(func() { _ = authorities.Close() })
	config.Processes.Authorities, config.Processes.URL = authorities, "http://host"
	if _, _, _, err := NewNativeHost(config); err != nil {
		t.Fatal(err)
	}
	grant, err := config.Processes.Delegate(t.Context(), parent, delegation, *services)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = grant.Revoke() }()
	snapshot, err := authorities.ReadAuthentication(t.Context(), processLookup(capability.Issued{Raw: grant.Binding.Routes.Bearer}), capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	authorized := capability.Authorized{Principal: snapshot.Authorities[0].Principal, Binding: delegation.Binding}
	_, err = callService(t, config.Router.(*serviceRegistrar), authorized, "/node/v1/masks", `{"value":"native-service-secret"}`)
	if err != nil || config.Masks.masker.Mask("native-service-secret") != "***" {
		t.Fatal("registered handler lost the native process service registry:", err)
	}
}
