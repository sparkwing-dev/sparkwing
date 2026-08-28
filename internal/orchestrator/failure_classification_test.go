package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestCanceledByRun_DistinguishesTeardownFromGenuineFault(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, true},
		{"wrapped context canceled", fmt.Errorf("step: %w", context.Canceled), true},
		{"sigkilled child", errors.New("command terminated by cancellation: go test (signal: killed)"), true},
		{"genuine non-zero exit", errors.New("version-freshness: dep behind by 1 tag"), false},
		{"deadline exceeded stays a fault", context.DeadlineExceeded, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canceledByRun(tc.err); got != tc.want {
				t.Fatalf("canceledByRun(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestFailureFrom_StageFromReasonSurvivesBoundary(t *testing.T) {
	inproc := failureFrom(store.FailureVerify, &sparkwing.VerifyError{Err: errors.New("unhealthy")})
	if inproc.Stage != sparkwing.StageVerify {
		t.Fatalf("in-process stage = %v, want StageVerify", inproc.Stage)
	}
	if inproc.Err == nil || inproc.Err.Error() != "unhealthy" {
		t.Fatalf("in-process err = %v, want unwrapped %q", inproc.Err, "unhealthy")
	}

	flattened := errors.New("verify: unhealthy")
	ctrl := failureFrom(store.FailureVerify, flattened)
	if ctrl.Stage != sparkwing.StageVerify {
		t.Fatalf("cluster stage = %v, want StageVerify (recovered from failure_reason)", ctrl.Stage)
	}
	if ctrl.Err == nil || ctrl.Err.Error() != "verify: unhealthy" {
		t.Fatalf("cluster err = %v, want %q", ctrl.Err, "verify: unhealthy")
	}

	act := failureFrom(store.FailureUnknown, errors.New("boom"))
	if act.Stage != sparkwing.StageAction {
		t.Fatalf("action stage = %v, want StageAction", act.Stage)
	}
}
