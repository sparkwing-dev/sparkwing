package sparkwing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"runtime/debug"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type sdkOperations struct{}

func init() { sdkserve.DefaultOperations = sdkOperations{} }
func recoveredError(value any) *apierr.Error {
	if failure, ok := value.(error); ok {
		var structured *apierr.Error
		if errors.As(failure, &structured) {
			return structured
		}
	}
	return sdkError(apierr.SDKError, fmt.Sprint(value))
}

func sdkVersion() string {
	if b, ok := debug.ReadBuildInfo(); ok {
		if b.Main.Path == "github.com/sparkwing-dev/sparkwing" {
			return b.Main.Version
		}
		for _, d := range b.Deps {
			if d.Path == "github.com/sparkwing-dev/sparkwing" {
				return d.Version
			}
		}
	}
	return "development"
}

func (sdkOperations) Document(plan any) nodeproto.PlanDoc { return planDocument(plan.(*Plan)) }

func (sdkOperations) Context(ctx context.Context, rc nodeproto.RunContext, runner nodeproto.RunnerIdentity, s sdkserve.Services) context.Context {
	ctx = sdkserve.WithServices(ctx, s)
	ctx = context.WithValue(ctx, planningMaskServicesKey{}, s)
	ctx = context.WithValue(ctx, keyLogger, protocolLogger{s: s})
	ctx = context.WithValue(ctx, runnerCtxKey{}, &RunnerInfo{Name: runner.Name, Type: runner.Type, Labels: append([]string(nil), runner.Labels...)})
	ctx = context.WithValue(ctx, dryRunKey{}, rc.DryRun)
	ctx = context.WithValue(ctx, keySecretResolver, secretResolverFunc(func(ctx context.Context, name string) (string, bool, error) {
		v, masked, err := s.Secret(ctx, name)
		var e *apierr.Error
		if errors.As(err, &e) && e.Code == apierr.NotFound {
			err = errors.Join(ErrSecretMissing, err)
		}
		return v, masked, err
	}))
	ctx = context.WithValue(ctx, oidcTokenSourceKey{}, oIDCTokenSource(func(ctx context.Context, a string) (string, error) { return s.OIDC(ctx, a) }))
	ctx = context.WithValue(ctx, keyJSONRefResolver, func(ctx context.Context, id string) ([]byte, error) {
		raw, err := s.Output(ctx, id)
		return raw, referenceError(err, false)
	})
	ctx = context.WithValue(ctx, keyPipelineResolver, pipelineResolverFunc(func(ctx context.Context, p, j string, age time.Duration) (*resolvedPipelineRef, error) {
		raw, err := s.Latest(ctx, p, j, age)
		return &resolvedPipelineRef{Data: raw}, referenceError(err, true)
	}))
	ctx = context.WithValue(ctx, keyPipelineAwaiter, pipelineAwaiterFunc(func(ctx context.Context, r awaitRequest) (*resolvedPipelineRef, error) {
		if r.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, r.Timeout)
			defer cancel()
		}
		raw, err := s.RunPipeline(ctx, sdkserve.ChildRequest{Pipeline: r.Pipeline, Job: r.NodeID, Args: r.Args, Repo: r.Repo, Branch: r.Branch})
		return &resolvedPipelineRef{Data: raw}, err
	}))
	return ctx
}

func (sdkOperations) Plan(ctx context.Context, name string, args map[string]string, run nodeproto.RunContext) (built any, document nodeproto.PlanDoc, failure error) {
	diagnostics := &planningDiagnostics{ctx: ctx, args: maps.Clone(args)}
	defer func() {
		if value := recover(); value != nil {
			built = nil
			document = nodeproto.PlanDoc{}
			failure = planningFailure(value)
		}
		failure = diagnostics.sanitize(failure, apierr.PlanFailed, "")
	}()
	reg, ok := lookup(name)
	if !ok {
		return nil, nodeproto.PlanDoc{}, sdkError(apierr.UnknownPipeline, "pipeline is not registered")
	}
	diagnostics.collect(reg.Schema)
	ctx = withPlanningDiagnostics(ctx, diagnostics)
	rc := RunContext{RunID: run.RunID, Pipeline: run.Pipeline, StartedAt: run.StartedAt, NoMemo: run.NoMemo, DryRun: run.DryRun}
	if run.Git != nil {
		g := run.Git
		rc.Git = NewGit(WorkDir(), g.SHA, g.Branch, g.DefaultBranch, g.Repo, g.RepoURL)
	}
	if run.Trigger != nil {
		v := run.Trigger
		rc.Trigger = TriggerInfo{Source: v.Source, User: v.User}
		if v.PullRequest != nil {
			pr := v.PullRequest
			rc.Trigger.PullRequest = &PullRequest{Number: pr.Number, Action: pr.Action, BaseRef: pr.BaseRef, BaseSHA: pr.BaseSHA, HeadRef: pr.HeadRef, HeadSHA: pr.HeadSHA}
		}
	}
	p, err := reg.Invoke(ctx, args, rc)
	if err != nil {
		code := apierr.PlanFailed
		var input *InputError
		if errors.As(err, &input) {
			code = apierr.InvalidArgs
		}
		var canonical *apierr.Error
		if errors.As(err, &canonical) {
			return nil, nodeproto.PlanDoc{}, canonical
		}
		return nil, nodeproto.PlanDoc{}, sdkError(code, err.Error())
	}
	p.registration = reg
	p.secretInputs = slices.Clone(diagnostics.values)
	doc := planDocument(p)
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, doc, sdkError(apierr.PlanFailed, "plan cannot be encoded")
	}
	if _, err := nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: declarationSecrets(ctx, p.secretInputs)}); err != nil {
		return nil, doc, err
	}
	return p, doc, nil
}

