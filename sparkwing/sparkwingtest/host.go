package sparkwingtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type host struct {
	opts    Options
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	work    sync.WaitGroup
	masks   []string
	records []sparkwing.LogRecord
	plans   []json.RawMessage
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
	ctx = h.Context(ctx)
	if err := h.begin(ctx); err != nil {
		return nodeproto.PlanDoc{}, err
	}
	defer h.work.Done()
	_, doc, err := sdkserve.DefaultOperations.Plan(ctx, name, args, h.runContext(name))
	if err != nil {
		return doc, err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return doc, err
	}
	return h.accept(raw)
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
	result, err := sdkserve.DefaultOperations.RunStep(ctx, p, nodeproto.Request{Job: job, Step: step, Attempt: 1})
	if err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, result.Error
	}
	return result.Output, nil
}

func (h *host) Eval(ctx context.Context, p *sparkwing.Plan, job, closure string, input json.RawMessage) (json.RawMessage, error) {
	ctx = h.Context(ctx)
	if err := h.begin(ctx); err != nil {
		return nil, err
	}
	defer h.work.Done()
	if err := h.remember(p); err != nil {
		return nil, err
	}
	result, err := sdkserve.DefaultOperations.Eval(ctx, p, nodeproto.Request{Job: job, Closure: closure, Input: input})
	if err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, result.Error
	}
	if err := h.remember(p); err != nil {
		return nil, err
	}
	return result.Value, nil
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
	return append([]string(nil), h.masks...)
}
func (h *host) Close() error {
	h.mu.Lock()
	h.cancel()
	h.mu.Unlock()
	h.work.Wait()
	return nil
}

func (h *host) begin(ctx context.Context) error {
	// safety: Close prevents new work before waiting for admitted calls to finish.
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.ctx.Err(); err != nil {
		return err
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
		if err := h.Mask(ctx, v.Value); err != nil {
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
		err = h.Mask(ctx, token)
	}
	return token, err
}

func (h *host) RegisterCleanup(context.Context, []string, string) (string, error) {
	return "", &apierr.Error{Code: apierr.Unavailable, Message: "fake host does not execute cleanups"}
}

func (h *host) ReleaseCleanup(context.Context, string) error {
	return &apierr.Error{Code: apierr.Unavailable, Message: "fake host does not execute cleanups"}
}

func (h *host) Mask(ctx context.Context, value string) error {
	if err := h.executionError(ctx); err != nil {
		return err
	}
	if value == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.executionError(ctx); err != nil {
		return err
	}
	for _, plan := range h.plans {
		if _, err := nodeproto.ValidatePlan(plan, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: registeredMasks{value}}); err != nil {
			return err
		}
	}
	for _, v := range h.masks {
		if v == value {
			return nil
		}
	}
	h.masks = append(h.masks, value)
	slices.SortFunc(h.masks, func(a, b string) int { return len(b) - len(a) })
	return nil
}

func (h *host) remember(p *sparkwing.Plan) error {
	raw, err := json.Marshal(sdkserve.DefaultOperations.Document(p))
	if err != nil {
		return err
	}
	_, err = h.accept(raw)
	return err
}

type registeredMasks []string

func (values registeredMasks) ContainsSecret(id string) bool {
	for _, value := range values {
		if value != "" && strings.Contains(id, value) {
			return true
		}
	}
	return false
}

func (h *host) accept(raw json.RawMessage) (nodeproto.PlanDoc, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	doc, err := nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: registeredMasks(h.masks)})
	if err == nil && (len(h.plans) == 0 || !bytes.Equal(raw, h.plans[len(h.plans)-1])) {
		h.plans = append(h.plans, raw)
	}
	return doc, err
}

func (h *host) ContainsSecret(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return registeredMasks(h.masks).ContainsSecret(id)
}

func (h *host) emit(ctx context.Context, frame nodeproto.FrameLog, job string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if frame.Event == "annotation" || frame.Event == "summary" {
		if err := h.executionError(ctx); err != nil {
			panic(err)
		}
	}
	mask := func(s string) string {
		for _, v := range h.masks {
			s = strings.ReplaceAll(s, v, "***")
		}
		return s
	}
	ts, _ := time.Parse(time.RFC3339Nano, frame.TS)
	attrs := map[string]any{}
	if len(frame.Attrs) > 0 && string(frame.Attrs) != "null" {
		if !json.Valid(frame.Attrs) {
			panic("invalid log attributes")
		}
		// safety: rounding a numeric secret before masking can conceal the match.
		decoder := json.NewDecoder(bytes.NewReader(frame.Attrs))
		decoder.UseNumber()
		if err := decoder.Decode(&attrs); err != nil {
			panic(err)
		}
	}
	attrs = maskAttrs(attrs, mask).(map[string]any)
	message := mask(frame.Message)
	if (frame.Event == "annotation" || frame.Event == "summary") && int64(len(message)) > limits.AnnotationBytes {
		message = limits.Record(message)
	}
	if frame.Event == "summary" {
		attrs["markdown"] = message
	}
	h.records = append(h.records, sparkwing.LogRecord{TS: ts, JobID: job, Level: frame.Level, Step: frame.Step, Event: frame.Event, Msg: message, Attrs: attrs})
}

func maskAttrs(v any, mask func(string) string) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, x := range value {
			out[mask(k)] = maskAttrs(x, mask)
		}
		return out
	case []any:
		for i, x := range value {
			value[i] = maskAttrs(x, mask)
		}
	case string:
		return mask(value)
	case json.Number, bool, nil:
		text := "null"
		if value != nil {
			text = fmt.Sprint(value)
		}
		if masked := mask(text); masked != text {
			return masked
		}
	}
	return v
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
	h := &host{opts: opts, ctx: ctx, cancel: cancel}
	for _, v := range opts.Secrets {
		if v.Masked && v.Value != "" {
			h.masks = append(h.masks, v.Value)
		}
	}
	slices.SortFunc(h.masks, func(a, b string) int { return len(b) - len(a) })
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
