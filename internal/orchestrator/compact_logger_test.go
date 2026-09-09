package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestCompactRunKeepsTerminalStatusAndBoundsNoise(t *testing.T) {
	for _, status := range []string{"success", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			var out bytes.Buffer
			r := newCompactRunRenderer(&out)
			r.Emit(sparkwing.LogRecord{Event: "run_start", Attrs: map[string]any{"run_id": "fixture", "pipeline": "test", "args": strings.Repeat("secret", 10000)}})
			for i := 0; i < 1000; i++ {
				r.Emit(sparkwing.LogRecord{Event: "exec_line", JobID: "test", Level: "error", Msg: strings.Repeat("界", 10000)})
				r.Emit(sparkwing.LogRecord{Event: "node_end", JobID: "test", Attrs: map[string]any{"outcome": "success"}})
				r.Emit(sparkwing.LogRecord{Event: "run_start", JobID: "child", Attrs: map[string]any{"run_id": "child"}})
			}
			var nodes []any
			for i := 0; i < 100; i++ {
				nodes = append(nodes, map[string]any{"id": "failed-node", "outcome": "failed", "error": strings.Repeat("界", 10000)})
			}
			r.Emit(sparkwing.LogRecord{Event: "run_summary", Attrs: map[string]any{"duration_ms": int64(2), "nodes": nodes}})
			r.Emit(sparkwing.LogRecord{Event: "run_finish", Attrs: map[string]any{"run_id": "fixture", "status": status, "error": strings.Repeat("cause", 10000)}})
			if err := r.Err(); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) > 50 {
				t.Fatalf("%d lines", len(lines))
			}
			for _, line := range lines {
				if len(line) > 32768 {
					t.Fatalf("oversize record: %d", len(line))
				}
				if !json.Valid([]byte(line)) {
					t.Fatal("invalid JSON")
				}
			}
			var finish sparkwing.LogRecord
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &finish); err != nil {
				t.Fatal(err)
			}
			if finish.Attrs["status"] != status || finish.Event != "run_finish" {
				t.Fatalf("terminal state lost: %v", finish)
			}
			if finish.Attrs["omitted_failures"] != float64(95) {
				t.Fatalf("failure truncation hidden: %v", finish.Attrs)
			}
			if !strings.Contains(out.String(), "[truncated]") || strings.Contains(out.String(), "secret") {
				t.Fatal("display bounds or attribute filtering failed")
			}
		})
	}
}

func TestCompactRunPreservesFullStoredLog(t *testing.T) {
	var out bytes.Buffer
	display := newCompactRunRenderer(&out)
	path := t.TempDir() + "/node.log"
	log, err := newNodeLogger(path, "test", display)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("complete child output", 1000)
	log.Emit(sparkwing.LogRecord{Event: "exec_line", Msg: body})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record sparkwing.LogRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Msg != body || out.Len() != 0 {
		t.Fatal("stored log changed or child noise displayed")
	}
}

type compactBrokenWriter struct{}

func (compactBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("closed output") }
func TestCompactRunReportsWriteFailure(t *testing.T) {
	r := newCompactRunRenderer(compactBrokenWriter{})
	r.Emit(sparkwing.LogRecord{Event: "run_start"})
	if r.Err() == nil {
		t.Fatal("output failure lost")
	}
}

func TestCompactRunBoundsEscapedFieldsAndConcurrentProgress(t *testing.T) {
	var out bytes.Buffer
	r := newCompactRunRenderer(&out)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.Emit(sparkwing.LogRecord{Event: "node_end", JobID: "worker"}) }()
	}
	wg.Wait()
	large := strings.Repeat("\x00", 10000)
	var nodes []any
	for i := 0; i < 100; i++ {
		nodes = append(nodes, map[string]any{"id": large, "outcome": "failed", "error": large})
	}
	r.Emit(sparkwing.LogRecord{Event: "run_summary", Attrs: map[string]any{"nodes": nodes}})
	r.Emit(sparkwing.LogRecord{Event: "run_finish", Level: large, Step: large, Msg: large, Attrs: map[string]any{"run_id": large, "status": large, "error": large}})
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if len(line) >= 32768 || !json.Valid([]byte(line)) {
			t.Fatalf("invalid or oversized line: %d bytes", len(line))
		}
	}
	if r.Err() != nil {
		t.Fatal(r.Err())
	}
}

