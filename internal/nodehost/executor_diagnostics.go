package nodehost

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func (h *nodeExecutor) diagnostic(ctx context.Context, input NodeInput, level, event, step, message string, attrs map[string]any) error {
	if h.deps.Log == nil {
		return nil
	}
	if err := executorLostAuthority(ctx); err != nil {
		return err
	}
	raw, err := json.Marshal(attrs)
	if err != nil {
		return err
	}
	return h.deps.Log(ctx, input, nodewire.FrameLog{
		TS: time.Now().UTC().Format(time.RFC3339Nano), Level: level, Event: event,
		Step: h.deps.Masker.Mask(step), Message: h.deps.Masker.Mask(message), Attrs: h.deps.Masker.Attributes(raw),
	})
}

func (h *nodeExecutor) stepDiagnostic(ctx context.Context, input NodeInput, report store.StepReport, reason string) error {
	level, event := "info", "step_end"
	attrs := map[string]any{"step": report.StepID}
	switch report.State {
	case "running":
		event = "step_start"
	case "skipped":
		event, attrs["outcome"] = "step_skipped", "skipped"
		if reason != "" {
			attrs["reason"] = reason
		}
	default:
		attrs["outcome"] = report.State
		if report.State == "passed" {
			attrs["outcome"] = "success"
		}
		attrs["duration_ms"] = max(0, report.FinishedAt.Sub(*report.StartedAt).Milliseconds())
		if report.MaskedError != "" {
			attrs["error"] = report.MaskedError
		}
		if report.State == "failed" {
			level = "error"
		}
	}
	return h.diagnostic(ctx, input, level, event, report.StepID, report.StepID, attrs)
}

func (h *nodeExecutor) cacheReporter(ctx context.Context, input NodeInput) CacheReporter {
	return func(level, message string) {
		attrs := map[string]any{}
		if h.deps.Annotate != nil {
			if err := h.deps.Annotate(ctx, input, nodewire.AnnotationRequest{Annotation: nodewire.Annotation{Level: level, Message: h.deps.Masker.Mask(message)}}); err != nil {
				attrs["annotation_error"] = h.deps.Masker.Mask(err.Error())
			}
		}
		if err := h.diagnostic(ctx, input, level, "cache", "", message, attrs); err != nil {
			// safety: a cache line is advisory, so a failed log write is reported, not fatal.
			slog.Warn(h.deps.Masker.Mask(message), "cache_log_error", h.deps.Masker.Mask(err.Error()))
		}
	}
}
