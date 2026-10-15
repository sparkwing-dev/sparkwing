package nodehost

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type sdkCode struct {
	Store         EvaluationStore
	ValidatePlan  func(json.RawMessage, nodeproto.ValidationContext) (nodeproto.PlanDoc, error)
	CanonicalHash func(any) (runspec.Hash, error)
	Validation    nodeproto.ValidationContext
}

type executorSession struct {
	process  ExecutorProcess
	sequence atomic.Int64
	failure  *nodeproto.JobFailure
}

func checkProcessBinding(parent, child nodeproto.ProcessBinding) error {
	if child.RunID != parent.RunID || child.JobID != parent.JobID {
		return executorError(apierr.SpecDrift, "process binding changed its accepted job")
	}
	if parent.Routes.Bearer != "" && (child.Routes.Bearer == "" || child.Routes.Bearer == parent.Routes.Bearer) {
		return executorError(apierr.SpecDrift, "process requires a fresh delegated route bearer")
	}
	return nil
}

func (s *executorSession) call(ctx context.Context, request nodeproto.Request, result any) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	request.ID = strconv.FormatInt(s.sequence.Add(1), 10)
	if request.Op == nodeproto.OpRunStep {
		request.Failure = s.failure
	}
	reply, err := s.process.Request(ctx, request)
	if err != nil {
		return err
	}
	if reply.ID != request.ID || reply.OK != (reply.Error == nil) || reply.OK == (len(reply.Result) == 0) || reply.Error != nil && len(reply.Result) != 0 {
		return executorError(apierr.ProtocolError, "SDK returned an invalid reply")
	}
	if reply.Error != nil {
		return reply.Error
	}
	if err = json.Unmarshal(reply.Result, result); err != nil {
		return executorError(apierr.ProtocolError, "SDK reply has an invalid result")
	}
	return nil
}

func (h *sdkCode) hello(ctx context.Context, session *executorSession, input NodeInput) error {
	return sdkHello(ctx, session, input.Engine.Identity, input.GrantedRunner, &input.RouteBinding)
}

func sdkHello(ctx context.Context, session *executorSession, identity engine.Identity, runner nodeproto.RunnerIdentity, binding *nodeproto.ProcessBinding) error {
	var hello nodeproto.HelloResult
	err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpHello, Protocol: nodeproto.Version, Engine: &nodeproto.EngineIdentity{Version: identity.Version}, Runner: &runner, Binding: binding}, &hello)
	if err != nil {
		return err
	}
	if hello.Protocol != nodeproto.Version || hello.SDK.Language != string(identity.Pin.SDK) || strings.TrimPrefix(hello.SDK.Version, "v") != strings.TrimPrefix(identity.Pin.Version, "v") {
		return executorError(apierr.ProtocolMismatch, "SDK hello does not match the sealed engine and SDK pin")
	}
	return nil
}

func (h *sdkCode) replan(ctx context.Context, session *executorSession, input NodeInput, spec runspec.Spec) error {
	run := executorRunContext(spec)
	run.RunID = input.Accepted.RunID
	args := spec.Resolved.Args
	if args == nil {
		args = map[string]string{}
	}
	var raw json.RawMessage
	entrypoint := spec.Resolved.Entrypoint
	if entrypoint == "" {
		entrypoint = spec.Request.Request.Pipeline
	}
	err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: entrypoint, Args: args, Run: &run}, &raw)
	if err != nil {
		return err
	}
	plan, err := h.ValidatePlan(raw, h.Validation)
	if err != nil {
		return err
	}
	hash, err := h.CanonicalHash(plan)
	if err != nil {
		return err
	}
	if string(hash) != input.Accepted.PlanHash {
		return executorError(apierr.PlanDrift, "SDK plan differs from the accepted plan")
	}
	jobs, err := h.replay(ctx, session, input, plan.Jobs)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.ID != input.NodeID {
			continue
		}
		hash, err := h.CanonicalHash(job)
		if err != nil {
			return err
		}
		if string(hash) != input.SpecHash {
			return executorError(apierr.PlanDrift, "SDK job differs from the accepted job")
		}
		return nil
	}
	return executorError(apierr.PlanDrift, "SDK did not reconstruct the accepted job")
}

func (h *sdkCode) eval(ctx context.Context, session *executorSession, input NodeInput, closure string, payload json.RawMessage, settled []string) (json.RawMessage, error) {
	var result nodeproto.EvalResult
	request := nodeproto.Request{Op: nodeproto.OpEval, Closure: closure, Input: payload}
	if settled != nil {
		request.Attempt, request.Settled = int(input.Attempt), settled
	}
	if err := session.call(ctx, request, &result); err != nil {
		return nil, err
	}
	return evalValue(result, "")
}

func executorRunContext(spec runspec.Spec) nodeproto.RunContext {
	request := spec.Request.Request
	git := request.Git
	context := nodeproto.RunContext{RunID: spec.Request.RunID, Pipeline: request.Pipeline, StartedAt: spec.Request.AdmittedAt, NoMemo: spec.Resolved.Settings.Options.NoMemo.Value, DryRun: spec.Resolved.Settings.Options.DryRun.Value, Git: &nodeproto.Git{SHA: git.SHA, Branch: git.Ref, DefaultBranch: spec.Resolved.DefaultBranch.Value, Repo: git.Repository.Display}, Trigger: &nodeproto.Trigger{Source: string(request.Origin), User: request.Actor.Display}}
	if request.PullRequest.Set {
		pr := request.PullRequest.Value
		context.Trigger.PullRequest = &nodeproto.PullRequest{Number: int(pr.Number), Action: pr.Action, BaseRef: pr.BaseRef, BaseSHA: pr.BaseSHA, HeadRef: pr.HeadRef, HeadSHA: pr.HeadSHA}
	}
	return context
}
