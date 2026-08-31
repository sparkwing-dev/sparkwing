package jobs

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestRunIDProofProducesCurrentRunID(t *testing.T) {
	work := sparkwing.NewWork()
	step, err := (&runIDProof{runID: "run-retry-proof"}).Work(work)
	if err != nil {
		t.Fatalf("build run ID proof: %v", err)
	}
	if _, err := sparkwing.RunWork(context.Background(), work); err != nil {
		t.Fatalf("run ID proof: %v", err)
	}
	if got := step.Output(); got != "run-retry-proof" {
		t.Fatalf("run ID proof output = %v, want run-retry-proof", got)
	}
}