func (sdkOperations) RunStep(ctx context.Context, built any, req nodeproto.Request) (result nodeproto.RunResult, err error) {
	p, ok := built.(*Plan)
	if !ok {
		return result, sdkError(apierr.NoPlan, "no SDK plan")
	}
	n := planJob(p, req.Job)
	if n == nil {
		return result, sdkError(apierr.UnknownJob, "job is not declared")
	}
	if n.work == nil {
		return result, sdkError(apierr.UnknownStep, "job has no executable steps")
	}
	step := n.work.byID[req.Step]
	if step == nil {
		return result, sdkError(apierr.UnknownStep, "step is not declared")
	}
	if err := n.work.beginStep(req.Attempt, step, req.Settled); err != nil {
		return result, err
	}
	defer func() {
		step.markDone(nil)
		n.work.finishStep(step.id)
	}()
	result.Outcome = "failed"
	defer func() {
		if v := recover(); v != nil {
			result.Error = recoveredError(v)
			err = nil
		}
	}()
	ctx, err = executionContext(ctx, p, n, req)
	if err != nil {
		result.Error = sdkError(apierr.SDKError, err.Error())
		return result, nil
	}
	out, bodyErr := runStep(ctx, n.work, step)
	if bodyErr != nil {
		if !errors.As(bodyErr, &result.Error) {
			result.Error = sdkError(apierr.SDKError, bodyErr.Error())
		}
		return result, nil
	}
	result.Output, err = json.Marshal(out)
	if err != nil {
		result.Error = sdkError(apierr.SDKError, err.Error())
		return result, nil
	}
	if int64(len(result.Output)) > limits.NodeOutputBytes {
		result.Output = nil
		result.Error = sdkError(apierr.SDKError, "step output exceeds the output limit")
		return result, nil
	}
	result.Outcome = "success"
	return result, nil
}

func executionContext(ctx context.Context, p *Plan, n *JobNode, req nodeproto.Request) (context.Context, error) {
	ctx = context.WithValue(ctx, keyNode, n.id)
	ctx = context.WithValue(ctx, keyInputs, p.inputs)
	ctx = withStep(ctx, req.Step)
	env := map[string]string{}
	if !generatorClosure(n, req.Closure) {
		for k, v := range n.declaredEnv {
			if !v.Startup {
				env[k] = v.Value
			}
		}
	}
	ctx = WithCommandEnv(ctx, env)
	if req.Failure != nil {
		stage := StageAction
		if req.Failure.Stage == "verify" {
			stage = StageVerify
		}
		ctx = withFailure(ctx, Failure{Stage: stage, Err: req.Failure.Error})
	}
	if p.registration != nil {
		ctx = context.WithValue(ctx, keySecretProvider, p.registration.instance)
	}
	return ctx, nil
}

