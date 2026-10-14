package nodehost

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

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
	m        *secrets.Masker
	bindings []maskBinding
}

type maskBinding struct {
	group string
	attrs []slog.Attr
}

func (h maskHandler) Handle(ctx context.Context, r slog.Record) error {
	inner := h.Handler
	// safety: secrets may be registered after With, so binding cannot cache masked values.
	for _, binding := range h.bindings {
		if binding.group != "" {
			inner = inner.WithGroup(binding.group)
			continue
		}
		masked := make([]slog.Attr, len(binding.attrs))
		for i, a := range binding.attrs {
			masked[i] = h.attr(a)
		}
		inner = inner.WithAttrs(masked)
	}
	out := slog.NewRecord(r.Time, r.Level, h.m.Mask(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return inner.Handle(ctx, out)
}

func (h maskHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.bindings = append(h.bindings[:len(h.bindings):len(h.bindings)], maskBinding{attrs: attrs})
	return h
}

func (h maskHandler) WithGroup(name string) slog.Handler {
	if name != "" {
		h.bindings = append(h.bindings[:len(h.bindings):len(h.bindings)], maskBinding{group: name})
	}
	return h
}

func (h maskHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	a.Value = v
	if a.Equal(slog.Attr{}) {
		return a
	}
	if v.Kind() == slog.KindGroup {
		group := v.Group()
		masked := make([]any, len(group))
		for i, g := range group {
			masked[i] = h.attr(g)
		}
		return slog.Group(a.Key, masked...)
	}
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.m.Mask(v.String()))
	case slog.KindAny:
		// safety: JSON text keeps structured content but loses object nesting, so downstream handlers
		// cannot invoke the original value's encoders after masking.
		return slog.String(a.Key, h.m.Mask(renderMaskValue(v.Any())))
	}
	return a
}

func renderMaskValue(value any) (text string) {
	const unrenderable = "!masked: unrenderable"
	defer func() {
		if recover() != nil {
			text = unrenderable
		}
	}()
	if err, ok := value.(error); ok {
		return err.Error()
	}
	encoded, err := json.Marshal(value)
	if err == nil {
		return string(encoded)
	}
	if _, unsupported := err.(*json.UnsupportedTypeError); !unsupported {
		return unrenderable
	}
	text = fmt.Sprintf("%+v", value)
	// safety: fmt recovers method panics itself and embeds their payload in its output.
	if strings.Contains(text, "%!v(PANIC=") {
		return unrenderable
	}
	return text
}
