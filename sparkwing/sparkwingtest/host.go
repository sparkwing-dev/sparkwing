package sparkwingtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type host struct {
	opts            Options
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	work            sync.WaitGroup
	masker          *secrets.Masker
	identifierMasks *secrets.Masker
	presenters      map[string]*secrets.LogStreams
	records         []sparkwing.LogRecord
	plans           []json.RawMessage
	settled         map[stepStateKey][]string
	args            map[string]string
	initErr         error
}

type pendingLog struct {
	frame nodeproto.FrameLog
	job   string
}

type (
	outputGateKey struct{}
	outputGate    struct {
		mu      sync.Mutex
		pending []pendingLog
		state   uint8 // 0 pending, 1 released, 2 discarded
	}
)

type stepStateKey struct {
	plan *sparkwing.Plan
	job  string
}

func (h *host) Context(ctx context.Context) context.Context {
	return h.context(ctx, "")
}

func (h *host) linkedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	if h.ctx.Err() != nil {
		cancel()
	}
	stop := context.AfterFunc(h.ctx, cancel)
	context.AfterFunc(ctx, func() { stop() })
	return ctx, cancel
}

func (h *host) context(ctx context.Context, job string) context.Context {
	ctx, _ = h.linkedContext(ctx)
	rc := h.runContext(h.opts.Run.Pipeline)
	r := h.opts.Runner
	return sdkserve.DefaultOperations.Context(ctx, rc, nodeproto.RunnerIdentity{Name: r.Name, Type: r.Type, Labels: r.Labels}, boundServices{host: h, ctx: ctx, job: job})
}

type boundServices struct {
	*host
	ctx context.Context
	job string
}

