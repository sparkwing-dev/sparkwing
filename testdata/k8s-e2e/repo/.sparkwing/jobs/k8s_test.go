package jobs

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestRunIDProofProducesCurrentRunID(t *testing.T) {
	const runID = "run-retry-proof"
	plan := sparkwing.NewPlan()
	if err := (k8sSuccess{}).Plan(t.Context(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{RunID: runID}); err != nil {
		t.Fatal(err)
	}
	host := sparkwingtest.New(sparkwingtest.Options{Run: sparkwing.RunContext{RunID: runID}})
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	output, err := host.RunStep(t.Context(), plan, "prove-controller-runner-logs", "emit-run-id")
	if err != nil {
		t.Fatalf("run ID proof: %v", err)
	}
	var got string
	if err := json.Unmarshal(output, &got); err != nil || got != runID {
		t.Fatalf("run ID proof output = %s, %v; want %s", output, err, runID)
	}
	for _, record := range host.Records() {
		if record.Msg == "sparkwing-k8s-e2e-success run_id="+runID {
			return
		}
	}
	t.Fatal("run ID proof emitted no matching log")
}
