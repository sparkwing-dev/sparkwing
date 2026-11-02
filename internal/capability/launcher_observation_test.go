package capability

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestLauncherCapacityFailureValidateRefusesBothReasons(t *testing.T) {
	for _, kind := range []UnitKind{UnitPlan, UnitEval, UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			observation := LauncherObservation{Award: LeaseRef{Kind: kind, ID: "unit", Holder: "holder", Generation: 1}, Reservation: true}
			for _, unavailable := range []bool{false, true} {
				observation.CapacityUnconfigured, observation.CapacityUnavailable = !unavailable, unavailable
				if err := observation.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			observation.CapacityUnconfigured, observation.CapacityUnavailable = true, true
			requireCode(t, observation.Validate(), apierr.BadRequest)
		})
	}
}
