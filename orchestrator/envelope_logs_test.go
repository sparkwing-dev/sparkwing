package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
)

// TestEnvelopeLog_PersistsRunStartFinish verifies the envelope tee
// writes run_start + run_finish records (and node_start/node_end) to
// <runDir>/_envelope.ndjson during a local dispatch. IMP-010.
func TestEnvelopeLog_PersistsRunStartFinish(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}

	data, err := os.ReadFile(p.EnvelopeLog(res.RunID))
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("envelope file is empty; expected at least run_start + run_finish")
	}

	want := []string{"run_start", "run_finish", "node_start", "node_end"}
	for _, evt := range want {
		if !bytes.Contains(data, []byte(`"event":"`+evt+`"`)) {
			t.Errorf("envelope missing event %q\n%s", evt, data)
		}
	}
}

// TestJobLogs_EventsOnlyFiltersBodyLines confirms --events-only
// drops exec_line records and keeps the bracket events.
func TestJobLogs_EventsOnlyFiltersBodyLines(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}

	var buf bytes.Buffer
	if err := orchestrator.JobLogs(context.Background(), p, res.RunID,
		orchestrator.LogsOpts{EventsOnly: true, JSON: true}, &buf); err != nil {
		t.Fatalf("JobLogs --events-only: %v", err)
	}

	gotStart := false
	gotFinish := false
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("non-json line %q: %v", line, err)
		}
		evt, _ := rec["event"].(string)
		if evt == "" || evt == "exec_line" {
			t.Errorf("--events-only leaked record event=%q line=%s", evt, line)
		}
		if evt == "run_start" {
			gotStart = true
		}
		if evt == "run_finish" {
			gotFinish = true
		}
	}
	if !gotStart || !gotFinish {
		t.Errorf("missing canonical events: start=%v finish=%v\n%s", gotStart, gotFinish, buf.String())
	}
}

// TestJobLogs_NoEventsMatchesLegacy verifies --no-events restores
// today's body-only behavior so existing scripts keep working.
func TestJobLogs_NoEventsMatchesLegacy(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}

	var buf bytes.Buffer
	if err := orchestrator.JobLogs(context.Background(), p, res.RunID,
		orchestrator.LogsOpts{NoEvents: true, JSON: true}, &buf); err != nil {
		t.Fatalf("JobLogs --no-events: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "work complete") {
		t.Fatalf("--no-events should still surface body output, got:\n%s", out)
	}
	// Run-level events must NOT appear under --no-events.
	if strings.Contains(out, `"event":"run_start"`) || strings.Contains(out, `"event":"run_finish"`) {
		t.Fatalf("--no-events leaked envelope events:\n%s", out)
	}
}

// TestJobLogs_DefaultIsMergedStream confirms the new default mode
// includes both bracket events and body output -- the canonical
// "watch this run" surface IMP-010 ships.
func TestJobLogs_DefaultIsMergedStream(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}

	var buf bytes.Buffer
	if err := orchestrator.JobLogs(context.Background(), p, res.RunID,
		orchestrator.LogsOpts{JSON: true}, &buf); err != nil {
		t.Fatalf("JobLogs default: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"event":"run_start"`) {
		t.Fatalf("default mode missing run_start (envelope event):\n%s", out)
	}
	if !strings.Contains(out, "work complete") {
		t.Fatalf("default mode missing body output:\n%s", out)
	}
}

// TestJobLogs_GrepWorksWithEventsOnly confirms --grep composes with
// --events-only, satisfying the ticket's filter-flags spec.
func TestJobLogs_GrepWorksWithEventsOnly(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}

	var buf bytes.Buffer
	if err := orchestrator.JobLogs(context.Background(), p, res.RunID,
		orchestrator.LogsOpts{
			EventsOnly: true,
			Grep:       "run_finish",
			JSON:       true,
		}, &buf); err != nil {
		t.Fatalf("JobLogs --events-only --grep: %v", err)
	}
	out := strings.TrimSpace(buf.String())
	if out == "" {
		t.Fatal("expected at least one matching line for --grep run_finish")
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "run_finish") {
			t.Errorf("grep leaked non-matching line: %q", line)
		}
	}
}

// TestJobLogs_EventsOnlyAndNoEventsConflict guards the API contract.
func TestJobLogs_EventsOnlyAndNoEventsConflict(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	var buf bytes.Buffer
	err = orchestrator.JobLogs(context.Background(), p, res.RunID,
		orchestrator.LogsOpts{EventsOnly: true, NoEvents: true}, &buf)
	if err == nil {
		t.Fatal("expected error when both --events-only and --no-events are set")
	}
}
