package orchestrator

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func TestRenderNodesWithSteps_IncludesNodeSummary(t *testing.T) {
	nodes := []*store.Node{{
		NodeID:  "deploy",
		Status:  "done",
		Outcome: "success",
		Summary: "## Deployed\n- replicas: 3",
	}}
	var buf bytes.Buffer
	renderNodesWithSteps(&buf, nodes, nil, false)
	got := buf.String()
	if !strings.Contains(got, "summary:") {
		t.Errorf("missing summary: header:\n%s", got)
	}
	if !strings.Contains(got, "Deployed") || !strings.Contains(got, "replicas: 3") {
		t.Errorf("summary body missing:\n%s", got)
	}
}

func TestRenderNodesWithSteps_IncludesStepSummary(t *testing.T) {
	nodes := []*store.Node{{NodeID: "deploy", Status: "done", Outcome: "success"}}
	steps := map[string][]*store.NodeStep{
		"deploy": {{
			NodeID:  "deploy",
			StepID:  "rollout",
			Status:  store.StepPassed,
			Summary: "## Rollout\n3/3 ready",
		}},
	}
	var buf bytes.Buffer
	renderNodesWithSteps(&buf, nodes, steps, false)
	got := buf.String()
	if !strings.Contains(got, "rollout") {
		t.Errorf("missing step row:\n%s", got)
	}
	if !strings.Contains(got, "summary:") {
		t.Errorf("missing summary: header:\n%s", got)
	}
	if !strings.Contains(got, "Rollout") || !strings.Contains(got, "3/3 ready") {
		t.Errorf("step summary body missing:\n%s", got)
	}
}

func TestRenderNodesWithSteps_SuccessNodeWithoutSummaryIsCollapsed(t *testing.T) {
	nodes := []*store.Node{{NodeID: "noop", Status: "done", Outcome: "success"}}
	var buf bytes.Buffer
	renderNodesWithSteps(&buf, nodes, nil, false)
	if strings.Contains(buf.String(), "summary:") {
		t.Errorf("unexpected summary section for vanilla success node:\n%s", buf.String())
	}
}
