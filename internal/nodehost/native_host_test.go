//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type nativeHostState struct {
	OwnerStore
	ClaimStore
}

func (s nativeHostState) RenewNodeClaim(ctx context.Context, ref controllerwire.NodeRef, actor store.Actor) (store.LeaseBeat, error) {
	return s.ClaimStore.RenewNodeClaim(ctx, ref, actor)
}

func (s nativeHostState) ReadSnapshot(ctx context.Context, runID string) (store.RunSnapshot, error) {
	return s.ClaimStore.ReadSnapshot(ctx, runID)
}

func (s nativeHostState) TransitionNode(ctx context.Context, change store.NodeCommand) (store.Applied, error) {
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
			ReadOutput: func(context.Context, controllerwire.NodeRef, int64) (json.RawMessage, error) {
				return json.RawMessage("null"), nil
			},
		},
		Planner: SDKPlanner{Log: func(context.Context, nodeproto.FrameLog) error { return nil }},
		Owner:   OwnerHost{Renew: func(context.Context, store.Actor, bool) (store.LeaseBeat, error) { return store.LeaseBeat{}, nil }, Wait: func(context.Context, store.RunSnapshot) error { return nil }},
	}
	return config, input, state, claimed
}

func persistNativeAcceptance(t *testing.T, state *acquiredClaimStore, accepted AcceptedRun) {
	t.Helper()
	var spec runspec.Spec
	if err := json.Unmarshal(accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(accepted.Plan)
	if err != nil {
		t.Fatal(err)
	}
	state.run.ID, state.run.Request, state.run.Spec, state.run.PlanHash, state.run.Plan = accepted.RunID, spec.Request, &spec, accepted.PlanHash, raw
}

func TestNativeHostDefaultExpansionUsesSharedEvaluator(t *testing.T) {
	config, _, state, _ := nativeHostFixture(t)
	input, evaluationState, _, _ := evaluationFixture(t)
	state.executorStore = evaluationState
	persistNativeAcceptance(t, state, input.Accepted)
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	state.run.Spec = &spec
	refused := errors.New("evaluation delegation refused")
	bound := false
	config.Evaluation.Bind = func(context.Context, EvaluationInput) (ProcessGrant, error) {
		bound = true
		return ProcessGrant{}, refused
	}
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	body, _, failure, err := owner.Expand(t.Context(), input, state.row, StorageBinding{})
	if !bound || err != nil || len(body) != 0 || failure == nil || failure.MaskedError != "generator failed (details withheld: secret declarations unavailable)" {
		t.Fatalf("body=%s failure=%+v error=%v", body, failure, err)
	}
}

func TestNativeHostOpensApprovalThroughOwnerTransition(t *testing.T) {
	config, _, state, _ := nativeHostFixture(t)
	input, policyState, _, _ := policyFixture(t, func(job *nodeproto.JobSpec) {
		job.Approval, job.Steps = &nodeproto.ApprovalSpec{Message: "ship?"}, nil
	})
	state.executorStore = policyState
	persistNativeAcceptance(t, state, input.Accepted)
	config.Owner.Local = func(store.NodeRow) bool {
		t.Fatal("approval entered compute admission")
		return false
	}
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	request, err := releasedRequest(owner, t.Context(), RunInput{GrantedRunner: input.GrantedRunner}, input.Accepted, input.Entry, state.row, store.DecisionApproval)
	if err != nil || request != nil || len(state.changes) != 1 || state.changes[0].To != store.NodeApprovalPending || state.changes[0].Cause != store.CauseApprovalOpen {
		t.Fatalf("request=%+v changes=%+v error=%v", request, state.changes, err)
	}
}

func TestNativeHostRequiresOnlyActiveOwnerPolicyAdapters(t *testing.T) {
	for _, mode := range []string{"remote", "local", "concurrency", "memo", "debug", "debug-after"} {
		t.Run(mode, func(t *testing.T) {
			config, _, state, _ := nativeHostFixture(t)
			input, policyState, _, _ := policyFixture(t, func(job *nodeproto.JobSpec) {
				if mode == "memo" {
					job.Memo, job.Closures.MemoKey = &nodeproto.MemoSpec{}, "key"
				}
			})
			state.executorStore = policyState
			persistNativeAcceptance(t, state, input.Accepted)
			if mode == "local" {
				config.Owner.Local = func(store.NodeRow) bool { return true }
			}
			if mode == "concurrency" {
				state.row.Execution.Concurrency = []runspec.Concurrency{{Group: "db", Capacity: 1, Cost: 1, Scope: "global", OnLimit: "queue"}}
			}
			if mode == "debug" || mode == "debug-after" {
				var spec runspec.Spec
				if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
					t.Fatal(err)
				}
				if mode == "debug" {
					spec.Resolved.Settings.Options.Pause.Value.Before.Value = []string{input.NodeID}
				} else {
					spec.Resolved.Settings.Options.Pause.Value.After.Value = []string{input.NodeID}
				}
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
			request, err := releasedRequest(owner, t.Context(), RunInput{GrantedRunner: input.GrantedRunner}, input.Accepted, input.Entry, state.row, store.DecisionReady)
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
	persistNativeAcceptance(t, state, input.Accepted)
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
	request, err := releasedRequest(owner, t.Context(), run, input.Accepted, input.Entry, state.row, store.DecisionReady)
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.SDKError || failure.Message != refused.Error() || request != nil || len(layers) != 0 || len(state.changes) != 0 {
		t.Fatalf("request=%+v layers=%v changes=%+v error=%v", request, layers, state.changes, err)
	}
}

func TestNativeHostSkipPolicyJoinsEvaluationBeforeTransition(t *testing.T) {
	config, _, state, _ := nativeHostFixture(t)
	input, policyState, _, _ := policyFixture(t, func(job *nodeproto.JobSpec) { *job = ownerPolicyJob() })
	state.executorStore = policyState
	persistNativeAcceptance(t, state, input.Accepted)
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
	request, err := releasedRequest(owner, ctx, run, input.Accepted, input.Entry, state.row, store.DecisionReady)
	if err != nil || request != nil || delegated != 2 || len(layers) != 0 || len(state.changes) != 1 || state.changes[0].To != store.NodeSkipped {
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
