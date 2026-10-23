package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

type sdkCode struct {
	Store      EvaluationStore
	ReadOutput func(context.Context, runspec.NodeRef, int64) (json.RawMessage, error)
	Validation nodeproto.ValidationContext
}

func (h *sdkCode) readResult(ctx context.Context, row runstate.NodeRow, storage StorageBinding) (json.RawMessage, error) {
	if storage.Bytes != nil || storage.Records != nil {
		var job nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &job); err != nil {
			return nil, executorError(apierr.PlanDrift, "generator source declaration is invalid")
		}
		if job.ResultStep == "" && row.OutputRecord == nil {
			return json.RawMessage("null"), nil
		}
		return (OutputRecords{Blobs: storage.Bytes, Records: storage.Records}).ReadResult(ctx, row, storage.Authority)
	}
	if h.ReadOutput == nil {
		return nil, executorError(apierr.Unavailable, "result storage is not configured")
	}
	return h.ReadOutput(ctx, row.Ref, int64(row.Generation))
}

type executorSession struct {
	rawArgs     map[string]string
	jobArgs     map[string]map[string]string
	closureArgs map[string]map[string]string
	arguments   []nodeproto.ArgumentDescription
	process     ExecutorProcess
	sequence    atomic.Int64
	failure     *nodeproto.JobFailure
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

func (s *executorSession) call(ctx context.Context, request nodeproto.Request, result any, kind string) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	validDestination := false
	switch request.Op {
	case nodeproto.OpHello:
		_, validDestination = result.(*nodeproto.HelloResult)
	case nodeproto.OpPlan, nodeproto.OpDescribe:
		_, validDestination = result.(*json.RawMessage)
	case nodeproto.OpEval:
		_, validDestination = result.(*nodeproto.EvalResult)
	case nodeproto.OpRunStep:
		_, validDestination = result.(*nodeproto.RunResult)
	case nodeproto.OpCancel:
		_, validDestination = result.(*json.RawMessage)
	}
	if request.Op == nodeproto.OpEval && !slices.Contains([]string{"skip_if", "memo_key", "before_run", "after_run", "verify", "generate"}, kind) {
		return executorError(apierr.ProtocolError, "SDK eval requires an accepted closure kind")
	}
	if !validDestination {
		return executorError(apierr.ProtocolError, "SDK result destination does not match the operation")
	}
	if request.Op == nodeproto.OpRunStep {
		request.Args = s.jobArgs[request.Job]
	}
	if request.Op == nodeproto.OpEval {
		request.Args = s.closureArgs[request.Closure]
	}
	request.ID = strconv.FormatInt(s.sequence.Add(1), 10)
	if request.Op == nodeproto.OpRunStep {
		request.Failure = s.failure
	}
	reply, err := s.process.Request(ctx, request)
	if err != nil {
		return err
	}
	if reply.ID != request.ID {
		return executorError(apierr.ProtocolError, "SDK returned an invalid reply ID")
	}
	if err = reply.Decode(result); err != nil {
		if errors.Is(err, nodeproto.ErrMalformed) {
			return executorError(apierr.ProtocolError, "SDK reply has an invalid result")
		}
		return err
	}
	if request.Op == nodeproto.OpEval {
		if err := result.(*nodeproto.EvalResult).ValidateValue(kind); err != nil {
			return executorError(apierr.ProtocolError, "SDK closure returned an invalid value")
		}
	}
	return nil
}

func (h *sdkCode) hello(ctx context.Context, session *executorSession, input NodeInput) error {
	return sdkHello(ctx, session, input.Engine.Identity, input.GrantedRunner, &input.RouteBinding)
}

func sdkHello(ctx context.Context, session *executorSession, identity engineid.Identity, runner nodeproto.RunnerIdentity, binding *nodeproto.ProcessBinding) error {
	var hello nodeproto.HelloResult
	err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpHello, Protocol: nodeproto.Version, Engine: &nodeproto.EngineIdentity{Version: identity.Version}, Runner: &runner, Binding: binding}, &hello, "")
	if err != nil {
		return err
	}
	if hello.Protocol != nodeproto.Version || hello.SDK.Language != string(identity.Pin.SDK) || identity.Pin.Replacement == "" && strings.TrimPrefix(hello.SDK.Version, "v") != strings.TrimPrefix(identity.Pin.Version, "v") {
		return executorError(apierr.ProtocolMismatch, "SDK hello does not match the sealed engine and SDK pin")
	}
	return nil
}

