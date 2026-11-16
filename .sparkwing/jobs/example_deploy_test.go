package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

func TestExampleDeployBudgetAllowsControllerAcknowledgements(t *testing.T) {
	doc := inspectPlan(t, func(plan *sparkwing.Plan) error {
		return (&Example{}).Plan(newTestContext(t), plan, ExampleInputs{}, sparkwing.RunContext{})
	})
	for _, job := range doc.Jobs {
		if job.ID != "deploy" {
			continue
		}
		// perf: synchronous reporting shares the job deadline with startup and host scheduling.
		const requestBudget = 30 * time.Second
		const acknowledgementCount = 3
		got := time.Duration(job.TimeoutMS) * time.Millisecond
		if got < acknowledgementCount*requestBudget+30*time.Second || got > 2*time.Minute {
			t.Fatalf("deploy budget = %s; need room for three controller acknowledgements and startup, bounded at two minutes", got)
		}
		return
	}
	t.Fatal("deploy job missing")
}

func TestExampleDeployCompletesItsReportingAndHonoursCancellation(t *testing.T) {
	host := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{
		"build": []byte(`{"tag":"example:sha-abc1234","digest":"sha256:fixture","size_mb":14}`),
	}})
	plan := sparkwing.NewPlan()
	build := sparkwing.Job(plan, "build", &ExampleBuild{})
	sparkwing.Job(plan, "deploy", &ExampleDeploy{Build: sparkwing.RefTo[BuildOut](build)}).Needs(build)
	for _, step := range []string{"render", "apply", "rollout"} {
		if _, err := host.RunStep(t.Context(), plan, "deploy", step); err != nil {
			t.Fatal(err)
		}
	}
	var apply, rollout, summary bool
	for _, record := range host.Records() {
		if record.Event == "annotation" {
			apply = apply || strings.Contains(record.Msg, "applied 6 manifests")
			rollout = rollout || strings.Contains(record.Msg, "rolled out 3/3")
		}
		summary = summary || record.Event == "summary" && strings.Contains(record.Msg, "Rollout complete")
	}
	if !apply || !rollout || !summary {
		t.Fatalf("missing reports: apply=%t rollout=%t summary=%t", apply, rollout, summary)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := host.RunStep(ctx, plan, "deploy", "apply"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled apply = %v, want context cancellation", err)
	}
}
