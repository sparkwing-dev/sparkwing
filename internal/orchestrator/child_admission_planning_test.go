package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type runOnlyState struct {
	StateBackend
	run store.Run
}

func (s runOnlyState) GetRun(context.Context, string) (*store.Run, error) {
	r := s.run
	return &r, nil
}

// A controller-dispatched child has an empty plan until its plan is accepted;
// it is queued, not admitted, so the parent's timeout stays paused. The same
// empty plan on a run with no admission state is admitted, as before.
func TestChildPlanAdmission_APlanningChildIsNotAdmitted(t *testing.T) {
	created := time.Unix(1700000000, 0)
	cases := []struct {
		name, admission, status string
		want                    childPlanAdmissionStatus
	}{
		{"planning", store.RunAdmissionPlanning, "pending", childPlanAdmissionQueued},
		{"planning ended", store.RunAdmissionPlanning, "cancelled", childPlanAdmissionUnknown},
		{"accepted", store.RunAdmissionAdmitted, "running", childPlanAdmissionAdmitted},
		{"no admission state", "", "pending", childPlanAdmissionAdmitted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := runOnlyState{run: store.Run{ID: "child", Status: tc.status, Admission: tc.admission, CreatedAt: created}}
			got, err := childPlanAdmissionStatusForRun(context.Background(), state, nil, "child")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %v, want %v", got.Status, tc.want)
			}
			if tc.want == childPlanAdmissionQueued && !got.QueuedAt.Equal(created) {
				t.Fatalf("queued at %v, want the run's creation %v", got.QueuedAt, created)
			}
		})
	}
}
