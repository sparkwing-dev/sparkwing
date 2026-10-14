package nodehost

import (
	"context"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a backstop under each source's own masking: every text a run writes
// to its store passes the run's masker, so a source that forgets cannot
// persist a secret.
type maskedState struct {
	storage.StateStore
	m *secrets.Masker
}

func maskState(st storage.StateStore, m *secrets.Masker) storage.StateStore {
	if _, done := st.(maskedState); done || st == nil || m == nil {
		return st
	}
	return maskedState{StateStore: st, m: m}
}

// safety: the backstop hides the store's optional surfaces, so they are looked up on the store it wraps.
func baseState(st storage.StateStore) storage.StateStore {
	if s, ok := st.(maskedState); ok {
		return s.StateStore
	}
	return st
}

func (s maskedState) FinishRun(ctx context.Context, runID, status, errMsg string) error {
	return s.StateStore.FinishRun(ctx, runID, status, s.m.Mask(errMsg))
}

func (s maskedState) FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error {
	return s.StateStore.FinishNode(ctx, runID, nodeID, outcome, s.m.Mask(errMsg), output)
}

func (s maskedState) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	return s.StateStore.FinishNodeWithReason(ctx, runID, nodeID, outcome, s.m.Mask(errMsg), output, reason, exitCode)
}

func (s maskedState) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	return s.StateStore.UpdateNodeActivity(ctx, runID, nodeID, s.m.Mask(detail))
}

func (s maskedState) AppendNodeAnnotation(ctx context.Context, runID, nodeID, msg string) error {
	return s.StateStore.AppendNodeAnnotation(ctx, runID, nodeID, s.m.Mask(msg))
}

func (s maskedState) SetNodeSummary(ctx context.Context, runID, nodeID, md string) error {
	return s.StateStore.SetNodeSummary(ctx, runID, nodeID, s.m.Mask(md))
}

func (s maskedState) AppendStepAnnotation(ctx context.Context, runID, nodeID, stepID, msg string) error {
	return s.StateStore.AppendStepAnnotation(ctx, runID, nodeID, stepID, s.m.Mask(msg))
}

func (s maskedState) SetStepSummary(ctx context.Context, runID, nodeID, stepID, md string) error {
	return s.StateStore.SetStepSummary(ctx, runID, nodeID, stepID, s.m.Mask(md))
}

func (s maskedState) AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error {
	return s.StateStore.AppendEvent(ctx, runID, nodeID, kind, s.m.MaskJSON(payload))
}

func (s maskedState) CreateApproval(ctx context.Context, a store.Approval) error {
	a.Message = s.m.Mask(a.Message)
	return s.StateStore.CreateApproval(ctx, a)
}

// safety: host diagnostics quote pipeline text, such as a generator's error or
// a concurrency key, so a run's logger masks its message and every attribute.
func maskLogger(l *slog.Logger, m *secrets.Masker) *slog.Logger {
	if _, done := l.Handler().(maskHandler); done || m == nil {
		return l
	}
	return slog.New(maskHandler{Handler: l.Handler(), m: m})
}

type maskHandler struct {
	slog.Handler
	m *secrets.Masker
}

func (h maskHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.m.Mask(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.Handler.Handle(ctx, out)
}

func (h maskHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	masked := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		masked[i] = h.attr(a)
	}
	return maskHandler{Handler: h.Handler.WithAttrs(masked), m: h.m}
}

func (h maskHandler) WithGroup(name string) slog.Handler {
	return maskHandler{Handler: h.Handler.WithGroup(name), m: h.m}
}

func (h maskHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		group := v.Group()
		masked := make([]any, len(group))
		for i, g := range group {
			masked[i] = h.attr(g)
		}
		return slog.Group(a.Key, masked...)
	}
	if text := v.String(); h.m.Mask(text) != text {
		return slog.String(a.Key, h.m.Mask(text))
	}
	return a
}
