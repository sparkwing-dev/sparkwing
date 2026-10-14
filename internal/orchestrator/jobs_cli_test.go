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
	p := newPathsWithStore(t)
	var buf bytes.Buffer
	if err := orchestrator.ListJobs(context.Background(), p, orchestrator.ListOpts{Limit: 10}, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if !strings.Contains(buf.String(), "no runs yet") {
		t.Fatalf("expected empty-state message, got %q", buf.String())
	}
}

func TestListJobs_ShowsLocalAdmissionWait(t *testing.T) {
	p := newPathsWithStore(t)
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
	if err := st.AppendEvent(ctx, "run-admission-list", "", "admission_wait", []byte(`{"position":1,"queue_length":3}`)); err != nil {
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

func TestListJobs_TracksInterleavedNodeAdmissionWaitsIndependently(t *testing.T) {
	p := newPathsWithStore(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	if err := st.CreateRun(ctx, store.Run{
		ID: "run-node-admission-list", Pipeline: "push-checks", Status: "running", StartedAt: time.Now().Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, event := range []struct {
		node, kind, payload string
	}{
		{node: "node-a", kind: "admission_wait", payload: `{"position":2,"queue_length":13}`},
		{node: "node-b", kind: "admission_wait", payload: `{"position":12,"queue_length":12}`},
		{node: "node-a", kind: "admission_granted"},
	} {
		if err := st.AppendEvent(ctx, "run-node-admission-list", event.node, event.kind, []byte(event.payload)); err != nil {
			t.Fatalf("AppendEvent %s %s: %v", event.node, event.kind, err)
		}
	}

	var list bytes.Buffer
	if err := orchestrator.ListJobs(ctx, p, orchestrator.ListOpts{Limit: 10}, &list); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if !strings.Contains(list.String(), "running (1 admission-waiting)") {
		t.Fatalf("list should retain node-b's active wait without presenting its position as the run's, got:\n%s", list.String())
	}
	if strings.Contains(list.String(), "queued (") {
		t.Fatalf("node admission must not replace the running run's status with one participant's queue position, got:\n%s", list.String())
	}

	var status bytes.Buffer
	if err := orchestrator.JobStatus(ctx, p, "run-node-admission-list", orchestrator.StatusOpts{}, &status); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	if !strings.Contains(status.String(), "admission: 1 node waiting for local admission") {
		t.Fatalf("status should report the active node count, got:\n%s", status.String())
	}
}

func TestListJobs_PreservesPlanAdmissionAsRootWait(t *testing.T) {
	p := newPathsWithStore(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	const runID = "run-plan-admission-list"
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "push-checks", Status: "running", StartedAt: time.Now().Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.AppendEvent(ctx, runID, runID+"/plan", "admission_wait", []byte(`{"position":3,"queue_length":7}`)); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	var out bytes.Buffer
	if err := orchestrator.ListJobs(ctx, p, orchestrator.ListOpts{Limit: 10}, &out); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if !strings.Contains(out.String(), "queued (3/7)") {
		t.Fatalf("plan-level admission should retain the run queue position, got:\n%s", out.String())
	}
	if strings.Contains(out.String(), "admission-waiting") {
		t.Fatalf("plan-level admission must not be presented as a node wait, got:\n%s", out.String())
	}
}

func TestListJobs_ClearsInterleavedNodeAdmissionTerminalsIndependently(t *testing.T) {
	p := newPathsWithStore(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	const runID = "run-node-admission-terminals"
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "push-checks", Status: "running", StartedAt: time.Now().Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, event := range []struct {
		node, kind, payload string
	}{
		{node: "node-a", kind: "admission_wait", payload: `{"position":2,"queue_length":4}`},
		{node: "node-b", kind: "admission_wait", payload: `{"position":3,"queue_length":4}`},
		{node: "node-a", kind: "admission_cancelled"},
		{node: "node-b", kind: "admission_queue_timeout"},
	} {
		if err := st.AppendEvent(ctx, runID, event.node, event.kind, []byte(event.payload)); err != nil {
			t.Fatalf("AppendEvent %s %s: %v", event.node, event.kind, err)
		}
	}

	var out bytes.Buffer
	if err := orchestrator.ListJobs(ctx, p, orchestrator.ListOpts{Limit: 10}, &out); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if strings.Contains(out.String(), "admission-waiting") || strings.Contains(out.String(), "queued (") {
		t.Fatalf("terminal admission events should clear only their matching waits, got:\n%s", out.String())
	}
}

func TestListJobs_StaleAdmissionTerminalPreservesNewerRequest(t *testing.T) {
	p := newPathsWithStore(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	const runID = "run-stale-admission-terminal"
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "push-checks", Status: "running", StartedAt: time.Now().Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, event := range []struct {
		kind, payload string
	}{
		{kind: "admission_wait", payload: `{"position":2,"queue_length":4,"request_id":"request-1"}`},
		{kind: "admission_wait", payload: `{"position":3,"queue_length":5,"request_id":"request-2"}`},
		{kind: "admission_granted", payload: `{"request_id":"request-1"}`},
	} {
		if err := st.AppendEvent(ctx, runID, "node-a", event.kind, []byte(event.payload)); err != nil {
			t.Fatalf("AppendEvent %s: %v", event.kind, err)
		}
	}

	var out bytes.Buffer
	if err := orchestrator.ListJobs(ctx, p, orchestrator.ListOpts{Limit: 10}, &out); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if !strings.Contains(out.String(), "running (1 admission-waiting)") {
		t.Fatalf("a stale terminal must not clear the participant's newer request, got:\n%s", out.String())
	}
}

func TestListJobs_ClearsLegacyAdmissionWaitOnLegacyTerminal(t *testing.T) {
	p := newPathsWithStore(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	const runID = "run-legacy-admission-terminal"
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "push-checks", Status: "running", StartedAt: time.Now().Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.AppendEvent(ctx, runID, "", "admission_wait", []byte(`{"position":2,"queue_length":4,"request_id":"run-legacy-admission-terminal/node-host/Y29tcGlsZQ"}`)); err != nil {
		t.Fatalf("AppendEvent wait: %v", err)
	}
	if err := st.AppendEvent(ctx, runID, "", "admission_granted", nil); err != nil {
		t.Fatalf("AppendEvent terminal: %v", err)
	}

	var out bytes.Buffer
	if err := orchestrator.ListJobs(ctx, p, orchestrator.ListOpts{Limit: 10}, &out); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if strings.Contains(out.String(), "admission-waiting") || strings.Contains(out.String(), "queued (") {
		t.Fatalf("legacy terminal should clear the legacy wait during rolling upgrades, got:\n%s", out.String())
	}
}

func TestListJobs_IgnoresStaleAdmissionWaitForFinishedRun(t *testing.T) {
	p := newPathsWithStore(t)
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
	if err := st.AppendEvent(ctx, "run-stale-admission-list", "", "admission_wait", []byte(`{"position":1,"queue_length":3}`)); err != nil {
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

func TestJobStatus_OmitsLogPathWhenRunHasNone(t *testing.T) {
	p := newPathsWithStore(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	const runID = "run-remote-logs"
	if err := st.CreateRun(ctx, store.Run{
		ID:         runID,
		Pipeline:   "orch-ok",
		Status:     "running",
		StartedAt:  time.Now(),
		Invocation: map[string]any{"run_id": runID, "pipeline": "orch-ok"},
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	var text bytes.Buffer
	if err := orchestrator.JobStatus(ctx, p, runID, orchestrator.StatusOpts{}, &text); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	if strings.Contains(text.String(), "log_path") {
		t.Fatalf("status invented a log_path for a run without one:\n%s", text.String())
	}
	if strings.Contains(text.String(), p.RunDir(runID)) {
		t.Fatalf("status leaked a local run dir for a run without one:\n%s", text.String())
	}

	var jsonOut bytes.Buffer
	if err := orchestrator.JobStatus(ctx, p, runID, orchestrator.StatusOpts{JSON: true}, &jsonOut); err != nil {
		t.Fatalf("JobStatus json: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(jsonOut.Bytes(), &payload); err != nil {
		t.Fatalf("json parse: %v\n%s", err, jsonOut.String())
	}
	if v, ok := payload["log_path"]; ok {
		t.Errorf("json log_path present for a run without one: %v", v)
	}
}

func TestJobStatus_MarksLogPathAbsentOnThisMachine(t *testing.T) {
	p := newPathsWithStore(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	const runID = "run-executed-elsewhere"
	const podPath = "/root/.sparkwing/runs/run-executed-elsewhere"
	if err := st.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "orch-ok",
		Status:    "success",
		StartedAt: time.Now(),
		Invocation: map[string]any{
			"run_id":   runID,
			"pipeline": "orch-ok",
			"log_path": podPath,
		},
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	var text bytes.Buffer
	if err := orchestrator.JobStatus(ctx, p, runID, orchestrator.StatusOpts{}, &text); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	if !strings.Contains(text.String(), podPath) {
		t.Fatalf("status dropped the recorded log_path:\n%s", text.String())
	}
	if !strings.Contains(text.String(), "not present on this machine") {
		t.Fatalf("status must mark a log_path that is not here:\n%s", text.String())
	}

	var jsonOut bytes.Buffer
	if err := orchestrator.JobStatus(ctx, p, runID, orchestrator.StatusOpts{JSON: true}, &jsonOut); err != nil {
		t.Fatalf("JobStatus json: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(jsonOut.Bytes(), &payload); err != nil {
		t.Fatalf("json parse: %v\n%s", err, jsonOut.String())
	}
	if payload["log_path"] != podPath {
		t.Errorf("json log_path = %v, want the recorded %q unannotated", payload["log_path"], podPath)
	}
}

func TestJobStatus_ShowsLocalAdmissionWait(t *testing.T) {
	p := newPathsWithStore(t)
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
	if err := st.AppendEvent(ctx, "run-admission-wait", "", "admission_wait", []byte(`{"position":2,"queue_length":5}`)); err != nil {
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
	p := newPathsWithStore(t)
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
	if err := st.AppendEvent(ctx, "run-stale-admission-status", "", "admission_wait", []byte(`{"position":2,"queue_length":5}`)); err != nil {
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
	p := newPathsWithStore(t)
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
	if err := st.AppendEvent(ctx, "run-admission-long-events", "", "admission_wait", []byte(`{"position":1,"queue_length":1}`)); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	for i := 0; i < 500; i++ {
		if err := st.AppendEvent(ctx, "run-admission-long-events", "node", "node_started", nil); err != nil {
			t.Fatalf("AppendEvent filler %d: %v", i, err)
		}
	}
	if err := st.AppendEvent(ctx, "run-admission-long-events", "", "admission_granted", nil); err != nil {
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

func TestJobStatus_ReportsTheComputeGuardThatRefusedWork(t *testing.T) {
	p := newPathsWithStore(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	if err := st.CreateRun(ctx, store.Run{
		ID: "run-guarded", Pipeline: "push-checks", Status: "running", StartedAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-guarded", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.AppendEvent(ctx, "run-guarded", "build", store.EventKindComputeLimitBlocked,
		[]byte(`{"limit":"max_concurrent_runners","cap":4,"observed":4,"scope":"principal agent:cloud"}`)); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	var status bytes.Buffer
	if err := orchestrator.JobStatus(ctx, p, "run-guarded", orchestrator.StatusOpts{}, &status); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	if !strings.Contains(status.String(), "guard:     max_concurrent_runners reached (4 of 4) on principal agent:cloud") {
		t.Fatalf("status should name the guard that refused work, got:\n%s", status.String())
	}

	var payload map[string]any
	var asJSON bytes.Buffer
	if err := orchestrator.JobStatus(ctx, p, "run-guarded", orchestrator.StatusOpts{JSON: true}, &asJSON); err != nil {
		t.Fatalf("JobStatus json: %v", err)
	}
	if err := json.Unmarshal(asJSON.Bytes(), &payload); err != nil {
		t.Fatalf("json parse: %v\n%s", err, asJSON.String())
	}
	guard, ok := payload["compute_guard"].(map[string]any)
	if !ok || guard["limit"] != "max_concurrent_runners" {
		t.Fatalf("json should carry the guard, got %v", payload["compute_guard"])
	}
}

func TestJobStatus_ShowsTheSecretPreflightRefusal(t *testing.T) {
	p := newPathsWithStore(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-secrets", Pipeline: "deploy", Status: "failed", StartedAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	detail := "pipeline \"deploy\" did not start: it declares 1 required secret this run cannot read.\n- DEPLOY_KEY is not set"
	raw, _ := json.Marshal(map[string]string{"message": detail})
	if err := st.AppendEvent(ctx, "run-secrets", store.PlanNodeID, store.EventKindSecretPreflightRefused, raw); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	var status bytes.Buffer
	if err := orchestrator.JobStatus(ctx, p, "run-secrets", orchestrator.StatusOpts{}, &status); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	if !strings.Contains(status.String(), "it declares 1 required secret this run cannot read.\n           - DEPLOY_KEY is not set") {
		t.Fatalf("status should show the preflight refusal, got:\n%s", status.String())
	}
	var asJSON bytes.Buffer
	if err := orchestrator.JobStatus(ctx, p, "run-secrets", orchestrator.StatusOpts{JSON: true}, &asJSON); err != nil {
		t.Fatalf("JobStatus json: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(asJSON.Bytes(), &payload); err != nil || payload["secret_preflight"] != detail {
		t.Fatalf("json should carry the refusal, got %v (%v)", payload["secret_preflight"], err)
	}
}