func (s boundServices) Emit(frame nodeproto.FrameLog) { s.host.emit(s.ctx, frame, s.job) }
func (h *host) Plan(ctx context.Context, name string, args map[string]string) (nodeproto.PlanDoc, error) {
	baseCtx := ctx
	discoveryGate := &outputGate{}
	ctx = h.Context(context.WithValue(baseCtx, outputGateKey{}, discoveryGate))
	if err := h.begin(ctx); err != nil {
		return nodeproto.PlanDoc{}, err
	}
	defer h.work.Done()
	described, err := sdkserve.DefaultOperations.Describe(ctx)
	if err != nil {
		h.discardGate(discoveryGate)
		return nodeproto.PlanDoc{}, fmt.Errorf("pipeline discovery failed")
	}
	declarations, err := runspec.NewDeclarationGate(described, h.identifierMasks)
	if err != nil {
		h.discardGate(discoveryGate)
		return nodeproto.PlanDoc{}, err
	}
	selected, err := declarations.Resolve(name, args, nil)
	h.masker.Merge(h.identifierMasks)
	if err != nil {
		h.discardGate(discoveryGate)
		return nodeproto.PlanDoc{}, err
	}
	h.mu.Lock()
	h.args = selected.Args
	h.mu.Unlock()
	failedDiscovery := !declarations.Complete()
	if failedDiscovery {
		h.discardGate(discoveryGate)
	} else {
		h.releaseGate(discoveryGate)
	}
	planGate := &outputGate{}
	ctx = h.Context(context.WithValue(baseCtx, outputGateKey{}, planGate))
	_, doc, err := sdkserve.DefaultOperations.Plan(ctx, name, args, h.runContext(name))
	if err != nil {
		h.discardGate(planGate)
		return nodeproto.PlanDoc{}, declarations.Error(err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		h.discardGate(planGate)
		return doc, err
	}
	accepted, err := h.accept(raw)
	if err != nil {
		h.discardGate(planGate)
		return accepted, err
	}
	if failedDiscovery {
		h.discardGate(planGate)
	} else {
		h.releaseGate(planGate)
	}
	return accepted, nil
}

func (h *host) discardGate(g *outputGate) {
	g.mu.Lock()
	g.state = 2
	g.pending = nil
	g.mu.Unlock()
}

func (h *host) releaseGate(g *outputGate) {
	g.mu.Lock()
	pending := g.pending
	g.pending = nil
	g.state = 1
	g.mu.Unlock()
	for _, item := range pending {
		h.emit(h.ctx, item.frame, item.job)
	}
}

func (h *host) maskError(err error) error {
	var canonical *apierr.Error
	if errors.As(err, &canonical) {
		return h.masker.Error(canonical)
	}
	return h.masker.Error(&apierr.Error{Code: apierr.SDKError, Message: err.Error()})
}

func (h *host) RunStep(ctx context.Context, p *sparkwing.Plan, job, step string) (json.RawMessage, error) {
	ctx = h.context(ctx, job)
	if err := h.begin(ctx); err != nil {
		return nil, err
	}
	defer h.work.Done()
	if err := h.remember(p); err != nil {
		return nil, err
	}
	result, err := sdkserve.DefaultOperations.RunStep(ctx, p, nodeproto.Request{Job: job, Step: step, Attempt: 1, Settled: h.stepSnapshot(p, job)})
	if err != nil {
		return nil, h.maskError(err)
	}
	h.settleStep(p, job, step)
	if result.Error != nil {
		return nil, h.masker.Error(result.Error)
	}
	return h.output(ctx, result.Output), nil
}

func (h *host) Eval(ctx context.Context, p *sparkwing.Plan, job, closure string, input json.RawMessage) (json.RawMessage, error) {
	baseCtx := ctx
	ctx = h.Context(ctx)
	if err := h.begin(ctx); err != nil {
		return nil, err
	}
	defer h.work.Done()
	if err := h.remember(p); err != nil {
		return nil, err
	}
	generator := false
	for _, item := range sdkserve.DefaultOperations.Document(p).Jobs {
		for _, id := range item.Closures.Generate {
			if id == closure {
				generator = true
			}
		}
	}
	var gate *outputGate
	if generator {
		gate = &outputGate{}
		ctx = h.Context(context.WithValue(baseCtx, outputGateKey{}, gate))
	}
	request := nodeproto.Request{Job: job, Closure: closure, Input: input}
	step := predicateStep(p, job, closure)
	if step != "" {
		request.Attempt = 1
		request.Settled = h.stepSnapshot(p, job)
	}
	result, err := sdkserve.DefaultOperations.Eval(ctx, p, request)
	if ctx.Err() != nil {
		if generator {
			h.discardGate(gate)
		}
		return nil, ctx.Err()
	}
	if err != nil {
		if generator {
			h.discardGate(gate)
			return nil, fmt.Errorf("generator declaration failed")
		}
		return nil, h.maskError(err)
	}
	if result.Error != nil {
		if generator {
			h.discardGate(gate)
			return nil, fmt.Errorf("generator declaration failed")
		}
		if step != "" {
			h.settleStep(p, job, step)
		}
		return nil, h.masker.Error(result.Error)
	}
	if generator {
		if !result.DeclarationsKnown {
			h.discardGate(gate)
			return nil, fmt.Errorf("generator declaration failed")
		}
		if err := h.registerGenerated(result.Arguments); err != nil {
			h.discardGate(gate)
			return nil, err
		}
		h.releaseGate(gate)
	}
	if err := h.remember(p); err != nil {
		return nil, err
	}
	if step != "" && string(result.Value) == "true" {
		h.settleStep(p, job, step)
	}
	return h.output(ctx, result.Value), nil
}

func (h *host) output(ctx context.Context, raw json.RawMessage) json.RawMessage {
	if h.masker.Mask(string(raw)) != string(raw) {
		h.emit(ctx, nodeproto.FrameLog{Level: "warn", Event: "output_withheld", Message: "output withheld: contains a secret"}, "")
		return nil
	}
	return raw
}

func (h *host) registerGenerated(arguments []nodeproto.ArgumentDescription) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	selected, err := runspec.RegisterArguments(arguments, h.args, nil, h.identifierMasks)
	h.masker.Merge(h.identifierMasks)
	if err != nil {
		return err
	}
	h.args = selected.Args
	return nil
}

func (h *host) stepSnapshot(p *sparkwing.Plan, job string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.settled[stepStateKey{plan: p, job: job}]...)
}

func (h *host) settleStep(p *sparkwing.Plan, job, step string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.settled == nil {
		h.settled = make(map[stepStateKey][]string)
	}
	key := stepStateKey{plan: p, job: job}
	if !slices.Contains(h.settled[key], step) {
		h.settled[key] = append(h.settled[key], step)
	}
}

func predicateStep(p *sparkwing.Plan, job, closure string) string {
	for _, node := range sdkserve.DefaultOperations.Document(p).Jobs {
		if node.ID != job {
			continue
		}
		for _, step := range node.Steps {
			if slices.Contains(step.Closures.SkipIf, closure) {
				return step.ID
			}
		}
	}
	return ""
}

func (h *host) Records() []sparkwing.LogRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, err := json.Marshal(h.records)
	if err != nil {
		panic(err)
	}
	var records []sparkwing.LogRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&records); err != nil {
		panic(err)
	}
	return records
}

func (h *host) Masks() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.masker.Values()
}

func (h *host) Close() error {
	h.mu.Lock()
	h.cancel()
	h.mu.Unlock()
	h.work.Wait()
	h.mu.Lock()
	for job, presenter := range h.presenters {
		for _, frame := range presenter.Flush() {
			h.appendRecord(frame, job)
		}
	}
	h.mu.Unlock()
	return nil
}

