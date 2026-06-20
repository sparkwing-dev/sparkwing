package logpretty

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func quietRun(t *testing.T, status string, nodes []any, noise ...sparkwing.LogRecord) string {
	t.Helper()
	var buf bytes.Buffer
	r := NewQuietRendererTo(&buf, false)
	r.Emit(sparkwing.LogRecord{TS: time.Now(), Event: "run_start", Attrs: map[string]any{
		"pipeline": "pre-push",
		"run_id":   "run-abc",
	}})
	for _, n := range noise {
		r.Emit(n)
	}
	r.Emit(sparkwing.LogRecord{TS: time.Now(), Event: "run_summary", Attrs: map[string]any{
		"status":      status,
		"duration_ms": int64(12300),
		"nodes":       nodes,
	}})
	r.Emit(sparkwing.LogRecord{TS: time.Now(), Event: "run_finish", Attrs: map[string]any{
		"run_id": "run-abc",
		"status": status,
	}})
	r.Flush()
	return buf.String()
}

func TestQuiet_SuccessShowsProgressAndStatusOnly(t *testing.T) {
	out := quietRun(t, "success", []any{
		map[string]any{"id": "pre-push", "outcome": "success", "duration_ms": int64(12300)},
	},
		sparkwing.LogRecord{Event: "run_plan", Attrs: map[string]any{"plan_hash": "sha256:deadbeef"}},
		sparkwing.LogRecord{Event: "node_start", JobID: "pre-push"},
		sparkwing.LogRecord{Event: "step_start", JobID: "pre-push", Msg: "golangci-lint"},
		sparkwing.LogRecord{Event: "exec_line", JobID: "pre-push", Msg: "linting 412 files"},
		sparkwing.LogRecord{Event: "step_end", JobID: "pre-push", Msg: "golangci-lint", Attrs: map[string]any{"outcome": "success"}},
	)

	if !strings.Contains(out, "pre-push running") {
		t.Errorf("missing progress line:\n%s", out)
	}
	if !strings.Contains(out, "passed") || !strings.Contains(out, "run-abc") {
		t.Errorf("missing final pass status with run id:\n%s", out)
	}
	if !strings.Contains(out, "12.3s") {
		t.Errorf("final status should carry duration:\n%s", out)
	}
	for _, noise := range []string{"golangci-lint", "linting 412 files", "node_start", "Plan", "sha256"} {
		if strings.Contains(out, noise) {
			t.Errorf("quiet output leaked per-step detail %q:\n%s", noise, out)
		}
	}
	if got := strings.Count(strings.TrimSpace(out), "\n"); got != 1 {
		t.Errorf("clean run should be exactly two lines, got %d:\n%s", got+1, out)
	}
}

func TestQuiet_FailureSurfacesFailingStepAndLogPointer(t *testing.T) {
	out := quietRun(t, "failed", []any{
		map[string]any{
			"id": "pre-push", "outcome": "failed", "duration_ms": int64(8000),
			"error": "step \"checks\": 2 pre-push check(s) failed:\n  - gofmt: drift\n  - govulncheck: GO-2026-0001",
		},
	})

	if !strings.Contains(out, "failed") || !strings.Contains(out, "run-abc") {
		t.Errorf("missing final fail status with run id:\n%s", out)
	}
	for _, want := range []string{"checks", "gofmt: drift", "govulncheck: GO-2026-0001"} {
		if !strings.Contains(out, want) {
			t.Errorf("failure detail missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "sparkwing runs logs --run run-abc") {
		t.Errorf("failure should point at the retained log:\n%s", out)
	}
}

func TestQuiet_FailureOmitsCascadeNodes(t *testing.T) {
	out := quietRun(t, "failed", []any{
		map[string]any{"id": "gate", "outcome": "failed", "error": "boom"},
		map[string]any{"id": "downstream", "outcome": "cancelled"},
	})
	if !strings.Contains(out, "boom") {
		t.Errorf("want the failing node's error:\n%s", out)
	}
	if strings.Contains(out, "downstream") {
		t.Errorf("cancelled cascade node should not appear:\n%s", out)
	}
}
