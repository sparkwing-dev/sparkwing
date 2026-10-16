package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

type scriptedSDK struct {
	requestsMu      sync.Mutex
	plan            nodeproto.PlanDoc
	input           NodeInput
	calls           []string
	requests        []nodeproto.Request
	closed, cleaned bool
	request         func(context.Context, nodeproto.Request) (nodeproto.Reply, error)
	onClose         func() error
	onCleanup       func(context.Context) error
}

func (p *scriptedSDK) Request(ctx context.Context, request nodeproto.Request) (nodeproto.Reply, error) {
	p.requestsMu.Lock()
	p.requests = append(p.requests, request)
	p.requestsMu.Unlock()
	if p.request == nil {
		return nodeproto.Reply{}, errors.New("unexpected SDK request")
	}
	return p.request(ctx, request)
}

func (p *scriptedSDK) Close() error {
	p.closed = true
	if p.onClose != nil {
		return p.onClose()
	}
	return nil
}

func (p *scriptedSDK) Cleanup(ctx context.Context) error {
	if !p.closed {
		return errors.New("cleanup preceded process close")
	}
	p.cleaned = true
	if p.onCleanup != nil {
		return p.onCleanup(ctx)
	}
	return nil
}

func newExpansionSDK(plan nodeproto.PlanDoc, mode string) *scriptedSDK {
	p := &scriptedSDK{plan: plan}
	p.request = func(_ context.Context, in nodeproto.Request) (nodeproto.Reply, error) {
		var result any
		switch in.Op {
		case nodeproto.OpHello:
			result = nodeproto.HelloResult{Protocol: nodeproto.Version, SDK: nodeproto.SDKIdentity{Language: "go", Version: "v1.0.0"}, Features: []string{}}
		case nodeproto.OpPlan:
			result = p.plan
		case nodeproto.OpEval:
			p.calls = append(p.calls, in.Closure)
			if in.Job != "" || string(in.Input) != `{"value":7}` {
				return nodeproto.Reply{}, errors.New("generator did not read recorded output")
			}
			if mode == "failure" && in.Closure == "second" {
				result = nodeproto.EvalResult{Error: &apierr.Error{Code: apierr.SDKError, Message: "private failure"}}
				break
			}
			if raw := strictExpansionJobs(mode, in.Closure); raw != nil {
				result = nodeproto.EvalResult{Value: raw}
				break
			}
			jobs := []nodeproto.JobSpec{}
			if mode != "empty" {
				jobs = append(jobs, nodeproto.JobSpec{ID: in.Closure, Groups: []string{in.Closure}, Steps: []nodeproto.StepSpec{{ID: "body"}}})
			}
			raw, _ := json.Marshal(jobs)
			result = nodeproto.EvalResult{Value: raw}
		default:
			return nodeproto.Reply{}, fmt.Errorf("owner invoked %s", in.Op)
		}
		raw, err := json.Marshal(result)
		return nodeproto.Reply{ID: in.ID, OK: true, Result: raw}, err
	}
	return p
}

func newExecutionSDK(plan nodeproto.PlanDoc, input NodeInput, mode string, trace *[]string) *scriptedSDK {
	p := newExpansionSDK(plan, mode)
	p.input = input
	base := p.request
	p.request = func(ctx context.Context, request nodeproto.Request) (nodeproto.Reply, error) {
		var result any
		switch request.Op {
		case nodeproto.OpHello:
			if !reflect.DeepEqual(request.Binding, &p.input.RouteBinding) {
				return nodeproto.Reply{}, errors.New("hello lost its delegated binding")
			}
			return base(ctx, request)
		case nodeproto.OpEval:
			*trace = append(*trace, request.Closure)
			if request.Closure == "step_guard" && mode == "step_skip" {
				result = nodeproto.EvalResult{Value: json.RawMessage("true")}
				break
			}
			if request.Closure != "before" && request.Closure != "after" && request.Closure != "verify" {
				return nodeproto.Reply{}, errors.New("executor evaluated an owner closure")
			}
			result = nodeproto.EvalResult{Value: json.RawMessage("null")}
			if mode == "before_refused" && request.Closure == "before" {
				result = nodeproto.EvalResult{Error: &apierr.Error{Code: apierr.SDKError, Message: "private hook refused", Hint: "private fix", Details: map[string]any{"output": "private source bytes"}}}
			}
			if mode == "verify_refused" && request.Closure == "verify" {
				result = nodeproto.EvalResult{Error: &apierr.Error{Code: apierr.SDKError, Message: "private verification refused", Hint: "private fix", Details: map[string]any{"output": "private source bytes"}}}
			}
		case nodeproto.OpRunStep:
			if request.Job != p.input.NodeID || int64(request.Attempt) != p.input.Attempt {
				return nodeproto.Reply{}, errors.New("body changed its award or ordinal")
			}
			*trace = append(*trace, fmt.Sprintf("body:%d", request.Attempt))
			outcome := "success"
			if (mode == "retry" || mode == "artifact_retry" || mode == "artifact_publish_retry" || mode == "cache_retry") && request.Attempt < 3 || mode == "artifact_publish_failed" || mode == "cache_failed" || mode == "step_failed" && request.Step == "body" {
				outcome = "failed"
			}
			result = nodeproto.RunResult{Outcome: outcome}
			if outcome == "failed" {
				result = nodeproto.RunResult{Outcome: outcome, Error: &apierr.Error{Code: apierr.SDKError, Message: "body failed"}}
			}
			if mode == "output_refused" {
				result = nodeproto.RunResult{Outcome: outcome, Output: json.RawMessage(`{"value":7}`)}
			}
		default:
			return base(ctx, request)
		}
		raw, err := json.Marshal(result)
		return nodeproto.Reply{ID: request.ID, OK: true, Result: raw}, err
	}
	if mode == "cleanup_refused" {
		p.onCleanup = func(context.Context) error { return errors.New("private cleanup refused") }
	}
	return p
}