func (h *sdkCode) replan(ctx context.Context, session *executorSession, input NodeInput, spec runspec.Spec) error {
	session.rawArgs = spec.Resolved.Args
	run := executorRunContext(spec)
	run.RunID = input.Accepted.RunID
	args := spec.Resolved.Args
	if args == nil {
		args = map[string]string{}
	}
	entrypoint := spec.Resolved.Entrypoint
	if entrypoint == "" {
		entrypoint = spec.Request.Request.Source.Entrypoint
	}
	if entrypoint == "" {
		entrypoint = spec.Request.Request.Pipeline
	}
	var raw json.RawMessage
	err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: entrypoint, Args: args, Run: &run}, &raw, "")
	if err != nil {
		return err
	}
	plan, err := nodeproto.ValidatePlan(raw, h.Validation)
	if err != nil {
		return err
	}
	hash, err := runspec.CanonicalHash(plan)
	if err != nil {
		return err
	}
	if string(hash) != input.Accepted.PlanHash {
		return executorError(apierr.PlanDrift, "SDK plan differs from the accepted plan")
	}
	if len(input.Replay) == 0 && !slices.ContainsFunc(plan.Jobs, func(job nodeproto.JobSpec) bool { return job.ID == input.NodeID }) {
		input.Replay, err = h.recordedReplay(ctx, input, plan.Jobs)
		if err != nil {
			return err
		}
	}
	jobs, err := h.replay(ctx, session, input, plan.Jobs)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.ID != input.NodeID {
			continue
		}
		hash, err := runspec.CanonicalHash(job)
		if err != nil {
			return err
		}
		if string(hash) != input.SpecHash {
			return executorError(apierr.PlanDrift, "SDK job differs from the accepted job")
		}
		return session.prepareArguments(job)
	}
	return executorError(apierr.PlanDrift, "SDK did not reconstruct the accepted job")
}

func (h *sdkCode) eval(ctx context.Context, session *executorSession, input NodeInput, closure, kind string, payload json.RawMessage, settled []string) (json.RawMessage, error) {
	var result nodeproto.EvalResult
	if kind == "generate" {
		if gate, ok := session.process.(declarationProcess); ok {
			gate.gateDeclarations()
		}
	}
	request := nodeproto.Request{Op: nodeproto.OpEval, Closure: closure, Input: payload}
	if settled != nil {
		request.Attempt, request.Settled = int(input.Attempt), settled
	}
	if err := session.call(ctx, request, &result, kind); err != nil {
		if kind == "generate" {
			return nil, executorError(apierr.SDKError, generatorFailureMessage)
		}
		return nil, err
	}
	if kind == "generate" {
		if result.Error != nil {
			return nil, executorError(apierr.SDKError, generatorFailureMessage)
		}
		session.arguments = append(session.arguments, result.Arguments...)
		if err := finishGeneratorLogs(ctx, session.process, input.Accepted, result); err != nil {
			return nil, err
		}
	}
	return evalValue(result, kind)
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

func (s *executorSession) prepareArguments(job nodeproto.JobSpec) error {
	args, err := nodeproto.PrepareJobArguments(job.Arguments, s.rawArgs)
	if err != nil {
		return err
	}
	if len(job.Arguments) == 0 {
		return nil
	}
	if s.jobArgs == nil {
		s.jobArgs = map[string]map[string]string{}
		s.closureArgs = map[string]map[string]string{}
	}
	s.jobArgs[job.ID] = args
	closures := job.Closures
	ids := append([]string{closures.MemoKey, closures.Verify}, closures.SkipIf...)
	ids = append(ids, closures.BeforeRun...)
	ids = append(ids, closures.AfterRun...)
	ids = append(ids, closures.Generate...)
	for _, step := range job.Steps {
		ids = append(ids, step.Closures.SkipIf...)
	}
	for _, id := range ids {
		if id != "" {
			s.closureArgs[id] = args
		}
	}
	return nil
}
