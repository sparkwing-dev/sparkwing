package orchestrator

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TestFailureFrom_StageFromReasonSurvivesBoundary verifies the failing
// stage is attributed from the serializable store reason, not the Go
// error type. In-process a failure carries a typed *sparkwing.VerifyError;
// on the cluster path the controller has only the flattened string from
// store.Node.Error (the pod's typed error cannot cross the process
// boundary, see warmpool.resultFromNode / k8s.readFinalResult). Both must
// resolve to StageVerify because both runners persist
// failure_reason="verify", which is what failureFrom keys on -- so an
// OnFailure recovery that branches on f.Stage behaves the same in-process
// and on the controller.
func TestFailureFrom_StageFromReasonSurvivesBoundary(t *testing.T) {
	// In-process: typed VerifyError + verify reason -> StageVerify, unwrapped.
	inproc := failureFrom(store.FailureVerify, &sparkwing.VerifyError{Err: errors.New("unhealthy")})
	if inproc.Stage != sparkwing.StageVerify {
		t.Fatalf("in-process stage = %v, want StageVerify", inproc.Stage)
	}
	if inproc.Err == nil || inproc.Err.Error() != "unhealthy" {
		t.Fatalf("in-process err = %v, want unwrapped %q", inproc.Err, "unhealthy")
	}

	// Cluster: only a flattened string is available, but the persisted
	// reason still attributes the failure to the verify stage.
	flattened := errors.New("verify: unhealthy")
	ctrl := failureFrom(store.FailureVerify, flattened)
	if ctrl.Stage != sparkwing.StageVerify {
		t.Fatalf("cluster stage = %v, want StageVerify (recovered from failure_reason)", ctrl.Stage)
	}
	if ctrl.Err == nil || ctrl.Err.Error() != "verify: unhealthy" {
		t.Fatalf("cluster err = %v, want %q", ctrl.Err, "verify: unhealthy")
	}

	// A non-verify reason is an action-stage failure.
	act := failureFrom(store.FailureUnknown, errors.New("boom"))
	if act.Stage != sparkwing.StageAction {
		t.Fatalf("action stage = %v, want StageAction", act.Stage)
	}
}
