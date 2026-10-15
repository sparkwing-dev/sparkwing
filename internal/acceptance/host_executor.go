package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type executionHostStore struct {
	claimedHostStore
	attempt int64
	reject  store.TransitionCause
}

func (s *executionHostStore) TransitionNode(_ context.Context, change store.NodeTransition) (store.Applied, error) {
	row := s.snapshot.Nodes[0]
	if change.Ref != row.Ref || change.Actor.Kind != store.ActorHolder || change.Actor.Holder != row.ClaimedBy || change.Actor.CapabilityPrefix != row.CapabilityPrefix || change.Actor.Generation != row.Generation {
		return store.Applied{}, errors.New("transition changed claim authority")
	}
	s.reports = append(s.reports, change)
	if change.Cause == s.reject {
		return store.Applied{}, errors.New("result transaction refused")
	}
	if change.Cause == store.CauseRetry {
		s.attempt++
	}
	return store.Applied{Generation: row.Generation, Attempt: s.attempt}, nil
}

type executionSDK struct {
	expansionSDK
	input nodehost.NodeInput
	mode  string
	trace *[]string
}

func (p *executionSDK) Request(ctx context.Context, request nodeproto.Request) (nodeproto.Reply, error) {
	var result any
	switch request.Op {
	case nodeproto.OpHello:
		if !reflect.DeepEqual(request.Binding, &p.input.RouteBinding) {
			return nodeproto.Reply{}, errors.New("hello lost its delegated binding")
		}
		return p.expansionSDK.Request(ctx, request)
	case nodeproto.OpEval:
		*p.trace = append(*p.trace, request.Closure)
		if request.Closure != "before" && request.Closure != "after" {
			return nodeproto.Reply{}, errors.New("executor evaluated an owner closure")
		}
		result = nodeproto.EvalResult{Value: json.RawMessage("null")}
		if p.mode == "before_refused" && request.Closure == "before" {
			result = nodeproto.EvalResult{Error: &apierr.Error{Code: apierr.SDKError, Message: "private hook refused", Hint: "private fix", Details: map[string]any{"output": "private source bytes"}}}
		}
	case nodeproto.OpRunStep:
		if request.Job != p.input.NodeID || int64(request.Attempt) != p.input.Attempt {
			return nodeproto.Reply{}, errors.New("body changed its award or ordinal")
		}
		*p.trace = append(*p.trace, fmt.Sprintf("body:%d", request.Attempt))
		outcome := "success"
		if p.mode == "retry" && request.Attempt < 3 {
			outcome = "failed"
		}
		result = nodeproto.RunResult{Outcome: outcome}
		if p.mode == "output_refused" {
			result = nodeproto.RunResult{Outcome: outcome, Output: json.RawMessage(`{"value":7}`)}
		}
	default:
		return p.expansionSDK.Request(ctx, request)
	}
	raw, err := json.Marshal(result)
	return nodeproto.Reply{ID: request.ID, OK: true, Result: raw}, err
}

func (p *executionSDK) Cleanup(ctx context.Context) error {
	if err := p.expansionSDK.Cleanup(ctx); err != nil {
		return err
	}
	if p.mode == "cleanup_refused" {
		return errors.New("private cleanup refused")
	}
	return nil
}

