package jobs

import (
	"context"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestTestPipelineReservesAndBoundsItsCPU(t *testing.T) {
	plan := sparkwing.NewPlan()
	if err := (&Test{}).Plan(context.Background(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "test"}); err != nil {
		t.Fatal(err)
	}

	wantCores := float64(preCommitCPUReservation(runtime.NumCPU()))
	if hints := plan.ResourceHints(); hints == nil || hints.Cores != wantCores {
		t.Fatalf("reserved cores = %#v, want %.0f", hints, wantCores)
	}
	if got := testGoCommand(14); got != "GOMAXPROCS=6 go test -p 6 ./..." {
		t.Fatalf("bounded command = %q", got)
	}
}
