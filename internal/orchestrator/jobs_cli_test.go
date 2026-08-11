package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
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

func TestListJobs_TracksInterleavedNodeAdmissionWaitsIndependently(t *testing.T) {
	p := newPaths(t)
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
		if _, err := st.AppendEvent(ctx, "run-node-admission-list", event.node, event.kind, []byte(event.payload)); err != nil {
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
	p := newPaths(t)
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
	if _, err := st.AppendEvent(ctx, runID, runID+"/plan", "admission_wait", []byte(`{"position":3,"queue_length":7}`)); err != nil {
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
	p := newPaths(t)
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
		if _, err := st.AppendEvent(ctx, runID, event.node, event.kind, []byte(event.payload)); err != nil {
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
	p := newPaths(t)
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
		if _, err := st.AppendEvent(ctx, runID, "node-a", event.kind, []byte(event.payload)); err != nil {
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
	p := newPaths(t)
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
	if _, err := st.AppendEvent(ctx, runID, "", "admission_wait", []byte(`{"position":2,"queue_length":4,"request_id":"run-legacy-admission-terminal/node-host/Y29tcGlsZQ"}`)); err != nil {
		t.Fatalf("AppendEvent wait: %v", err)
	}
	if _, err := st.AppendEvent(ctx, runID, "", "admission_granted", nil); err != nil {
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

func TestJobStatus_ShowsLogPath(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := p.RunDir(res.RunID)
	if _, err := os.Stat(p.EnvelopeLog(res.RunID)); err != nil {
		t.Fatalf("envelope log not under the advertised dir: %v", err)
	}

	var text bytes.Buffer
	if err := orchestrator.JobStatus(context.Background(), p, res.RunID, orchestrator.StatusOpts{}, &text); err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	if !strings.Contains(text.String(), want) {
		t.Fatalf("status missing log_path %q in:\n%s", want, text.String())
	}
	if strings.Contains(text.String(), "not present on this machine") {
		t.Errorf("a run whose dir is right here must not be marked absent:\n%s", text.String())
	}

	var jsonOut bytes.Buffer
	if err := orchestrator.JobStatus(context.Background(), p, res.RunID, orchestrator.StatusOpts{JSON: true}, &jsonOut); err != nil {
		t.Fatalf("JobStatus json: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(jsonOut.Bytes(), &payload); err != nil {
		t.Fatalf("json parse: %v\n%s", err, jsonOut.String())
	}
	if payload["log_path"] != want {
		t.Errorf("json log_path = %v, want %q", payload["log_path"], want)
	}
	run, _ := payload["run"].(map[string]any)
	inv, _ := run["invocation"].(map[string]any)
	if inv["log_path"] != want {
		t.Errorf("stored invocation log_path = %v, want %q", inv["log_path"], want)
	}
}

// A run whose invocation carries no log_path (logs written to a
// controller or object store, or a run predating the field) must drop
// the line rather than point at this reader's own sparkwing home.
func TestJobStatus_OmitsLogPathWhenRunHasNone(t *testing.T) {
	p := newPaths(t)
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

// The recorded path is the executing machine's. A cluster pod with no
// logs backend records its own pod-local directory, and a laptop
// reading that run back gets the string verbatim -- so the text output
// says the directory is not here rather than inviting an `ls` against
// someone else's filesystem. JSON still reports it as recorded.
func TestJobStatus_MarksLogPathAbsentOnThisMachine(t *testing.T) {
	p := newPaths(t)
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
