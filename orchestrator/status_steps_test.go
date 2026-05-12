package orchestrator

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func ptrTime(t time.Time) *time.Time { return &t }

func TestRenderNodesWithSteps_FailedNodeAlwaysIncludesSteps(t *testing.T) {
	start := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Second)
	nodes := []*store.Node{
		{NodeID: "build", Status: "done", Outcome: "failed", StartedAt: &start, FinishedAt: &end},
	}
	steps := map[string][]*store.NodeStep{
		"build": {
			{NodeID: "build", StepID: "compile", Status: store.StepPassed, StartedAt: &start, FinishedAt: &end},
			{NodeID: "build", StepID: "link", Status: store.StepFailed, StartedAt: &start, FinishedAt: &end},
		},
	}
	var buf bytes.Buffer
	renderNodesWithSteps(&buf, nodes, steps, false)
	out := buf.String()
	if !strings.Contains(out, "link") || !strings.Contains(out, "compile") {
		t.Errorf("step rows missing for failed node:\n%s", out)
	}
}

func TestRenderNodesWithSteps_SuccessNodeOmitsStepsByDefault(t *testing.T) {
	start := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Second)
	nodes := []*store.Node{
		{NodeID: "build", Status: "done", Outcome: "success", StartedAt: &start, FinishedAt: &end},
	}
	steps := map[string][]*store.NodeStep{
		"build": {
			{NodeID: "build", StepID: "compile", Status: store.StepPassed, StartedAt: &start, FinishedAt: &end},
		},
	}
	var buf bytes.Buffer
	renderNodesWithSteps(&buf, nodes, steps, false)
	out := buf.String()
	if strings.Contains(out, "compile") {
		t.Errorf("steps should be hidden for vanilla success node:\n%s", out)
	}
}

func TestRenderNodesWithSteps_ForceFlagShowsAll(t *testing.T) {
	start := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Second)
	nodes := []*store.Node{
		{NodeID: "build", Status: "done", Outcome: "success", StartedAt: &start, FinishedAt: &end},
	}
	steps := map[string][]*store.NodeStep{
		"build": {
			{NodeID: "build", StepID: "compile", Status: store.StepPassed, StartedAt: &start, FinishedAt: &end},
		},
	}
	var buf bytes.Buffer
	renderNodesWithSteps(&buf, nodes, steps, true)
	out := buf.String()
	if !strings.Contains(out, "compile") {
		t.Errorf("--steps should force step rendering:\n%s", out)
	}
}

func TestRenderNodesWithSteps_AnnotationsPrintedWithAtPrefix(t *testing.T) {
	start := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Second)
	nodes := []*store.Node{
		{NodeID: "build", Status: "done", Outcome: "success", StartedAt: &start, FinishedAt: &end,
			Annotations: []string{"cache hit on layer 2"}},
	}
	steps := map[string][]*store.NodeStep{
		"build": {{NodeID: "build", StepID: "compile", Status: store.StepPassed,
			StartedAt: &start, FinishedAt: &end,
			Annotations: []string{"linked 12 objects"}}},
	}
	var buf bytes.Buffer
	renderNodesWithSteps(&buf, nodes, steps, false)
	out := buf.String()
	if !strings.Contains(out, "@ cache hit on layer 2") {
		t.Errorf("node annotation missing:\n%s", out)
	}
	if !strings.Contains(out, "@ linked 12 objects") {
		t.Errorf("step annotation missing:\n%s", out)
	}
}

func TestJoinStepsByNode_Wrapping(t *testing.T) {
	start := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Second)
	nodes := []*store.Node{
		{NodeID: "a", Status: "done"},
		{NodeID: "b", Status: "done"},
	}
	steps := []*store.NodeStep{
		{NodeID: "a", StepID: "s1", Status: store.StepPassed, StartedAt: &start, FinishedAt: &end},
		{NodeID: "b", StepID: "s2", Status: store.StepFailed, StartedAt: &start, FinishedAt: &end},
	}
	wrapped := joinStepsByNode(nodes, steps)
	if len(wrapped) != 2 {
		t.Fatalf("wrapped len = %d", len(wrapped))
	}
	if len(wrapped[0].Steps) != 1 || wrapped[0].Steps[0].StepID != "s1" {
		t.Errorf("node a steps wrong: %+v", wrapped[0].Steps)
	}
	if len(wrapped[1].Steps) != 1 || wrapped[1].Steps[0].StepID != "s2" {
		t.Errorf("node b steps wrong: %+v", wrapped[1].Steps)
	}
}

func TestFormatStepDuration_RunningShowsAge(t *testing.T) {
	start := time.Now().Add(-2 * time.Second)
	got := formatStepDuration(&store.NodeStep{StartedAt: &start})
	if !strings.HasPrefix(got, "running") {
		t.Errorf("expected running prefix, got %q", got)
	}
}

func TestFormatStepDuration_TerminalShowsDuration(t *testing.T) {
	start := time.Now().Add(-2 * time.Second)
	end := start.Add(time.Second)
	got := formatStepDuration(&store.NodeStep{StartedAt: &start, FinishedAt: &end})
	if got == "running" || got == "—" {
		t.Errorf("expected duration, got %q", got)
	}
}

// keep import alive for future test plumbing
var _ = ptrTime
