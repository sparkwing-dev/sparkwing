package orchestrator

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func TestPrintRunSummaries_NodeAndStepScopes(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	seedSummaryRun(t, st, "run-1")

	if err := st.SetNodeSummary(ctx, "run-1", "deploy", "## node summary\n- ok"); err != nil {
		t.Fatalf("SetNodeSummary: %v", err)
	}
	if err := st.StartNodeStep(ctx, "run-1", "deploy", "rollout"); err != nil {
		t.Fatalf("StartNodeStep: %v", err)
	}
	if err := st.SetStepSummary(ctx, "run-1", "deploy", "rollout", "## rollout\n3/3 ready"); err != nil {
		t.Fatalf("SetStepSummary: %v", err)
	}

	var buf bytes.Buffer
	if err := printRunSummaries(ctx, &buf, false, st, "run-1"); err != nil {
		t.Fatalf("printRunSummaries: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "Summaries") {
		t.Errorf("missing Summaries header:\n%s", got)
	}
	if !strings.Contains(got, "deploy\n") {
		t.Errorf("missing node-scope header:\n%s", got)
	}
	if !strings.Contains(got, "deploy › rollout") {
		t.Errorf("missing step-scope header:\n%s", got)
	}
	if !strings.Contains(got, "## node summary") || !strings.Contains(got, "- ok") {
		t.Errorf("missing node summary body:\n%s", got)
	}
	if !strings.Contains(got, "## rollout") || !strings.Contains(got, "3/3 ready") {
		t.Errorf("missing step summary body:\n%s", got)
	}
}

func TestPrintRunSummaries_EmptyWritesNothing(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	seedSummaryRun(t, st, "run-1")

	var buf bytes.Buffer
	if err := printRunSummaries(ctx, &buf, false, st, "run-1"); err != nil {
		t.Fatalf("printRunSummaries: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output for run with no summaries; got:\n%s", buf.String())
	}
}

func seedSummaryRun(t *testing.T, st *store.Store, runID string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: "deploy", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
}