func (sdkOperations) Eval(ctx context.Context, built any, req nodeproto.Request) (result nodeproto.EvalResult, err error) {
	p, ok := built.(*Plan)
	if !ok {
		return result, sdkError(apierr.NoPlan, "no SDK plan")
	}
	// safety: a queued eval must honor cancellation without waiting for another closure.
	select {
	case p.evalGate <- struct{}{}:
		defer func() { <-p.evalGate }()
	case <-ctx.Done():
		return result, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	n := closureJob(p, req.Closure)
	if n == nil || (req.Job != "" && n.id != req.Job && !generatorClosure(n, req.Closure)) {
		return result, sdkError(apierr.UnknownClosure, "closure is not declared for the bound job")
	}
	if generatorClosure(n, req.Closure) {
		if p.diagnostics == nil {
			p.diagnostics = &planningDiagnostics{ctx: ctx, args: p.args, values: slices.Clone(p.secretInputs)}
		}
		p.diagnostics.ctx = ctx
		defer func() {
			err = p.diagnostics.sanitize(err, apierr.SDKError, "")
			if result.Error != nil {
				result.Error = p.diagnostics.sanitize(result.Error, apierr.SDKError, "").(*apierr.Error)
			}
		}()
	}
	if step := closureStep(n, req.Closure); step != nil {
		if err := n.work.beginStep(req.Attempt, step, req.Settled); err != nil {
			return result, err
		}
		defer n.work.finishStep(step.id)
		req.Step = step.id
	} else if req.Attempt != 0 || req.Settled != nil {
		return result, sdkError(apierr.ProtocolError, "job closure cannot carry step state")
	}
	ctx, err = executionContext(ctx, p, n, req)
	if err != nil {
		return result, err
	}
	defer func() {
		if v := recover(); v != nil {
			result.Value = json.RawMessage("null")
			result.Error = recoveredError(v)
			err = nil
		}
	}()
	value, evalErr := evaluate(ctx, p, n, req.Closure, req.Input)
	if evalErr != nil {
		result.Value = json.RawMessage("null")
		if !errors.As(evalErr, &result.Error) {
			result.Error = sdkError(apierr.SDKError, evalErr.Error())
		}
		return result, nil
	}
	result.Value, err = json.Marshal(value)
	return result, err
}

func evaluate(ctx context.Context, p *Plan, n *JobNode, id string, input json.RawMessage) (any, error) {
	for i, f := range n.skipIf {
		if id == closureID(n.id, "skip", i) {
			return f(ctx), nil
		}
	}
	for i, f := range n.beforeRun {
		if id == closureID(n.id, "before", i) {
			return nil, f(ctx)
		}
	}
	for i, f := range n.afterRun {
		if id == closureID(n.id, "after", i) {
			var final nodeproto.RunResult
			if err := (nodeproto.Reply{ID: id, OK: true, Result: input}).Decode(&final); err != nil {
				return nil, err
			}
			var failure error
			if final.Error != nil {
				failure = final.Error
			}
			f(ctx, failure)
			return nil, nil
		}
	}
	if n.verify != nil && id == closureID(n.id, "verify", 0) {
		return nil, n.verify(ctx)
	}
	if n.memoKey != nil && id == closureID(n.id, "memo", 0) {
		v, err := n.memoKey(ctx)
		if err != nil {
			return nil, err
		}
		if v == NoMemo {
			return nil, nil
		}
		if v == "" {
			return nil, fmt.Errorf("memo key must not be empty")
		}
		return string(v), nil
	}
	if n.work != nil {
		for _, step := range n.work.steps {
			for i, f := range step.skipIf {
				if id == closureID(n.id, "step/"+step.id+"/skip", i) {
					return f(ctx), nil
				}
			}
		}
	}
	for _, g := range n.generators {
		if id != g.id {
			continue
		}
		if prior, ok := p.generated[g.id]; ok {
			return prior, nil
		}
		expansion := NewPlan()
		expansion.diagnostics = p.diagnostics
		base := jsonResolverFromContext(ctx)
		ctx = context.WithValue(ctx, keyJSONRefResolver, func(ctx context.Context, j string) ([]byte, error) {
			if j == n.id && len(input) > 0 {
				return input, nil
			}
			if base == nil {
				return nil, fmt.Errorf("output unavailable")
			}
			return base(ctx, j)
		})
		if err := g.fn(ctx, expansion); err != nil {
			return nil, err
		}
		var members []*JobNode
		for _, root := range expansion.nodes {
			for child := range recoveryChain(root) {
				child.addNeed(n.id)
				members = append(members, child)
			}
		}
		GroupJobs(expansion, g.id, members...)
		for _, ancestor := range jobGroups(p, n) {
			GroupJobs(expansion, ancestor, members...)
		}
		doc := planDocument(expansion)
		raw, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		existing := planDocument(p)
		p.secretInputs = slices.Clone(p.diagnostics.values)
		if _, err = nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{
			Placement: nodeproto.PlacementLocal, Secrets: declarationSecrets(ctx, p.secretInputs),
			Expansion: true, ExistingJobs: existing.Jobs, ExistingConcurrency: existing.Concurrency, SourceJob: n.id,
		}); err != nil {
			return nil, err
		}
		registerRecoveryArgs(expansion)
		var schema inputSchema
		if p.registration != nil {
			schema = p.registration.Schema
		}
		if err := assertJobArgsDisjoint(expansion, schema, p.jobArgs...); err != nil {
			return nil, sdkError(apierr.PlanInvalid, err.Error())
		}
		if err := resolveAndBindJobArgs(expansion, p.args); err != nil {
			return nil, sdkError(apierr.InvalidArgs, err.Error())
		}
		p.mu.Lock()
		for _, child := range expansion.nodes {
			p.nodes = append(p.nodes, child)
			p.byID[child.id] = child
		}
		p.groups = append(p.groups, expansion.groups...)
		p.jobArgs = append(p.jobArgs, expansion.jobArgs...)
		if p.generated == nil {
			p.generated = map[string][]nodeproto.JobSpec{}
		}
		p.generated[g.id] = doc.Jobs
		p.mu.Unlock()
		return doc.Jobs, nil
	}
	return nil, sdkError(apierr.UnknownClosure, "closure is not declared")
}

