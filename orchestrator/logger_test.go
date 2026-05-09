package orchestrator

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TestPrettyRenderer_StepEvents asserts the renderer recognizes the
// current step event names (`step_start`, `step_end`,
// `step_skipped`). Before this fix the switch only matched a literal
// `"step"` event, so the new names fell through to the default branch
// and rendered as plain breadcrumb lines -- losing both the step
// glyph and the duration tail. Guard with this so a future rename
// trips a check instead of silently regressing the CLI.
func TestPrettyRenderer_StepEvents(t *testing.T) {
	t.Parallel()

	t.Run("step_start renders ● glyph and step name", func(t *testing.T) {
		var buf bytes.Buffer
		r := NewPrettyRendererTo(&buf, false /* useColor */)
		r.Emit(sparkwing.LogRecord{
			TS:    time.Now(),
			Level: "info",
			Node:  "build",
			Event: "step_start",
			Msg:   "compile",
			Attrs: map[string]any{"step": "compile"},
		})
		got := buf.String()
		if !strings.Contains(got, "●") {
			t.Errorf("step_start: expected ● glyph, got %q", got)
		}
		if !strings.Contains(got, "compile") {
			t.Errorf("step_start: expected step name 'compile', got %q", got)
		}
		// Should NOT render the breadcrumb form when no Job is set --
		// that's how we know it didn't fall through to the default
		// branch (which prints `<node> │ <msg>`).
		if strings.Contains(got, "│") {
			t.Errorf("step_start: unexpected breadcrumb pipe (default-branch fallthrough?), got %q", got)
		}
	})

	t.Run("step_end success followed by step_start renders ✓ glyph + duration stand-alone", func(t *testing.T) {
		// step_end is buffered so it can collapse into a following
		// node_end on a single-step node. To force the stand-alone
		// rendering we emit a follow-up event (here, the next
		// step_start) which flushes the buffer first.
		var buf bytes.Buffer
		r := NewPrettyRendererTo(&buf, false)
		r.Emit(sparkwing.LogRecord{
			TS:    time.Now(),
			Level: "info",
			Node:  "build",
			Event: "step_end",
			Msg:   "compile",
			Attrs: map[string]any{
				"step":        "compile",
				"outcome":     "success",
				"duration_ms": int64(1234),
			},
		})
		r.Emit(sparkwing.LogRecord{
			TS:    time.Now(),
			Level: "info",
			Node:  "build",
			Event: "step_start",
			Msg:   "test",
		})
		got := buf.String()
		if !strings.Contains(got, "✓") {
			t.Errorf("step_end success: expected ✓ glyph, got %q", got)
		}
		if !strings.Contains(got, "1.2s") {
			t.Errorf("step_end success: expected duration '1.2s', got %q", got)
		}
	})

	t.Run("step_end failed followed by step_start renders ✗ glyph + duration stand-alone", func(t *testing.T) {
		var buf bytes.Buffer
		r := NewPrettyRendererTo(&buf, false)
		r.Emit(sparkwing.LogRecord{
			TS:    time.Now(),
			Level: "error",
			Node:  "build",
			Event: "step_end",
			Msg:   "push",
			Attrs: map[string]any{
				"step":        "push",
				"outcome":     "failed",
				"duration_ms": int64(2600),
				"error":       "exit 1",
			},
		})
		r.Emit(sparkwing.LogRecord{
			TS:    time.Now(),
			Level: "info",
			Node:  "build",
			Event: "step_start",
			Msg:   "next",
		})
		got := buf.String()
		if !strings.Contains(got, "✗") {
			t.Errorf("step_end failed: expected ✗ glyph, got %q", got)
		}
		if !strings.Contains(got, "push") {
			t.Errorf("step_end failed: expected step name 'push', got %q", got)
		}
		if !strings.Contains(got, "2.6s") {
			t.Errorf("step_end failed: expected duration '2.6s', got %q", got)
		}
	})

	t.Run("node_start + step_start collapses to a single line", func(t *testing.T) {
		// The single-step collapse: a node_start whose immediately
		// following event is step_start for the same node renders as
		// `▶ <node>  ● <step>` on one line, instead of two.
		var buf bytes.Buffer
		r := NewPrettyRendererTo(&buf, false)
		r.Emit(sparkwing.LogRecord{
			TS: time.Now(), Level: "info", Node: "build",
			Event: "node_start",
		})
		r.Emit(sparkwing.LogRecord{
			TS: time.Now(), Level: "info", Node: "build",
			Event: "step_start", Msg: "compile",
		})
		got := buf.String()
		// One newline only -- single combined line.
		if n := strings.Count(got, "\n"); n != 1 {
			t.Errorf("expected exactly 1 line, got %d: %q", n, got)
		}
		if !strings.Contains(got, "▶ build") {
			t.Errorf("expected node arrow + name, got %q", got)
		}
		if !strings.Contains(got, "● compile") {
			t.Errorf("expected step glyph + step name on same line, got %q", got)
		}
	})

	t.Run("step_end + node_end collapses to a single line carrying step name", func(t *testing.T) {
		// The other half of the collapse: a step_end immediately
		// followed by node_end for the same node renders as a single
		// node-end line that includes the step name as a dim suffix.
		var buf bytes.Buffer
		r := NewPrettyRendererTo(&buf, false)
		r.Emit(sparkwing.LogRecord{
			TS: time.Now(), Level: "error", Node: "build",
			Event: "step_end", Msg: "run",
			Attrs: map[string]any{"outcome": "failed", "duration_ms": int64(1200)},
		})
		r.Emit(sparkwing.LogRecord{
			TS: time.Now(), Level: "info", Node: "build",
			Event: "node_end",
			Attrs: map[string]any{"outcome": "failed", "duration_ms": int64(1200)},
		})
		got := buf.String()
		if n := strings.Count(got, "\n"); n != 1 {
			t.Errorf("expected exactly 1 combined line, got %d: %q", n, got)
		}
		if !strings.Contains(got, "✗") {
			t.Errorf("expected ✗ glyph, got %q", got)
		}
		if !strings.Contains(got, "build") {
			t.Errorf("expected node name 'build', got %q", got)
		}
		if !strings.Contains(got, "run") {
			t.Errorf("expected step name 'run' as suffix, got %q", got)
		}
		if !strings.Contains(got, "1.2s") {
			t.Errorf("expected duration '1.2s', got %q", got)
		}
	})

	t.Run("step_skipped renders ⊘ glyph and reason", func(t *testing.T) {
		var buf bytes.Buffer
		r := NewPrettyRendererTo(&buf, false)
		r.Emit(sparkwing.LogRecord{
			TS:    time.Now(),
			Level: "info",
			Node:  "build",
			Event: "step_skipped",
			Msg:   "deploy",
			Attrs: map[string]any{
				"step":    "deploy",
				"outcome": "skipped",
				"reason":  "downstream of --stop-at=push",
			},
		})
		got := buf.String()
		if !strings.Contains(got, "⊘") {
			t.Errorf("step_skipped: expected ⊘ glyph, got %q", got)
		}
		if !strings.Contains(got, "deploy") {
			t.Errorf("step_skipped: expected step name 'deploy', got %q", got)
		}
		if !strings.Contains(got, "downstream of --stop-at=push") {
			t.Errorf("step_skipped: expected reason, got %q", got)
		}
	})
}
