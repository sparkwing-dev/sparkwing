package orchestrator

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func TestBuildSummary_AnnotationsFromNodesAndSteps(t *testing.T) {
	start := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	run := &store.Run{ID: "run-x", Pipeline: "deploy", Status: "success", StartedAt: start}
	nodes := []*store.Node{
		{NodeID: "build", Status: "done", Annotations: []string{"cache hit"}},
	}
	steps := []*store.NodeStep{
		{NodeID: "build", StepID: "compile", Annotations: []string{"12 objects"}},
	}
	got := buildSummary(run, nodes, steps, nil)
	if len(got.Annotations) != 2 {
		t.Fatalf("annotations=%d, want 2", len(got.Annotations))
	}
	var sawNode, sawStep bool
	for _, a := range got.Annotations {
		if a.NodeID == "build" && a.StepID == "" && a.Message == "cache hit" {
			sawNode = true
		}
		if a.NodeID == "build" && a.StepID == "compile" && a.Message == "12 objects" {
			sawStep = true
		}
	}
	if !sawNode || !sawStep {
		t.Errorf("expected both node + step annotations: %+v", got.Annotations)
	}
}

func TestBuildSummary_WorkItemsIncludeNodesAndSteps(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Second)
	run := &store.Run{ID: "r", StartedAt: start}
	nodes := []*store.Node{
		{NodeID: "build", Status: "done", StartedAt: &start, FinishedAt: &end, Deps: []string{"setup"}},
	}
	steps := []*store.NodeStep{
		{NodeID: "build", StepID: "compile", Status: store.StepPassed, StartedAt: &start, FinishedAt: &end},
	}
	got := buildSummary(run, nodes, steps, nil)
	if len(got.WorkItems) != 2 {
		t.Fatalf("work items=%d, want 2", len(got.WorkItems))
	}
	if !got.WorkItems[0].IsNode || got.WorkItems[0].NodeID != "build" {
		t.Errorf("first item should be the node: %+v", got.WorkItems[0])
	}
	if got.WorkItems[1].StepID != "compile" || got.WorkItems[1].IsNode {
		t.Errorf("second should be the step: %+v", got.WorkItems[1])
	}
}

func TestRenderSummary_TextHasSections(t *testing.T) {
	start := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Second)
	s := RunSummary{
		RunID: "run-x", Pipeline: "deploy", Status: "success",
		StartedAt: start, FinishedAt: &end, DurationMS: 2000,
		Annotations: []SummaryAnnotation{{NodeID: "build", Message: "ok"}},
		Groups:      []SummaryGroup{{Name: "ci", Members: []string{"build", "test"}}},
		Modifiers:   []SummaryModifier{{Modifier: "Retry", Nodes: []string{"deploy"}}},
		WorkItems:   []SummaryWorkItem{{NodeID: "build", Status: "done", IsNode: true}},
	}
	var buf bytes.Buffer
	if err := renderSummary(s, SummaryOpts{}, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"run:       run-x",
		"pipeline:  deploy",
		"annotations:",
		"groups:",
		"ci: build, test",
		"modifiers:",
		"Retry: deploy",
		"work:",
		"build",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderSummary_JSON(t *testing.T) {
	start := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	s := RunSummary{RunID: "run-x", Pipeline: "deploy", StartedAt: start, Status: "success"}
	var buf bytes.Buffer
	if err := renderSummary(s, SummaryOpts{JSON: true}, &buf); err != nil {
		t.Fatal(err)
	}
	var got RunSummary
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if got.RunID != "run-x" {
		t.Errorf("run id mismatch: %+v", got)
	}
}
