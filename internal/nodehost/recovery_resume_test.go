package nodehost

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestResumeOnlyAdoptsTerminalRecoveryResults(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "requeued", true: "disruption_limit"}[terminal], func(t *testing.T) {
			s := &scheduler{results: map[string]Result{}, started: map[string]bool{}}
			row := &store.Node{NodeID: "job", Status: "pending"}
			if terminal {
				row.Status, row.Outcome, row.FailureReason = "done", "failed", store.FailureAgentLost
			}
			s.reuseRow(t.Context(), nodeproto.JobSpec{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}, row)
			result, adopted := s.results["job"]
			if adopted != terminal || s.started["job"] != terminal {
				t.Fatalf("adopted=%t, started=%t, terminal=%t", adopted, s.started["job"], terminal)
			}
			if terminal && (result.Reason != store.FailureAgentLost || !result.recorded) {
				t.Fatalf("adopted result = %+v", result)
			}
		})
	}
}
