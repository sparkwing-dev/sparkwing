package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	if request.Op == nodeproto.OpRunStep || request.Op == nodeproto.OpEval {
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
	err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: spec.Request.Request.Pipeline, Args: args, Run: &run}, &raw)
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
	jobs := append([]nodeproto.JobSpec(nil), plan.Jobs...)
	var snapshot store.RunSnapshot
	if len(input.Replay) > 0 {
		snapshot, err = h.Store.ReadSnapshot(ctx, input.Accepted.RunID)
		if err != nil {
			return err
		}
		if snapshot.Run.ID != input.Accepted.RunID {
			return executorError(apierr.PlanDrift, "generator snapshot belongs to another run")
		}
	}
	for _, replay := range input.Replay {
		if replay.SourceGeneration < 0 || replay.SourceJob == "" || replay.GeneratorID == "" {
			return executorError(apierr.PlanDrift, "generator replay lacks its persisted source identity")
		}
		var sourceJob *nodeproto.JobSpec
		for i := range jobs {
			if jobs[i].ID == replay.SourceJob {
				sourceJob = &jobs[i]
				break
			}
		}
		if sourceJob == nil || !executorContains(sourceJob.Closures.Generate, replay.GeneratorID) {
			return executorError(apierr.PlanDrift, "generator replay names an undeclared source")
		}
		replayInput := input
		replayInput.NodeID = replay.SourceJob
		value, evalErr := h.eval(ctx, session, replayInput, replay.GeneratorID, replay.Output)
		if evalErr != nil {
			return evalErr
		}
		validation := h.Validation
		validation.Expansion = true
		validation.ExistingJobs = jobs
		validation.SourceJob = replay.SourceJob
		value = bytes.TrimSpace(value)
		if len(value) == 0 || value[0] != '[' {
			return executorError(apierr.ProtocolError, "generator replay must return a jobs array")
		}
		wrapped := append([]byte(`{"jobs":`), value...)
		wrapped = append(wrapped, '}')
		expansion, validateErr := h.ValidatePlan(wrapped, validation)
		if validateErr != nil {
			return validateErr
		}
		expected, expectedErr := h.replayMembers(snapshot, replay)
		if expectedErr != nil {
			return expectedErr
		}
		if len(expansion.Jobs) != len(expected) {
			return executorError(apierr.PlanDrift, "generator replay changed the accepted member set")
		}
		for _, generated := range expansion.Jobs {
			for _, existing := range jobs {
				if existing.ID == generated.ID {
					return executorError(apierr.PlanDrift, "generator replay overwrote an existing job")
				}
			}
			if !expected[generated.ID] || !executorContains(generated.Groups, replay.GeneratorID) {
				return executorError(apierr.PlanDrift, "generator replay changed the accepted member set")
			}
			row, readErr := h.Store.ReadNode(ctx, store.NodeRef{RunID: input.Accepted.RunID, NodeID: generated.ID})
			if readErr != nil {
				return executorError(apierr.PlanDrift, "generator replay added a job absent from the accepted expansion")
			}
			hash, hashErr := h.CanonicalHash(generated)
			if hashErr != nil {
				return hashErr
			}
			if string(hash) != row.SpecHash {
				return executorError(apierr.PlanDrift, "generator replay changed a persisted job")
			}
			if row.Ref.RunID != input.Accepted.RunID || row.Ref.NodeID != generated.ID {
				return executorError(apierr.PlanDrift, "generated job row belongs to another identity")
			}
			delete(expected, generated.ID)
			jobs = append(jobs, generated)
		}
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

func (h *sdkCode) replayMembers(snapshot store.RunSnapshot, replay GeneratorReplay) (map[string]bool, error) {
	for _, accepted := range snapshot.Expansions {
		if accepted.Source.RunID != snapshot.Run.ID || accepted.Source.NodeID != replay.SourceJob || accepted.SourceGeneration != replay.SourceGeneration {
			continue
		}
		var plan nodeproto.PlanDoc
		if err := json.Unmarshal(accepted.Body, &plan); err != nil {
			return nil, executorError(apierr.PlanDrift, "accepted expansion is malformed")
		}
		hash, err := h.CanonicalHash(plan)
		if err != nil {
			return nil, err
		}
		if string(hash) != accepted.BodyHash {
			return nil, executorError(apierr.PlanDrift, "accepted expansion hash differs from its body")
		}
		members := map[string]bool{}
		for _, job := range plan.Jobs {
			if executorContains(job.Groups, replay.GeneratorID) {
				members[job.ID] = true
			}
		}
		return members, nil
	}
	return nil, executorError(apierr.PlanDrift, "generator replay has no accepted expansion for its source generation")
}

func (h *sdkCode) eval(ctx context.Context, session *executorSession, input NodeInput, closure string, payload json.RawMessage) (json.RawMessage, error) {
	var result nodeproto.EvalResult
	if err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpEval, Job: input.NodeID, Closure: closure, Input: payload, Attempt: int(input.Attempt)}, &result); err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, result.Error
	}
	if !json.Valid(result.Value) {
		return nil, executorError(apierr.ProtocolError, "SDK closure returned invalid JSON")
	}
	return result.Value, nil
}

func executorRunContext(spec runspec.Spec) nodeproto.RunContext {
	request := spec.Request.Request
	git := request.Git
	context := nodeproto.RunContext{RunID: spec.Request.RunID, Pipeline: request.Pipeline, StartedAt: spec.Request.AdmittedAt, NoMemo: spec.Resolved.Settings.Options.NoMemo.Value, DryRun: spec.Resolved.Settings.Options.DryRun.Value, Git: &nodeproto.Git{SHA: git.SHA, Branch: git.Ref, DefaultBranch: spec.Resolved.DefaultBranch.Value, Repo: git.Repository.Display}, Trigger: &nodeproto.Trigger{Source: string(request.Origin), User: request.Actor.Display}}
	if request.PullRequest.Set {
		pr := request.PullRequest.Value
		context.Trigger.PullRequest = &nodeproto.PullRequest{Number: int(pr.Number), BaseRef: pr.BaseRef, HeadRef: pr.HeadRef, HeadSHA: pr.HeadSHA}
	}
	return context
}
