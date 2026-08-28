package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

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

func TestEnvelopeLog_RunStartCarriesLogPath(t *testing.T) {
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
	var runStart map[string]any
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if evt, _ := rec["event"].(string); evt == "run_start" {
			runStart, _ = rec["attrs"].(map[string]any)
			break
		}
	}
	if runStart == nil {
		t.Fatalf("no run_start record with attrs in:\n%s", data)
	}
	want := p.RunDir(res.RunID)
	if runStart["log_path"] != want {
		t.Errorf("run_start log_path = %v, want %q", runStart["log_path"], want)
	}
	if filepath.Dir(p.EnvelopeLog(res.RunID)) != want {
		t.Errorf("log_path %q does not contain the envelope log", want)
	}
}

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
	if strings.Contains(out, `"event":"run_start"`) || strings.Contains(out, `"event":"run_finish"`) {
		t.Fatalf("--no-events leaked envelope events:\n%s", out)
	}
}

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
	if !strings.HasPrefix(err.Error(), "runs logs:") {
		t.Fatalf("error = %q, want runs logs prefix", err)
	}
}
