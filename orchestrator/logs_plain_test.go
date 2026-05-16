package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TestFormatPlain_IncludesStepWhenSet pins the dashboard-style
// `node/step` prefix on plain log output so agents can group by step
// without parsing body lines.
func TestFormatPlain_IncludesStepWhenSet(t *testing.T) {
	ts := time.Date(2026, 5, 12, 14, 0, 0, 0, time.UTC)
	rec := sparkwing.LogRecord{
		TS:    ts,
		Level: "info",
		JobID: "deploy",
		Step:  "canary",
		Msg:   "rolling 5%",
	}
	out := formatPlain(rec)
	if !strings.Contains(out, "deploy/canary") {
		t.Errorf("missing node/step prefix in %q", out)
	}
}

// TestFormatPlain_NodeOnlyWhenNoStep keeps the legacy "node " prefix
// (no trailing slash) when a record carries no step id.
func TestFormatPlain_NodeOnlyWhenNoStep(t *testing.T) {
	ts := time.Date(2026, 5, 12, 14, 0, 0, 0, time.UTC)
	rec := sparkwing.LogRecord{
		TS:    ts,
		Level: "info",
		JobID: "build",
		Msg:   "starting",
	}
	out := formatPlain(rec)
	if strings.Contains(out, "build/") {
		t.Errorf("step prefix should not appear when Step is empty: %q", out)
	}
	if !strings.Contains(out, " build ") {
		t.Errorf("expected ' build ' in %q", out)
	}
}