func (h *host) begin(ctx context.Context) error {
	// safety: Close prevents new work before waiting for admitted calls to finish.
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.ctx.Err(); err != nil {
		return err
	}
	if h.initErr != nil {
		return h.initErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	h.work.Add(1)
	return nil
}

func (h *host) executionError(ctx context.Context) error {
	if err := h.ctx.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if sdkserve.Planning(ctx) {
		return fmt.Errorf("host services require an execution binding")
	}
	return nil
}

func (h *host) Secret(ctx context.Context, name string) (string, bool, error) {
	if err := h.executionError(ctx); err != nil {
		return "", false, err
	}
	v, ok := h.opts.Secrets[name]
	if !ok {
		return "", false, fmt.Errorf("secret %q: %w", name, sparkwing.ErrSecretMissing)
	}
	if v.Masked {
		if err := h.registerIssued(ctx, v.Value); err != nil {
			return "", false, err
		}
	}
	return v.Value, v.Masked, nil
}

func (h *host) Output(ctx context.Context, job string) (json.RawMessage, error) {
	if err := h.executionError(ctx); err != nil {
		return nil, err
	}
	raw, ok := h.opts.Outputs[job]
	if !ok {
		return nil, sparkwing.ErrRefAbsent
	}
	return append(json.RawMessage(nil), raw...), nil
}

func (h *host) Latest(ctx context.Context, p, j string, _ time.Duration) (json.RawMessage, error) {
	if err := h.executionError(ctx); err != nil {
		return nil, err
	}
	if h.opts.Latest == nil {
		return nil, sparkwing.ErrRefAbsent
	}
	if err := h.begin(ctx); err != nil {
		return nil, err
	}
	defer h.work.Done()
	ctx, cancel := h.linkedContext(ctx)
	defer cancel()
	return h.opts.Latest(ctx, p, j)
}

func (h *host) RunPipeline(ctx context.Context, r sdkserve.ChildRequest) (json.RawMessage, error) {
	if err := h.executionError(ctx); err != nil {
		return nil, err
	}
	if h.opts.RunPipeline == nil {
		return nil, errors.New("fake host has no child pipeline handler")
	}
	if err := h.begin(ctx); err != nil {
		return nil, err
	}
	defer h.work.Done()
	ctx, cancel := h.linkedContext(ctx)
	defer cancel()
	return h.opts.RunPipeline(ctx, r.Pipeline, r.Args)
}

func (h *host) OIDC(ctx context.Context, a string) (string, error) {
	if err := h.executionError(ctx); err != nil {
		return "", err
	}
	if h.opts.OIDCToken == nil {
		return "", sparkwing.ErrOIDCUnavailable
	}
	if err := h.begin(ctx); err != nil {
		return "", err
	}
	defer h.work.Done()
	ctx, cancel := h.linkedContext(ctx)
	defer cancel()
	token, err := h.opts.OIDCToken(ctx, a)
	if err == nil && token == "" {
		err = fmt.Errorf("host OIDC response needs a token")
	}
	if err == nil {
		err = h.registerIssued(ctx, token)
	}
	return token, err
}

func (h *host) RegisterCleanup(context.Context, []string, string) (string, error) {
	return "", &apierr.Error{Code: apierr.Unavailable, Message: "fake host does not execute cleanups"}
}

func (h *host) ReleaseCleanup(context.Context, string) error {
	return &apierr.Error{Code: apierr.Unavailable, Message: "fake host does not execute cleanups"}
}

func (h *host) registerIssued(ctx context.Context, value string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.executionError(ctx); err != nil {
		return err
	}
	return h.masker.Register(value)
}

func (h *host) Mask(ctx context.Context, value string) error {
	if err := h.executionError(ctx); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.executionError(ctx); err != nil {
		return err
	}
	candidate := secrets.NewMasker()
	if err := candidate.Register(value); err != nil {
		return err
	}
	for _, plan := range h.plans {
		if _, err := nodeproto.ValidatePlan(plan, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: candidate}); err != nil {
			return err
		}
	}
	h.masker.Merge(candidate)
	h.identifierMasks.Merge(candidate)
	return nil
}

func (h *host) remember(p *sparkwing.Plan) error {
	if err := h.registerGenerated(sdkserve.DefaultOperations.Arguments(p)); err != nil {
		return err
	}
	raw, err := json.Marshal(sdkserve.DefaultOperations.Document(p))
	if err != nil {
		return err
	}
	_, err = h.accept(raw)
	return err
}

