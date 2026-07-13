package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestListJobs_EmptyDB(t *testing.T) {
	p := newPaths(t)
	var buf bytes.Buffer
	if err := orchestrator.ListJobs(context.Background(), p, orchestrator.ListOpts{Limit: 10}, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if !strings.Contains(buf.String(), "no runs yet") {
		t.Fatalf("expected empty-state message, got %q", buf.String())
	}
}

func TestListJobs_ShowsRecentRun(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var buf bytes.Buffer
	if err := orchestrator.ListJobs(context.Background(), p, orchestrator.ListOpts{Limit: 10}, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, res.RunID) {
		t.Fatalf("list missing run id %s: %s", res.RunID, out)
	}
	if !strings.Contains(out, "orch-ok") {
		t.Fatalf("list missing pipeline name: %s", out)
	}
	if !strings.Contains(out, "success") {
		t.Fatalf("list missing status: %s", out)
	}
}

func TestListJobs_FilterByPipeline(t *testing.T) {
	p := newPaths(t)
	_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-fail"})

	var buf bytes.Buffer
	err := orchestrator.ListJobs(context.Background(), p, orchestrator.ListOpts{
		Limit:     10,
		Pipelines: []string{"orch-fail"},
	}, &buf)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "orch-ok") {
		t.Fatalf("filter by pipeline leaked other pipelines: %s", out)
	}
	if !strings.Contains(out, "orch-fail") {
		t.Fatalf("filter missing matching pipeline: %s", out)
	}
}

func TestListJobs_FilterByStatus(t *testing.T) {
	p := newPaths(t)
	_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-fail"})

	var buf bytes.Buffer
	err := orchestrator.ListJobs(context.Background(), p, orchestrator.ListOpts{
		Limit:    10,
		Statuses: []string{"failed"},
	}, &buf)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "orch-ok") {
		t.Fatalf("status filter leaked successes: %s", out)
	}
	if !strings.Contains(out, "orch-fail") {
		t.Fatalf("status filter missing expected run: %s", out)
	}
}

func TestListJobs_FilterBySinceHidesOldRuns(t *testing.T) {
	p := newPaths(t)
	_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	time.Sleep(50 * time.Millisecond)

	var buf bytes.Buffer
	err := orchestrator.ListJobs(context.Background(), p, orchestrator.ListOpts{
		Limit: 10,
		Since: 10 * time.Millisecond,
	}, &buf)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if !strings.Contains(buf.String(), "no runs yet") {
		t.Fatalf("expected since-filter to hide older run, got %s", buf.String())
	}
}

func TestListJobs_JSONOutput(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var buf bytes.Buffer
	if err := orchestrator.ListJobs(context.Background(), p, orchestrator.ListOpts{JSON: true, Limit: 10}, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	var runs []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &runs); err != nil {
		t.Fatalf("json parse: %v\n%s", err, buf.String())
	}
	if len(runs) != 1 || runs[0]["id"] != res.RunID {
		t.Fatalf("unexpected json: %v", runs)
	}
}

