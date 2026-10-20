package nodehost

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (h *nodeExecutor) diagnostic(ctx context.Context, input NodeInput, level, event, step, message string, attrs map[string]any) error {
	if h.deps.Log == nil {
		return nil
	}
	if err := executorLostAuthority(ctx); err != nil {
		return err
	}
	for key, value := range attrs {
		switch value := value.(type) {
		case string:
			attrs[key] = h.deps.Mask(value)
		case []string:
			masked := make([]string, len(value))
			for i, text := range value {
				masked[i] = h.deps.Mask(text)
			}
			attrs[key] = masked
		}
	}
	raw, err := json.Marshal(attrs)
	if err != nil {
		return err
	}
	return h.deps.Log(ctx, input, nodeproto.FrameLog{
		TS: time.Now().UTC().Format(time.RFC3339Nano), Level: level, Event: event,
		Step: h.deps.Mask(step), Message: h.deps.Mask(message), Attrs: raw,
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