func newPlanningSDK(plan nodeproto.PlanDoc, secret string) *scriptedSDK {
	p := newExpansionSDK(plan, "")
	base := p.request
	p.request = func(ctx context.Context, request nodeproto.Request) (nodeproto.Reply, error) {
		if request.Op == nodeproto.OpHello && request.Binding != nil {
			return nodeproto.Reply{}, errors.New("planner received job route authority")
		}
		if request.Op == nodeproto.OpDescribe {
			raw, err := json.Marshal(nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{{Name: "build", Args: []nodeproto.ArgumentDescription{{Name: "token", Secret: true, Default: &secret}}}}})
			return nodeproto.Reply{ID: request.ID, OK: true, Result: raw}, err
		}
		if request.Op == nodeproto.OpPlan && request.Args["token"] != secret {
			return nodeproto.Reply{}, errors.New("planner lost its secret default")
		}
		return base(ctx, request)
	}
	return p
}

func newCheckoutSDK(plan nodeproto.PlanDoc, description nodeproto.PipelineDescription) *scriptedSDK {
	p := newExpansionSDK(plan, "")
	base := p.request
	p.request = func(ctx context.Context, request nodeproto.Request) (nodeproto.Reply, error) {
		if request.Op == nodeproto.OpDescribe {
			raw, err := json.Marshal(nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{description}})
			return nodeproto.Reply{ID: request.ID, OK: true, Result: raw}, err
		}
		return base(ctx, request)
	}
	return p
}

func newPolicySDK(plan nodeproto.PlanDoc, value json.RawMessage, fail bool) *scriptedSDK {
	p := newExpansionSDK(plan, "")
	base := p.request
	p.request = func(ctx context.Context, request nodeproto.Request) (nodeproto.Reply, error) {
		if request.Op != nodeproto.OpEval {
			return base(ctx, request)
		}
		if request.Job != "" || request.Attempt != 0 || len(request.Input) != 0 || request.Closure != "condition" {
			return nodeproto.Reply{}, errors.New("policy changed its accepted identity")
		}
		p.calls = append(p.calls, request.Closure)
		result := nodeproto.EvalResult{Value: value}
		if fail {
			result = nodeproto.EvalResult{Error: &apierr.Error{Code: apierr.SDKError, Message: "private predicate failure"}}
		}
		raw, err := json.Marshal(result)
		return nodeproto.Reply{ID: request.ID, OK: true, Result: raw}, err
	}
	return p
}

func replaySDK(sdk *scriptedSDK, failure *nodeproto.JobFailure, evaluate func(nodeproto.Request) (json.RawMessage, error)) *scriptedSDK {
	p := sdk
	base := p.request
	p.request = func(ctx context.Context, request nodeproto.Request) (nodeproto.Reply, error) {
		if request.Op == nodeproto.OpRunStep && !reflect.DeepEqual(request.Failure, failure) {
			return nodeproto.Reply{}, errors.New("body lost persisted recovery failure")
		}
		if request.Op != nodeproto.OpEval || request.Closure != "target" && request.Closure != "peers" && request.Closure != "ancestors" && request.Closure != "empty" {
			return base(ctx, request)
		}
		value, err := evaluate(request)
		if err != nil {
			return nodeproto.Reply{}, err
		}
		raw, err := json.Marshal(nodeproto.EvalResult{Value: value})
		return nodeproto.Reply{ID: request.ID, OK: true, Result: raw}, err
	}
	return p
}

type noV2Secrets struct{}

func (noV2Secrets) ContainsSecret(string) bool { return false }