func TestListJobs_ShowsLocalAdmissionWait(t *testing.T) {
	p := newPaths(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	started := time.Now().Add(-2 * time.Minute)
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-admission-list",
		Pipeline:  "push-checks",
		Status:    "running",
		StartedAt: started,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := st.AppendEvent(ctx, "run-admission-list", "", "admission_wait", []byte(`{"position":1,"queue_length":3}`)); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	var buf bytes.Buffer
	if err := orchestrator.ListJobs(ctx, p, orchestrator.ListOpts{Limit: 10}, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "queued (1/3)") {
		t.Fatalf("list should surface admission wait, got:\n%s", out)
	}
}

func TestListJobs_IgnoresStaleAdmissionWaitForFinishedRun(t *testing.T) {
	p := newPaths(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	started := time.Now().Add(-3 * time.Minute)
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-stale-admission-list",
		Pipeline:  "push-checks",
		Status:    "running",
		StartedAt: started,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.FinishRun(ctx, "run-stale-admission-list", "cancelled", "cancelled via test"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if _, err := st.AppendEvent(ctx, "run-stale-admission-list", "", "admission_wait", []byte(`{"position":1,"queue_length":3}`)); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	var buf bytes.Buffer
	if err := orchestrator.ListJobs(ctx, p, orchestrator.ListOpts{Limit: 10}, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "admission_wait") {
		t.Fatalf("finished run should not show stale admission wait, got:\n%s", out)
	}
	if !strings.Contains(out, "cancelled") {
		t.Fatalf("finished run should keep terminal status, got:\n%s", out)
	}
}

func TestJobStatus_RendersFanOutDAG(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-fanout-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var buf bytes.Buffer
	if err := orchestrator.JobStatus(context.Background(), p, res.RunID, orchestrator.StatusOpts{}, &buf); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"setup", "a", "b", "fin", "success"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q in:\n%s", want, out)
		}
	}
}

func TestJobStatus_ShowsError(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-middle-fails"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var buf bytes.Buffer
	if err := orchestrator.JobStatus(context.Background(), p, res.RunID, orchestrator.StatusOpts{}, &buf); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "mid fail") {
		t.Fatalf("status should include error message, got:\n%s", out)
	}
	if !strings.Contains(out, "cancelled") {
		t.Fatalf("status should show cancelled downstream, got:\n%s", out)
	}
	if strings.Count(out, "upstream-failed") > 0 {
		if strings.Contains(out, "c error:") {
			t.Fatalf("upstream-failed should not appear as error trailer: %s", out)
		}
	}
}

func TestJobStatus_JSON(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var buf bytes.Buffer
	if err := orchestrator.JobStatus(context.Background(), p, res.RunID, orchestrator.StatusOpts{JSON: true}, &buf); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("json parse: %v\n%s", err, buf.String())
	}
	run, _ := payload["run"].(map[string]any)
	if run["id"] != res.RunID {
		t.Fatalf("json run id mismatch: %v", run)
	}
}

func TestJobStatus_ShowsLocalAdmissionWait(t *testing.T) {
	p := newPaths(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	started := time.Now().Add(-2 * time.Minute)
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-admission-wait",
		Pipeline:  "push-checks",
		Status:    "running",
		StartedAt: started,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID:  "run-admission-wait",
		NodeID: "gate-test",
		Status: "pending",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if _, err := st.AppendEvent(ctx, "run-admission-wait", "", "admission_wait", []byte(`{"position":2,"queue_length":5}`)); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	var buf bytes.Buffer
	if err := orchestrator.JobStatus(ctx, p, "run-admission-wait", orchestrator.StatusOpts{}, &buf); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"status:    running",
		"admission: queued for local admission",
		"position 2 of 5",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q in:\n%s", want, out)
		}
	}
}

