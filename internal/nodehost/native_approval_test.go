//go:build unix

package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestNativeApprovalPredicateSkipsWithoutJobCapacityOrCompute(t *testing.T) {
	job := nodeproto.JobSpec{ID: "gate", Approval: &nodeproto.ApprovalSpec{Message: "ship?"}, Closures: nodeproto.ClosureSpec{SkipIf: []string{"guard"}}}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}}
	f := newStoreHostFixture(t, plan)
	ctx := t.Context()
	var spec runspec.Spec
	if err := json.Unmarshal(f.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	selected := engine.Resolved{Identity: spec.Resolved.Engine}
	daemon := newFakeDaemon()
	var process *executorProcess
	evaluator, err := newOwnerEvaluator(evaluationDependencies{
		Store: f.State, Masker: testMasker(),
		Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}},
		EnsureEngine: func(context.Context, engineid.Identity, releaseasset.Target) (engine.Resolved, error) {
			return selected, nil
		},
		Start: func(context.Context, EvaluationInput) (ExecutorProcess, error) {
			capacity, err := f.Tenant.GetConcurrencyState(ctx, "job:global:approval")
			if !errors.Is(err, store.ErrNotFound) && (err != nil || len(capacity.Holders) != 0 || capacity.UsedCost != 0) {
				t.Errorf("approval predicate held job groups: %+v, %v", capacity, err)
			}
			if live, granted, released := daemon.counts(); live != 0 || granted != 0 || released != 0 {
				t.Errorf("approval predicate acquired compute: %d/%d/%d", live, granted, released)
			}
			process = &executorProcess{plan: plan, answer: func(_ context.Context, request nodeproto.Request) (any, error) {
				if request.Op == nodeproto.OpEval {
					return nodeproto.EvalResult{Value: json.RawMessage("true")}, nil
				}
				return nil, nil
			}}
			return executorCleanupProcess{process, func(context.Context) error {
				if live, _, released := daemon.counts(); live != 0 || released != 0 {
					t.Error("approval released compute before its process joined")
				}
				return nil
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.Host.executionAdmission = ComposeStartGate(f.Tenant, &LocalAdmission{Daemon: daemon}, false)
	f.Host.SkipPolicy = func(ctx context.Context, in EvaluationInput, row runstate.NodeRow, _ StorageBinding) (ownerLayerResult, error) {
		return evaluator.skipLayer(ctx, in, row, func(context.Context, string, string, nodeproto.FrameLog) error { return nil })
	}
	result, err := f.Host.Run(ctx, f.Input)
	if err != nil || result.Outcome != "success" {
		t.Fatalf("approval bypass: %+v, %v", result, err)
	}
	row, err := f.Tenant.ReadNode(ctx, runspec.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: runspec.NodeID(job.ID)})
	if err != nil || row.Status != runstate.StatusDone || row.Outcome != runstate.NodeSkipped || row.Attempt != 0 {
		t.Fatalf("approval bypass created execution: %+v, %v", row, err)
	}
	if process == nil || process.closed != 1 || process.cleaned != 1 {
		t.Fatal("approval predicate process was not joined")
	}
	if live, granted, released := daemon.counts(); live != 0 || granted != 0 || released != 0 {
		t.Fatalf("approval retained compute: %d/%d/%d", live, granted, released)
	}
	capacity, err := f.Tenant.GetConcurrencyState(ctx, "job:global:approval")
	if !errors.Is(err, store.ErrNotFound) && (err != nil || len(capacity.Holders) != 0 || len(capacity.Waiters) != 0) {
		t.Fatalf("approval bypass retained its group: %+v, %v", capacity, err)
	}
}

func TestControllerApprovalEvaluationRunsPredicateAndCommitsTypedResult(t *testing.T) {
	for _, mode := range []string{"open", "skip", "fail"} {
		t.Run(mode, func(t *testing.T) {
			job := nodeproto.JobSpec{ID: "gate", Approval: &nodeproto.ApprovalSpec{Message: "ship?"}, Closures: nodeproto.ClosureSpec{SkipIf: []string{"guard"}}}
			f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if _, err := teststore.SQL(t, f.Store).ExecContext(ctx, `UPDATE runs SET dispatch='controller' WHERE id=?`, f.Input.RunID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Store.AsOperator().ReconcileControllerRuns(ctx, "", 100); err != nil {
				t.Fatal(err)
			}
			award, err := f.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: f.Control.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitEval, RunID: runspec.RunID(f.Input.RunID), NodeID: "gate", SourceGeneration: 0}})
			if err != nil {
				t.Fatal(err)
			}
			principal, err := f.Store.AsOperator().LookupCapability(ctx, sha256.Sum256(award.Bearer))
			if err != nil || principal.Capability.Binding.Expansion == nil {
				t.Fatalf("eval binding=%+v error=%v", principal.Capability.Binding, err)
			}
			input := f.Input
			input.Owner = runstate.Actor{Kind: runstate.ActorRunHost, Authority: principal.Authority, CapabilityPrefix: award.Award.CapabilityPrefix, Owner: award.Owner}
			var accepted runspec.Spec
			if err := json.Unmarshal(f.Accepted.Spec, &accepted); err != nil {
				t.Fatal(err)
			}
			selected := engine.Resolved{Identity: accepted.Resolved.Engine}
			var process *executorProcess
			evaluator, err := newOwnerEvaluator(evaluationDependencies{Store: f.State, Masker: testMasker(), Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, EnsureEngine: func(context.Context, engineid.Identity, releaseasset.Target) (engine.Resolved, error) {
				return selected, nil
			}, Start: func(context.Context, EvaluationInput) (ExecutorProcess, error) {
				if state, err := f.Tenant.GetConcurrencyState(ctx, "job:global:approval"); !errors.Is(err, store.ErrNotFound) && (err != nil || len(state.Holders) != 0) {
					t.Fatalf("approval evaluator held job capacity: %+v error=%v", state, err)
				}
				process = &executorProcess{plan: f.Accepted.Plan, answer: func(_ context.Context, request nodeproto.Request) (any, error) {
					if request.Op != nodeproto.OpEval {
						return nil, nil
					}
					if mode == "fail" {
						return nodeproto.EvalResult{Error: &apierr.Error{Code: apierr.SDKError, Message: "predicate failed"}}, nil
					}
					return nodeproto.EvalResult{Value: json.RawMessage(map[bool]string{false: "false", true: "true"}[mode == "skip"])}, nil
				}}
				return process, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			f.Host.SkipPolicy = func(ctx context.Context, in EvaluationInput, row runstate.NodeRow, _ StorageBinding) (ownerLayerResult, error) {
				return evaluator.skipLayer(ctx, in, row, func(context.Context, string, string, nodeproto.FrameLog) error { return nil })
			}
			f.Host.executionAdmission.compute = &LocalAdmission{Daemon: admissionAcquireFunc(func(context.Context, wingwire.AdmissionRequest, func(wingwire.Queued)) (Lease, error) {
				t.Fatal("approval evaluator acquired compute")
				return nil, nil
			})}
			if _, err := f.Store.AsOperator().RenewUnit(ctx, award.Award.CapabilityPrefix, true); err != nil {
				t.Fatalf("eval renewal: %v", err)
			}
			if err := f.Host.Evaluate(ctx, input, "gate", 0, *principal.Capability.Binding.Expansion); err != nil {
				t.Fatal(err)
			}
			row := capacityRow(t, f, "gate")
			if mode == "open" && row.Status != runstate.StatusApprovalPending || mode == "skip" && row.Outcome != runstate.NodeSkipped || mode == "fail" && row.Outcome != runstate.NodeFailed || row.Attempt != 0 {
				t.Fatalf("mode=%s result=%+v", mode, row)
			}
			var status string
			if err := teststore.SQL(t, f.Store).QueryRowContext(ctx, `SELECT status FROM eval_units WHERE run_id=? AND source_node_id='gate' AND source_generation=0`, f.Input.RunID).Scan(&status); err != nil || status != "done" {
				t.Fatalf("eval status=%s error=%v", status, err)
			}
			if process == nil || process.closed != 1 || process.cleaned != 1 {
				t.Fatal("approval evaluator process did not join")
			}
		})
	}
}

func TestNativeApprovalPredicateReadsDeclaredDependencyOnBothPlacements(t *testing.T) {
	testNativeApprovalPredicateReads(t, "approvalfixture", false)
}

func TestNativeApprovalPredicateReadsPipelineReferenceOnBothPlacements(t *testing.T) {
	testNativeApprovalPredicateReads(t, "approvalreferencefixture", true)
}

func testNativeApprovalPredicateReads(t *testing.T, fixture string, reference bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	bin := buildNativeSDKFixture(t, ctx, fixture)
	runner := nodeproto.RunnerIdentity{Name: "host", Type: "local", Labels: []string{}}
	hello, plan := planNativeSDKFixture(t, ctx, bin, "p", map[string]string{}, nodeproto.RunContext{RunID: "describe", Pipeline: "p"}, runner)
	selected := engine.Resolved{Identity: engineid.Identity{Version: "fixture", ManifestDigest: "fixture", Pin: engineid.Pin{SDK: engineid.GoSDK, Version: hello.SDK.Version}}}
	for _, placement := range []string{"host", "controller"} {
		t.Run(placement, func(t *testing.T) {
			request := runspec.Request{Version: 1, Team: string(store.DefaultTeam), Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}}
			f := newStoreHostFixtureForEngine(t, openStore(t), plan, request, selected.Identity)
			// The output service below supplies the selected dependency's recorded payload.
			if _, err := teststore.SQL(t, f.Store).ExecContext(ctx, `UPDATE nodes SET status='done',outcome='success',finished_at=? WHERE run_id=? AND node_id='build'`, teststore.Now(f.Store).UnixNano(), f.Input.RunID); err != nil {
				t.Fatal(err)
			}
			principal, err := f.Store.AsOperator().LookupCapability(ctx, sha256.Sum256(f.OwnerBearer))
			if err != nil {
				t.Fatal(err)
			}
			input := f.Input
			if placement == "controller" {
				if _, err := teststore.SQL(t, f.Store).ExecContext(ctx, `UPDATE runs SET dispatch='controller' WHERE id=?`, f.Input.RunID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.Store.AsOperator().ReconcileControllerRuns(ctx, "", 100); err != nil {
					t.Fatal(err)
				}
				award, err := f.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: f.Control.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitEval, RunID: runspec.RunID(f.Input.RunID), NodeID: "gate"}})
				if err != nil {
					t.Fatal(err)
				}
				principal, err = f.Store.AsOperator().LookupCapability(ctx, sha256.Sum256(award.Bearer))
				if err != nil {
					t.Fatal(err)
				}
				input.Owner = runstate.Actor{Kind: runstate.ActorRunHost, Authority: principal.Authority, CapabilityPrefix: award.Award.CapabilityPrefix, Owner: award.Owner}
			}
			authorities, err := NewProcessAuthorities(ProcessAuthorityDependencies{Parent: f.Store.AsOperator(), Bindings: f.Store.AsOperator()})
			if err != nil {
				t.Fatal(err)
			}
			defer authorities.Close()
			router := routepolicy.New(routepolicy.Authorization{Authenticator: capability.NewAuthenticator(authorities), Bindings: authorities})
			if err := RegisterProcessServices(router, authorities.Resolve); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(router)
			defer server.Close()
			processes := &NativeProcesses{Authorities: authorities, Masks: NewMaskRegistry(secrets.NewMasker()), URL: server.URL}
			var reads atomic.Int32
			claims := &nodeClaims{processes: processes, readNode: f.Tenant.ReadNode, current: func(context.Context) (capability.Principal, error) { return principal, nil }, services: func(ctx context.Context, p capability.Principal, job nodeproto.JobSpec) (ProcessServices, error) {
				service, err := completeServiceFixture(ctx, p, job)
				service.Output = func(_ context.Context, auth capability.Authorized, job string) (json.RawMessage, error) {
					if job != "build" || !reflect.DeepEqual(auth.Binding, principal.Capability.Binding) {
						return nil, errors.New("dependency read lost its exact evaluation fence")
					}
					reads.Add(1)
					return json.RawMessage("true"), nil
				}
				service.Latest = func(_ context.Context, auth capability.Authorized, pipeline, job string, _ time.Duration) (ProcessOutput, error) {
					want := capability.PipelineReferenceTarget{Pipeline: "upstream", JobID: "artifact"}
					if !reference || pipeline != want.Pipeline || job != want.JobID || auth.Target.Reference == nil || *auth.Target.Reference != want || auth.Target.RunID != "" || auth.Target.NodeID != "" || !reflect.DeepEqual(auth.Binding, principal.Capability.Binding) {
						return ProcessOutput{}, errors.New("pipeline reference mixed caller identity with its resource or lost its exact evaluation fence")
					}
					reads.Add(1)
					return ProcessOutput{RunID: "upstream-run", Output: json.RawMessage("true")}, nil
				}
				return service, err
			}}
			evaluator, err := newOwnerEvaluator(evaluationDependencies{Store: f.State, Bind: claims.evaluate, Masker: testMasker(), Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, EnsureEngine: func(context.Context, engineid.Identity, releaseasset.Target) (engine.Resolved, error) {
				return selected, nil
			}, Start: func(ctx context.Context, in EvaluationInput) (ExecutorProcess, error) {
				return startProtocolSession(ctx, ProtocolSessionConfig{Command: []string{bin}, Environment: testProcessEnv(t, "PATH="+os.Getenv("PATH")), Revoke: in.processRevoke})
			}})
			if err != nil {
				t.Fatal(err)
			}
			f.Host.SkipPolicy = func(ctx context.Context, in EvaluationInput, row runstate.NodeRow, _ StorageBinding) (ownerLayerResult, error) {
				return evaluator.skipLayer(ctx, in, row, func(context.Context, string, string, nodeproto.FrameLog) error { return nil })
			}
			if placement == "controller" {
				if err := f.Host.Evaluate(ctx, input, "gate", 0, *principal.Capability.Binding.Expansion); err != nil {
					t.Fatal(err)
				}
			} else {
				if result, err := f.Host.Run(ctx, input); err != nil || result.Outcome != "success" {
					t.Fatalf("run=%+v error=%v", result, err)
				}
			}
			row := capacityRow(t, f, "gate")
			if row.Outcome != runstate.NodeSkipped || row.Attempt != 0 || reads.Load() != 1 {
				t.Fatalf("approval=%+v dependency reads=%d", row, reads.Load())
			}
		})
	}
}
