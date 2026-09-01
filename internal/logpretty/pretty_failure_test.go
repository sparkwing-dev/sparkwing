package logpretty

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func renderRunFailure(t *testing.T, nodes []any) string {
	t.Helper()
	var buf bytes.Buffer
	r := NewPrettyRendererTo(&buf, false)
	r.Emit(sparkwing.LogRecord{TS: time.Now(), Level: "info", Event: "run_summary", Attrs: map[string]any{
		"status":      "failed",
		"duration_ms": int64(154000),
		"nodes":       nodes,
	}})
	r.Emit(sparkwing.LogRecord{TS: time.Now(), Level: "info", Event: "run_finish", Attrs: map[string]any{
		"run_id":      "run-xyz",
		"status":      "failed",
		"duration_ms": int64(154000),
	}})
	r.Flush()
	return buf.String()
}

func TestRunSummary_HeadlineLeadsWithRootCauseLeaf(t *testing.T) {
	out := renderRunFailure(t, []any{
		map[string]any{"id": "build", "outcome": "success", "duration_ms": int64(2000)},
		map[string]any{
			"id": "deploy", "outcome": "failed", "duration_ms": int64(900),
			"error": `step "push": connection refused`,
		},
		map[string]any{"id": "verify", "outcome": "cancelled"},
		map[string]any{"id": "notify", "outcome": "cancelled"},
	})

	causeIdx := strings.Index(out, "cause")
	if causeIdx < 0 {
		t.Fatalf("no root-cause line in headline:\n%s", out)
	}
	for _, want := range []string{"deploy", "connection refused"} {
		if !strings.Contains(out, want) {
			t.Errorf("headline missing %q:\n%s", want, out)
		}
	}

	if !strings.Contains(out, "2 nodes cancelled by the failure") {
		t.Errorf("missing cascade summary:\n%s", out)
	}
	cascadeIdx := strings.Index(out, "cascade")
	if cascadeIdx < 0 || cascadeIdx < causeIdx {
		t.Errorf("cascade line should follow the root cause; cause@%d cascade@%d:\n%s", causeIdx, cascadeIdx, out)
	}

	if !strings.Contains(out, "1 failed") {
		t.Errorf("tally should show 1 failed:\n%s", out)
	}
	if !strings.Contains(out, "2 cancelled") {
		t.Errorf("tally should show 2 cancelled distinctly:\n%s", out)
	}
	if strings.Contains(out, "3 failed") {
		t.Errorf("dependents must not be counted as failed:\n%s", out)
	}
}

func TestRunSummary_SkippedDistinctFromCancelled(t *testing.T) {
	out := renderRunFailure(t, []any{
		map[string]any{"id": "gate", "outcome": "failed", "error": "boom"},
		map[string]any{"id": "downstream", "outcome": "cancelled"},
		map[string]any{"id": "optional", "outcome": "skipped"},
	})
	if !strings.Contains(out, "1 cancelled") {
		t.Errorf("want 1 cancelled in tally:\n%s", out)
	}
	if !strings.Contains(out, "1 skipped") {
		t.Errorf("want 1 skipped in tally:\n%s", out)
	}
}