func sdkError(code apierr.Code, message string) *apierr.Error {
	return &apierr.Error{Code: code, Message: message}
}

type protocolLogger struct{ s sdkserve.Services }

func (l protocolLogger) Log(level, message string) { l.Emit(LogRecord{Level: level, Msg: message}) }
func (l protocolLogger) Emit(r LogRecord) {
	if r.TS.IsZero() {
		r.TS = time.Now()
	}
	var attrs json.RawMessage
	if r.Attrs != nil {
		var err error
		attrs, err = json.Marshal(r.Attrs)
		if err != nil {
			panic(err)
		}
	}
	l.s.Emit(nodeproto.FrameLog{TS: r.TS.Format(time.RFC3339Nano), Level: r.Level, Step: r.Step, Event: r.Event, Message: r.Msg, Attrs: attrs})
}

func closureJob(p *Plan, id string) *JobNode {
	for _, spec := range planDocument(p).Jobs {
		if hasClosure(spec.Closures, id) {
			return planJob(p, spec.ID)
		}
		for _, step := range spec.Steps {
			if hasClosure(step.Closures, id) {
				return planJob(p, spec.ID)
			}
		}
	}
	return nil
}

func hasClosure(c nodeproto.ClosureSpec, id string) bool {
	if id == "" {
		return false
	}
	if c.MemoKey == id || c.Verify == id {
		return true
	}
	for _, ids := range [][]string{c.SkipIf, c.BeforeRun, c.AfterRun, c.Generate} {
		for _, v := range ids {
			if v == id {
				return true
			}
		}
	}
	return false
}

func closureStep(n *JobNode, id string) *WorkStep {
	if n.work != nil {
		for _, step := range n.work.steps {
			for i := range step.skipIf {
				if id == closureID(n.id, "step/"+step.id+"/skip", i) {
					return step
				}
			}
		}
	}
	return nil
}

func (sdkOperations) Prepare(built any, request nodeproto.Request) error {
	p, ok := built.(*Plan)
	if !ok {
		return sdkError(apierr.NoPlan, "plan is required")
	}
	if request.Op == nodeproto.OpEval {
		if source := closureJob(p, request.Closure); source != nil && generatorClosure(source, request.Closure) {
			// safety: ancestor replay reconstructs the target before its body environment can be applied.
			return nil
		}
	}
	n := planJob(p, request.Job)
	if n == nil {
		return sdkError(apierr.UnknownJob, "job is not declared")
	}
	for key, value := range n.declaredEnv {
		if !value.Startup {
			if err := os.Setenv(key, value.Value); err != nil {
				return sdkError(apierr.PlanInvalid, "invalid job environment")
			}
		}
	}
	return nil
}

func generatorClosure(job *JobNode, closure string) bool {
	return slices.ContainsFunc(job.generators, func(generator *Generator) bool { return generator.id == closure })
}

type combinedSecrets struct {
	values *secrets.Masker
	host   nodeproto.SecretMatcher
}

func (s combinedSecrets) ContainsSecret(id string) bool {
	return s.values.ContainsSecret(id) || (s.host != nil && s.host.ContainsSecret(id))
}

func declarationSecrets(ctx context.Context, values []diagnosticSecret) nodeproto.SecretMatcher {
	host, _ := sdkserve.ServicesFrom(ctx).(nodeproto.SecretMatcher)
	masker := secrets.NewMasker()
	for _, value := range values {
		value.register(masker)
	}
	return combinedSecrets{values: masker, host: host}
}

func referenceError(err error, latest bool) error {
	var e *apierr.Error
	if !errors.As(err, &e) {
		return err
	}
	reason := fmt.Sprint(e.Details["reason"])
	if (e.Code == apierr.NotFound && (latest || reason == string(apierr.ReasonNoOutput))) || (e.Code == apierr.Conflict && reason == string(apierr.ReasonNotReady)) {
		return errors.Join(ErrRefAbsent, err)
	}
	return err
}
