package orchestrator

import (
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// A node's error text can carry a value the run resolved as a secret.
// The node log masks it; this process's stderr does not, and the
// dispatcher relays stderr into the live delegate and the run's
// envelope file. So the spawned node reports its failure by exit
// status alone.
func TestCoordinatedExitStatus_DoesNotRepeatTheNodesError(t *testing.T) {
	const secret = "deploy-token-abc123"
	err := coordinatedExitStatus("run-1", "deploy", runner.Result{
		Outcome: sparkwing.Failed,
		Err:     errors.New("push failed: auth token " + secret + " rejected"),
	})
	if err == nil {
		t.Fatal("a failed node exited zero; the dispatcher loses the signal when the row write also failed")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the node's raw error reached stderr unmasked: %q", err)
	}
	for _, want := range []string{"run-1", "deploy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not name %q", err, want)
		}
	}
}

func TestCoordinatedExitStatus_SuccessIsSilent(t *testing.T) {
	if err := coordinatedExitStatus("run-1", "deploy",
		runner.Result{Outcome: sparkwing.Success}); err != nil {
		t.Fatalf("successful node returned %v", err)
	}
}
