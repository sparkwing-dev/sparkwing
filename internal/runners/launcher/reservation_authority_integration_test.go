package launcher

import (
	"testing"
	"time"
)

func TestReservationAcceptsUnexpiringServiceRoot(t *testing.T) {
	boundary, _ := boundaryFixture()
	binding := CloudJobBinding{Kind: CloudPlan, UnitID: "plan", Team: "team", RunID: "run", Holder: "holder", Generation: 1, OwnerDeadline: time.Now().Add(time.Hour)}
	if _, _, err := boundary.Reserve(t.Context(), binding, "plan-job"); err != nil {
		t.Fatalf("bounded Cloud award under an unexpiring service root cannot reserve capacity: %v", err)
	}
}
