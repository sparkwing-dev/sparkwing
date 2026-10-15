package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type policySDK struct {
	expansionSDK
	value json.RawMessage
	fail  bool
}

func (p *policySDK) Request(ctx context.Context, request nodeproto.Request) (nodeproto.Reply, error) {
	if request.Op != nodeproto.OpEval {
		return p.expansionSDK.Request(ctx, request)
	}
	if request.Job != "gate" || request.Attempt != 0 || len(request.Input) != 0 || request.Closure != "condition" {
		return nodeproto.Reply{}, errors.New("policy changed its accepted identity")
	}
	p.calls = append(p.calls, request.Closure)
	result := nodeproto.EvalResult{Value: p.value}
	if p.fail {
		result = nodeproto.EvalResult{Error: &apierr.Error{Code: apierr.SDKError, Message: "private predicate failure"}}
	}
	raw, err := json.Marshal(result)
	return nodeproto.Reply{ID: request.ID, OK: true, Result: raw}, err
}

// CheckOwnerApprovalPolicies proves conditional-approval evaluation and process
// cleanup in the common host. Store resolution, human decisions and placement
// authority remain separate acceptance cells.
func CheckOwnerApprovalPolicies(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"skip", "wait", "predicate_error", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			job := nodeproto.JobSpec{ID: "gate", Approval: &nodeproto.ApprovalSpec{}, Closures: nodeproto.ClosureSpec{SkipIf: []string{"condition"}}}
			plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}}
			planHash, _ := expansionHash(plan)
			jobHash, _ := expansionHash(job)
			identity := engine.Identity{Version: "v1.0.0", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.0.0"}}
			resolved := engine.Resolved{Identity: identity}
			spec := runspec.Spec{Hash: "spec", PlanHash: planHash, Resolved: runspec.Resolved{Engine: identity}}
			rawSpec, _ := json.Marshal(spec)
			rawJob, _ := json.Marshal(job)
			st := &expansionHostStore{node: store.NodeRow{Ref: store.NodeRef{RunID: "run", NodeID: "gate"}, Status: store.StatusPending, Spec: rawJob, SpecHash: string(jobHash)}}
			process := &policySDK{expansionSDK: expansionSDK{plan: plan}, value: json.RawMessage("false")}
			if mode == "skip" {
				process.value = json.RawMessage("true")
			}
			process.fail = mode == "predicate_error"
			if mode == "malformed" {
				process.value = json.RawMessage("null")
			}
			host, err := nodehost.NewOwnerEvaluator(nodehost.EvaluationDependencies{
				Store: st,
				Start: func(context.Context, nodehost.EvaluationInput) (nodehost.ExecutorProcess, error) { return process, nil },
				EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
					return resolved, nil
				},
				CanonicalHash: expansionHash,
				ValidatePlan: func(raw json.RawMessage, _ nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
					var plan nodeproto.PlanDoc
					err := json.Unmarshal(raw, &plan)
					return plan, err
				},
				ChildContext: func(ctx context.Context, budget, _ time.Duration) (context.Context, context.CancelFunc, error) {
					bounded, cancel := context.WithTimeout(ctx, budget)
					return bounded, cancel, nil
				},
				ReadOutput: func(context.Context, store.NodeRef, int64) (json.RawMessage, error) {
					return nil, errors.New("approval cannot read its own unexecuted output")
				},
				Mask: func(text string) string { return strings.ReplaceAll(text, "private", "***") },
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := host.SkipIf(t.Context(), nodehost.EvaluationInput{
				Accepted: nodehost.AcceptedRun{RunID: "run", Spec: rawSpec, SpecHash: "spec", Plan: plan, PlanHash: string(planHash)},
				Engine:   resolved, Entry: &source.Entry{Engine: resolved}, NodeID: "gate", SpecHash: string(jobHash),
				RouteBinding: nodeproto.ProcessBinding{RunID: "run", JobID: "gate"},
			})
			failed := mode == "predicate_error" || mode == "malformed"
			if (err != nil) != failed || result.Skip != (mode == "skip") || len(result.TimedOut) != 0 {
				t.Fatalf("decision = %+v, error = %v", result, err)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("predicate error was not masked")
			}
			if !process.cleaned || len(process.calls) != 1 || st.node.Status != store.StatusPending || st.node.Attempt != 0 {
				t.Fatal("policy did not clean up or modified execution state")
			}
		})
	}
}