func executionHostFixture(t *testing.T, mode string, trace *[]string, processes *[]*executionSDK) (nodehost.NodeInput, *executionHostStore, *nodehost.ClaimedNodeHost) {
	t.Helper()
	job := nodeproto.JobSpec{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body", Risks: []string{"prod"}}},
		Closures: nodeproto.ClosureSpec{SkipIf: []string{"owner_skip"}, MemoKey: "owner_key", BeforeRun: []string{"before"}, AfterRun: []string{"after"}},
	}
	if mode == "output_refused" {
		job.ResultStep = "body"
	}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}}
	planHash, _ := expansionHash(plan)
	jobHash, _ := expansionHash(job)
	identity := engine.Identity{Version: "v1.0.0", ManifestDigest: "verified", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.0.0"}}
	resolved := engine.Resolved{Identity: identity}
	execution := runspec.JobExecutionSettings{}
	execution.Retry.Set, execution.Retry.Value = true, 2
	request := runspec.SealedRequest{RunID: "run", Request: runspec.Request{Pipeline: "build"}}
	request.Request.Options.RepoLocation.Set, request.Request.Options.RepoLocation.Value = true, "https://example.com/acme/repo.git"
	request.Request.Git.SHA, request.Request.Git.Repository.Key = strings.Repeat("a", 40), "example.com/acme/repo"
	spec := runspec.Spec{Hash: "spec", PlanHash: planHash, Request: request, Resolved: runspec.Resolved{Engine: identity, Settings: runspec.ExecutionSettings{Jobs: map[string]runspec.JobExecutionSettings{"job": execution}}}}
	if mode != "risk_refused" {
		spec.Resolved.Settings.Options.AllowRisk.Set, spec.Resolved.Settings.Options.AllowRisk.Value = true, []string{"prod"}
	}
	raw, _ := json.Marshal(spec)
	body, _ := json.Marshal(plan)
	jobBody, _ := json.Marshal(job)
	row := store.NodeRow{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Status: store.StatusPending, ClaimedBy: "runner", CapabilityPrefix: "claim", Generation: 2, Attempt: 1, Spec: jobBody, SpecHash: string(jobHash), Execution: execution}
	s := &executionHostStore{claimedHostStore: claimedHostStore{snapshot: store.RunSnapshot{Run: store.RunRow{ID: "run", Spec: &spec, PlanHash: string(planHash), Plan: body}, Nodes: []store.NodeRow{row}}}, attempt: 1}
	if mode == "result_refused" {
		s.reject = store.CauseReport
	}
	input := nodehost.NodeInput{Accepted: nodehost.AcceptedRun{RunID: "run", Spec: raw, SpecHash: "spec", PlanHash: string(planHash), Plan: plan}, NodeID: "job", Generation: 2, Attempt: 1, SpecHash: string(jobHash), Execution: execution, Engine: resolved, Authority: nodehost.ClaimAuthority{Holder: "runner", CapabilityPrefix: "claim"}, RouteBinding: nodeproto.ProcessBinding{RunID: "run", JobID: "job"}}
	input.RouteBinding.Routes.Bearer = "parent-authority"
	executor, err := nodehost.NewNodeExecutor(nodehost.ExecutorDependencies{
		Store: s, Mask: func(text string) string { return strings.ReplaceAll(text, "private", "***") },
		Bind: func(_ context.Context, in nodehost.NodeInput) (nodeproto.ProcessBinding, error) {
			binding := in.RouteBinding
			binding.Routes.Bearer = fmt.Sprintf("process-%d", in.Attempt)
			return binding, nil
		},
		Start: func(_ context.Context, in nodehost.NodeInput, _ nodehost.ExecutorActivity) (nodehost.ExecutorProcess, error) {
			process := &executionSDK{expansionSDK: expansionSDK{plan: plan}, input: in, mode: mode, trace: trace}
			if mode == "drift" {
				process.plan.Jobs = []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "changed"}}}}
			}
			*processes = append(*processes, process)
			return process, nil
		},
		EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
			return resolved, nil
		},
		CanonicalHash: expansionHash,
		ValidatePlan: func(raw json.RawMessage, _ nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
			var plan nodeproto.PlanDoc
			err := json.Unmarshal(raw, &plan)
			return plan, err
		},
		ChildContext: func(ctx context.Context, _, _ time.Duration) (context.Context, context.CancelFunc, error) {
			child, cancel := context.WithCancel(ctx)
			return child, cancel, nil
		},
		PrepareLayer: func(_ context.Context, layer nodehost.LayerSpec, _ nodehost.NodeInput, _ nodeproto.JobSpec) (nodehost.ExecutorFinalizer, error) {
			*trace = append(*trace, layer.Name+":in")
			return func(context.Context, *store.NodeResult) error { *trace = append(*trace, layer.Name+":out"); return nil }, nil
		},
		PublishOutput: func(context.Context, nodehost.NodeInput, json.RawMessage) (*blob.RecordKey, error) {
			return nil, errors.New("private output refused")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	host := &nodehost.ClaimedNodeHost{Store: s, Executor: executor, Lease: lease.Keeper{Interval: time.Minute, Lease: time.Hour}, Mask: func(string) string { return "masked" },
		Acquire: func(_ context.Context, in source.Spec, _ source.Options) (*source.Entry, error) {
			return &source.Entry{Engine: resolved, SHA: in.SHA, RepoKey: in.RepoKey}, nil
		},
	}
	return input, s, host
}

// CheckExecutorEnvelope drives the real claim host and executor with contract
// fakes. Native runners, storage transactions and route delegation remain separate proofs.
func CheckExecutorEnvelope(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "retry", "drift", "risk_refused", "before_refused", "cleanup_refused", "output_refused", "result_refused"} {
		t.Run(mode, func(t *testing.T) {
			var trace []string
			var processes []*executionSDK
			input, s, host := executionHostFixture(t, mode, &trace, &processes)
			err := host.Execute(t.Context(), input)
			ok := mode == "success" || mode == "retry"
			if (err == nil) != ok {
				t.Fatalf("trace=%v, error=%v", trace, err)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatalf("executor returned an unmasked error: %v", err)
			}
			var failure *apierr.Error
			if errors.As(err, &failure) {
				raw, marshalErr := json.Marshal(failure)
				if marshalErr != nil || strings.Contains(string(raw), "private") {
					t.Fatalf("structured executor error leaked: %s (%v)", raw, marshalErr)
				}
			}
			attempts := 1
			if mode == "retry" {
				attempts = 3
			}
			if len(processes) != attempts {
				t.Fatalf("processes=%d, want %d", len(processes), attempts)
			}
			for _, process := range processes {
				if !process.closed || !process.cleaned || process.input.RouteBinding.Routes.Bearer == input.RouteBinding.Routes.Bearer {
					t.Fatal("process authority was reused or cleanup was not joined")
				}
			}
			if mode == "retry" {
				want := []string{"artifacts:in", "caches:in", "before", "body:1", "body:2", "body:3", "after", "caches:out", "artifacts:out"}
				if !reflect.DeepEqual(trace, want) || len(s.reports) != 4 || s.reports[0].Cause != store.CauseStart || s.reports[1].Cause != store.CauseRetry || s.reports[2].Cause != store.CauseRetry {
					t.Fatalf("trace=%v, transitions=%+v", trace, s.reports)
				}
			}
			for _, step := range trace {
				if (mode == "drift" || mode == "risk_refused" || mode == "before_refused") && strings.HasPrefix(step, "body:") {
					t.Fatal("refused code reached the body")
				}
			}
			last := s.reports[len(s.reports)-1]
			want := store.NodeFailed
			if ok || mode == "result_refused" {
				want = store.NodeSuccess
			}
			if last.Cause != store.CauseReport || last.To != want || strings.Contains(last.Extra.Result.MaskedError, "private") {
				t.Fatalf("terminal request=%+v", last)
			}
		})
	}
}
