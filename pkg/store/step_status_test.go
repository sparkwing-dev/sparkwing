package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestValidateStepTerminalStatusMatchesFinishTransition(t *testing.T) {
	for _, tc := range []struct {
		status string
		valid  bool
	}{
		{status: store.StepPassed, valid: true},
		{status: store.StepFailed, valid: true},
		{status: store.StepCancelled, valid: true},
		{status: store.StepRunning},
		{status: store.StepSkipped},
		{status: "unknown"},
	} {
		err := store.ValidateStepTerminalStatus(tc.status)
		if tc.valid && err != nil {
			t.Errorf("status %q rejected: %v", tc.status, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("status %q accepted", tc.status)
		}
	}
}
