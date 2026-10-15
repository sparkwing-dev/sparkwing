package controller_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

const contractRunID = "run-contract"

func TestNodeClientContract_EveryRouteTheNodeClientCalls(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.4s of real work; the fast class runs under -short")
	}
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := httptest.NewServer(controller.New(st, quietLogger()).Handler())
	t.Cleanup(srv.Close)

	runContractSurface(t, client.NewWithToken(srv.URL, nil, ""))
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func runContractSurface(t *testing.T, c *client.Client) {
	t.Helper()
	ctx := context.Background()
	const runID = contractRunID

	if err := c.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "contract", Status: "running",
		Args: map[string]string{"token": "s3cret"}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := c.UpdatePlanSnapshot(ctx, runID, []byte(`{"nodes":[]}`)); err != nil {
		t.Fatalf("UpdatePlanSnapshot: %v", err)
	}

	run, err := c.GetRunForExecution(ctx, runID)
	if err != nil {
		t.Fatalf("GetRunForExecution: %v", err)
	}
	if run.Pipeline != "contract" {
		t.Errorf("GetRunForExecution pipeline = %q", run.Pipeline)
	}
	if _, err := c.GetRun(ctx, runID); err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if _, err := c.GetTrigger(ctx, runID); err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetTrigger: %v", err)
	}

	if err := c.CreateNode(ctx, store.Node{RunID: runID, NodeID: "produce", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := c.UpdateNodeDeps(ctx, runID, "produce", []string{}); err != nil {
		t.Fatalf("UpdateNodeDeps: %v", err)
	}
	if err := c.StartNode(ctx, runID, "produce"); err != nil {
		t.Fatalf("StartNode: %v", err)
	}
	if err := c.SetNodeStatus(ctx, runID, "produce", "running"); err != nil {
		t.Fatalf("SetNodeStatus: %v", err)
	}
	if err := c.UpdateNodeActivity(ctx, runID, "produce", "compiling"); err != nil {
		t.Fatalf("UpdateNodeActivity: %v", err)
	}

	if err := c.AppendNodeAnnotation(ctx, runID, "produce", "note"); err != nil {
		t.Fatalf("AppendNodeAnnotation: %v", err)
	}
	if err := c.SetNodeSummary(ctx, runID, "produce", "# done"); err != nil {
		t.Fatalf("SetNodeSummary: %v", err)
	}
	for i, kind := range []store.MetricKind{store.MetricInterval, store.MetricCommand, store.MetricUnknown} {
		if err := c.AddNodeMetricSample(ctx, runID, "produce", store.MetricSample{Kind: kind, TS: time.Unix(int64(i+1), 0), MemoryBytes: 400}); err != nil {
			t.Fatal(err)
		}
	}
	samples, err := c.ListNodeMetrics(ctx, runID, "produce")
	if err != nil || len(samples) != 3 {
		t.Fatalf("samples=%+v, %v", samples, err)
	}
	for i, want := range []store.MetricKind{store.MetricInterval, store.MetricCommand, store.MetricUnknown} {
		if samples[i].Kind != want || samples[i].CPUTime != 0 || samples[i].CPUMillicores != 0 || samples[i].MemoryBytes != 400 {
			t.Fatalf("sample %d=%+v; want kind %q with zero CPU and 400 bytes", i, samples[i], want)
		}
	}

	if err := c.AppendEvent(ctx, runID, "produce", "node_started", []byte(`{}`)); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	if err := c.StartNodeStep(ctx, runID, "produce", "run"); err != nil {
		t.Fatalf("StartNodeStep: %v", err)
	}
	if err := c.AppendStepAnnotation(ctx, runID, "produce", "run", "step note"); err != nil {
		t.Fatalf("AppendStepAnnotation: %v", err)
	}
	if err := c.SetStepSummary(ctx, runID, "produce", "run", "## step"); err != nil {
		t.Fatalf("SetStepSummary: %v", err)
	}
	if err := c.FinishNodeStep(ctx, runID, "produce", "run", store.StepPassed); err != nil {
		t.Fatalf("FinishNodeStep: %v", err)
	}
	if err := c.StartNodeStep(ctx, runID, "produce", "cancelled"); err != nil {
		t.Fatalf("StartNodeStep(cancelled): %v", err)
	}
	if err := c.FinishNodeStep(ctx, runID, "produce", "cancelled", store.StepCancelled); err != nil {
		t.Fatalf("FinishNodeStep(cancelled): %v", err)
	}
	if err := c.SkipNodeStep(ctx, runID, "produce", "skipped"); err != nil {
		t.Fatalf("SkipNodeStep: %v", err)
	}
	steps, err := c.ListNodeSteps(ctx, runID)
	if err != nil {
		t.Fatalf("ListNodeSteps: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("ListNodeSteps returned %d rows after passed, cancelled, and skipped writes", len(steps))
	}
	statuses := make(map[string]string, len(steps))
	for _, step := range steps {
		statuses[step.StepID] = step.Status
	}
	if statuses["run"] != store.StepPassed || statuses["cancelled"] != store.StepCancelled || statuses["skipped"] != store.StepSkipped {
		t.Fatalf("step statuses = %v", statuses)
	}

	if err := c.FinishNodeWithReason(ctx, runID, "produce", "success", "",
		[]byte(`{"digest":"sha-abc123"}`), "", nil); err != nil {
		t.Fatalf("FinishNodeWithReason: %v", err)
	}
	out, err := c.GetNodeOutput(ctx, runID, "produce")
	if err != nil {
		t.Fatalf("GetNodeOutput: %v", err)
	}
	if string(out) != `{"digest":"sha-abc123"}` {
		t.Errorf("GetNodeOutput = %s", out)
	}
	node, err := c.GetNode(ctx, runID, "produce")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node.Outcome != "success" {
		t.Errorf("node outcome = %q, want success", node.Outcome)
	}

	if err := c.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: "produce/scan", Status: "pending",
	}); err != nil {
		t.Fatalf("CreateNode(spawn child): %v", err)
	}
	if err := c.FinishNode(ctx, runID, "produce/scan", "success", "", []byte(`{"findings":7}`)); err != nil {
		t.Fatalf("FinishNode(spawn child): %v", err)
	}
	childOut, err := c.GetNodeOutput(ctx, runID, "produce/scan")
	if err != nil {
		t.Fatalf("GetNodeOutput(spawn child): %v", err)
	}
	if string(childOut) != `{"findings":7}` {
		t.Errorf("spawn child output = %s", childOut)
	}

	if err := c.FinishRun(ctx, runID, "success", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	latest, err := c.GetLatestRun(ctx, "contract", []string{"success"}, time.Hour)
	if err != nil {
		t.Fatalf("GetLatestRun: %v", err)
	}
	if latest.ID != runID {
		t.Errorf("GetLatestRun = %q, want %q", latest.ID, runID)
	}

	if id, err := c.FindSpawnedChildTriggerID(ctx, runID, "produce", "child-pipeline"); err != nil {
		if !isUnsupported(err) {
			t.Fatalf("FindSpawnedChildTriggerID: %v", err)
		}
	} else if id != "" {
		t.Errorf("FindSpawnedChildTriggerID on a run that spawned nothing = %q", id)
	}
	childRunID, err := c.EnqueueTriggerWithEnv(ctx, "child-pipeline", map[string]string{"k": "v"},
		runID, "produce", "", "await-pipeline", "", "", "", nil)
	if err != nil && !isUnsupported(err) {
		t.Fatalf("EnqueueTriggerWithEnv: %v", err)
	}
	if err == nil {
		if childRunID == "" {
			t.Error("EnqueueTriggerWithEnv returned an empty child run id")
		}

		if id, ferr := c.FindSpawnedChildTriggerID(ctx, runID, "produce", "child-pipeline"); ferr == nil && id == "" {
			t.Errorf("FindSpawnedChildTriggerID found no child after enqueueing %q", childRunID)
		}
	}

	pauseErr := c.CreateDebugPause(ctx, store.DebugPause{
		RunID: runID, NodeID: "produce", Reason: "contract", PausedAt: time.Now().UTC(),
	})
	if pauseErr != nil && !isUnsupported(pauseErr) {
		t.Fatalf("CreateDebugPause: %v", pauseErr)
	}
	if _, err := c.ListDebugPauses(ctx, runID); err != nil && !isUnsupported(err) {
		t.Fatalf("ListDebugPauses: %v", err)
	}
	if pauseErr == nil {
		if _, err := c.GetActiveDebugPause(ctx, runID, "produce"); err != nil {
			t.Fatalf("GetActiveDebugPause: %v", err)
		}
		if err := c.ReleaseDebugPause(ctx, runID, "produce", "tester", store.PauseReleaseManual); err != nil {
			t.Fatalf("ReleaseDebugPause: %v", err)
		}
	}

	if err := c.CreateApproval(ctx, store.Approval{RunID: runID, NodeID: "gate", Message: "ok?"}); err != nil {
		if !isUnsupported(err) {
			t.Fatalf("CreateApproval: %v", err)
		}
	} else {
		if _, err := c.GetApproval(ctx, runID, "gate"); err != nil {
			t.Fatalf("GetApproval: %v", err)
		}
		if _, err := c.ResolveApproval(ctx, runID, "gate",
			store.ApprovalResolutionApproved, "tester", ""); err != nil {
			t.Fatalf("ResolveApproval: %v", err)
		}
		if _, err := c.ListPendingApprovals(ctx); err != nil {
			t.Fatalf("ListPendingApprovals: %v", err)
		}
	}

	if _, err := c.GetRun(ctx, "run-does-not-exist"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetRun(missing) = %v, want store.ErrNotFound", err)
	}
	if _, err := c.GetNode(ctx, runID, "no-such-node"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetNode(missing) = %v, want store.ErrNotFound", err)
	}
}

func isUnsupported(err error) bool {
	return errors.Is(err, storage.ErrNotSupported) ||
		strings.Contains(err.Error(), "not supported")
}