func (h *host) accept(raw json.RawMessage) (nodeproto.PlanDoc, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	doc, err := nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: h.identifierMasks})
	if err == nil && (len(h.plans) == 0 || !bytes.Equal(raw, h.plans[len(h.plans)-1])) {
		h.plans = append(h.plans, raw)
	}
	return doc, err
}

func (h *host) ContainsSecret(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.masker.ContainsSecret(id)
}

func (h *host) emit(ctx context.Context, frame nodeproto.FrameLog, job string) {
	if gate, ok := ctx.Value(outputGateKey{}).(*outputGate); ok {
		gate.mu.Lock()
		switch gate.state {
		case 0:
			gate.pending = append(gate.pending, pendingLog{frame: frame, job: job})
			gate.mu.Unlock()
			return
		case 2:
			gate.mu.Unlock()
			return
		}
		gate.mu.Unlock()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if frame.Event == "annotation" || frame.Event == "summary" {
		if err := h.executionError(ctx); err != nil {
			panic(err)
		}
	}
	if h.presenters == nil {
		h.presenters = map[string]*secrets.LogStreams{}
	}
	presenter := h.presenters[job]
	if presenter == nil {
		presenter = secrets.NewLogStreams(h.masker)
		h.presenters[job] = presenter
	}
	frames, err := presenter.Push(frame)
	if err != nil {
		frames = []nodeproto.FrameLog{{Level: "error", Message: "invalid SDK log stream"}}
	}
	for _, presented := range frames {
		h.appendRecord(presented, job)
	}
}

func (h *host) appendRecord(frame nodeproto.FrameLog, job string) {
	ts, _ := time.Parse(time.RFC3339Nano, frame.TS)
	attrs := map[string]any{}
	if len(frame.Attrs) > 0 && string(frame.Attrs) != "null" {
		decoder := json.NewDecoder(bytes.NewReader(frame.Attrs))
		decoder.UseNumber()
		if err := decoder.Decode(&attrs); err != nil {
			attrs = map[string]any{"masked_attrs": "***"}
		}
	}
	message := frame.Message
	if (frame.Event == "annotation" || frame.Event == "summary") && int64(len(message)) > limits.AnnotationBytes {
		message = limits.Record(message)
	}
	if frame.Event == "summary" {
		attrs["markdown"] = message
	}
	h.records = append(h.records, sparkwing.LogRecord{TS: ts, JobID: job, Level: frame.Level, Step: frame.Step, Event: frame.Event, Msg: message, Attrs: attrs})
}

func newHost(opts Options) Host {
	if opts.Run.Git != nil {
		git := *opts.Run.Git
		opts.Run.Git = &git
	}
	if opts.Run.Trigger.PullRequest != nil {
		pr := *opts.Run.Trigger.PullRequest
		opts.Run.Trigger.PullRequest = &pr
	}
	opts.Secrets = maps.Clone(opts.Secrets)
	outputs := make(map[string]json.RawMessage, len(opts.Outputs))
	for k, v := range opts.Outputs {
		outputs[k] = append(json.RawMessage(nil), v...)
	}
	opts.Outputs = outputs
	opts.Runner.Labels = append([]string(nil), opts.Runner.Labels...)
	ctx, cancel := context.WithCancel(context.Background())
	h := &host{opts: opts, ctx: ctx, cancel: cancel, masker: secrets.NewMasker(), identifierMasks: secrets.NewMasker()}
	for _, v := range opts.Secrets {
		if v.Masked {
			if err := h.identifierMasks.Register(v.Value); err != nil {
				h.initErr = err
			}
		}
	}
	h.masker.Merge(h.identifierMasks)
	return h
}

func (h *host) runContext(name string) nodeproto.RunContext {
	r := h.opts.Run
	if r.Pipeline != "" {
		name = r.Pipeline
	}
	out := nodeproto.RunContext{RunID: r.RunID, Pipeline: name, StartedAt: r.StartedAt, NoMemo: r.NoMemo, DryRun: r.DryRun}
	if g := r.Git; g != nil {
		out.Git = &nodeproto.Git{SHA: g.SHA, Branch: g.Branch, DefaultBranch: g.DefaultBranch, Repo: g.Repo, RepoURL: g.RepoURL}
	}
	out.Trigger = &nodeproto.Trigger{Source: r.Trigger.Source, User: r.Trigger.User}
	if pr := r.Trigger.PullRequest; pr != nil {
		out.Trigger.PullRequest = &nodeproto.PullRequest{Number: pr.Number, Action: pr.Action, BaseRef: pr.BaseRef, BaseSHA: pr.BaseSHA, HeadRef: pr.HeadRef, HeadSHA: pr.HeadSHA}
	}
	return out
}
