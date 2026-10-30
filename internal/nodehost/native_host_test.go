//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type nativeHostState struct {
	ownerDebugStore
	OwnerStore
	ClaimStore
}

func (s nativeHostState) ReleaseEvaluation(context.Context, runspec.NodeRef, runstate.Actor) (runstate.Applied, error) {
	return runstate.Applied{}, nil
}

func (s nativeHostState) RenewNodeClaim(ctx context.Context, ref runspec.NodeRef, actor runstate.Actor) (runstate.LeaseBeat, error) {
	return s.ClaimStore.RenewNodeClaim(ctx, ref, actor)
}

func (s nativeHostState) ReadSnapshot(ctx context.Context, runID string) (store.RunSnapshot, error) {
	return s.ClaimStore.ReadSnapshot(ctx, runID)
}

func (s nativeHostState) TransitionNode(ctx context.Context, change store.NodeCommand) (runstate.Applied, error) {
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
		Admission: ComposeStartGate(state, nil, false),
		Store:     nativeHostState{ClaimStore: state}, Lease: claimed.Lease, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal},
		Source:    source.Options{Engines: claimEngine{resolved: input.Engine}},
		Masks:     NewMaskRegistry(secrets.NewMasker()),
		Router:    &serviceRegistrar{},
		Processes: &NativeProcesses{Authorities: authorities, Log: func(context.Context, string, string, nodeproto.FrameLog) error { return nil }},

		Executor: executionOperationsFixture(execution),
		ReadOutput: func(context.Context, runspec.NodeRef, int64) (json.RawMessage, error) {
			return json.RawMessage("null"), nil
		},
		Evaluation: EvaluationOperations{
			Bind: func(context.Context, EvaluationInput) (ProcessGrant, error) {
				return ProcessGrant{}, errors.New("fixture delegation unavailable")
			},
		},
		PlanningLog: func(context.Context, nodeproto.FrameLog) error { return nil },
		Owner: OwnerOperations{Renew: func(context.Context, runstate.Actor, bool) (runstate.LeaseBeat, error) {
			return runstate.LeaseBeat{}, nil
		}, Wait: func(context.Context, store.RunSnapshot) error { return nil }},
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
	config.Owner.Local = func(runstate.NodeRow) bool {
		t.Fatal("approval entered compute admission")
		return false
	}
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	request, err := releasedRequest(owner, t.Context(), RunInput{GrantedRunner: input.GrantedRunner}, input.Accepted, input.Entry, state.row, store.DecisionApproval)
	if err != nil || request != nil || len(state.changes) != 1 || state.changes[0].To != runstate.NodeApprovalPending || state.changes[0].Cause != runstate.CauseApprovalOpen {
		t.Fatalf("request=%+v changes=%+v error=%v", request, state.changes, err)
	}
}

func TestNativeHostRequiresOnlyActiveOwnerPolicyAdapters(t *testing.T) {
	for _, mode := range []string{"remote", "local", "concurrency", "debug", "debug-after"} {
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
				config.Owner.Local = func(runstate.NodeRow) bool { return true }
				config.Admission = ComposeStartGate(nil, nil, true)
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
			if (request != nil) != (mode == "local") || (err != nil) != (mode == "debug" || mode == "debug-after") {
				t.Fatalf("request=%+v error=%v", request, err)
			}
			if (mode == "remote" || mode == "concurrency" || mode == "local") && (len(state.changes) != 1 || state.changes[0].To != runstate.NodeReady) || (mode == "debug" || mode == "debug-after") && len(state.changes) != 0 {
				t.Fatalf("changes=%+v", state.changes)
			}
		})
	}
}

func TestNativeHostSkipPolicyUsesSharedEvaluator(t *testing.T) {
	config, _, state, _ := nativeHostFixture(t)
	input, policyState, _, _ := policyFixture(t, func(job *nodeproto.JobSpec) {
		job.Approval, job.Steps = &nodeproto.ApprovalSpec{Message: "ship?"}, nil
		job.Closures.SkipIf = []string{"guard"}
	})
	state.executorStore = policyState
	persistNativeAcceptance(t, state, input.Accepted)
	refused := errors.New("evaluation delegation refused")
	config.Evaluation.Bind = func(context.Context, EvaluationInput) (ProcessGrant, error) {
		return ProcessGrant{}, refused
	}
	var layers []string
	setTestNativePolicies(&config.Owner, func(_ context.Context, layer testPolicyName, _ EvaluationInput, _ runstate.NodeRow, _ StorageBinding) (ownerLayerResult, error) {
		layers = append(layers, layer.Name)
		return ownerLayerResult{}, nil
	})
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	run := RunInput{GrantedRunner: input.GrantedRunner}
	request, err := releasedRequest(owner, t.Context(), run, input.Accepted, input.Entry, state.row, store.DecisionReady)
	if err != nil || request != nil || len(layers) != 0 || len(state.changes) != 1 || state.changes[0].To != runstate.NodeFailed || state.changes[0].Extra.Result.MaskedError != refused.Error() {
		t.Fatalf("request=%+v layers=%v changes=%+v error=%v", request, layers, state.changes, err)
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
	bound, resolveErr := authorities.processServices(t.Context(), grant.Binding)
	if err != nil || resolveErr != nil || bound.Masks.masker.Mask("native-service-secret") != "***" || config.Masks.masker.Mask("native-service-secret") != "native-service-secret" {
		t.Fatal("registered handler lost the native process service registry:", err)
	}
}

func TestNativeHostIssuedMaskSurvivesReplanAndRunStep(t *testing.T) {
	config, input, _, _ := nativeHostFixture(t)
	config.Validation.Placement = nodeproto.PlacementLocal
	_, claimed, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Masks.initializeSeal(input.Accepted); err != nil {
		t.Fatal(err)
	}
	if err := config.Masks.acceptPlan(input.Accepted.Plan, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := config.Masks.registerIssued("build"); err != nil {
		t.Fatal(err)
	}
	executor := claimed.Executor.(*nodeExecutor)
	process := &executorProcess{plan: input.Accepted.Plan, result: nodeproto.RunResult{Outcome: "success"}}
	executor.deps.Start = func(context.Context, NodeInput, ExecutorActivity) (ExecutorProcess, error) { return process, nil }
	executor.deps.Bind = func(context.Context, NodeInput) (ProcessGrant, error) {
		return ProcessGrant{Binding: input.RouteBinding, Revoke: func() error { return nil }}, nil
	}
	input.Entry = &source.Entry{Engine: input.Engine}
	if err := executor.Execute(t.Context(), input); err != nil {
		t.Fatalf("issued value invalidated accepted execution: %v", err)
	}
	if !slices.ContainsFunc(process.requests, func(request nodeproto.Request) bool { return request.Op == nodeproto.OpRunStep }) {
		t.Fatal("accepted step did not run")
	}
	if config.Masks.masker.Mask("build") != "***" {
		t.Fatal("issued value lost its presentation mask")
	}
}