func TestJobStatus_IgnoresStaleAdmissionWaitForFinishedRun(t *testing.T) {
	p := newPaths(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	started := time.Now().Add(-3 * time.Minute)
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-stale-admission-status",
		Pipeline:  "push-checks",
		Status:    "running",
		StartedAt: started,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.FinishRun(ctx, "run-stale-admission-status", "cancelled", "cancelled via test"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if _, err := st.AppendEvent(ctx, "run-stale-admission-status", "", "admission_wait", []byte(`{"position":2,"queue_length":5}`)); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	var buf bytes.Buffer
	if err := orchestrator.JobStatus(ctx, p, "run-stale-admission-status", orchestrator.StatusOpts{}, &buf); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "admission:") {
		t.Fatalf("finished run should not show stale admission wait, got:\n%s", out)
	}
}

func TestListJobs_ReadsAdmissionTerminalEventPastFirstPage(t *testing.T) {
	p := newPaths(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	started := time.Now().Add(-2 * time.Minute)
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-admission-long-events",
		Pipeline:  "push-checks",
		Status:    "running",
		StartedAt: started,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := st.AppendEvent(ctx, "run-admission-long-events", "", "admission_wait", []byte(`{"position":1,"queue_length":1}`)); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	for i := 0; i < 500; i++ {
		if _, err := st.AppendEvent(ctx, "run-admission-long-events", "node", "node_started", nil); err != nil {
			t.Fatalf("AppendEvent filler %d: %v", i, err)
		}
	}
	if _, err := st.AppendEvent(ctx, "run-admission-long-events", "", "admission_granted", nil); err != nil {
		t.Fatalf("AppendEvent granted: %v", err)
	}

	var buf bytes.Buffer
	if err := orchestrator.ListJobs(ctx, p, orchestrator.ListOpts{Limit: 10}, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "queued") {
		t.Fatalf("admitted run should not show stale queued state, got:\n%s", out)
	}
}

func TestJobLogs_WholeRunAndNodeScoped(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var all bytes.Buffer
	if err := orchestrator.JobLogs(context.Background(), p, res.RunID, orchestrator.LogsOpts{}, &all); err != nil {
		t.Fatalf("JobLogs all: %v", err)
	}
	if !strings.Contains(all.String(), "work complete") {
		t.Fatalf("whole-run logs missing content: %q", all.String())
	}

	var scoped bytes.Buffer
	if err := orchestrator.JobLogs(context.Background(), p, res.RunID,
		orchestrator.LogsOpts{Node: "orch-ok"}, &scoped); err != nil {
		t.Fatalf("JobLogs scoped: %v", err)
	}
	if !strings.Contains(scoped.String(), "work complete") {
		t.Fatalf("scoped logs missing content: %q", scoped.String())
	}
}

func TestJobLogs_UnknownNode(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var buf bytes.Buffer
	err = orchestrator.JobLogs(context.Background(), p, res.RunID,
		orchestrator.LogsOpts{Node: "nope"}, &buf)
	if err == nil {
		t.Fatal("expected error for unknown node")
	}
}

func TestJobLogs_CancelledNodeIsQuiet(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-middle-fails"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var buf bytes.Buffer
	if err := orchestrator.JobLogs(context.Background(), p, res.RunID,
		orchestrator.LogsOpts{}, &buf); err != nil {
		t.Fatalf("JobLogs: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "(no log file yet)") {
		t.Fatalf("cancelled node should be summarized, not show 'no log file': %s", out)
	}
	if !strings.Contains(out, "cancelled") {
		t.Fatalf("cancelled node should be summarized via envelope stream: %s", out)
	}

	var legacy bytes.Buffer
	if err := orchestrator.JobLogs(context.Background(), p, res.RunID,
		orchestrator.LogsOpts{NoEvents: true}, &legacy); err != nil {
		t.Fatalf("JobLogs --no-events: %v", err)
	}
	if !strings.Contains(legacy.String(), "did not execute") {
		t.Fatalf("--no-events should show legacy 'did not execute' banner: %s", legacy.String())
	}
}

func TestJobErrors(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-middle-fails"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var buf bytes.Buffer
	if err := orchestrator.JobErrors(context.Background(), p, res.RunID, false, &buf); err != nil {
		t.Fatalf("JobErrors: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "mid fail") {
		t.Fatalf("errors missing root-cause: %s", out)
	}
	if strings.Contains(out, "c:\n") {
		t.Fatalf("errors should skip cancelled-downstream nodes: %s", out)
	}
}

func TestJobErrors_NoFailures(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var buf bytes.Buffer
	if err := orchestrator.JobErrors(context.Background(), p, res.RunID, false, &buf); err != nil {
		t.Fatalf("JobErrors: %v", err)
	}
	if !strings.Contains(buf.String(), "no failing nodes") {
		t.Fatalf("expected no-failures message: %s", buf.String())
	}
}

func TestJobErrors_JSON(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-middle-fails"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var buf bytes.Buffer
	if err := orchestrator.JobErrors(context.Background(), p, res.RunID, true, &buf); err != nil {
		t.Fatalf("JobErrors: %v", err)
	}
	var failed []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &failed); err != nil {
		t.Fatalf("json parse: %v\n%s", err, buf.String())
	}
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed node, got %d: %v", len(failed), failed)
	}
	if failed[0]["node"] != "b" {
		t.Fatalf("unexpected failed node: %v", failed[0])
	}
}
