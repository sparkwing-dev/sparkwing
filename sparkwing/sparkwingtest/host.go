package sparkwingtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	masks   []string
	records []sparkwing.LogRecord
}

func (h *host) Context(ctx context.Context) context.Context {
	return h.context(ctx, "")
}

func (h *host) context(ctx context.Context, job string) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	if h.ctx.Err() != nil {
		cancel()
	}
	stop := context.AfterFunc(h.ctx, cancel)
	context.AfterFunc(ctx, func() { stop() })
	rc := h.runContext(h.opts.Run.Pipeline)
	r := h.opts.Runner
	return sdkserve.DefaultOperations.Context(ctx, rc, nodeproto.RunnerIdentity{Name: r.Name, Type: r.Type, Labels: r.Labels}, boundServices{h, job})
}

type boundServices struct {
	*host
	job string
}

func (s boundServices) Emit(frame nodeproto.FrameLog) { s.host.emit(frame, s.job) }
func (h *host) Plan(ctx context.Context, name string, args map[string]string) (nodeproto.PlanDoc, error) {
	ctx = h.Context(ctx)
	if err := ctx.Err(); err != nil {
		return nodeproto.PlanDoc{}, err
	}
	_, doc, err := sdkserve.DefaultOperations.Plan(ctx, name, args, h.runContext(name))
	if err != nil {
		return doc, err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return doc, err
	}
	return nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: h})
}

func (h *host) RunStep(ctx context.Context, p *sparkwing.Plan, job, step string) (json.RawMessage, error) {
	ctx = h.context(ctx, job)
	if err := ctx.Err(); err != nil {
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := sdkserve.DefaultOperations.Eval(ctx, p, nodeproto.Request{Job: job, Closure: closure, Input: input})
	if err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, result.Error
	}
	return result.Value, nil
}

func (h *host) Records() []sparkwing.LogRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, _ := json.Marshal(h.records)
	var records []sparkwing.LogRecord
	_ = json.Unmarshal(raw, &records)
	return records
}

func (h *host) Masks() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.masks...)
}
func (h *host) Close() error { h.cancel(); return nil }
func (h *host) Secret(ctx context.Context, name string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, ok := h.opts.Outputs[job]
	if !ok {
		return nil, sparkwing.ErrRefAbsent
	}
	return append(json.RawMessage(nil), raw...), nil
}

func (h *host) Latest(ctx context.Context, p, j string, _ time.Duration) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h.opts.Latest == nil {
		return nil, sparkwing.ErrRefAbsent
	}
	return h.opts.Latest(ctx, p, j)
}

func (h *host) RunPipeline(ctx context.Context, r sdkserve.ChildRequest) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h.opts.RunPipeline == nil {
		return nil, errors.New("fake host has no child pipeline handler")
	}
	return h.opts.RunPipeline(ctx, r.Pipeline, r.Args)
}

func (h *host) OIDC(ctx context.Context, a string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if h.opts.OIDCToken == nil {
		return "", sparkwing.ErrOIDCUnavailable
	}
	token, err := h.opts.OIDCToken(ctx, a)
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if value == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, v := range h.masks {
		if v == value {
			return nil
		}
	}
	h.masks = append(h.masks, value)
	return nil
}

func (h *host) ContainsSecret(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, v := range h.masks {
		if strings.Contains(id, v) {
			return true
		}
	}
	return false
}

func (h *host) emit(frame nodeproto.FrameLog, job string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	mask := func(s string) string {
		for _, v := range h.masks {
			s = strings.ReplaceAll(s, v, "***")
		}
		return s
	}
	ts, _ := time.Parse(time.RFC3339Nano, frame.TS)
	attrs := map[string]any{}
	if len(frame.Attrs) > 0 && string(frame.Attrs) != "null" {
		if err := json.Unmarshal(frame.Attrs, &attrs); err != nil {
			panic(err)
		}
	}
	maskAttrs(attrs, mask)
	message := mask(frame.Message)
	if (frame.Event == "annotation" || frame.Event == "summary") && int64(len(message)) > limits.AnnotationBytes {
		message = limits.Record(message)
	}
	if frame.Event == "summary" {
		attrs["markdown"] = message
	}
	h.records = append(h.records, sparkwing.LogRecord{TS: ts, JobID: job, Level: frame.Level, Step: frame.Step, Event: frame.Event, Msg: message, Attrs: attrs})
}

func maskAttrs(v any, mask func(string) string) {
	switch value := v.(type) {
	case map[string]any:
		for k, x := range value {
			if s, ok := x.(string); ok {
				value[k] = mask(s)
			} else {
				maskAttrs(x, mask)
			}
		}
	case []any:
		for i, x := range value {
			if s, ok := x.(string); ok {
				value[i] = mask(s)
			} else {
				maskAttrs(x, mask)
			}
		}
	}
}

func newHost(opts Options) Host {
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
