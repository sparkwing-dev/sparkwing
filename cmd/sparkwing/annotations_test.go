package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// seedAnnotationsRun creates a finished run with two nodes; one has
// node-level annotations, the other has a step with a step-level
// annotation. Returns the resolved paths so tests can re-open the
// store.
func seedAnnotationsRun(t *testing.T) orchestrator.Paths {
	t.Helper()
	dir := t.TempDir()
	paths := orchestrator.PathsAt(dir)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	start := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-annot-1", Pipeline: "deploy", Status: "running", StartedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-annot-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-annot-1", NodeID: "deploy", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendNodeAnnotation(ctx, "run-annot-1", "build", "produced 3 artifacts"); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendNodeAnnotation(ctx, "run-annot-1", "build", "cache hit on layer 2"); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendStepAnnotation(ctx, "run-annot-1", "deploy", "canary", "rolled to 5%"); err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestAnnotationsList_LocalDefaultIsNodeOnly(t *testing.T) {
	paths := seedAnnotationsRun(t)
	out := captureStdout(t, func() {
		if err := runAnnotationsList(context.Background(), paths,
			[]string{"--run", "run-annot-1", "-o", "json"}); err != nil {
			t.Fatalf("runAnnotationsList: %v", err)
		}
	})
	var got []annotationEntry
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 node-level entries; out=%s", len(got), out)
	}
	for _, e := range got {
		if e.StepID != "" {
			t.Errorf("step-level entry leaked into default list: %+v", e)
		}
		if e.NodeID != "build" {
			t.Errorf("unexpected node %q (only build has annotations)", e.NodeID)
		}
	}
	if got[0].Message != "produced 3 artifacts" || got[1].Message != "cache hit on layer 2" {
		t.Errorf("order not preserved: %+v", got)
	}
}

func TestAnnotationsList_StepsFlagIncludesStepRows(t *testing.T) {
	paths := seedAnnotationsRun(t)
	out := captureStdout(t, func() {
		if err := runAnnotationsList(context.Background(), paths,
			[]string{"--run", "run-annot-1", "--steps", "-o", "json"}); err != nil {
			t.Fatalf("runAnnotationsList: %v", err)
		}
	})
	var got []annotationEntry
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	var hasStep bool
	for _, e := range got {
		if e.StepID == "canary" && e.Message == "rolled to 5%" {
			hasStep = true
		}
	}
	if !hasStep {
		t.Fatalf("step annotation missing with --steps; got %+v", got)
	}
	if len(got) != 3 {
		t.Errorf("got %d entries, want 3 (2 node + 1 step)", len(got))
	}
}

func TestAnnotationsList_NodeFilter(t *testing.T) {
	paths := seedAnnotationsRun(t)
	out := captureStdout(t, func() {
		if err := runAnnotationsList(context.Background(), paths,
			[]string{"--run", "run-annot-1", "--node", "deploy", "--steps", "-o", "json"}); err != nil {
			t.Fatalf("runAnnotationsList: %v", err)
		}
	})
	var got []annotationEntry
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(got) != 1 || got[0].NodeID != "deploy" || got[0].StepID != "canary" {
		t.Errorf("node filter failed: %+v", got)
	}
}

func TestAnnotationsList_RequiresRunFlag(t *testing.T) {
	paths := orchestrator.PathsAt(t.TempDir())
	err := runAnnotationsList(context.Background(), paths, nil)
	if err == nil || !strings.Contains(err.Error(), "--run is required") {
		t.Fatalf("want --run required error, got %v", err)
	}
}

func TestAnnotationsAdd_LocalAppendsToNode(t *testing.T) {
	paths := seedAnnotationsRun(t)
	_ = captureStdout(t, func() {
		if err := runAnnotationsAdd(context.Background(), paths,
			[]string{"--run", "run-annot-1", "--node", "build", "-m", "agent added this"}); err != nil {
			t.Fatalf("runAnnotationsAdd: %v", err)
		}
	})
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n, err := st.GetNode(context.Background(), "run-annot-1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Annotations) != 3 {
		t.Fatalf("annotations len = %d, want 3; %+v", len(n.Annotations), n.Annotations)
	}
	if n.Annotations[2] != "agent added this" {
		t.Errorf("last annotation = %q, want %q", n.Annotations[2], "agent added this")
	}
}

func TestAnnotationsAdd_LocalAppendsToStep(t *testing.T) {
	paths := seedAnnotationsRun(t)
	_ = captureStdout(t, func() {
		if err := runAnnotationsAdd(context.Background(), paths,
			[]string{"--run", "run-annot-1", "--node", "deploy", "--step", "canary", "-m", "promoted to 50%"}); err != nil {
			t.Fatalf("runAnnotationsAdd: %v", err)
		}
	})
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	steps, err := st.ListNodeSteps(context.Background(), "run-annot-1")
	if err != nil {
		t.Fatal(err)
	}
	var found *store.NodeStep
	for _, s := range steps {
		if s.NodeID == "deploy" && s.StepID == "canary" {
			found = s
			break
		}
	}
	if found == nil {
		t.Fatal("canary step missing")
	}
	if len(found.Annotations) != 2 || found.Annotations[1] != "promoted to 50%" {
		t.Errorf("step annotations = %+v", found.Annotations)
	}
}

func TestAnnotationsAdd_RequiresRunAndNode(t *testing.T) {
	paths := orchestrator.PathsAt(t.TempDir())
	err := runAnnotationsAdd(context.Background(), paths,
		[]string{"--run", "x", "-m", "hi"})
	if err == nil || !strings.Contains(err.Error(), "--node is required") {
		t.Fatalf("want --node required error, got %v", err)
	}
	err = runAnnotationsAdd(context.Background(), paths,
		[]string{"--run", "x", "--node", "build"})
	if err == nil || !strings.Contains(err.Error(), "--message is required") {
		t.Fatalf("want --message required error, got %v", err)
	}
}