func TestRunRendererSelectionKeepsDetailedTransport(t *testing.T) {
	t.Setenv("SPARKWING_LOG_FORMAT", "json")
	t.Setenv("SPARKWING_LOG_LEVEL", "")
	if _, ok := selectRunRenderer().(*compactRunRenderer); !ok {
		t.Fatal("JSON run did not select compact display")
	}
	if _, ok := selectLocalRenderer().(*JSONRenderer); !ok {
		t.Fatal("node transport lost its complete renderer")
	}
	t.Setenv("SPARKWING_LOG_LEVEL", "debug")
	if _, ok := selectRunRenderer().(*JSONRenderer); !ok {
		t.Fatal("verbose run lost its full stream")
	}
	for _, format := range []string{"pretty", "quiet"} {
		t.Setenv("SPARKWING_LOG_FORMAT", format)
		if _, ok := selectRunRenderer().(*compactRunRenderer); ok {
			t.Fatalf("%s selected JSON", format)
		}
	}
}

func TestCompactRunReportsAdmissionFailure(t *testing.T) {
	registerAdmitFailPipeline()
	home := wingdTestHome(t)
	backends, _, _ := openWingdBackends(t, home)
	var out bytes.Buffer
	display := newCompactRunRenderer(&out)
	admission := &LocalAdmission{Home: home, Version: "test", Out: io.Discard, Spawn: func(string, string) error { return errors.New("no daemon for test") }, DialTimeout: 100 * time.Millisecond, Backoff: 10 * time.Millisecond}
	result, err := Run(context.Background(), backends, Options{Pipeline: "admit-fail-e2e", RunID: "compact-admission", Delegate: display, Admission: admission})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" {
		t.Fatalf("status: %s", result.Status)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var finish sparkwing.LogRecord
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &finish); err != nil {
		t.Fatal(err)
	}
	if finish.Event != "run_finish" || finish.Attrs["status"] != "failed" || finish.Attrs["error"] == "" {
		t.Fatalf("admission failure lost: %v", finish)
	}
	if len(lines) > 27 {
		t.Fatalf("%d lines", len(lines))
	}
}

func TestCompactRunFinishesEarlyFailureOnce(t *testing.T) {
	var out bytes.Buffer
	r := newCompactRunRenderer(&out)
	r.finishResult(&Result{RunID: "early", Status: "failed", Error: errors.New("planning failed")}, nil)
	r.finishResult(&Result{RunID: "early", Status: "success"}, nil)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("%d terminal records", len(lines))
	}
	var finish sparkwing.LogRecord
	if err := json.Unmarshal([]byte(lines[0]), &finish); err != nil {
		t.Fatal(err)
	}
	if finish.Attrs["status"] != "failed" || finish.Attrs["error"] != "planning failed" || finish.Attrs["run_id"] != "early" {
		t.Fatalf("early failure lost: %v", finish)
	}
}

func TestCompactRunRetainsStartIdentityOnEarlyReturn(t *testing.T) {
	var out bytes.Buffer
	r := newCompactRunRenderer(&out)
	r.Emit(sparkwing.LogRecord{Event: "run_start", Attrs: map[string]any{"run_id": "started"}})
	r.finishResult(nil, errors.New("early error"))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var finish sparkwing.LogRecord
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &finish); err != nil {
		t.Fatal(err)
	}
	if finish.Attrs["run_id"] != "started" || finish.Attrs["status"] != "failed" || finish.Attrs["hints"] == nil {
		t.Fatalf("identity or failure lost: %v", finish)
	}
}
